package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAdfRecoverResolveDataDir(t *testing.T) {
	cases := []struct {
		name            string
		db, data        string
		dbSet, dataSet  bool
		want            string
		wantErr         bool
		wantErrContains string
	}{
		{name: "only db", db: "/srv/pug", data: "./data", dbSet: true, want: "/srv/pug"},
		{name: "only data", db: "", data: "/srv/pug", dataSet: true, want: "/srv/pug"},
		{name: "both equal", db: "/srv/pug", data: "/srv/pug", dbSet: true, dataSet: true, want: "/srv/pug"},
		{name: "both equal after clean", db: "/srv/pug/", data: "/srv//pug", dbSet: true, dataSet: true, want: "/srv//pug"},
		{name: "both different", db: "/srv/pug", data: "/srv/pug-other", dbSet: true, dataSet: true, wantErr: true,
			wantErrContains: "-db is a deprecated alias of -data; they disagree"},
		{name: "neither", db: "", data: "./data", want: "./data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRecoverDataDir(tc.db, tc.data, tc.dbSet, tc.dataSet)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got dir %q", got)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAdfRecoverRewriteQueryKeepsFlags(t *testing.T) {
	q := strings.ToLower(query_rewriteDateSent)
	for _, col := range []string{"hide", "spam"} {
		if strings.Contains(q, col) {
			t.Fatalf("query_rewriteDateSent touches %q: %s", col, query_rewriteDateSent)
		}
	}
	if !strings.Contains(q, "set date_sent = ?") || !strings.Contains(q, "where article_num = ?") {
		t.Fatalf("query_rewriteDateSent does not update date_sent by article_num: %s", query_rewriteDateSent)
	}
}

func TestAdfRecoverWriteModes(t *testing.T) {
	cases := []struct {
		name                              string
		repair, rebuild, rewrite, reorder bool
		want                              []string
	}{
		{name: "none", want: nil},
		{name: "repair", repair: true, want: []string{"-repair"}},
		{name: "rewrite", rewrite: true, want: []string{"-rewritedates"}},
		{name: "all", repair: true, rebuild: true, rewrite: true, reorder: true,
			want: []string{"-repair", "-rebuild-threads", "-rewritedates", "-reorder-by-datesent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recoverWriteModes(tc.repair, tc.rebuild, tc.rewrite, tc.reorder)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAdfRecoverShouldRewrite(t *testing.T) {
	stored := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	plus2h := time.FixedZone("+0200", 2*3600)
	cases := []struct {
		name     string
		ok       bool
		reparsed time.Time
		want     bool
	}{
		{name: "parse failed", ok: false, reparsed: stored.Add(10 * time.Hour), want: false},
		{name: "parse failed zero", ok: false, reparsed: time.Time{}, want: false},
		{name: "equal", ok: true, reparsed: stored, want: false},
		{name: "within a second", ok: true, reparsed: stored.Add(time.Second), want: false},
		{name: "same instant other zone", ok: true, reparsed: time.Date(2020, 1, 1, 2, 0, 0, 0, plus2h), want: false},
		{name: "later", ok: true, reparsed: stored.Add(10 * time.Hour), want: true},
		{name: "earlier", ok: true, reparsed: stored.Add(-2 * time.Second), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRewrite(tc.ok, tc.reparsed, stored); got != tc.want {
				t.Fatalf("shouldRewrite(%v, %v, %v) = %v, want %v", tc.ok, tc.reparsed, stored, got, tc.want)
			}
		})
	}
}
