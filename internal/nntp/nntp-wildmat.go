package nntp

import (
	"strings"
	"unicode/utf8"
)

// Wildmat matching (RFC 3977 §4), used by LIST ACTIVE/NEWSGROUPS <wildmat>.
//
// Grammar (§4.1):
//
//	wildmat         = wildmat-pattern *("," ["!"] wildmat-pattern)
//	wildmat-pattern = 1*wildmat-item
//	wildmat-item    = wildmat-exact / wildmat-wild
//	wildmat-wild    = "*" / "?"
//	wildmat-exact   = any UTF-8 character except ! * , ? [ \ ] (and controls)
//
// "?" matches exactly one character (one rune, not one octet), "*" matches zero or
// more characters, everything else matches itself. A wildmat is a comma-separated
// list of patterns; the rightmost pattern that matches decides (§4.2): a plain
// pattern accepts the name, a "!" pattern rejects it; when none matches the name is
// rejected. As a lenient extension a leading "!" is also accepted on the first
// pattern (it can only exclude).

// wildmatSplit splits a wildmat into its patterns. neg[i] is true when pattern i
// was prefixed by "!" (the "!" is not part of pats[i]). ok is false when the
// wildmat is not valid UTF-8, a pattern is empty (also after its "!"), or a
// pattern contains "!", "\", "[", "]", a C0 control (0x00-0x1F) or DEL (0x7F).
func wildmatSplit(pattern string) (pats []string, neg []bool, ok bool) {
	if !utf8.ValidString(pattern) {
		return nil, nil, false
	}
	for _, el := range strings.Split(pattern, ",") {
		n := false
		if strings.HasPrefix(el, "!") {
			n = true
			el = el[1:]
		}
		if el == "" {
			return nil, nil, false
		}
		for i := 0; i < len(el); i++ {
			switch b := el[i]; {
			case b == '!', b == '\\', b == '[', b == ']', b < 0x20, b == 0x7f:
				return nil, nil, false
			}
		}
		pats = append(pats, el)
		neg = append(neg, n)
	}
	return pats, neg, true
}

// wildmatValid reports whether pattern is a non-empty, valid wildmat.
func wildmatValid(pattern string) bool {
	if pattern == "" {
		return false
	}
	_, _, ok := wildmatSplit(pattern)
	return ok
}

// wildmatGlob reports whether the single pattern pat (no "," or "!") matches the
// whole of name. It works on runes and backtracks iteratively to the last "*" only,
// so it is O(len(pat)*len(name)) and never recursive.
func wildmatGlob(pat, name string) bool {
	p := []rune(pat)
	n := []rune(name)
	pi, ni := 0, 0
	star, mark := -1, 0 // index of the last '*' in p and the name index it has matched up to
	for ni < len(n) {
		if pi < len(p) {
			switch p[pi] {
			case '*':
				star, mark = pi, ni
				pi++
				continue
			case '?':
				pi++
				ni++
				continue
			default:
				if p[pi] == n[ni] {
					pi++
					ni++
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		// let the last '*' absorb one more rune and retry the rest from there
		mark++
		pi, ni = star+1, mark
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// wildmatMatch reports whether name matches the wildmat pattern: the rightmost
// matching pattern decides (negated: false), no match is false (§4.2). An invalid
// wildmat matches nothing; callers check wildmatValid first to answer 501.
func wildmatMatch(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	pats, neg, ok := wildmatSplit(pattern)
	if !ok {
		return false
	}
	for i := len(pats) - 1; i >= 0; i-- {
		if wildmatGlob(pats[i], name) {
			return !neg[i]
		}
	}
	return false
}
