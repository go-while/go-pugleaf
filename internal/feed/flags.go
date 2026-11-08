package feed

// peers.out_flags: the subset of INN's newsfeeds(5) fourth field (the flag field) the
// outgoing feed understands (plan nntp-peering, slice feed-queue). The parser is modelled
// on INN's documentation and on the flag parser of OpenUsenetServer's internal/feed, but it
// is strict where those are lenient: a flag this build does not implement is a parse error,
// so database-level validation of a peer row can refuse it instead of silently feeding more
// than the admin asked for.
//
// Implemented (everything else is an error):
//
//	<N  the article's size must not exceed N bytes
//	>N  the article's size must be at least N bytes
//	HN  the article's Path must hold at most N elements ("H" alone means 1)
//	GN  the article must be posted to at most N newsgroups ("G" alone means 1)
//	Nm  the peer is only subscribed to the moderated groups of the article
//	Nu  the peer is only subscribed to the unmoderated groups of the article
//
// Flags are separated by commas or white space. The flag letters are upper case, as
// newsfeeds(5) writes them; the m/u of N are lower case.
//
// Two details follow INN's code rather than a loose reading of the manual page:
//
//   - the size bounds compare the way innd does ("refuse when size > N" for <N, "refuse
//     when size < N" for >N), so an article of exactly N bytes satisfies both;
//   - Nm/Nu are not a property of the article but of the peer's subscription: INN applies
//     them to the per-group subscription array (innd/newsfeeds.c SITEparseone) and then
//     feeds the site if any subscribed group survives (innd/art.c ARTpost). So a crosspost
//     into a moderated and an unmoderated group still goes to an Nm peer - for the
//     moderated group - instead of being refused outright.

import (
	"fmt"
	"strconv"
	"strings"
)

// Flags is a parsed out_flags field. The zero value lets every article through, which is
// what an empty out_flags means.
type Flags struct {
	// Raw is the field as it was parsed, for log lines and the admin page.
	Raw string
	// MaxBytes is N of "<N": the size must not exceed this (0: no bound).
	MaxBytes int
	// MinBytes is N of ">N": the size must be at least this (0: no bound).
	MinBytes int
	// MaxGroups is N of "GN": at most this many newsgroups (0: no bound).
	MaxGroups int
	// MaxPathHops is N of "HN": at most this many Path elements (0: no bound).
	MaxPathHops int
	// ModeratedOnly is "Nm": of the groups the peer subscribes to, only the moderated
	// ones count.
	ModeratedOnly bool
	// UnmoderatedOnly is "Nu": only the unmoderated subscribed groups count.
	UnmoderatedOnly bool
}

// ParseFlags parses an out_flags field. An empty field is the zero Flags and no error.
// An unknown flag, a missing or unreadable count, a count below 1 and the contradiction
// "Nm,Nu" are errors; the error names the flag so an admin form can show it.
func ParseFlags(raw string) (Flags, error) {
	f := Flags{Raw: strings.TrimSpace(raw)}
	if f.Raw == "" {
		return f, nil
	}
	for _, part := range strings.FieldsFunc(f.Raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		if err := f.parseOne(part); err != nil {
			return Flags{Raw: f.Raw}, err
		}
	}
	if f.ModeratedOnly && f.UnmoderatedOnly {
		return Flags{Raw: f.Raw}, fmt.Errorf("out_flags: Nm and Nu together accept no article at all")
	}
	// An article of exactly N bytes satisfies both bounds (INN's comparison), so only a
	// minimum above the maximum can never be satisfied.
	if f.MaxBytes > 0 && f.MinBytes > 0 && f.MinBytes > f.MaxBytes {
		return Flags{Raw: f.Raw}, fmt.Errorf("out_flags: <%d and >%d accept no size at all", f.MaxBytes, f.MinBytes)
	}
	return f, nil
}

