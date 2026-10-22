package processor

import (
	"testing"
	"time"
)

// TestDateParseArchiveShapes pins ParseNNTPDateStrict on Date header shapes taken from
// real archives (1986-2016: archive.org Atari groups, UAT exports, Japanese and European
// sites). Every case was wrong or dropped before 2026-10-22: a day with a leading zero
// became the 1st, a day equal to the month number was skipped, am/pm and numeric offsets
// were ignored once the brute-force parser ran, non-US zone names counted as UTC, and
// trailing junk pushed regular dates into the brute-force path. want is UTC.
func TestDateParseArchiveShapes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		// leading-zero day, day equal to the month number
		{"09-Jan-2007", "2007-01-09 12:00:00", true},
		{"05-Jan-2006 05:57", "2006-01-05 05:57:00", true},
		{"06 Jun 2006 1010", "2006-06-06 12:00:00", true},
		{"3  Mar 89 11:09 -0100", "1989-03-03 12:09:00", true},
		{"Thu,  4 Apr 96 01:20:00 UTC 0000", "1996-04-04 01:20:00", true},
		{"Tue, 03 Mar 92 16:54:26   M", "1992-03-03 16:54:26", true},
		{"Mon 02 Mar 1987 21:18 CST", "1987-03-03 03:18:00", true},
		{"Tuesday, June 06, 2000 1:54 PM", "2000-06-06 13:54:00", true},
		{"Sunday, September 26th 1999", "1999-09-26 12:00:00", true},
		{"7th May 1993", "1993-05-07 12:00:00", true},
		{"Wed,Nov10 2004 11:00 AM", "2004-11-10 11:00:00", true},
		// zone abbreviations on shapes the layouts do not cover (brute force applies the offset)
		{"Tue, 24-Nov-87 07:34:45 EST", "1987-11-24 12:34:45", true},
		{"Mon, 30-Nov-87 23:34:10 EST", "1987-12-01 04:34:10", true},
		{"Mon, 10-Aug-87 11:07:00 EDT", "1987-08-10 15:07:00", true},
		{"Thu 15 Oct 87 10:29:58 PDT", "1987-10-15 17:29:58", true},
		{"4 April 1988, 10:38:22 PST", "1988-04-04 18:38:22", true},
		{"31 Jan 1995 02:37 -0500", "1995-01-31 07:37:00", true},
		{"9 feb. 2005 14:13:40 -0800", "2005-02-09 22:13:40", true},
		{"Thu, 06 Oct 2005 07:22:32 +09:00", "2005-10-05 22:22:32", true},
		{"Fri, 1 Mar 85 12:45:53 est", "1985-03-01 17:45:53", true},
		// European and other zone names
		{"Fri, 24 Sep 93 11:00:07 CET", "1993-09-24 10:00:07", true},
		{"Thu, 20 Oct 1994 22:25:08 MET", "1994-10-20 21:25:08", true},
		{"Wed, 30 Nov 94 09:02:54 MEZ", "1994-11-30 08:02:54", true},
		{"Sat, 23 May 1992 18:46:50 MEZT", "1992-05-23 16:46:50", true},
		{"Mon, 06 Jan 86 13:01 MET", "1986-01-06 12:01:00", true},
		{"Fri, 5 May 95 13:23 BST-1", "1995-05-05 12:23:00", true},
		{"15 Oct 94 09:09 GMT+0300", "1994-10-15 06:09:00", true},
		{"Thu, 10 Oct 96 14:13 GMT+1300", "1996-10-10 01:13:00", true},
		{"Mon, 30 Mar 1992 07:04:43 GMT+1", "1992-03-30 06:04:43", true},
		{"6/29/02 6:07:13 PM Eastern Daylight Time", "2002-06-29 22:07:13", true},
		// am/pm
		{"3/22/2005 1:04:26 PM", "2005-03-22 13:04:26", true},
		{"6/12/2005 10:23:25 PM", "2005-06-12 22:23:25", true},
		{"Apr 1, 8:17 pm", "0001-01-01 00:00:00", false}, // no year: no date
		{"91May07 12:56 am", "1991-05-07 00:56:00", true},
		{"91Apr30 8:58 pm EST", "1991-05-01 01:58:00", true},
		// trailing junk
		{"Fri, 09 May 2008 23:24:58 -0400<BR>", "2008-05-10 03:24:58", true},
		{"Tue, 8 Aug 2000 19:17:23 -0600 From: Richard Fagerlund", "2000-08-09 01:17:23", true},
		{"Thu, 12 Jan 2006 09:48:22 +0900 ,", "2006-01-12 00:48:22", true},
		{"Sat, 07 Oct 2000 19:17:16 +0900\x1b$B$+$i\x1b(B", "2000-10-07 10:17:16", true},
		// ctime style with the year last, zone in the middle: must not lose the year
		{"Thu Mar  9 13:25:56 1989 CST", "1989-03-09 19:25:56", true},
		{"Wed Jul 13 08:37:07 PDT 1994", "1994-07-13 15:37:07", true},
		{"Wed Sep 27 18:07:23 MET 1995", "1995-09-27 17:07:23", true},
		{"Thu. May  4 18:27:24 1995", "1995-05-04 18:27:24", true},
		// unchanged behaviour
		{"Mon, 27 Oct 1986 11:22:58 +0000", "1986-10-27 11:22:58", true},
		{"Tue, 28-Oct-86 19:22:41 EST", "1986-10-29 00:22:41", true},
		{"Mon Oct 27 11:22:58 1986", "1986-10-27 11:22:58", true},
		{"10 Mar 86 1:36:00 EST", "1986-03-10 06:36:00", true},
		{"Sat, 1 Jan 1983", "1983-01-01 00:00:00", true},
		{"Date: unknown", "0001-01-01 00:00:00", false},
		{"Fri, 13 Feb 1969 23:59:59 GMT", "1969-02-13 23:59:59", true},
		{"26 Mar 1962 12:00:00 GMT (sic)", "1962-03-26 12:00:00", true},
		{"08-Nov-2011Sr. Biology Professional", "2011-11-08 12:00:00", true},
		{"18th August 2009", "2009-08-18 12:00:00", true},
	} {
		got, ok := ParseNNTPDateStrict(tc.in)
		if ok != tc.ok {
			t.Errorf("%q: ok=%v, want %v (got %v)", tc.in, ok, tc.ok, got.UTC())
			continue
		}
		if g := got.UTC().Format("2006-01-02 15:04:05"); g != tc.want {
			t.Errorf("%q = %s, want %s", tc.in, g, tc.want)
		}
		if l := ParseNNTPDate(tc.in); !l.Equal(got) && !(got.IsZero() && l.IsZero()) {
			t.Errorf("%q: lenient %v differs from strict %v", tc.in, l, got)
		}
	}
	// the strict flag stays false for a date without a readable year and month
	if _, ok := ParseNNTPDateStrict("14th century"); ok {
		t.Error("\"14th century\" reported as a good parse")
	}
}

