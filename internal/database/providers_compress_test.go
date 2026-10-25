package database

import (
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// pcmpAdd inserts a provider named by w0Name with the given compress flag and
// returns the stored row (AddProvider does not fill the ID). The row is removed
// again at the end of the test.
func pcmpAdd(t *testing.T, db *Database, compress bool) *models.Provider {
	t.Helper()
	name := w0Name("compress")
	p := &models.Provider{
		Name:     name,
		Host:     "news.example.invalid",
		Port:     119,
		MaxConns: 1,
		Enabled:  true,
		Priority: 1,
		Compress: compress,
	}
	if err := db.AddProvider(p); err != nil {
		t.Fatalf("AddProvider %s: %v", name, err)
	}
	stored, err := db.GetProviderByName(name)
	if err != nil {
		t.Fatalf("GetProviderByName %s: %v", name, err)
	}
	if stored == nil {
		t.Fatalf("GetProviderByName %s: no row after AddProvider", name)
	}
	t.Cleanup(func() {
		if err := db.DeleteProvider(int(stored.ID)); err != nil {
			t.Errorf("DeleteProvider %d: %v", stored.ID, err)
		}
	})
	return stored
}

// pcmpInList returns the compress flag of name as GetProviders reports it.
func pcmpInList(t *testing.T, db *Database, name string) bool {
	t.Helper()
	all, err := db.GetProviders()
	if err != nil {
		t.Fatalf("GetProviders: %v", err)
	}
	for _, p := range all {
		if p.Name == name {
			return p.Compress
		}
	}
	t.Fatalf("GetProviders: provider %s missing", name)
	return false
}

// TestProviderCompressRoundTrip: a provider stored with compress=true reads back
// true through every provider getter (migration 0033 column, Scan lists in order).
func TestProviderCompressRoundTrip(t *testing.T) {
	db := w0DB(t)
	stored := pcmpAdd(t, db, true)
	if !stored.Compress {
		t.Errorf("GetProviderByName %s: Compress = false, want true", stored.Name)
	}

	byID, err := db.GetProviderByID(stored.ID)
	if err != nil {
		t.Fatalf("GetProviderByID %d: %v", stored.ID, err)
	}
	if byID == nil {
		t.Fatalf("GetProviderByID %d: no row", stored.ID)
	}
	if !byID.Compress {
		t.Errorf("GetProviderByID %d: Compress = false, want true", stored.ID)
	}
	if !pcmpInList(t, db, stored.Name) {
		t.Errorf("GetProviders %s: Compress = false, want true", stored.Name)
	}
}

// TestProviderCompressDefaultOff: a provider added without the flag reads false
// everywhere — COMPRESS stays off unless the admin ticks it.
func TestProviderCompressDefaultOff(t *testing.T) {
	db := w0DB(t)
	stored := pcmpAdd(t, db, false)
	if stored.Compress {
		t.Errorf("GetProviderByName %s: Compress = true, want false", stored.Name)
	}

	byID, err := db.GetProviderByID(stored.ID)
	if err != nil {
		t.Fatalf("GetProviderByID %d: %v", stored.ID, err)
	}
	if byID == nil {
		t.Fatalf("GetProviderByID %d: no row", stored.ID)
	}
	if byID.Compress {
		t.Errorf("GetProviderByID %d: Compress = true, want false", stored.ID)
	}
	if pcmpInList(t, db, stored.Name) {
		t.Errorf("GetProviders %s: Compress = true, want false", stored.Name)
	}
}

// TestProviderCompressUpdate: SetProvider toggles the flag in both directions and
// leaves the other columns alone.
func TestProviderCompressUpdate(t *testing.T) {
	db := w0DB(t)
	stored := pcmpAdd(t, db, false)

	stored.Compress = true
	if err := db.SetProvider(stored); err != nil {
		t.Fatalf("SetProvider %d (on): %v", stored.ID, err)
	}
	after, err := db.GetProviderByID(stored.ID)
	if err != nil {
		t.Fatalf("GetProviderByID %d: %v", stored.ID, err)
	}
	if after == nil {
		t.Fatalf("GetProviderByID %d: no row", stored.ID)
	}
	if !after.Compress {
		t.Errorf("after SetProvider(true): Compress = false, want true")
	}
	if after.Host != stored.Host || after.Port != stored.Port || after.MaxConns != stored.MaxConns {
		t.Errorf("after SetProvider(true): row changed: %+v want host %s port %d max_conns %d",
			after, stored.Host, stored.Port, stored.MaxConns)
	}
	if !pcmpInList(t, db, stored.Name) {
		t.Errorf("GetProviders after SetProvider(true): Compress = false, want true")
	}

	after.Compress = false
	if err := db.SetProvider(after); err != nil {
		t.Fatalf("SetProvider %d (off): %v", after.ID, err)
	}
	off, err := db.GetProviderByName(stored.Name)
	if err != nil {
		t.Fatalf("GetProviderByName %s: %v", stored.Name, err)
	}
	if off == nil {
		t.Fatalf("GetProviderByName %s: no row", stored.Name)
	}
	if off.Compress {
		t.Errorf("after SetProvider(false): Compress = true, want false")
	}
}