// parseOne reads one flag into f.
func (f *Flags) parseOne(part string) error {
	switch part[0] {
	case '<':
		n, err := flagCount(part, part[1:], 1)
		if err != nil {
			return err
		}
		f.MaxBytes = n
	case '>':
		n, err := flagCount(part, part[1:], 1)
		if err != nil {
			return err
		}
		f.MinBytes = n
	case 'H':
		if part == "H" {
			f.MaxPathHops = 1 // newsfeeds(5): a bare H is H1
			return nil
		}
		n, err := flagCount(part, part[1:], 1)
		if err != nil {
			return err
		}
		f.MaxPathHops = n
	case 'G':
		if part == "G" {
			f.MaxGroups = 1 // newsfeeds(5): a bare G is G1, like a bare H
			return nil
		}
		n, err := flagCount(part, part[1:], 1)
		if err != nil {
			return err
		}
		f.MaxGroups = n
	case 'N':
		switch part {
		case "Nm":
			f.ModeratedOnly = true
		case "Nu":
			f.UnmoderatedOnly = true
		default:
			return fmt.Errorf("out_flags: %q is not a known flag (Nm or Nu)", part)
		}
	default:
		return fmt.Errorf("out_flags: %q is not a flag this server implements (<N, >N, HN, GN, Nm, Nu)", part)
	}
	return nil
}

// flagCount parses the count of a flag and bounds it from below.
func flagCount(flag, digits string, minimum int) (int, error) {
	if digits == "" {
		return 0, fmt.Errorf("out_flags: %q needs a number", flag)
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, fmt.Errorf("out_flags: %q: %q is not a number", flag, digits)
	}
	if n < minimum {
		return 0, fmt.Errorf("out_flags: %q: the number must be at least %d", flag, minimum)
	}
	return n, nil
}

// NeedsModeration reports whether these flags need to know the moderation status of the
// article's newsgroups. The enqueuer only looks that status up when some peer needs it.
func (f Flags) NeedsModeration() bool {
	return f.ModeratedOnly || f.UnmoderatedOnly
}

// Allows reports whether an article may be sent to the peer these flags belong to.
//
// wanted are the article's newsgroups the peer's out_patterns subscribe to (the Wanted of
// nntp.MatchNewsgroupPatterns); the caller has already refused a poisoned article and one
// with no wanted group at all. The size, Path-hop and newsgroup-count bounds are properties
// of the article, so they are checked against the whole article (a.Bytes, a.Path,
// a.Newsgroups). Nm/Nu narrow the subscription instead: the article goes out when at least
// one wanted group survives them (INN innd/art.c ARTpost).
func (f Flags) Allows(a Snapshot, wanted []string) bool {
	if f.MaxBytes > 0 && a.Bytes > f.MaxBytes {
		return false
	}
	if f.MinBytes > 0 && a.Bytes < f.MinBytes {
		return false
	}
	if f.MaxGroups > 0 && len(a.Newsgroups) > f.MaxGroups {
		return false
	}
	if f.MaxPathHops > 0 && PathHops(a.Path) > f.MaxPathHops {
		return false
	}
	if f.NeedsModeration() && len(f.ModerationWanted(wanted, a.Moderated)) == 0 {
		return false
	}
	return true
}

// ModerationWanted returns the groups of wanted that Nm/Nu leave subscribed. A group whose
// status is not in moderated has no known status: this server does not carry it, so it
// cannot be known to be moderated or unmoderated and it is dropped by both flags. Without
// Nm and without Nu every wanted group survives.
func (f Flags) ModerationWanted(wanted []string, moderated map[string]bool) []string {
	if !f.NeedsModeration() {
		return wanted
	}
	out := make([]string, 0, len(wanted))
	for _, g := range wanted {
		status, known := moderated[g]
		if !known {
			continue
		}
		if status == f.ModeratedOnly {
			// Nm keeps the moderated groups, Nu the unmoderated ones.
			out = append(out, g)
		}
	}
	return out
}

// PathHops counts the elements of a Path header value (RFC 5537 §3.2.1), which is what the
// H flag bounds. Empty elements (a trailing or doubled "!") are not counted.
func PathHops(path string) int {
	n := 0
	for _, el := range strings.Split(path, "!") {
		if strings.TrimSpace(el) != "" {
			n++
		}
	}
	return n
}
