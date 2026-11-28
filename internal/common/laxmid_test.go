package common

// Plan laxmid: the opt-in RFC 3977 message-id syntax (-laxmid, INN's laxmid). Default strict
// (RFC 5536 msg-id with "@") exactly as before; lax accepts "<" 1*248(%x21-3D / %x3F-7E) ">".

import (
	"strings"
	"testing"
)

// laxmidSet turns the lax syntax on for this test and back to the strict default afterwards.
func laxmidSet(t *testing.T) {
	t.Helper()
	SetLaxMessageID(true)
	t.Cleanup(func() { SetLaxMessageID(false) })
}

// ids of the news.php.net gateway articles (plan laxmid, context 2) and other RFC 3977 forms
var laxmidOnlyIDs = []string{
	"<12345_67890>",
	"<12799969_70875456>",
	"<00000003501CE2A1085223638>",
	"<20010303112347.31041.>",
	"<x>",                                // 3 octets: the RFC 3977 minimum
	"<a@b@c>",                            // two "@": not RFC 5536
	"<a@>",                               // empty right part
	"<@b>",                               // empty left part
	"<a<b@c>",                            // "<" in the left part (A-NOTGT allows it)
	"<a@b..c>",                           // not dot-atom-text
	"<" + strings.Repeat("a", 248) + ">", // 250 octets
}

// ids both syntaxes accept
var laxmidBothIDs = []string{
	"<a@b>",
	"<part1.part2@example.invalid>",
	"<x@[a<b]>",
	"<" + strings.Repeat("a", 242) + "@x.inv>", // 250 octets
}

// ids both syntaxes refuse
var laxmidNeitherIDs = []string{
	"",
	"<",
	"<>",                                 // no inner octet
	"12345_67890",                        // no brackets
	"<12345_67890",                       // no ">"
	"12345_67890>",                       // no "<"
	" <12345_67890>",                     // leading blank
	"<12345_67890> ",                     // trailing blank
	"<a>b>",                              // ">" inside
	"<a>b@c>",                            // ">" inside, with "@"
	"<a b>",                              // SP
	"<a\tb>",                             // HT
	"<a\rb>",                             // CR
	"<a\nb>",                             // LF
	"<a\x00b>",                           // NUL
	"<a\x01b@c>",                         // control
	"<a\x7fb>",                           // DEL
	"<a\x80b>",                           // 8-bit
	"<\xc3\xa4@b>",                       // UTF-8
	"<" + strings.Repeat("a", 249) + ">", // 251 octets
	"<" + strings.Repeat("a", 243) + "@x.inv>", // 251 octets, with "@"
}

// TestLaxmidDefaultStrict: without SetLaxMessageID the syntax is RFC 5536 as before, so the
// news.php.net ids are refused.
func TestLaxmidDefaultStrict(t *testing.T) {
	if LaxMessageID() {
		t.Fatal("LaxMessageID() = true by default, want false")
	}
	for _, id := range laxmidOnlyIDs {
		if ValidMessageID(id) {
			t.Errorf("strict: ValidMessageID(%q) = true, want false", id)
		}
		if ValidStrictMessageID(id) {
			t.Errorf("ValidStrictMessageID(%q) = true, want false", id)
		}
	}
	for _, id := range laxmidBothIDs {
		if !ValidMessageID(id) {
			t.Errorf("strict: ValidMessageID(%q) = false, want true", id)
		}
	}
	for _, id := range laxmidNeitherIDs {
		if ValidMessageID(id) {
			t.Errorf("strict: ValidMessageID(%q) = true, want false", id)
		}
	}
}

// TestLaxmidOn: with SetLaxMessageID(true) the RFC 3977 ids pass, every strict id still
// passes, and ">" inside, controls, blanks, 8-bit octets and more than 250 octets are refused.
// ValidStrictMessageID is not affected by the switch.
func TestLaxmidOn(t *testing.T) {
	laxmidSet(t)
	if !LaxMessageID() {
		t.Fatal("LaxMessageID() = false after SetLaxMessageID(true)")
	}
	for _, id := range laxmidOnlyIDs {
		if !ValidMessageID(id) {
			t.Errorf("lax: ValidMessageID(%q) = false, want true", id)
		}
		if ValidStrictMessageID(id) {
			t.Errorf("lax: ValidStrictMessageID(%q) = true, want false (not affected by the switch)", id)
		}
	}
	for _, id := range laxmidBothIDs {
		if !ValidMessageID(id) {
			t.Errorf("lax: ValidMessageID(%q) = false, want true", id)
		}
	}
	for _, id := range laxmidNeitherIDs {
		if ValidMessageID(id) {
			t.Errorf("lax: ValidMessageID(%q) = true, want false", id)
		}
	}
}

// TestLaxmidOffAgain: SetLaxMessageID(false) restores the strict syntax.
func TestLaxmidOffAgain(t *testing.T) {
	SetLaxMessageID(true)
	SetLaxMessageID(false)
	if LaxMessageID() || ValidMessageID("<12345_67890>") {
		t.Fatal("SetLaxMessageID(false) did not restore the strict syntax")
	}
}

// TestLaxmidStrictSuperset: every octet string of up to 3 inner octets from a small alphabet
// that the strict syntax accepts is also accepted in lax mode (lax only widens).
func TestLaxmidStrictSuperset(t *testing.T) {
	alphabet := []byte{'a', '@', '.', '[', ']', '\\', '<', '>', ' ', 0x01}
	var walk func(prefix []byte, depth int)
	checked := 0
	walk = func(prefix []byte, depth int) {
		id := "<" + string(prefix) + ">"
		SetLaxMessageID(false)
		strict := ValidMessageID(id)
		SetLaxMessageID(true)
		lax := ValidMessageID(id)
		if strict && !lax {
			t.Errorf("%q: strict accepts, lax refuses", id)
		}
		checked++
		if depth == 0 {
			return
		}
		for _, c := range alphabet {
			walk(append(append([]byte(nil), prefix...), c), depth-1)
		}
	}
	t.Cleanup(func() { SetLaxMessageID(false) })
	walk(nil, 4)
	if checked == 0 {
		t.Fatal("nothing checked")
	}
}

// TestLaxmidGenerateStaysStrict: GenerateMessageID always makes an RFC 5536 id, also with
// the lax syntax on: an identity that only the lax syntax would take is refused.
func TestLaxmidGenerateStaysStrict(t *testing.T) {
	laxmidSet(t)
	id, err := GenerateMessageID("news.example.org")
	if err != nil || !ValidStrictMessageID(id) {
		t.Fatalf("GenerateMessageID = %q, %v, want an RFC 5536 id", id, err)
	}
	for _, bad := range []string{"a..b", ".news.example.org", "news.example.org."} {
		if id, err := GenerateMessageID(bad); err == nil {
			t.Errorf("lax: GenerateMessageID(%q) = %q, want error", bad, id)
		}
	}
}
