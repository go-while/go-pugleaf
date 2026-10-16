package history

import (
	"crypto/md5"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// Plan xref-history: the history row stores "groupID:articleNum" tokens; plain "groupID"
// tokens of rows written before keep working.

// histXrefInsertRaw writes a row as an older version (or a rebuild) would have left it
func histXrefInsertRaw(t *testing.T, h *History, messageID string, newsgroups any) {
	t.Helper()
	dbIndex, tableName, err := h.routeHash(messageID)
	if err != nil {
		t.Fatal(err)
	}
	db, err := h.db.GetShardedDB(dbIndex, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO "+tableName+"(message_id,newsgroups) VALUES(?,?)", messageID, newsgroups); err != nil {
		t.Fatalf("insert raw row %s: %v", messageID, err)
	}
}

// histXrefRaw returns the row's raw CSV; "<none>" when the row does not exist
func histXrefRaw(t *testing.T, dir, messageID string) string {
	t.Helper()
	raw, found := histTestRawRow(t, dir, messageID)
	if !found {
		return "<none>"
	}
	return raw.String
}

func histXrefLocations(t *testing.T, h *History, messageID string) []Location {
	t.Helper()
	locs, err := h.LookupLocations(messageID)
	if err != nil {
		t.Fatalf("LookupLocations(%s): %v", messageID, err)
	}
	return locs
}

func TestHistoryXref(t *testing.T) {
	h, dir := histTestNew(t, nil)

	t.Run("AddWithNumberLookupReturnsNumber", func(t *testing.T) {
		m := "<xref-add-num@test>"
		h.AddArticleAt(m, 12, 4711)
		h.AddArticleAt(m, 57, 88)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "12:4711,57:88" {
			t.Fatalf("raw = %q, want \"12:4711,57:88\"", raw)
		}
		want := []Location{{GroupID: 12, ArticleNum: 4711}, {GroupID: 57, ArticleNum: 88}}
		if got := histXrefLocations(t, h, m); !reflect.DeepEqual(got, want) {
			t.Fatalf("locations = %+v, want %+v", got, want)
		}
		if got := histTestGroups(t, h, m); !reflect.DeepEqual(got, []int64{12, 57}) {
			t.Fatalf("groups = %v, want [12 57]", got)
		}
		if ok, err := h.Exists(m); err != nil || !ok {
			t.Fatalf("Exists = %v, %v", ok, err)
		}
	})

	t.Run("AddWithoutNumberKeepsPlainToken", func(t *testing.T) {
		m := "<xref-add-plain@test>"
		h.AddArticle(m, 5)
		h.AddArticleAt(m, 6, 0)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "5,6" {
			t.Fatalf("raw = %q, want \"5,6\"", raw)
		}
		want := []Location{{GroupID: 5}, {GroupID: 6}}
		if got := histXrefLocations(t, h, m); !reflect.DeepEqual(got, want) {
			t.Fatalf("locations = %+v, want %+v", got, want)
		}
	})

	t.Run("AddIdempotent", func(t *testing.T) {
		cases := []struct {
			name string
			id   string
			pre  any // raw row before the adds; nil = no row
			add  func(m string)
			want string
		}{
			{"same number twice", "<xref-idem-1@test>", nil, func(m string) {
				h.AddArticleAt(m, 3, 9)
				h.AddArticleAt(m, 3, 9)
			}, "3:9"},
			{"different number: the latest numbered add wins", "<xref-idem-2@test>", nil, func(m string) {
				h.AddArticleAt(m, 3, 9)
				h.AddArticleAt(m, 3, 10)
			}, "3:10"},
			{"numbered then plain", "<xref-idem-3@test>", nil, func(m string) {
				h.AddArticleAt(m, 4, 5)
				h.AddArticle(m, 4)
			}, "4:5"},
			{"plain then numbered upgrades", "<xref-idem-4@test>", nil, func(m string) {
				h.AddArticle(m, 4)
				h.AddArticleAt(m, 4, 5)
				h.AddArticle(m, 4)
				h.AddArticleAt(m, 4, 6)
			}, "4:6"},
			{"mixed row: plain upgraded in place, numbered renumbered, new group appended", "<xref-idem-5@test>", "1,2:20,3", func(m string) {
				h.AddArticleAt(m, 1, 10)
				h.AddArticleAt(m, 2, 21)
				h.AddArticle(m, 3)
				h.AddArticle(m, 2)
				h.AddArticleAt(m, 4, 40)
			}, "1:10,2:21,3,4:40"},
			{"prefix of a numbered token is another group", "<xref-idem-6@test>", "12:5", func(m string) {
				h.AddArticleAt(m, 1, 7)
				h.AddArticleAt(m, 2, 8)
			}, "12:5,1:7,2:8"},
			{"prefix of a plain token is another group", "<xref-idem-7@test>", "12,21", func(m string) {
				h.AddArticleAt(m, 1, 7)
				h.AddArticleAt(m, 2, 8)
			}, "12,21,1:7,2:8"},
			{"upgrade replaces only the exact plain token", "<xref-idem-7b@test>", "12,1,21,11,112", func(m string) {
				h.AddArticleAt(m, 1, 7)
				h.AddArticleAt(m, 11, 3)
				h.AddArticleAt(m, 2, 8)
			}, "12,1:7,21,11:3,112,2:8"},
			{"upgrade of the first and the last token", "<xref-idem-7c@test>", "1,5,11", func(m string) {
				h.AddArticleAt(m, 11, 4)
				h.AddArticleAt(m, 1, 9)
			}, "1:9,5,11:4"},
			{"upgrade of a single plain token", "<xref-idem-7d@test>", "1", func(m string) {
				h.AddArticleAt(m, 1, 1)
			}, "1:1"},
			{"longer id than a known one", "<xref-idem-8@test>", "1:7,2", func(m string) {
				h.AddArticleAt(m, 11, 3)
				h.AddArticleAt(m, 22, 4)
			}, "1:7,2,11:3,22:4"},
			{"legacy empty row", "<xref-idem-9@test>", "", func(m string) {
				h.AddArticleAt(m, 8, 80)
				h.AddArticleAt(m, 8, 81)
			}, "8:81"},
			{"new row", "<xref-idem-10@test>", nil, func(m string) {
				h.AddArticleAt(m, 8, 80)
			}, "8:80"},
		}
		for _, c := range cases {
			if c.pre != nil {
				histXrefInsertRaw(t, h, c.id, c.pre)
			}
			c.add(c.id)
		}
		histTestWaitIdle(t, h)
		for _, c := range cases {
			if raw := histXrefRaw(t, dir, c.id); raw != c.want {
				t.Errorf("%s: raw = %q, want %q", c.name, raw, c.want)
			}
		}
		// legacy NULL row: the number lands like on an empty row
		mNull := "<xref-idem-null@test>"
		histXrefInsertRaw(t, h, mNull, nil)
		h.AddArticleAt(mNull, 8, 80)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, mNull); raw != "8:80" {
			t.Errorf("legacy null row: raw = %q, want \"8:80\"", raw)
		}
	})

	// A group stored before the numbers were recorded has a plain token: the first add with a
	// number (e.g. a history-rebuild over the existing index) upgrades it in place; a later add
	// without a number leaves the known number alone.
	t.Run("PlainTokenUpgradedByNumberedAdd", func(t *testing.T) {
		m := "<xref-plain-upgrade@test>"
		histXrefInsertRaw(t, h, m, "7,9:90")
		h.AddArticle(m, 7) // without a number: stays plain, no duplicate
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "7,9:90" {
			t.Fatalf("after plain add: raw = %q, want \"7,9:90\"", raw)
		}
		h.AddArticleAt(m, 7, 70)
		histTestWaitIdle(t, h)
		h.AddArticle(m, 7) // next flush: never downgraded
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "7:70,9:90" {
			t.Fatalf("raw = %q, want \"7:70,9:90\"", raw)
		}
		want := []Location{{GroupID: 7, ArticleNum: 70}, {GroupID: 9, ArticleNum: 90}}
		if got := histXrefLocations(t, h, m); !reflect.DeepEqual(got, want) {
			t.Fatalf("locations = %+v, want %+v", got, want)
		}
	})

	// A numbered add is authoritative: a history-rebuild over an existing index fixes a group
	// whose numbers changed (recover-db renumbering). Plain adds never touch a known number.
	t.Run("NumberedAddIsAuthoritative", func(t *testing.T) {
		m := "<xref-renumber@test>"
		h.AddArticleAt(m, 3, 9)
		h.AddArticleAt(m, 12, 6)
		histTestWaitIdle(t, h)
		h.AddArticleAt(m, 3, 10) // different number: new number wins, order kept
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "3:10,12:6" {
			t.Fatalf("after renumber: raw = %q, want \"3:10,12:6\"", raw)
		}
		h.AddArticleAt(m, 3, 10) // same number: unchanged
		h.AddArticle(m, 3)       // plain add after numbered: unchanged
		h.AddArticle(m, 12)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "3:10,12:6" {
			t.Fatalf("after same/plain adds: raw = %q, want \"3:10,12:6\"", raw)
		}
		want := []Location{{GroupID: 3, ArticleNum: 10}, {GroupID: 12, ArticleNum: 6}}
		if got := histXrefLocations(t, h, m); !reflect.DeepEqual(got, want) {
			t.Fatalf("locations = %+v, want %+v", got, want)
		}

		// prefix collisions: renumbering group 1 touches neither 12 nor 21 nor 11
		mPrefix := "<xref-renumber-prefix@test>"
		histXrefInsertRaw(t, h, mPrefix, "12:5,1:7,21:8,11")
		h.AddArticleAt(mPrefix, 1, 70)
		// a duplicate group in a foreign row: the first token is rewritten, the others dropped
		mDup := "<xref-renumber-dup@test>"
		histXrefInsertRaw(t, h, mDup, "3:1,4,3:2")
		h.AddArticleAt(mDup, 3, 7)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, mPrefix); raw != "12:5,1:70,21:8,11" {
			t.Errorf("prefix row: raw = %q, want \"12:5,1:70,21:8,11\"", raw)
		}
		if raw := histXrefRaw(t, dir, mDup); raw != "3:7,4" {
			t.Errorf("dup row: raw = %q, want \"3:7,4\"", raw)
		}
	})

	t.Run("RemoveByExactToken", func(t *testing.T) {
		m := "<xref-rm-exact@test>"
		h.AddArticleAt(m, 1, 10)
		h.AddArticleAt(m, 2, 20)
		h.AddArticleAt(m, 3, 30)
		h.AddArticleAt(m, 12, 20)
		histTestWaitIdle(t, h)
		h.RemoveArticleAt(m, 2, 20)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "1:10,3:30,12:20" {
			t.Fatalf("after remove 2:20: raw = %q, want \"1:10,3:30,12:20\"", raw)
		}
		h.RemoveArticleAt(m, 1, 10)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "3:30,12:20" {
			t.Fatalf("after remove 1:10: raw = %q, want \"3:30,12:20\"", raw)
		}
	})

	// one article per (message-id, group): a stored token with another number is stale and goes
	t.Run("RemoveWithDifferentNumberStripsStaleToken", func(t *testing.T) {
		m := "<xref-rm-stale@test>"
		histXrefInsertRaw(t, h, m, "12:4711,1:10,21:7")
		h.RemoveArticleAt(m, 12, 5000)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "1:10,21:7" {
			t.Fatalf("after remove 12:5000: raw = %q, want \"1:10,21:7\"", raw)
		}
		h.RemoveArticleAt(m, 1, 99)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "21:7" {
			t.Fatalf("after remove 1:99: raw = %q, want \"21:7\"", raw)
		}
		// a group the row does not hold: other groups intact, row not deleted
		h.RemoveArticleAt(m, 2, 7)
		h.RemoveArticleAt(m, 7, 21)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "21:7" {
			t.Fatalf("after remove of absent groups: raw = %q, want \"21:7\"", raw)
		}
		h.RemoveArticleAt(m, 21, 8)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "<none>" {
			t.Fatalf("after remove 21:8: raw = %q, want row deleted", raw)
		}
	})

	t.Run("RemoveWithNumberStripsPlainTokenOfGroup", func(t *testing.T) {
		m := "<xref-rm-num-plain@test>"
		histXrefInsertRaw(t, h, m, "1,2:20,11")
		h.RemoveArticleAt(m, 1, 10)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "2:20,11" {
			t.Fatalf("raw = %q, want \"2:20,11\"", raw)
		}
	})

	t.Run("RemoveWithoutNumberStripsBothFormats", func(t *testing.T) {
		m := "<xref-rm-zero@test>"
		histXrefInsertRaw(t, h, m, "1,2:20,12:5,21,3:30")
		h.RemoveArticle(m, 2)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "1,12:5,21,3:30" {
			t.Fatalf("after remove 2: raw = %q, want \"1,12:5,21,3:30\"", raw)
		}
		h.RemoveArticle(m, 1)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "12:5,21,3:30" {
			t.Fatalf("after remove 1: raw = %q, want \"12:5,21,3:30\"", raw)
		}
		// unknown group: row unchanged
		h.RemoveArticleAt(m, 4, 0)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "12:5,21,3:30" {
			t.Fatalf("after remove 4: raw = %q, want unchanged", raw)
		}
		// a duplicate token of one group (not written by this code) is stripped completely
		m2 := "<xref-rm-zero-dup@test>"
		histXrefInsertRaw(t, h, m2, "5,6:60,5:50")
		h.RemoveArticle(m2, 5)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m2); raw != "6:60" {
			t.Fatalf("dup row: raw = %q, want \"6:60\"", raw)
		}
		// missing row: no-op, nothing created
		h.RemoveArticle("<xref-rm-zero-missing@test>", 5)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, "<xref-rm-zero-missing@test>"); raw != "<none>" {
			t.Fatalf("missing row: raw = %q, want no row", raw)
		}
	})

	t.Run("RowDeletedWhenEmpty", func(t *testing.T) {
		mExact := "<xref-empty-exact@test>"
		h.AddArticleAt(mExact, 1, 1)
		mZero := "<xref-empty-zero@test>"
		histXrefInsertRaw(t, h, mZero, "7,7:3")
		mPlain := "<xref-empty-plain@test>"
		histXrefInsertRaw(t, h, mPlain, "8")
		mLegacy := "<xref-empty-legacy@test>"
		histXrefInsertRaw(t, h, mLegacy, "")
		histTestWaitIdle(t, h)

		h.RemoveArticleAt(mExact, 1, 1)
		h.RemoveArticle(mZero, 7)
		h.RemoveArticleAt(mPlain, 8, 80)
		h.RemoveArticle(mLegacy, 9)
		histTestWaitIdle(t, h)
		for _, m := range []string{mExact, mZero, mPlain, mLegacy} {
			if raw := histXrefRaw(t, dir, m); raw != "<none>" {
				t.Errorf("%s: raw = %q, want row deleted", m, raw)
			}
			if ok, err := h.Exists(m); err != nil || ok {
				t.Errorf("%s: Exists = %v, %v (want false)", m, ok, err)
			}
			if locs := histXrefLocations(t, h, m); locs != nil {
				t.Errorf("%s: locations = %+v, want nil", m, locs)
			}
		}
	})

	t.Run("OrderWithinOneFlush", func(t *testing.T) {
		m := "<xref-order@test>"
		h.AddArticleAt(m, 1, 1)
		h.AddArticleAt(m, 2, 2)
		h.RemoveArticle(m, 1) // ops apply in queue order (in one transaction, the read sees earlier writes)
		h.AddArticleAt(m, 3, 3)
		h.RemoveArticleAt(m, 2, 2)
		h.AddArticleAt(m, 1, 11)
		histTestWaitIdle(t, h)
		if raw := histXrefRaw(t, dir, m); raw != "3:3,1:11" {
			t.Fatalf("raw = %q, want \"3:3,1:11\"", raw)
		}
	})

	t.Run("LookupOnMixedRows", func(t *testing.T) {
		m := "<xref-mixed-lookup@test>"
		histXrefInsertRaw(t, h, m, "4,5:50,x,6:bad,7:0,,8:80")
		wantGroups := []int64{4, 5, 6, 7, 8}
		if got := histTestGroups(t, h, m); !reflect.DeepEqual(got, wantGroups) {
			t.Fatalf("groups = %v, want %v", got, wantGroups)
		}
		wantLocs := []Location{{GroupID: 4}, {GroupID: 5, ArticleNum: 50}, {GroupID: 6}, {GroupID: 7}, {GroupID: 8, ArticleNum: 80}}
		if got := histXrefLocations(t, h, m); !reflect.DeepEqual(got, wantLocs) {
			t.Fatalf("locations = %+v, want %+v", got, wantLocs)
		}
		if locs := histXrefLocations(t, h, "<xref-not-there@test>"); locs != nil {
			t.Fatalf("missing id: locations = %+v, want nil", locs)
		}
	})

	t.Run("Stats", func(t *testing.T) {
		if st := h.GetStats(); st.Pending != 0 || st.Errors != 0 || st.TotalCommitted == 0 {
			t.Fatalf("unexpected stats: %+v", st)
		}
	})
}

