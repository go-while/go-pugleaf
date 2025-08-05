package processor

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	separatorRegex       = regexp.MustCompile(`[,;:\s]+`)
	parenRe              = regexp.MustCompile(`\s*\([^)]*\)$`)
	threeDigitTimezoneRe = regexp.MustCompile(`\s([+-])(\d{3})\s*$`)
	yearRegex            = regexp.MustCompile(`\b(19[7-9]\d|20[0-9]\d)\b`)
	monthRegex           = regexp.MustCompile(`\b(jan|january|feb|february|mar|march|apr|april|may|jun|june|jul|july|aug|august|sep|sept|september|oct|october|nov|november|dec|december)\b`)
	dayRegex             = regexp.MustCompile(`\b([1-9]|[12]\d|3[01])\b`)
	timeRegex            = regexp.MustCompile(`\b(\d{1,2}):(\d{1,2})(?::(\d{1,2}))?\b`)
	twoDigitYearRegex    = regexp.MustCompile(`\b([0-9]\d)\b`)
	numericRegex         = regexp.MustCompile(`\b(\d{1,4})[\/\-\.](\d{1,2})[\/\-\.](\d{1,4})\b`)
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
	if validGroupNameRegexchar.MatchString(name) {
		return true
	}

	if !UseStrictGroupValidation {

		if validGroupNameRegexLazy.MatchString(name) {
			return true
		}
		if validGroupNameRegexSingle.MatchString(name) {
			return true
		}
		// Allow both lowercase and mixed case group names
		if validGroupNameRegexCaps.MatchString(name) {
			return true
		}
		return false
	}
	if len(name) < 1 {
		log.Printf("IsValidGroupName: Group name '%s' is too short (%d characters)", name, len(name))
		return false
	}
	if len(name) > 255 {
		log.Printf("IsValidGroupName: Group name '%s' is too long (%d characters)", name, len(name))
		return false
	}
	name = strings.ToLower(name)
	// Special case for programming language groups ending with ++
	if strings.HasSuffix(name, "++") || strings.HasSuffix(name, "+") {
		// Allow C++, C+, etc. in programming contexts
		if validGroupNameRegexLazy.MatchString(strings.ReplaceAll(name, "+", "")) {
			return true
		}
	}
	if validGroupNameRegexStrict.MatchString(name) {
		return true
	}
	return false
}

func (proc *Processor) extractGroupsFromHeaders(msgID, groupsline string) []string {
	// Use a single regex to split on any combination of separators
	rawGroups := separatorRegex.Split(groupsline, -1)

	var validGroups []string
	seen := make(map[string]bool) // For deduplication

	for _, group := range rawGroups {
		group = strings.TrimSpace(group)
		newsgroupPtr := proc.DB.Batch.GetNewsgroupPointer(group) // Ensure group is registered in the batch
		if group == "" {
			continue
		}
		if seen[group] {
			continue
		}
		if RunRSLIGHTImport {
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
		if !seen[group] {
			seen[group] = true
			validGroups = append(validGroups, group)
		}
	}

	return validGroups
}

func multiLineHeaderToMergedString(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) == 1 {
		return vals[0] // Fast path for single-line headers (most common case)
	}
	return strings.Join(vals, "\n") // Ultra fast for multi-line
}

