package nntp

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// articleRange is a parsed RFC 3977 §3.2.1.1 range argument: "n", "n-" or "n-m".
// First and Last are inclusive; for "n-" Last is math.MaxInt64 and Open is true.
// A reversed range ("5-3") parses fine and is Empty(); it is not a syntax error.
type articleRange struct {
	First, Last int64
	Open        bool
}

// Empty reports whether the range contains no article numbers.
func (r articleRange) Empty() bool { return r.Last < r.First }

var errRangeSyntax = errors.New("syntax error in range")

// parseRangeArg parses arg. Each number is 1..19 ASCII digits whose value fits into int64,
// 0 allowed; anything else ("", "-", "x-5", "5-y", "1-2-3", "-5", "+1", 20+ digits, values
// above math.MaxInt64) returns errRangeSyntax. RFC 3977 §9.8 limits article-number to 16
// digits, but clients send "1-9223372036854775807" as "everything" (the SEC-1 probe and
// N22 rely on it), so the int64 range is accepted. It sends nothing: the caller answers
// rateLimitOnError() + 501 "Syntax error in range".
func (c *ClientConnection) parseRangeArg(arg string) (articleRange, error) {
	num := func(s string) (int64, bool) {
		if len(s) == 0 || len(s) > 19 {
			return 0, false
		}
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return 0, false
			}
		}
		n, err := strconv.ParseInt(s, 10, 64) // fails above math.MaxInt64
		return n, err == nil
	}

	first, rest, isRange := strings.Cut(arg, "-")
	lo, ok := num(first)
	if !ok {
		return articleRange{}, errRangeSyntax
	}
	if !isRange {
		return articleRange{First: lo, Last: lo}, nil
	}
	if rest == "" {
		return articleRange{First: lo, Last: math.MaxInt64, Open: true}, nil
	}
	hi, ok := num(rest)
	if !ok {
		return articleRange{}, errRangeSyntax
	}
	return articleRange{First: lo, Last: hi}, nil
}

// requireGroup sends rateLimitOnError() + "412 No newsgroup selected" and returns false when no
// group is selected. A send error is dropped on purpose (the next read on that connection fails).
func (c *ClientConnection) requireGroup() bool {
	if c.currentGroup != "" {
		return true
	}
	c.rateLimitOnError()
	_ = c.sendResponse(412, "No newsgroup selected")
	return false
}