func TestHistoryXrefNilReceiver(t *testing.T) {
	var hn *History
	hn.AddArticleAt("<x@test>", 1, 2)
	hn.RemoveArticleAt("<x@test>", 1, 2)
	if locs, err := hn.LookupLocations("<x@test>"); locs != nil || err != nil {
		t.Fatalf("nil LookupLocations = %v, %v", locs, err)
	}
}

func TestHistoryToken(t *testing.T) {
	for _, c := range []struct {
		id, num int64
		want    string
	}{{12, 4711, "12:4711"}, {12, 0, "12"}, {12, -1, "12"}, {1, 1, "1:1"}} {
		if got := historyToken(c.id, c.num); got != c.want {
			t.Errorf("historyToken(%d, %d) = %q, want %q", c.id, c.num, got, c.want)
		}
	}
}

func TestHistorySetGroupToken(t *testing.T) {
	for _, c := range []struct {
		in      string
		group   int64
		token   string
		want    string
		changed bool
	}{
		{"", 1, "1:5", "", false},
		{"2:3", 1, "1:5", "2:3", false}, // no token of the group: unchanged
		{"1:5", 1, "1:5", "1:5", false}, // same number: no write
		{"1:4", 1, "1:5", "1:5", true},
		{"1", 1, "1:5", "1:5", true},
		{"12:1,1:2,21:3,11", 1, "1:9", "12:1,1:9,21:3,11", true},
		{"1:2,3,1:4", 1, "1:9", "1:9,3", true},
		{"1:9,3,1", 1, "1:9", "1:9,3", true}, // right token first, duplicate dropped
	} {
		got, changed := setGroupToken(c.in, c.group, c.token)
		if got != c.want || changed != c.changed {
			t.Errorf("setGroupToken(%q, %d, %q) = %q, %v; want %q, %v", c.in, c.group, c.token, got, changed, c.want, c.changed)
		}
	}
}

