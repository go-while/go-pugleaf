package main

// Tests of the followup-leftovers slice fl-ops-tools (nntp-analyze): -timeout reaches
// the shared pool of the '$all'/'prefix.*' and -groups-file modes.

import (
	"testing"
	"time"
)

func TestFlOpsAnalyzeBackendConfigTimeout(t *testing.T) {
	cfg := analyzeBackendConfig("news.example.org", 563, "u", "p", true, 7)
	if cfg.ConnectTimeout != 7*time.Second {
		t.Fatalf("ConnectTimeout = %v, want 7s from -timeout", cfg.ConnectTimeout)
	}
	if cfg.Host != "news.example.org" || cfg.Port != 563 || !cfg.SSL || cfg.Username != "u" || cfg.Password != "p" || cfg.MaxConns != 5 {
		t.Fatalf("unexpected backend config: host %q port %d ssl %t user %q maxconns %d", cfg.Host, cfg.Port, cfg.SSL, cfg.Username, cfg.MaxConns)
	}
}
