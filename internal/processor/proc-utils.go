package processor

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
)

var (
	//separatorRegex       = regexp.MustCompile(`[,;:\s]+`)
	parenRe              = regexp.MustCompile(`\s*\([^)]*\)$`)
	threeDigitTimezoneRe = regexp.MustCompile(`\s([+-])(\d{3})\s*$`)
	yearRegex            = regexp.MustCompile(`\b(19\d\d|20\d\d)\b`)
	monthRegex           = regexp.MustCompile(`\b(jan|january|feb|february|mar|march|apr|april|may|jun|june|jul|july|aug|august|sep|sept|september|oct|october|nov|november|dec|december)\b`)
	dayRegex             = regexp.MustCompile(`\b(0?[1-9]|[12]\d|3[01])\b`)
	timeRegex            = regexp.MustCompile(`\b(\d{1,2}):(\d{1,2})(?::(\d{1,2}))?\b`)
	// day next to a month name: "3 Mar", "03-Mar", "Mar 3", "Nov10", "May 19th", "February 11,"
	dayBeforeMonthRe = regexp.MustCompile(`(?i)\b(0?[1-9]|[12]\d|3[01])(?:\s?(?:st|nd|rd|th))?[-\s/.,]*(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\b`)
	dayAfterMonthRe  = regexp.MustCompile(`(?i)\b(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?[-\s/.,]*(0?[1-9]|[12]\d|3[01])(?:\s?(?:st|nd|rd|th))?\b`)
	ampmRe           = regexp.MustCompile(`(?i)\d\s*([ap])\.?m\b`)
	numOffsetRe      = regexp.MustCompile(`(?:^|\s)([+-])(\d{2}):?(\d{2})\b`)
	gmtOffsetRe      = regexp.MustCompile(`(?i)\b(?:GMT|UTC)\s*([+-])\s*(\d{1,2})(?::?(\d{2}))?\b`)
	// "91May07 11:07 pm": YYMonDD as written by some BBS gateways
	yyMonDDRe     = regexp.MustCompile(`^(\d{2})([A-Za-z]{3})(\d{2})\b`)
	letterDigitRe = regexp.MustCompile(`([A-Za-z])(\d)|(\d)([A-Za-z])`)
	// everything up to the time and an optional zone/offset: what remains of a date line
	// that carries trailing junk ("<br>", " ,", "From: ...", a JIS escape)
	dateCoreRe        = regexp.MustCompile(`^(.*?\d{1,2}:\d{2}(?::\d{2})?(?:\s*[ap]\.?m\.?)?(?:\s*(?:[+-]\d{4}|[+-]\d{2}:\d{2}|GMT\s*[+-]\d{1,2}|[A-Za-z]{1,5}))?)`)
	zoneAbbrRe        = regexp.MustCompile(`(?i)\b(` + zoneAbbrAlternation() + `)\b`)
	twoDigitYearRegex = regexp.MustCompile(`\b([0-9]\d)\b`)
	numericRegex      = regexp.MustCompile(`\b(\d{1,4})[\/\-\.](\d{1,2})[\/\-\.](\d{1,4})\b`)
)