func TestHistoryStripGroupTokens(t *testing.T) {
	for _, c := range []struct {
		in      string
		group   int64
		want    string
		changed bool
	}{
		{"", 1, "", false},
		{"1", 1, "", true},
		{"1:10", 1, "", true},
		{"1,2", 1, "2", true},
		{"2,1:10,3", 1, "2,3", true},
		{"12,21:5,112", 1, "12,21:5,112", false},
		{"12:1,1:12", 1, "12:1", true},
		{"2,,3", 1, "2,3", true}, // empty parts dropped
		{"1, 1:3 ,4", 1, "4", true},
		{"5:50,x", 1, "5:50,x", false}, // unparsable tokens of other groups kept verbatim
	} {
		got, changed := stripGroupTokens(c.in, c.group)
		if got != c.want || changed != c.changed {
			t.Errorf("stripGroupTokens(%q, %d) = %q, %v; want %q, %v", c.in, c.group, got, changed, c.want, c.changed)
		}
	}
}

func TestHistoryParseLocations(t *testing.T) {
	for in, want := range map[string][]Location{
		"":                {},
		"1":               {{GroupID: 1}},
		"1:5":             {{GroupID: 1, ArticleNum: 5}},
		"1,2:7":           {{GroupID: 1}, {GroupID: 2, ArticleNum: 7}},
		"1:x,2:-3,3:0":    {{GroupID: 1}, {GroupID: 2}, {GroupID: 3}},
		",x:1,,-3:4, 4:5": {{GroupID: 4, ArticleNum: 5}},
	} {
		got := parseLocations(in)
		if got == nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseLocations(%q) = %#v, want %#v", in, got, want)
		}
	}
}

