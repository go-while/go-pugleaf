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
//	<N  the article must be smaller than N bytes
//	>N  the article must be larger than N bytes
//	HN  the article's Path must hold at most N elements ("H" alone means 1)
//	GN  the article must be posted to at most N newsgroups
//	Nm  every newsgroup of the article must be moderated
//	Nu  every newsgroup of the article must be unmoderated
//
// Flags are separated by commas or white space. The flag letters are upper case, as
// newsfeeds(5) writes them; the m/u of N are lower case.

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
	// MaxBytes is N of "<N": the article must be smaller than this (0: no bound).
	MaxBytes int
	// MinBytes is N of ">N": the article must be larger than this (0: no bound).
	MinBytes int
	// MaxGroups is N of "GN": at most this many newsgroups (0: no bound).
	MaxGroups int
	// MaxPathHops is N of "HN": at most this many Path elements (0: no bound).
	MaxPathHops int
	// ModeratedOnly is "Nm": every newsgroup of the article must be moderated.
	ModeratedOnly bool
	// UnmoderatedOnly is "Nu": every newsgroup of the article must be unmoderated.
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
	if f.MaxBytes > 0 && f.MinBytes > 0 && f.MinBytes+1 >= f.MaxBytes {
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

// Allows reports whether an article may be sent to the peer these flags belong to. It is a
// filter on the article alone; the peer's out_patterns are matched separately.
//
// The size bounds are strict, as newsfeeds(5) writes them ("<N" is "smaller than N"), and
// they are compared against the article's wire size. Nm/Nu read a.Moderated, which the
// enqueuer fills; a newsgroup that is not in it counts as unmoderated (plan nntp-peering:
// an unknown status is treated as unmoderated), so Nm refuses an article with a group this
// server does not know.
func (f Flags) Allows(a Snapshot) bool {
	if f.MaxBytes > 0 && a.Bytes >= f.MaxBytes {
		return false
	}
	if f.MinBytes > 0 && a.Bytes <= f.MinBytes {
		return false
	}
	if f.MaxGroups > 0 && len(a.Newsgroups) > f.MaxGroups {
		return false
	}
	if f.MaxPathHops > 0 && PathHops(a.Path) > f.MaxPathHops {
		return false
	}
	if f.NeedsModeration() {
		if len(a.Newsgroups) == 0 {
			return false // nothing to decide on: do not feed
		}
		for _, g := range a.Newsgroups {
			moderated := a.Moderated[g]
			if f.ModeratedOnly && !moderated {
				return false
			}
			if f.UnmoderatedOnly && moderated {
				return false
			}
		}
	}
	return true
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