var NNTPDateLayouts = []string{
	// Just date
	"2006/01/02",

	// Standard Go time formats
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	time.RFC850,

	// ISO 8601 variants
	"2006-01-02T15:04:05.000-07:00",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05.000Z",
	"2006-01-02T15:04:05Z",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05",

	// US/European date formats
	"01/02/2006 15:04:05 -0700",
	"01/02/2006 15:04:05 MST",
	"01/02/2006 15:04:05",
	"02/01/2006 15:04:05 -0700",
	"02/01/2006 15:04:05 MST",
	"02/01/2006 15:04:05",
	"01/02/06 15:04:05 MST",
	"01/02/06 15:04:05",
	"02.01.2006 15:04:05 MST",
	"02.01.2006 15:04:05",
	// RFC variants
	"Monday, 02-Jan-2006 15:04:05 MST",
	"Monday, 2-Jan-2006 15:04:05 MST",
	"Monday, 02-Jan-06 15:04:05 MST",
	"Monday, 2-Jan-06 15:04:05 MST",
	"Monday, _2-Jan-06 15:04:05 MST",
	"Mon, _2-Jan-2006 15:04:05 -0700",
	"Mon, _2-Jan-06 15:04:05 MST",
	"Mon, 02-Jan-2006 15:04:05 MST",
	"Mon, 2-Jan-2006 15:04:05 MST",
	"Mon, 02-Jan-06 15:04:05 MST",
	"Mon, 2-Jan-06 15:04:05 MST",

	// Longest formats first
	"Monday, _2 January 2006 15:04:05 -0700 (MST)",
	"Monday, _2 January 06 15:04:05 -0700 (MST)",
	"Mon, _2 January 2006 15:04:05 -0700 (MST)",
	"Mon, 02 Jan 2006 15:04:05 -0700 (MST)",
	"Monday, _2 Jan 2006 15:04:05 -0700 (MST)",
	"Mon, _2 Jan 2006 15:04:05 -0700 (MST)",
	"Monday, _2 Jan 06 15:04:05 -0700 (MST)",
	"Mon, 02 Jan 06 15:04:05 -0700 (MST)",
	"Mon, _2 Jan 06 15:04:05 -0700 (MST)",
	"January _2, 2006 15:04:05 -0700 (MST)",
	"January _2, 06 15:04:05 -0700 (MST)",
	"Jan _2, 2006 15:04:05 -0700 (MST)",
	"Jan _2, 06 15:04:05 -0700 (MST)",
	"_2 January 2006 15:04:05 -0700 (MST)",
	"_2 January 06 15:04:05 -0700 (MST)",
	"2 Jan 2006 15:04:05 -0700 (MST)",
	"_2 Jan 2006 15:04:05 -0700 (MST)",
	"_2 Jan 06 15:04:05 -0700 (MST)",
	"02 Jan 06 15:04:05 -0700 (MST)",
	"_2 Jan 2006 15:04 -0700 (MST)",
	"Mon, 02 Jan 06 15:04 -0700 (MST)",
	"Mon, _2 Jan 06 15:04 -0700 (MST)",
	"02 Jan 06 15:04 -0700 (MST)",

	"Monday, _2 January 2006 15:04:05 --0700",
	"Monday, _2 January 06 15:04:05 --0700",
	"Mon, _2 January 2006 15:04:05 --0700",
	"Monday, _2 Jan 2006 15:04:05 --0700",
	"Monday, _2 Jan 06 15:04:05 --0700",
	"Mon, _2 Jan 2006 15:04:05 --0700",
	"Mon, _2 Jan 06 15:04:05 --0700",
	"_2 January 2006 15:04:05 --0700",
	"_2 January 06 15:04:05 --0700",
	"_2 Jan 2006 15:04:05 --0700",
	"_2 Jan 06 15:04:05 --0700",
	"January _2, 2006 15:04:05 --0700",
	"January _2, 06 15:04:05 --0700",
	"Jan _2, 2006 15:04:05 --0700",
	"Jan _2, 06 15:04:05 --0700",

	"Mon, 2 Jan 2006 15:04:05 -0700 MST",
	"Mon, 02 Jan 2006 15:04:05 -0700 MST",
	"Mon, 2 Jan 06 15:04:05 -0700 MST",
	"Mon, 02 Jan 06 15:04:05 -0700 MST",
	"Mon, _2 Jan 2006 15:04:05 -0700 MST",
	"Mon, _2 Jan 06 15:04:05 -0700 MST",
	"Mon, 02 Jan 2006 15:04 -0700 MST",
	"Mon, 2 Jan 2006 15:04 -0700 MST",
	"Mon, _2 Jan 2006 15:04 -0700 MST",
	"Mon, _2 Jan 06 15:04 -0700 MST",
	"2 Jan 2006 15:04:05 -0700 MST",
	"_2 Jan 2006 15:04:05 -0700 MST",

	"Monday, _2 January 2006 15:04:05 -0700",
	"Monday, _2 January 06 15:04:05 -0700",
	"Mon, _2 January 2006 15:04:05 -0700",
	"Mon, _2 January 06 15:04:05 -0700",
	"Monday, _2 Jan 2006 15:04:05 -0700",
	"Monday, _2 Jan 06 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 02 Jan 2006 15:04:05 -0700",
	"Mon, _2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 06 15:04:05 -0700",
	"Mon, 02 Jan 06 15:04:05 -0700",
	"Mon, _2 Jan 06 15:04:05 -0700",
	"January _2, 2006 15:04:05 -0700",
	"January _2, 06 15:04:05 -0700",
	"Jan _2, 2006 15:04:05 -0700",
	"Jan _2, 06 15:04:05 -0700",
	"_2 January 2006 15:04:05 -0700",
	"_2 January 06 15:04:05 -0700",
	"_2 Jan 2006 15:04:05 -0700",
	"_2 Jan 06 15:04:05 -0700",
	"2 Jan 2006 15:04:05 -0700",
	"02 Jan 2006 15:04:05 -0700",
	"02 Jan 06 15:04:05 -0700",
	"2 Jan 06 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04 -0700",
	"Mon, _2 Jan 2006 15:04 -0700",
	"Mon, _2 January 2006 15:04 -0700",
	"02 Jan 06 15:04 -0700",
	"Mon, 02 Jan 06 15:04 -0700",
	"Mon, _2 Jan 06 15:04 -0700",

	"Monday, _2 January 2006 15:04:05 MST",
	"Monday, _2 January 06 15:04:05 MST",
	"Mon, _2 January 2006 15:04:05 MST",
	"Mon, _2 January 06 15:04:05 MST",
	"Monday, _2 Jan 2006 15:04:05 MST",
	"Monday, _2 Jan 06 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 02 Jan 2006 15:04:05 MST",
	"Mon, _2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 06 15:04:05 MST",
	"Mon, 02 Jan 06 15:04:05 MST",
	"Mon, _2 Jan 06 15:04:05 MST",
	"January _2, 2006 15:04:05 MST",
	"January _2, 06 15:04:05 MST",
	"Jan _2, 2006 15:04:05 MST",
	"Jan _2, 06 15:04:05 MST",
	"_2 January 2006 15:04:05 MST",
	"_2 January 06 15:04:05 MST",
	"_2 Jan 2006 15:04:05 MST",
	"_2 Jan 06 15:04:05 MST",
	"2 Jan 2006 15:04:05 MST",
	"02 Jan 2006 15:04:05 MST",
	"02 Jan 06 15:04:05 MST",
	"2 Jan 06 15:04:05 MST",
	"Mon, 02 Jan 2006 15:04 MST",
	"Mon, 02 Jan 06 15:04 MST",
	"Mon, _2 Jan 2006 15:04 MST",
	"Mon, _2 Jan 06 15:04 MST",
	"_2 January 2006 15:04 MST",
	"_2 Jan 2006 15:04 MST",

	"Mon, 02 Jan 2006 15:04:05 (MST)",
	"Mon, 02 Jan 06 15:04:05 (MST)",
	"Mon, _2 Jan 2006 15:04:05 (MST)",
	"Mon, _2 Jan 06 15:04:05 (MST)",
	"Mon, 02 Jan 2006 15:04 (MST)",
	"Mon, 02 Jan 06 15:04 (MST)",
	"Mon, _2 Jan 2006 15:04 (MST)",
	"Mon, _2 Jan 06 15:04 (MST)",

	"Monday, _2 January 2006 15:04:05",
	"Monday, _2 January 06 15:04:05",
	"Mon, _2 January 2006 15:04:05",
	"Mon, _2 January 06 15:04:05",
	"Monday, _2 Jan 2006 15:04:05",
	"Monday, _2 Jan 06 15:04:05",
	"Mon, 02 Jan 2006 15:04:05",
	"Mon, 2 Jan 2006 15:04:05",
	"Mon, _2 Jan 2006 15:04:05",
	"Mon, 02 Jan 06 15:04:05",
	"Mon, 2 Jan 06 15:04:05",
	"Mon, _2 Jan 06 15:04:05",
	"January _2, 2006 15:04:05",
	"January _2, 06 15:04:05",
	"Jan _2, 2006 15:04:05",
	"Jan _2, 06 15:04:05",
	"_2 January 2006 15:04:05",
	"_2 January 06 15:04:05",
	"_2 Jan 2006 15:04:05",
	"_2 Jan 06 15:04:05",
	"2 Jan 2006 15:04:05",
	"02 Jan 2006 15:04:05",
	"02 Jan 06 15:04:05",
	"2 Jan 06 15:04:05",

	"Monday, _2 January 2006 15:04",
	"Monday, _2 January 06 15:04",
	"Mon, _2 January 2006 15:04",
	"Mon, _2 January 06 15:04",
	"Monday, _2 Jan 2006 15:04",
	"Monday, _2 Jan 06 15:04",
	"Mon, 02 Jan 2006 15:04",
	"Mon, 2 Jan 2006 15:04",
	"Mon, _2 Jan 2006 15:04",
	"Mon, 02 Jan 06 15:04",
	"Mon, 2 Jan 06 15:04",
	"Mon, _2 Jan 06 15:04",
	"January _2, 2006 15:04",
	"January _2, 06 15:04",
	"Jan _2, 2006 15:04",
	"Jan _2, 06 15:04",
	"_2 January 2006 15:04",
	"_2 January 06 15:04",
	"_2 Jan 2006 15:04",
	"_2 Jan 06 15:04",
	"02 Jan 06 15:04",

	"Monday, _2 January 2006",
	"Monday, _2 January 06",
	"Mon, _2 January 2006",
	"Mon, _2 January 06",
	"Monday, _2 Jan 2006",
	"Monday, _2 Jan 06",
	"Mon, 2 Jan 2006",
	"Mon, _2 Jan 2006",
	"Mon, 2 Jan 06",
	"Mon, _2 Jan 06",
	"January _2, 2006",
	"January _2, 06",
	"Jan _2, 2006",
	"Jan _2, 06",
	"_2 January 2006",
	"_2 January 06",
	"_2 Jan 2006",
	"_2 Jan 06",
	"2 Jan 2006",
	"2 Jan 06",

	// Special formats with malformed time
	"Mon, 2 Jan 2006 15:4:05 -0700",
	"Mon, 2 Jan 2006 15:4:5 -0700",
	"Mon, _2 Jan 2006 15:4:05 -0700",
	"Mon, _2 Jan 2006 15:4:5 -0700",
	"Mon, _2 Jan 2006 15: 4:05 MST",
	"Mon, _2 Jan 2006 15:4:05 MST",
	"Mon, _2 Jan 2006 15:4:5 MST",
	"Mon, _2 Jan 06 15:4:05 MST",
	"Mon, _2 Jan 06 15:4:5 MST",
	"Mon, _2 Jan 2006 15:4:05",
	"Mon, _2 Jan 2006 15:4:5",
	"Mon, _2 Jan 06 15:4:05",
	"Mon, _2 Jan 06 15:4:5",
	"2 Jan 2006 15:4:05 -0700",
	"2 Jan 2006 15:4:5 -0700",
	"_2 Jan 2006 15:4:5",
	"_2 Jan 06 15:4:05 MST",
	"_2 Jan 06 15:4:5 MST",

	// Weird date formats
	"Mon, _2 Jan 2006 15:04:05 UNDEFINED",
	"Mon, _2 Jan 06 15:04:05 UNDEFINED",
	"Mon, _2 Jan 2006 15:04:05 LOCAL",
	"Mon, _2 Jan 06 15:04:05 LOCAL",
	"January _2, 2006 15:04:05 PM MST",
	"Jan _2, 2006 15:04:05 PM MST",
	"Monday,_2 Jan 2006 15:04:05 PM MST",
	"Mon _2 Jan 2006 15:04:05 PM MST",

	// Timezone variations
	"Mon,_2 Jan 2006 15:04:05 MST -0700",
	"Mon, _2 January 2006 15:04:05 -0700",
	"Monday, _2 January 2006 15:04:05 MST",
	"Mon, Jan _2 2006 15:04:05 MST-0700",
	"Mon, _2 Jan 2006 15 04 05 MST -0700",
	"Mon, _2 Jan 2006 15 04 05 -0700",
	"_2-Jan-2006 15:04:05 MST -0700",
	"_2-Jan-2006 15:04:05 -0700",
	"_2-Jan-2006 15:04:05 MST",
	"_2 Jan 2006 15:04:05 -0700",

	// No comma variations
	"Mon,02 Jan 2006 15:04:05 -0700",
	"Mon ,02 Jan 2006 15:04:05 -0700",
	"Mon, 02Jan2006 15:04:05 -0700",
	"Mon, 02 Jan2006 15:04:05 -0700",
	"Mon,_2 Jan 2006 15:04:05-0700",
	"Mon,_2 Jan 2006 15:04:05 MST",
	"Mon,_2 Jan 06 15:04:05 -0700",
	"Mon,_2 Jan 06 15:04:05 MST",
	"Mon,_2 Jan 2006 15:04:05",
	"Mon,28 Sep 94 16:11:41 GMT",
	"Fri,28 Sep 94 16:11:41 GMT",

	// Special separators and formats
	"Mon, 02 Jan 2006  15:04:05 -0700",
	"Mon, 02 Jan 2006 15:04:05  -0700",
	"Mon, 02 Jan 2006 15:04:05+0000",
	"Mon, 02 Jan 2006 15:04:05 +0000",
	"Mon, 02 Jan 2006 15:04:05 GMT",
	"Mon, 02 Jan 2006 15:04:05 UTC",
	"Mon, 02 Jan 2006 15:04:05 UT",
	"Mon, 02 Jan 2006 15:04:05 Z",
	"Mon _2 Jan 2006 15:04:05 -0700",
	"Fri 31 Aug 2012 22:45:37 -0400",
	"Wen, 17 May 2006 10:11:41 -0100",
	"Mo, _2 Jan 2006 15:04:05 -0700",
	"Fr, 20 Feb 2004 16:11:20 +0100",

	// Date without weekday variations
	"02-Jan-2006 15:04:05 -0700",
	"02-Jan-2006 15:04:05 MST",
	"02-Jan-2006 15:04:05",
	"2-Jan-2006 15:04:05 -0700",
	"2-Jan-2006 15:04:05 MST",
	"2-Jan-2006 15:04:05",
	"_2Jan 2006 15:04:05 -0700",
	"29Aug 2006 21:11:48 -0600",

	// Very short formats
	"Mon Jan _2 15:04:05 2006",
	"Fri Apr 8 00:49:21 1983",
	", _2 Jan 2006 15:04:5 -0700",
	", _2 Jan 2006 15:4:5 -0700",
	"_2 Jan. 2006 15:04:05",
	"_2-Jan-06 15:04 MST",
	"18-Feb-90 20:52 CST",

	// European timezone variants (MESZ, MEZ, CET, CEST, etc.)
	"Mon, _2 Jan 2006 15:04:05 MESZ",
	"Mon, _2 Jan 06 15:04:05 MESZ",
	"Mon, 2 Jan 2006 15:04:05 MESZ",
	"Mon, 2 Jan 06 15:04:05 MESZ",
	"Mon, 02 Jan 2006 15:04:05 MESZ",
	"Mon, 02 Jan 06 15:04:05 MESZ",
	"Mon, _2 Jan 2006 15:04:05 MEZ",
	"Mon, _2 Jan 06 15:04:05 MEZ",
	"Mon, _2 Jan 2006 15:04:05 CET",
	"Mon, _2 Jan 06 15:04:05 CET",
	"Mon, _2 Jan 2006 15:04:05 CEST",
	"Mon, _2 Jan 06 15:04:05 CEST",

	// Special prefix
	"Date: Tue, _2 Jun 2006 15:04:05 MST",
	"Wes, 23 Jun 2010 11:24:30 -0500",
	"Sum, 2 Jan 2006 15:04:05 MST", // if RunRSLIGHTImport

	// Malformed timezone formats from early 90s
	"_2 Jan 06 15:04:05 +-0700",
	"02 Jan 06 15:04:05 +-0700",
	"_2 Jan 2006 15:04:05 +-0700",
	"02 Jan 2006 15:04:05 +-0700",
	"Mon, _2 Jan 06 15:04:05 +-0700",
	"Mon, 02 Jan 06 15:04:05 +-0700",
	"Mon, _2 Jan 2006 15:04:05 +-0700",
	"Mon, 02 Jan 2006 15:04:05 +-0700",

	// 3-digit timezone formats (missing leading zero) - Go time patterns
	"_2 Jan 06 15:04:05 -700",
	"02 Jan 06 15:04:05 -700",
	"_2 Jan 2006 15:04:05 -700",
	"02 Jan 2006 15:04:05 -700",
	"Mon, _2 Jan 06 15:04:05 -700",
	"Mon, 02 Jan 06 15:04:05 -700",
	"Mon, _2 Jan 2006 15:04:05 -700",
	"Mon, 02 Jan 2006 15:04:05 -700",

	// Single-digit timezone and GMT variants with 2-digit years
	"Mon, _2 Jan 06 15:04:05 -7",
	"Mon, 02 Jan 06 15:04:05 -7",
	"Wed, 24 Nov 06 19:45:40 -1", // Specific layout for Nov 93 format (93 maps to 06 in Go)
	"Fri, 3 Jan 06 12:00:00 GMT", // Specific layout for Jan 05 format (05 maps to 06 in Go)
	"Mon, _2 Jan 06 15:04:05 GMT",
	"Tue, 02 Jan 06 15:04:05 GMT",

	// AM/PM formats with 2-digit years
	"_2 Jan 06 3:04:05 PM",
	"02 Jan 06 3:04:05 PM",
	"_2 January 06 3:04:05 PM",
	"02 January 06 3:04:05 PM",
	"Mon, _2 Jan 06 3:04:05 PM",
	"Mon, 02 Jan 06 3:04:05 PM",
}