// Plan xref-batched-lookup: LookupLocationsBatch gives the same answers as one
// LookupLocations per id, with one query per shard table (chunked).

func histXrefBatch(t *testing.T, h *History, ids []string) map[string][]Location {
	t.Helper()
	got, err := h.LookupLocationsBatch(ids)
	if err != nil {
		t.Fatalf("LookupLocationsBatch(%d ids): %v", len(ids), err)
	}
	return got
}

// histXrefCheckBatch compares the batch result for ids with one LookupLocations per id:
// a found id has the same locations, an absent id is missing from the map.
func histXrefCheckBatch(t *testing.T, h *History, ids []string, got map[string][]Location) {
	t.Helper()
	unique := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		unique[id] = true
		want := histXrefLocations(t, h, id)
		locs, found := got[id]
		switch {
		case want == nil && found:
			t.Errorf("%s: absent per id, batch = %+v", id, locs)
		case want != nil && !found:
			t.Errorf("%s: per id = %+v, missing from the batch", id, want)
		case want != nil && (locs == nil || !reflect.DeepEqual(locs, want)):
			t.Errorf("%s: batch = %#v, per id = %#v", id, locs, want)
		}
	}
	for id := range got {
		if !unique[id] {
			t.Errorf("batch holds %s, not asked for", id)
		}
	}
}