func multiLineStringToSlice(input string) []string {
	// Replace newlines with spaces, trim leading/trailing spaces
	result := strings.Split(input, "\n")
	return result
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

/* UNUSED KEEP FOR REFERENCE
func multiLineHeaderToStringSpaced(vals []string) string {
	var sb strings.Builder
	for i, line := range vals {
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(line)
	}
	result := sb.String()
	result = strings.TrimSuffix(result, " ")
	return result
}
*/

// getHeaderFirst returns the first value for a header, or "" if not present
func getHeaderFirst(headers map[string][]string, key string) string {
	if vals, ok := headers[key]; ok && len(vals) > 0 {
		// For headers that can be folded across multiple lines (like References),
		// we need to join with spaces instead of newlines to properly unfold them
		if key == "references" || key == "References" || key == "in-reply-to" || key == "In-Reply-To" {
			return multiLineHeaderToStringSpaced(vals)
		}
		return multiLineHeaderToMergedString(vals)
	}
	return ""
}

// multiLineHeaderToStringSpaced joins multi-line headers with spaces (for RFC-compliant header unfolding)
func multiLineHeaderToStringSpaced(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) == 1 {
		return vals[0] // Fast path for single-line headers
	}
	var sb strings.Builder
	for i, line := range vals {
		// Trim each line and add spaces between them
		line = strings.TrimSpace(line)
		if line == "" {
			continue // Skip empty lines
		}
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// parseNNTPDate parses an NNTP date string to time.Time, handling common NNTP quirks.
func ParseNNTPDate(dateStr string) time.Time {
	if dateStr == "" {
		return time.Time{}
	}
	// Remove trailing parenthesized timezone, e.g., " (NZDT)"
	dateStr = parenRe.ReplaceAllString(dateStr, "")
	dateStr = strings.TrimSpace(dateStr)

	if RunRSLIGHTImport && strings.HasPrefix(dateStr, "Sum, ") {
		dateStr = strings.ReplaceAll(dateStr, "Sum, ", "Sun, ")
	}

	// Fix 3-digit timezone formats like +200 -> +0200
	if match := threeDigitTimezoneRe.FindStringSubmatch(dateStr); len(match) == 3 {
		sign := match[1]
		digits := match[2]
		// Convert 3-digit to 4-digit by adding leading zero
		normalizedTz := fmt.Sprintf("%s0%s", sign, digits)
		dateStr = threeDigitTimezoneRe.ReplaceAllString(dateStr, " "+normalizedTz)
	}

	// Try a list of common NNTP date layouts
	for _, layout := range NNTPDateLayouts {
		t, err := time.Parse(layout, dateStr)
		if err == nil {
			return t
		}
	}

	// If standard parsing failed, try bruteforce extraction
	if t := bruteForceDateParse(dateStr); !t.IsZero() {
		return t
	}

	// Last resort for RSLIGHT import
	if RunRSLIGHTImport {
		return parseUnixEpochToRFC1123(dateStr)
	}
	return time.Time{}
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
	if dateStr == "" {
		return time.Time{}
	}

	// Clean up common issues
	dateStr = strings.TrimSpace(dateStr)
	dateStr = strings.ReplaceAll(dateStr, "  ", " ") // Double spaces to single

	// Remove common prefixes that might interfere
	dateStr = strings.TrimPrefix(dateStr, "Date:")
	dateStr = strings.TrimSpace(dateStr)

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

	// Pattern 3: Find day (1-31, but be flexible)
	dayMatches := dayRegex.FindAllString(dateStr, -1)
	for _, dayMatch := range dayMatches {
		if d, err := strconv.Atoi(dayMatch); err == nil && d >= 1 && d <= 31 {
			// Make sure this isn't the year we already found
			if d != year && d != month {
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

	// Final validation and fallbacks
	if year < 1970 || year > 2099 {
		year = 1990 // Default fallback year
	}
	if month < 1 || month > 12 {
		month = 1 // Default to January
	}
	if day < 1 || day > 31 {
		day = 1 // Default to 1st
	}

	// Validate day against month
	if month == 2 && day > 29 {
		day = 28
	} else if (month == 4 || month == 6 || month == 9 || month == 11) && day > 30 {
		day = 30
	}

	// If we have at least year and month, create a date
	if year >= 1970 && month >= 1 && month <= 12 {
		parsedDate := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)

		// Sanity check: parsed date cannot be more than 25 hours in the future
		now := time.Now().UTC()
		maxFutureTime := now.Add(25 * time.Hour)

		if parsedDate.After(maxFutureTime) {
			// Date is too far in the future, likely a parsing error
			return time.Time{}
		}

		return parsedDate
	}

	return time.Time{}
}