// IsValidGroupName validates a newsgroup name according to RFC standards
// Returns true if the group name is valid (lowercase, alphanumeric components separated by dots)
func IsValidGroupName(name string) bool {
	return common.IsValidGroupName(name)
}

func (proc *Processor) extractGroupsFromHeaders(msgID, groupsline string) []string {
	// Use a single regex to split on any combination of separators
	rawGroups := common.SeparatorRegex.Split(groupsline, -1)

	var validGroups []string
	seen := make(map[string]bool, len(rawGroups)) // For deduplication

	for _, group := range rawGroups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if seen[group] {
			continue
		}
		if RunRSLIGHTImport {
			// the rslight sections check needs the batch pointer before validation (this
			// branch only); the non-rslight path below registers a pointer only for names
			// that pass validation (INT-3)
			newsgroupPtr := proc.DB.Batch.GetNewsgroupPointer(group)
			if proc.IsNewsGroupInSectionsDB(newsgroupPtr) {
				if DebugProcessorHeaders {
					log.Printf("[RSLIGHT] extractGroupsFromHeaders Article '%s' newsgroup '%s' IS IN sections DB", msgID, group)
				}
				if !seen[group] {
					seen[group] = true
					validGroups = append(validGroups, group)
				}
				continue
			}
		}
		if !IsValidGroupName(group) {
			// If group name is invalid, log it and skip
			log.Printf("extractGroupsFromHeaders: Invalid group name: %s", group)
			continue
		}

		// Deduplicate using map (faster than slices.Contains)
		seen[group] = true
		validGroups = append(validGroups, group)
	}

	return validGroups
}