// histXrefRoutedIDs returns n message-ids that all route to shard database hashdb_1, and with
// sameTable all to its table _23, so one shard (or one table) gets more ids than one query of
// lookupBatchChunk holds.
func histXrefRoutedIDs(prefix string, n int, sameTable bool) []string {
	ids := make([]string, 0, n)
	buf := make([]byte, 0, 64)
	for i := 0; len(ids) < n; i++ {
		buf = append(buf[:0], prefix...)
		buf = strconv.AppendInt(buf, int64(i), 10)
		buf = append(buf, "@test>"...)
		sum := md5.Sum(buf)
		if sum[0]>>4 == 0x1 && (!sameTable || (sum[0]&0xf == 0x2 && sum[1]>>4 == 0x3)) {
			ids = append(ids, string(buf))
		}
	}
	return ids
}

func TestHistoryXrefBatch(t *testing.T) {
	h, _ := histTestNew(t, nil)

	// mixed rows: numbered, plain, mixed/invalid tokens, legacy empty and NULL rows
	histXrefInsertRaw(t, h, "<xb-plain@test>", "4,5")
	histXrefInsertRaw(t, h, "<xb-mixed@test>", "4,5:50,x,6:bad,7:0,,8:80")
	histXrefInsertRaw(t, h, "<xb-empty@test>", "")
	histXrefInsertRaw(t, h, "<xb-null@test>", nil)
	h.AddArticleAt("<xb-num@test>", 12, 4711)
	h.AddArticleAt("<xb-num@test>", 57, 88)
	const numbered = 1200 // > lookupBatchChunk in total, spread over many tables
	var many []string
	for i := 0; i < numbered; i++ {
		id := fmt.Sprintf("<xb-many-%d@test>", i)
		many = append(many, id)
		h.AddArticleAt(id, int64(i%7+1), int64(i+1))
		if i%3 == 0 {
			h.AddArticle(id, 100) // plain token next to the numbered one
		}
	}
	// > lookupBatchChunk ids in one shard table: two queries for one table group
	sameTable := histXrefRoutedIDs("<xb-same-", 530, true)
	for i, id := range sameTable[:510] {
		h.AddArticleAt(id, 9, int64(i+1))
	}
	// > lookupBatchChunk ids in one shard database over most of its tables: a chunk boundary
	// in the middle of the UNION ALL terms
	sameShard := histXrefRoutedIDs("<xb-shard-", 1100, false)
	for i, id := range sameShard[:1050] {
		h.AddArticleAt(id, int64(i%5+20), int64(i+1))
	}
	histTestWaitIdle(t, h)

	t.Run("EqualsPerID", func(t *testing.T) {
		ids := []string{"<xb-plain@test>", "<xb-mixed@test>", "<xb-empty@test>", "<xb-null@test>",
			"<xb-num@test>", "<xb-absent-1@test>", "", "<xb-absent-2@test>"}
		ids = append(ids, many...)
		got := histXrefBatch(t, h, ids)
		if len(got) != 5+numbered {
			t.Errorf("batch found %d ids, want %d", len(got), 5+numbered)
		}
		histXrefCheckBatch(t, h, ids, got)
		if locs, ok := got["<xb-empty@test>"]; !ok || locs == nil || len(locs) != 0 {
			t.Errorf("empty row: batch = %#v, %v; want empty non-nil slice", locs, ok)
		}
		want := []Location{{GroupID: 12, ArticleNum: 4711}, {GroupID: 57, ArticleNum: 88}}
		if !reflect.DeepEqual(got["<xb-num@test>"], want) {
			t.Errorf("numbered row: batch = %+v, want %+v", got["<xb-num@test>"], want)
		}
	})

	t.Run("AbsentIDs", func(t *testing.T) {
		got := histXrefBatch(t, h, []string{"<xb-absent-3@test>", "<xb-absent-4@test>"})
		if got == nil || len(got) != 0 {
			t.Fatalf("absent ids: batch = %#v, want empty map", got)
		}
	})

	t.Run("OneTableOverChunk", func(t *testing.T) {
		if len(sameTable) <= lookupBatchChunk+1 {
			t.Fatalf("test needs more than %d ids in one table", lookupBatchChunk)
		}
		got := histXrefBatch(t, h, sameTable)
		if len(got) != 510 {
			t.Fatalf("one table: batch found %d ids, want 510", len(got))
		}
		for i, id := range sameTable {
			locs, found := got[id]
			if i >= 510 {
				if found {
					t.Errorf("%s: never added, batch = %+v", id, locs)
				}
				continue
			}
			if want := []Location{{GroupID: 9, ArticleNum: int64(i + 1)}}; !reflect.DeepEqual(locs, want) {
				t.Errorf("%s: batch = %+v, want %+v", id, locs, want)
			}
		}
	})

	t.Run("OneShardOverChunk", func(t *testing.T) {
		got := histXrefBatch(t, h, sameShard)
		if len(got) != 1050 {
			t.Fatalf("one shard: batch found %d ids, want 1050", len(got))
		}
		for i, id := range sameShard {
			locs, found := got[id]
			if i >= 1050 {
				if found {
					t.Errorf("%s: never added, batch = %+v", id, locs)
				}
				continue
			}
			if want := []Location{{GroupID: int64(i%5 + 20), ArticleNum: int64(i + 1)}}; !reflect.DeepEqual(locs, want) {
				t.Errorf("%s: batch = %+v, want %+v", id, locs, want)
			}
		}
	})

	t.Run("DuplicateIDs", func(t *testing.T) {
		before := h.GetStats()
		ids := []string{"<xb-num@test>", "<xb-plain@test>", "<xb-num@test>", "<xb-absent-5@test>", "<xb-absent-5@test>", "<xb-num@test>"}
		got := histXrefBatch(t, h, ids)
		if len(got) != 2 {
			t.Fatalf("duplicates: batch = %+v, want 2 ids", got)
		}
		after := h.GetStats()
		// each distinct id is one lookup; hits count like LookupLocations' duplicates
		if d := after.TotalLookups - before.TotalLookups; d != 3 {
			t.Errorf("lookups grew by %d, want 3", d)
		}
		if d := after.Duplicates - before.Duplicates; d != 2 {
			t.Errorf("duplicates grew by %d, want 2", d)
		}
		histXrefCheckBatch(t, h, ids, got)
	})

	t.Run("StatsLikePerID", func(t *testing.T) {
		ids := []string{"<xb-num@test>", "<xb-mixed@test>", "<xb-absent-6@test>"}
		s0 := h.GetStats()
		for _, id := range ids {
			histXrefLocations(t, h, id)
		}
		s1 := h.GetStats()
		histXrefBatch(t, h, ids)
		s2 := h.GetStats()
		if s1.TotalLookups-s0.TotalLookups != s2.TotalLookups-s1.TotalLookups ||
			s1.Duplicates-s0.Duplicates != s2.Duplicates-s1.Duplicates {
			t.Errorf("per id: lookups +%d duplicates +%d; batch: lookups +%d duplicates +%d",
				s1.TotalLookups-s0.TotalLookups, s1.Duplicates-s0.Duplicates,
				s2.TotalLookups-s1.TotalLookups, s2.Duplicates-s1.Duplicates)
		}
		if s2.Errors != 0 {
			t.Errorf("errors = %d", s2.Errors)
		}
	})

	t.Run("EmptyInput", func(t *testing.T) {
		before := h.GetStats().TotalLookups
		for _, ids := range [][]string{nil, {}, {""}, {"", ""}} {
			if got, err := h.LookupLocationsBatch(ids); got != nil || err != nil {
				t.Errorf("LookupLocationsBatch(%q) = %v, %v; want nil, nil", ids, got, err)
			}
		}
		if after := h.GetStats().TotalLookups; after != before {
			t.Errorf("empty input counted %d lookups", after-before)
		}
	})
}

