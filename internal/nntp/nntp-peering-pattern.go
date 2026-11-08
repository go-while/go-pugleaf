package nntp

// Peer newsgroup patterns (plan nntp-peering): the subscription list of a peer, in the
// syntax of INN's newsfeeds(5) third field, used for peers.in_patterns on intake (and later
// for out_patterns on the outgoing feed).

import "strings"

// PeerPatternResult is the decision of MatchNewsgroupPatterns for one article.
type PeerPatternResult struct {
	// Wanted holds the article's newsgroups the pattern list accepts, in the order of the
	// Newsgroups header; nil when it accepts none.
	Wanted []string
	// Poisoned is set when a "@pattern" decided one of the newsgroups: the whole article is
	// refused, also when other newsgroups would be wanted.
	Poisoned bool
	// Pattern is the pattern that poisoned the article, or the one that accepted the first
	// wanted newsgroup ("" when nothing matched).
	Pattern string
}

// Accept reports whether the article may be taken: at least one wanted newsgroup and no
// poisoned one.
func (r PeerPatternResult) Accept() bool {
	return !r.Poisoned && len(r.Wanted) > 0
}

// MatchNewsgroupPatterns evaluates a peer's subscription list against the newsgroups of one
// article. The list is comma or white-space separated and read left to right; for each
// newsgroup the last pattern that matches it decides (INN newsfeeds(5)):
//
//	pattern    the newsgroup is wanted
//	!pattern   the newsgroup is not wanted (the rest of the article may still be)
//	@pattern   the newsgroup poisons the article: nothing of it is wanted
//
// A newsgroup that no pattern matches is not wanted. Wildcards are "*" (zero or more
// characters) and "?" (one character), matched against the whole name.
func MatchNewsgroupPatterns(patterns string, newsgroups []string) PeerPatternResult {
	pats := ParsePeerPatterns(patterns)
	var res PeerPatternResult
	for _, group := range newsgroups {
		decision, pattern := matchPeerPatterns(pats, group)
		if decision < 0 {
			return PeerPatternResult{Poisoned: true, Pattern: pattern}
		}
		if decision > 0 {
			res.Wanted = append(res.Wanted, group)
			if res.Pattern == "" {
				res.Pattern = pattern
			}
		}
	}
	return res
}

// ParsePeerPatterns splits a pattern list (comma or white-space separated) into its patterns,
// dropping empty entries and those that are only a prefix ("!" or "@").
func ParsePeerPatterns(patterns string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(patterns, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		if p = strings.TrimSpace(p); p == "" || p == "!" || p == "@" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// matchPeerPatterns returns the decision of the last pattern that matches group (1 wanted,
// 0 not wanted, -1 poisoned) and that pattern.
func matchPeerPatterns(pats []string, group string) (decision int, pattern string) {
	for _, p := range pats {
		body, want := p, 1
		switch p[0] {
		case '!':
			body, want = p[1:], 0
		case '@':
			body, want = p[1:], -1
		}
		if wildmatGlob(body, group) {
			decision, pattern = want, p
		}
	}
	return decision, pattern
}