/* UNUSED KEEP FOR REFERENCE
func jsonStringValToMultiLineHeader(val string) []string {
	// Split the JSON string by newline characters
	lines := strings.Split(val, "\n")

	// Remove any leading or trailing whitespace from each line
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}

	// Return the cleaned-up lines
	return lines
}
*/

/* UNUSED KEEP FOR REFERENCE
func multiLineHeaderToNewlineString(vals []string) string {
	var sb strings.Builder
	for i, line := range vals {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(line)
	}
	result := sb.String()
	result = strings.TrimSuffix(result, "\n")
	return result
}
*/

/* UNUSED KEEP FOR REFERENCE
func multiLineHeaderToStringHTML(vals []string) string {
	var sb strings.Builder
	for i, line := range vals {
		if i > 0 {
			sb.WriteString("<br>")
		}
		sb.WriteString(line)
	}
	result := sb.String()
	result = strings.TrimSuffix(result, "<br>")
	return result
}
*/

// MaxFutureDate is how far a Date (or Injection-Date) may lie in the future before
// ProcessArticle refuses the article (RFC 5537 §3.7 step 2: 24 hours; A-intake-7).
// bruteForceDateParse keeps its own internal 25 h sanity bound.
var MaxFutureDate = 24 * time.Hour

// ParseNNTPDate parses an NNTP date string to time.Time, handling common NNTP quirks.
// It is lenient about the shape (330 layouts, then brute force on any string that holds
// a year and a month; a missing day or time defaults to the 1st and noon) but invents
// nothing: without a readable year and month it returns the zero time. The rslight
// import falls back to Unix epoch strings.
func ParseNNTPDate(dateStr string) time.Time {
	t, ok := ParseNNTPDateStrict(dateStr)
	if ok || !t.IsZero() {
		return t
	}
	// Last resort for RSLIGHT import. The normalization in ParseNNTPDateStrict only
	// changes strings that contain whitespace or letters, which never parse as an
	// integer, so the epoch parser sees the same digits as before.
	if RunRSLIGHTImport && dateStr != "" {
		return parseUnixEpochToRFC1123(strings.TrimSpace(parenRe.ReplaceAllString(dateStr, "")))
	}
	return time.Time{}
}