// TestHistoryXrefBatchQueryError: a failing table query fails the whole call and counts an error
func TestHistoryXrefBatchQueryError(t *testing.T) {
	h, _ := histTestNew(t, nil)
	ok := "<xb-err-ok@test>"
	h.AddArticleAt(ok, 1, 1)
	histTestWaitIdle(t, h)
	bad := "<xb-err-bad@test>"
	dbIndex, tableName, err := h.routeHash(bad)
	if err != nil {
		t.Fatal(err)
	}
	if okIdx, okTable, _ := h.routeHash(ok); okIdx == dbIndex && okTable == tableName {
		t.Fatal("test ids route to the same table")
	}
	db, err := h.db.GetShardedDB(dbIndex, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE " + tableName); err != nil {
		t.Fatalf("drop %s: %v", tableName, err)
	}
	before := h.GetStats().Errors
	got, err := h.LookupLocationsBatch([]string{ok, bad})
	if err == nil || got != nil {
		t.Fatalf("batch over a missing table = %v, %v; want nil, error", got, err)
	}
	if d := h.GetStats().Errors - before; d != 1 {
		t.Errorf("errors grew by %d, want 1", d)
	}
}

func TestHistoryXrefBatchClosed(t *testing.T) {
	h, _ := histTestNew(t, nil)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := h.LookupLocationsBatch([]string{"<xb-closed@test>"}); got != nil || !errors.Is(err, ErrHistoryClosed) {
		t.Fatalf("closed: batch = %v, %v; want nil, ErrHistoryClosed", got, err)
	}
}

func TestHistoryXrefBatchDisabled(t *testing.T) {
	histTestEnable(t, false)
	h, err := NewHistory(histTestConfig(filepath.Join(t.TempDir(), "history-disabled")), nil)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	var hn *History
	for name, hist := range map[string]*History{"disabled": h, "nil": hn} {
		if got, err := hist.LookupLocationsBatch([]string{"<xb-disabled@test>"}); got != nil || err != nil {
			t.Errorf("%s: batch = %v, %v; want nil, nil", name, got, err)
		}
	}
}