// TestDateParseFallbackHeaders: an archive article whose Date is unreadable takes its
// date from Posted (B-news), Date-Received or the trace headers, in that order.
func TestDateParseFallbackHeaders(t *testing.T) {
	for _, tc := range []struct {
		headers  map[string][]string
		want     string
		wantName string
	}{
		{map[string][]string{"date": {"garbage"}, "posted": {"Mon Oct 27 11:22:58 1986"}, "date-received": {"Tue, 28-Oct-86 19:22:41 EST"}}, "1986-10-27 11:22:58", "posted"},
		{map[string][]string{"date-received": {"Tue, 28-Oct-86 19:22:41 EST"}}, "1986-10-29 00:22:41", "date-received"},
		{map[string][]string{"posted": {"no date"}, "nntp-posting-date": {"Sat, 11 Oct 2026 12:00:00 +0000"}}, "2026-10-11 12:00:00", "nntp-posting-date"},
		{map[string][]string{"injection-date": {"Sat, 11 Oct 2026 12:00:00 +0000"}, "x-original-date": {"Mon Oct 27 11:22:58 1986"}}, "2026-10-11 12:00:00", "injection-date"},
		{map[string][]string{"date": {"garbage"}}, "0001-01-01 00:00:00", ""},
		{nil, "0001-01-01 00:00:00", ""},
		// utzoo-era ctime(-1) in Posted is skipped like garbage; Date-Received is real
		{map[string][]string{"posted": {"Wed Dec 31 18:59:59 1969"}, "date-received": {"Sat, 8-Aug-87 11:47:19 EDT"}}, "1987-08-08 15:47:19", "date-received"},
		{map[string][]string{"posted": {"Wed Dec 31 18:59:59 1969"}}, "0001-01-01 00:00:00", ""},
	} {
		got, name := fallbackDate(tc.headers)
		if g := got.UTC().Format("2006-01-02 15:04:05"); g != tc.want || name != tc.wantName {
			t.Errorf("fallbackDate(%v) = %s via %q, want %s via %q", tc.headers, g, name, tc.want, tc.wantName)
		}
	}
}