// ParseNNTPDateStrict runs the ParseNNTPDate pipeline (normalization, NNTP layouts,
// brute-force extraction) without the rslight epoch fallback. ok is true for a layout
// match or a brute-force result whose year and month were both found in the string;
// otherwise the returned time is the lenient result (possibly zero) and ok is false.
// Non-bulk intake rejects dates with !ok (D10).
func ParseNNTPDateStrict(dateStr string) (time.Time, bool) {
	if dateStr == "" {
		return time.Time{}, false
	}
	// Remove trailing parenthesized timezone, e.g., " (NZDT)"
	dateStr = parenRe.ReplaceAllString(dateStr, "")
	dateStr = strings.TrimSpace(dateStr)

	if RunRSLIGHTImport && strings.HasPrefix(dateStr, "Sum, ") {
		dateStr = strings.ReplaceAll(dateStr, "Sum, ", "Sun, ")
	}

	dateStr = normalizeDateString(dateStr)

	// Try a list of common NNTP date layouts
	for _, layout := range NNTPDateLayouts {
		t, err := time.Parse(layout, dateStr)
		if err == nil {
			return t, true
		}
	}

	// Trailing junk ("<br>", " ,", a glued "From:", an escape sequence) keeps an otherwise
	// regular date from matching a layout: retry with the part up to the time and zone.
	core := strings.TrimSpace(dateCoreRe.FindString(dateStr))
	if core != "" && core != dateStr {
		for _, layout := range NNTPDateLayouts {
			t, err := time.Parse(layout, core)
			if err == nil {
				return t, true
			}
		}
	}

	// If standard parsing failed, try bruteforce extraction: the whole string first (a
	// ctime-style year comes after the time and would be cut off), then the cut one.
	t, ok := bruteForceDateParseOK(dateStr)
	if !ok && core != "" && core != dateStr {
		if tc, okc := bruteForceDateParseOK(core); okc {
			return tc, true
		}
	}
	return t, ok
}

