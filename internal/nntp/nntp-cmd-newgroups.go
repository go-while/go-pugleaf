package nntp

import (
	"errors"
	"log"
	"strconv"
	"strings"
	"time"
)

// parseNNTPDateTime parses the date and time arguments of NEWGROUPS (RFC 3977
// §7.3.2): date is "yymmdd" or "yyyymmdd", clock is "hhmmss", all ASCII digits.
// A 2-digit year is in the current century when it is not greater than the last two
// digits of now's (UTC) year, else in the previous century. A 4-digit year must be
// 1900..9999. Second 60 (a leap second) is read as 59. With gmt the time is UTC,
// otherwise the server's local time zone (time.Local). Calendar dates that do not
// exist (e.g. 20260231) are rejected.
func parseNNTPDateTime(date, clock string, gmt bool, now time.Time) (time.Time, error) {
	digits := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
	if (len(date) != 6 && len(date) != 8) || len(clock) != 6 || !digits(date) || !digits(clock) {
		return time.Time{}, errors.New("date must be yymmdd or yyyymmdd and time hhmmss")
	}
	num := func(s string) int {
		n, _ := strconv.Atoi(s) // only ASCII digits, checked above
		return n
	}

	var year int
	var rest string // "mmdd"
	if len(date) == 8 {
		year = num(date[:4])
		if year < 1900 {
			return time.Time{}, errors.New("year before 1900")
		}
		rest = date[4:]
	} else {
		yy := num(date[:2])
		cy := now.UTC().Year()
		year = cy - cy%100 + yy
		if yy > cy%100 {
			year -= 100
		}
		rest = date[2:]
	}
	month, day := num(rest[:2]), num(rest[2:4])
	hour, minute, sec := num(clock[:2]), num(clock[2:4]), num(clock[4:6])
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || sec > 60 {
		return time.Time{}, errors.New("date or time out of range")
	}
	if sec == 60 {
		sec = 59 // leap second
	}
	loc := time.Local
	if gmt {
		loc = time.UTC
	}
	t := time.Date(year, time.Month(month), day, hour, minute, sec, 0, loc)
	if t.Day() != day || t.Month() != time.Month(month) {
		return time.Time{}, errors.New("no such calendar date")
	}
	return t, nil
}

// handleNewGroups handles NEWGROUPS date time [GMT] (RFC 3977 §7.3): 231 and, in the
// LIST ACTIVE line format, every active group created at or after the given time,
// ordered by name. There is no result cap (decision D4): the list is a subset of
// LIST ACTIVE, read into memory before anything is written.
func (c *ClientConnection) handleNewGroups(args []string) error {
	if len(args) < 2 || len(args) > 3 || (len(args) == 3 && !strings.EqualFold(args[2], "GMT")) {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax: NEWGROUPS date time [GMT]")
	}
	since, err := parseNNTPDateTime(args[0], args[1], len(args) == 3, time.Now())
	if err != nil {
		c.rateLimitOnError()
		return c.sendResponse(501, "Invalid date or time")
	}
	groups, err := c.server.DB.NNTPNewGroupsSince(since)
	if err != nil {
		log.Printf("[NNTP] NEWGROUPS: %v", err)
		return c.sendResponse(403, "Internal fault")
	}

	if err := c.sendResponse(231, "List of new newsgroups follows"); err != nil {
		return err
	}
	d := c.newDataBlock()
	for _, g := range groups {
		if err := d.Line(c.activeLine(g)); err != nil {
			return err
		}
	}
	return d.Close()
}