// TestDateParseBulkArticleDate: the bulk-mode date choice. A Date from the Usenet era wins;
// a pre-1980 Date (ctime(-1) from a 1987 B-news site, as found in comp.lang.c articles 1-18
// of the utzoo archive) or an unreadable one yields to the first usable fallback header; a
// pre-1980 Date without a fallback is kept so the article is still filed.
func TestDateParseBulkArticleDate(t *testing.T) {
	epochMinusOne := "Wed, 31-Dec-69 18:59:59 EDT"
	for _, tc := range []struct {
		date     string
		headers  map[string][]string
		want     string
		wantName string
	}{
		{"Tue, 28 Oct 1986 19:22:41 -0500", map[string][]string{"date-received": {"Sat, 8-Aug-87 11:47:19 EDT"}}, "1986-10-29 00:22:41", ""},
		{"Tue, 28 Oct 1986 19:22:41 -0500", nil, "1986-10-29 00:22:41", ""},
		{"Tue, 1 Jan 1980 00:00:00 +0000", map[string][]string{"date-received": {"Sat, 8-Aug-87 11:47:19 EDT"}}, "1980-01-01 00:00:00", ""},
		{epochMinusOne, map[string][]string{"posted": {"Wed Dec 31 18:59:59 1969"}, "date-received": {"Sat, 8-Aug-87 11:47:19 EDT"}}, "1987-08-08 15:47:19", "date-received"},
		{"Mon, 31 Dec 1979 23:59:59 +0000", map[string][]string{"nntp-posting-date": {"Sat, 11 Oct 2026 12:00:00 +0000"}}, "2026-10-11 12:00:00", "nntp-posting-date"},
		{epochMinusOne, nil, "1969-12-31 22:59:59", ""},
		{"garbage", map[string][]string{"posted": {"Mon Oct 27 11:22:58 1986"}}, "1986-10-27 11:22:58", "posted"},
		{"garbage", nil, "0001-01-01 00:00:00", ""},
		{"", map[string][]string{"date-received": {"Sat, 8-Aug-87 11:47:19 EDT"}}, "1987-08-08 15:47:19", "date-received"},
	} {
		got, name := bulkArticleDate(tc.date, tc.headers)
		if g := got.UTC().Format("2006-01-02 15:04:05"); g != tc.want || name != tc.wantName {
			t.Errorf("bulkArticleDate(%q, %v) = %s via %q, want %s via %q", tc.date, tc.headers, g, name, tc.want, tc.wantName)
		}
	}
	if !minArchiveDate.Equal(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("minArchiveDate = %v, want 1980-01-01 UTC", minArchiveDate)
	}
}