// zoneOffsets maps time zone abbreviations (and the spelled-out US names) to the numeric
// offset Go's layouts understand. time.Parse accepts any letters where a layout says MST
// but silently treats an abbreviation it does not know as UTC, so every abbreviation that
// shows up in archived Date headers must be listed here. Ambiguous ones (IST, AST in
// Asia, BST in Bangladesh, ...) use the Usenet-dominant reading.
var zoneOffsets = map[string]string{
	// North America
	"EST": "-0500", "EDT": "-0400", "CST": "-0600", "CDT": "-0500", "MST": "-0700", "MDT": "-0600",
	"PST": "-0800", "PDT": "-0700", "AKST": "-0900", "AKDT": "-0800", "HST": "-1000", "HDT": "-0900",
	"AST": "-0400", "ADT": "-0300", "NST": "-0330", "NDT": "-0230",
	// Europe
	"GMT": "+0000", "UTC": "+0000", "UT": "+0000", "WET": "+0000", "WEST": "+0100", "BST": "+0100",
	"IST": "+0100", "CET": "+0100", "CEST": "+0200", "MET": "+0100", "MEST": "+0200", "MESZ": "+0200",
	"MEZ": "+0100", "MES": "+0100", "MEZT": "+0200", "MDST": "+0200", "EET": "+0200", "EEST": "+0300",
	"MSK": "+0300", "MSD": "+0400",
	// Asia, Pacific
	"JST": "+0900", "KST": "+0900", "HKT": "+0800", "SGT": "+0800", "AWST": "+0800", "ACST": "+0930",
	"AEST": "+1000", "AEDT": "+1100", "NZST": "+1200", "NZDT": "+1300",
}

// zoneLongNames are the spelled-out zone names some Windows mailers wrote.
var zoneLongNames = []struct{ name, offset string }{
	{"Eastern Daylight Time", "-0400"}, {"Eastern Standard Time", "-0500"},
	{"Central Daylight Time", "-0500"}, {"Central Standard Time", "-0600"},
	{"Mountain Daylight Time", "-0600"}, {"Mountain Standard Time", "-0700"},
	{"Pacific Daylight Time", "-0700"}, {"Pacific Standard Time", "-0800"},
}

func zoneAbbrAlternation() string {
	names := make([]string, 0, len(zoneOffsets))
	for n := range zoneOffsets {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) > len(names[j]) || (len(names[i]) == len(names[j]) && names[i] < names[j])
	})
	return strings.Join(names, "|")
}

// normalizeDateString rewrites the spellings the layouts cannot express: runs of
// whitespace, "91May07", spelled-out and abbreviated zone names (any case), "GMT+1" and
// three-digit offsets all become the plain RFC 5322 forms.
func normalizeDateString(dateStr string) string {
	dateStr = strings.Join(strings.Fields(dateStr), " ")
	if m := yyMonDDRe.FindStringSubmatch(dateStr); m != nil {
		dateStr = m[3] + " " + m[2] + " " + m[1] + dateStr[len(m[0]):]
	}
	for _, z := range zoneLongNames {
		if i := strings.Index(strings.ToLower(dateStr), strings.ToLower(z.name)); i >= 0 {
			dateStr = dateStr[:i] + z.offset + dateStr[i+len(z.name):]
			break
		}
	}
	if m := gmtOffsetRe.FindStringSubmatchIndex(dateStr); m != nil {
		sign := dateStr[m[2]:m[3]]
		h := dateStr[m[4]:m[5]]
		if len(h) == 1 {
			h = "0" + h
		}
		mm := "00"
		if m[6] >= 0 {
			mm = dateStr[m[6]:m[7]]
		}
		dateStr = dateStr[:m[0]] + sign + h + mm + dateStr[m[1]:]
	}
	dateStr = zoneAbbrRe.ReplaceAllStringFunc(dateStr, func(abbr string) string {
		return zoneOffsets[strings.ToUpper(abbr)]
	})
	// Fix 3-digit timezone formats like +200 -> +0200
	if match := threeDigitTimezoneRe.FindStringSubmatch(dateStr); len(match) == 3 {
		dateStr = threeDigitTimezoneRe.ReplaceAllString(dateStr, " "+match[1]+"0"+match[2])
	}
	return dateStr
}

