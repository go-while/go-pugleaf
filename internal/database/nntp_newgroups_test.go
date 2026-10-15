package database

import (
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// lstNewGroupsByName indexes NNTPNewGroupsSince's result by group name.
func lstNewGroupsByName(t *testing.T, db *Database, since time.Time) map[string]*models.Newsgroup {
	t.Helper()
	groups, err := db.NNTPNewGroupsSince(since)
	if err != nil {
		t.Fatalf("NNTPNewGroupsSince(%s): %v", since, err)
	}
	out := make(map[string]*models.Newsgroup, len(groups))
	for _, g := range groups {
		out[g.Name] = g
	}
	return out
}

// TestLstNNTPNewGroupsSince: created_at in the CURRENT_TIMESTAMP form and in Go's
// time format (with a zone offset) are both compared in UTC; inactive groups and
// NULL last_article are handled. The shared w0DB holds other tests' groups, so the
// assertions are contains / doesn't contain only.
func TestLstNNTPNewGroupsSince(t *testing.T) {
	db := w0DB(t)
	a, b, c := w0Name("lst.ng.a"), w0Name("lst.ng.b"), w0Name("lst.ng.c")
	for _, g := range []struct {
		name   string
		active bool
	}{{a, true}, {b, true}, {c, false}} {
		if err := db.InsertNewsgroup(&models.Newsgroup{
			Name: g.name, Description: "lst " + g.name, Active: g.active, Status: "y", LowWater: 1, LastArticle: 5,
		}); err != nil {
			t.Fatalf("InsertNewsgroup %s: %v", g.name, err)
		}
	}
	for _, u := range []struct {
		q    string
		args []interface{}
	}{
		{`UPDATE newsgroups SET created_at = '2001-02-03 04:05:06', last_article = NULL WHERE name = ?`, []interface{}{a}},
		{`UPDATE newsgroups SET created_at = '2001-02-03 05:05:06.123456789+01:00' WHERE name = ?`, []interface{}{b}},
		{`UPDATE newsgroups SET created_at = '2001-02-03 04:05:06' WHERE name = ?`, []interface{}{c}},
	} {
		if _, err := RetryableExec(db.GetMainDB(), u.q, u.args...); err != nil {
			t.Fatalf("%s: %v", u.q, err)
		}
	}

	got := lstNewGroupsByName(t, db, time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC))
	ga, okA := got[a]
	if !okA {
		t.Errorf("since 04:05:00 UTC: %s (CURRENT_TIMESTAMP form) missing", a)
	} else {
		if ga.LastArticle != 0 {
			t.Errorf("%s: LastArticle = %d, want 0 for NULL", a, ga.LastArticle)
		}
		if !ga.Active || ga.Status != "y" || ga.LowWater != 1 || ga.Description != "lst "+a {
			t.Errorf("%s: got %+v", a, *ga)
		}
	}
	if gb, ok := got[b]; !ok {
		t.Errorf("since 04:05:00 UTC: %s (Go time form, +01:00) missing", b)
	} else if gb.LastArticle != 5 {
		t.Errorf("%s: LastArticle = %d, want 5", b, gb.LastArticle)
	}
	if _, ok := got[c]; ok {
		t.Errorf("since 04:05:00 UTC: inactive %s listed", c)
	}

	// The same instant given in another zone selects the same rows.
	east := time.FixedZone("UTC+3", 3*3600)
	got = lstNewGroupsByName(t, db, time.Date(2001, 2, 3, 7, 5, 0, 0, east))
	if _, ok := got[a]; !ok {
		t.Errorf("since 07:05:00 UTC+3: %s missing", a)
	}
	if _, ok := got[b]; !ok {
		t.Errorf("since 07:05:00 UTC+3: %s missing", b)
	}

	got = lstNewGroupsByName(t, db, time.Date(2001, 2, 3, 4, 5, 7, 0, time.UTC))
	for _, n := range []string{a, b, c} {
		if _, ok := got[n]; ok {
			t.Errorf("since 04:05:07 UTC: %s listed", n)
		}
	}
}
