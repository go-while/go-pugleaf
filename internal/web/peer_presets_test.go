package web

import (
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// TestPeerPresetsNewsdeefSemantics: the expanded newsdeef text-only preset behaves in our
// matcher like the INN newsfeeds line *,$NEWSDEEFDEFAULT,$NEWSDEEFNOBINARY.
func TestPeerPresetsNewsdeefSemantics(t *testing.T) {
	var text string
	for _, p := range peerPatternPresets() {
		if strings.Contains(p.INN, "$NEWSDEEFNOBINARY") {
			text = p.Patterns
		}
	}
	if text == "" {
		t.Fatal("no newsdeef text-only preset")
	}
	cases := []struct {
		groups   []string
		wanted   bool
		poisoned bool
	}{
		{[]string{"comp.lang.c"}, true, false},
		{[]string{"control.cancel"}, false, false},
		{[]string{"junk"}, false, false},
		{[]string{"gmane.comp.x"}, false, false},
		{[]string{"comp.lang.c", "gmane.comp.x"}, true, false},   // "!" only drops that group
		{[]string{"comp.lang.c", "alt.binaries.x"}, false, true}, // "@" drops the article
		{[]string{"rec.music.mp3"}, false, true},
		{[]string{"alt.sex.stories"}, false, true},
	}
	for _, c := range cases {
		res := nntp.MatchNewsgroupPatterns(text, c.groups)
		if (len(res.Wanted) > 0) != c.wanted || res.Poisoned != c.poisoned {
			t.Errorf("%v: wanted=%v poisoned=%v, want wanted=%v poisoned=%v", c.groups, res.Wanted, res.Poisoned, c.wanted, c.poisoned)
		}
	}
}

// TestPeerPresetsINNVariables: a peer whose in patterns are exactly a newsdeef preset gets the
// newsfeeds entry with the variables and their definitions; any other list is written as is.
func TestPeerPresetsINNVariables(t *testing.T) {
	presets := peerPatternPresets()
	text := presets[len(presets)-1]
	p := &models.Peer{Name: "n", PathToken: "news.n.example", Hosts: "192.0.2.9", InPatterns: text.Patterns}
	got := peerConfigSnippets(p, "news.a.example", 0).INN
	for _, want := range []string{
		"$NEWSDEEFDEFAULT=\\\n    !control,!control.*,!junk,!junk.*,!local,\\\n",
		"$NEWSDEEFNOBINARY=\\\n    @a.b.*,",
		"news.a.example\\\n    :*,$NEWSDEEFDEFAULT,$NEWSDEEFNOBINARY\\\n    :Tm:innfeed!",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("INN snippet lacks %q:\n%s", want, got)
		}
	}
	p.InPatterns = text.Patterns + ",!rec.*"
	got = peerConfigSnippets(p, "news.a.example", 0).INN
	if strings.Contains(got, "$NEWSDEEF") {
		t.Errorf("an edited preset must be written expanded:\n%s", got)
	}
}

// TestPeerPresetsShort: the peers table shows a preset by its variable form and cuts long lists.
func TestPeerPresetsShort(t *testing.T) {
	presets := peerPatternPresets()
	if got := patternsShort(presets[len(presets)-1].Patterns); got != "*,$NEWSDEEFDEFAULT,$NEWSDEEFNOBINARY" {
		t.Errorf("patternsShort(newsdeef text) = %q", got)
	}
	if got := patternsShort("*,!junk"); got != "*,!junk" {
		t.Errorf("patternsShort(short list) = %q", got)
	}
	long := strings.Repeat("comp.lang.x,", 10)
	if got := patternsShort(long); len([]rune(got)) != patternsShortMax+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("patternsShort(long) = %q, want %d characters and an ellipsis", got, patternsShortMax)
	}
}