// fallbackDateHeaders are tried, in order, when the Date header is missing or unreadable
// on a bulk-mode (archive) article: the B-news "Posted:" and "Date-Received:" of the utzoo
// era, then the trace dates of later software. Each value goes through the same parser.
var fallbackDateHeaders = []string{"posted", "date-received", "nntp-posting-date", "injection-date", "x-original-date"}

// fallbackDate returns the first readable secondary date header and its name, or the zero
// time and "" when none of fallbackDateHeaders parses.
func fallbackDate(headers map[string][]string) (time.Time, string) {
	for _, name := range fallbackDateHeaders {
		if v := common.GetHeaderFirst(headers, name); v != "" {
			if t, ok := ParseNNTPDateStrict(v); ok {
				return t, name
			}
		}
	}
	return time.Time{}, ""
}

func parseUnixEpochToRFC1123(epochStr string) time.Time {
	// Parse the epoch string to int64
	epoch, err := strconv.ParseInt(epochStr, 10, 64)
	if err != nil {
		return time.Time{}
	}
	if epoch < 473385600 { // January 1, 1985 00:00:00 UTC
		return time.Unix(473385600, 0).UTC()
	}
	// Convert to time.Time and format as RFC1123
	return time.Unix(epoch, 0).UTC()
}

// countLines counts the number of lines in a byte slice
func countLines(b []byte) int {
	count := 0
	for _, c := range b {
		if c == '\n' {
			count++
		}
	}
	return count
}

// bruteForceDateParse attempts to extract date components from malformed date strings
// This function tries to parse human-readable but non-standard date formats
func bruteForceDateParse(dateStr string) time.Time {
	t, _ := bruteForceDateParseOK(dateStr)
	return t
}

// bruteForceDateParseOK is bruteForceDateParse that also reports success: ok is false,
// with the zero time, when the string holds no year (1900-2099) or no month.
func bruteForceDateParseOK(dateStr string) (time.Time, bool) {
	if dateStr == "" {
		return time.Time{}, false
	}

	// Clean up common issues
	dateStr = strings.TrimSpace(dateStr)
	dateStr = strings.ReplaceAll(dateStr, "  ", " ") // Double spaces to single

	// Remove common prefixes that might interfere
	dateStr = strings.TrimPrefix(dateStr, "Date:")
	dateStr = strings.TrimSpace(dateStr)
	// "Nov10", "8pm", "2011Sr": letters glued to a number
	dateStr = letterDigitRe.ReplaceAllString(dateStr, "$1$3 $2$4")

	// Month name mappings for fuzzy matching
	monthMap := map[string]time.Month{
		"jan": time.January, "january": time.January,
		"feb": time.February, "february": time.February,
		"mar": time.March, "march": time.March,
		"apr": time.April, "april": time.April,
		"may": time.May,
		"jun": time.June, "june": time.June,
		"jul": time.July, "july": time.July,
		"aug": time.August, "august": time.August,
		"sep": time.September, "sept": time.September, "september": time.September,
		"oct": time.October, "october": time.October,
		"nov": time.November, "november": time.November,
		"dec": time.December, "december": time.December,
	}

	// Try to extract year, month, day using regex patterns
	var year, month, day int
	var hour, min, sec int = 12, 0, 0 // Default to noon if no time found

	// Pattern 1: Find 4-digit year (1970-2099)
	if yearMatch := yearRegex.FindString(dateStr); yearMatch != "" {
		if y, err := strconv.Atoi(yearMatch); err == nil {
			year = y
		}
	}

	// Pattern 2: Find month name (case insensitive)
	if monthMatch := monthRegex.FindString(strings.ToLower(dateStr)); monthMatch != "" {
		if m, exists := monthMap[monthMatch]; exists {
			month = int(m)
		}
	}

	// Pattern 3: the day. A number next to the month name wins ("3 Mar", "Mar 3", "Nov10");
	// otherwise the first 1..31 that is not part of a time (next to a colon). A leading
	// zero is allowed ("09-Jan-2007"), and a day may equal the month number ("3 Mar").
	if m := dayBeforeMonthRe.FindStringSubmatch(dateStr); m != nil {
		day, _ = strconv.Atoi(m[1])
	} else if m := dayAfterMonthRe.FindStringSubmatchIndex(dateStr); m != nil && (m[3] >= len(dateStr) || dateStr[m[3]] != ':') {
		day, _ = strconv.Atoi(dateStr[m[2]:m[3]])
	} else {
		for _, loc := range dayRegex.FindAllStringIndex(dateStr, -1) {
			if (loc[0] > 0 && dateStr[loc[0]-1] == ':') || (loc[1] < len(dateStr) && dateStr[loc[1]] == ':') {
				continue // hour, minute or second
			}
			if d, err := strconv.Atoi(dateStr[loc[0]:loc[1]]); err == nil && d >= 1 && d <= 31 {
				day = d
				break
			}
		}
	}

	// Pattern 4: Try to find time components HH:MM:SS or HH:MM
	if timeMatch := timeRegex.FindStringSubmatch(dateStr); len(timeMatch) >= 3 {
		if h, err := strconv.Atoi(timeMatch[1]); err == nil && h >= 0 && h <= 23 {
			hour = h
		}
		if m, err := strconv.Atoi(timeMatch[2]); err == nil && m >= 0 && m <= 59 {
			min = m
		}
		if len(timeMatch) > 3 && timeMatch[3] != "" {
			if s, err := strconv.Atoi(timeMatch[3]); err == nil && s >= 0 && s <= 59 {
				sec = s
			}
		}
		// "8:17 pm", "12:56 am"
		if m := ampmRe.FindStringSubmatch(dateStr); m != nil && hour <= 12 {
			if strings.EqualFold(m[1], "p") && hour < 12 {
				hour += 12
			} else if strings.EqualFold(m[1], "a") && hour == 12 {
				hour = 0
			}
		}
	}

	// If we couldn't find a year, try to extract 2-digit year and guess the century
	if year == 0 {
		// Look for all 2-digit numbers and filter intelligently
		allMatches := twoDigitYearRegex.FindAllString(dateStr, -1)

		// First pass: look for numbers >= 60 (clearly years from 1960s-1990s)
		for _, match := range allMatches {
			if y, err := strconv.Atoi(match); err == nil {
				if y >= 60 { // Likely a year if >= 60
					if y >= 69 {
						year = 1900 + y
					} else {
						year = 2000 + y
					}
					break
				}
			}
		}

		// Second pass: look for numbers that could be 2000s years (32-59 range)
		if year == 0 {
			for _, match := range allMatches {
				if y, err := strconv.Atoi(match); err == nil {
					if y >= 32 && y <= 59 {
						year = 2000 + y
						break
					}
				}
			}
		}

		// Third pass: handle ambiguous cases (00-31) - be more selective about skipping
		if year == 0 && len(allMatches) > 0 {
			for _, match := range allMatches {
				if y, err := strconv.Atoi(match); err == nil {
					// Skip numbers that are definitely day/month if we already have them
					if (y == day && day > 0) || (y == month && month > 0) {
						continue
					}
					// Skip obvious time components (appeared in HH:MM:SS context)
					if y <= 23 && strings.Contains(dateStr, fmt.Sprintf(":%02d", y)) {
						continue
					}
					if y <= 59 && (strings.Contains(dateStr, fmt.Sprintf(":%02d:", y)) || strings.Contains(dateStr, fmt.Sprintf(":%02d ", y))) {
						continue
					}
					// Apply 2-digit year logic
					if y >= 69 {
						year = 1900 + y
					} else {
						year = 2000 + y
					}
					break
				}
			}
		}
	}

	// If we still don't have enough components, try alternative patterns
	if year == 0 || month == 0 || day == 0 {
		// Try numeric date patterns like DD/MM/YY or MM/DD/YY or YYYY/MM/DD
		if numMatch := numericRegex.FindStringSubmatch(dateStr); len(numMatch) == 4 {
			part1, _ := strconv.Atoi(numMatch[1])
			part2, _ := strconv.Atoi(numMatch[2])
			part3, _ := strconv.Atoi(numMatch[3])

			// Determine which part is year, month, day
			if part1 > 1900 { // First part is year: YYYY/MM/DD
				year, month, day = part1, part2, part3
			} else if part3 > 1900 { // Last part is year: DD/MM/YYYY or MM/DD/YYYY
				year = part3
				// Heuristic: if part1 > 12, it's probably day/month, else month/day
				if part1 > 12 {
					day, month = part1, part2
				} else if part2 > 12 {
					month, day = part1, part2
				} else {
					// Ambiguous, default to MM/DD format
					month, day = part1, part2
				}
			} else { // Two-digit year
				if part3 >= 69 {
					year = 1900 + part3
				} else {
					year = 2000 + part3
				}
				if part1 > 12 {
					day, month = part1, part2
				} else {
					month, day = part1, part2
				}
			}
		}
	}

	// No year or no month in the string means no date: nothing is invented (the old
	// "1990-01-01 12:00" default filed unreadable archive dates on a day nobody chose;
	// the bulk intake tries the secondary date headers instead, see fallbackDate).
	if year < 1900 || year > 2099 || month < 1 || month > 12 {
		return time.Time{}, false
	}
	if day < 1 || day > 31 {
		day = 1 // month precision ("Oct 86")
	}

	// Validate day against month
	if month == 2 && day > 29 {
		day = 28
	} else if (month == 4 || month == 6 || month == 9 || month == 11) && day > 30 {
		day = 30
	}

	{
		parsedDate := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
		// "31 Jan 1995 02:37 -0500": the wall clock is local, the offset makes it UTC
		if m := numOffsetRe.FindStringSubmatch(dateStr); m != nil {
			oh, _ := strconv.Atoi(m[2])
			om, _ := strconv.Atoi(m[3])
			if oh <= 14 && om < 60 {
				off := time.Duration(oh)*time.Hour + time.Duration(om)*time.Minute
				if m[1] == "+" {
					parsedDate = parsedDate.Add(-off)
				} else {
					parsedDate = parsedDate.Add(off)
				}
			}
		}

		// Sanity check: parsed date cannot be more than 25 hours in the future
		now := time.Now().UTC()
		maxFutureTime := now.Add(25 * time.Hour)

		if parsedDate.After(maxFutureTime) {
			// Date is too far in the future, likely a parsing error
			return time.Time{}, false
		}

		return parsedDate, true
	}
}
