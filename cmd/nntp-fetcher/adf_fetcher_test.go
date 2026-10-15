package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// TestAdfFetchBackendConfigCopiesProxy checks that fetcherBackendConfig copies every
// proxy field (first level and Provider), sets Provider and uses maxConns as passed.
func TestAdfFetchBackendConfigCopiesProxy(t *testing.T) {
	p := &models.Provider{
		Name:          "adf-tor",
		Grp:           "adf-grp",
		Host:          "adfexample.onion",
		Port:          119,
		SSL:           false,
		Username:      "adf-user",
		Password:      "adf-pass",
		MaxConns:      7,
		Enabled:       true,
		Priority:      3,
		MaxArtSize:    32768,
		Posting:       true,
		ProxyEnabled:  true,
		ProxyType:     "socks5",
		ProxyHost:     "127.0.0.1",
		ProxyPort:     9050,
		ProxyUsername: "adf-proxy-user",
		ProxyPassword: "adf-proxy-pass",
	}
	for _, maxConns := range []int{1, p.MaxConns} {
		bc := fetcherBackendConfig(p, maxConns)
		if bc == nil {
			t.Fatalf("maxConns=%d: nil BackendConfig", maxConns)
		}
		if bc.Host != p.Host || bc.Port != p.Port || bc.SSL != p.SSL || bc.Username != p.Username || bc.Password != p.Password {
			t.Errorf("maxConns=%d: connection fields not copied: %+v", maxConns, bc)
		}
		if bc.MaxConns != maxConns {
			t.Errorf("maxConns=%d: BackendConfig.MaxConns=%d", maxConns, bc.MaxConns)
		}
		if !bc.ProxyEnabled || bc.ProxyType != p.ProxyType || bc.ProxyHost != p.ProxyHost || bc.ProxyPort != p.ProxyPort ||
			bc.ProxyUsername != p.ProxyUsername || bc.ProxyPassword != p.ProxyPassword {
			t.Errorf("maxConns=%d: proxy fields not copied to BackendConfig: enabled=%t type=%q host=%q port=%d user=%q pass-set=%t",
				maxConns, bc.ProxyEnabled, bc.ProxyType, bc.ProxyHost, bc.ProxyPort, bc.ProxyUsername, bc.ProxyPassword != "")
		}
		cp := bc.Provider
		if cp == nil {
			t.Fatalf("maxConns=%d: Provider not set", maxConns)
		}
		if cp.Name != p.Name || cp.Grp != p.Grp || cp.Host != p.Host || cp.Port != p.Port || cp.SSL != p.SSL ||
			cp.Username != p.Username || cp.Password != p.Password || cp.Enabled != p.Enabled || cp.Priority != p.Priority ||
			cp.MaxArtSize != p.MaxArtSize || cp.Posting != p.Posting {
			t.Errorf("maxConns=%d: provider fields not copied: %+v", maxConns, cp)
		}
		if cp.MaxConns != maxConns {
			t.Errorf("maxConns=%d: Provider.MaxConns=%d", maxConns, cp.MaxConns)
		}
		if !cp.ProxyEnabled || cp.ProxyType != p.ProxyType || cp.ProxyHost != p.ProxyHost || cp.ProxyPort != p.ProxyPort ||
			cp.ProxyUsername != p.ProxyUsername || cp.ProxyPassword != p.ProxyPassword {
			t.Errorf("maxConns=%d: proxy fields not copied to Provider", maxConns)
		}
	}
}

// adfFetchExists reports whether path exists (any error but not-exist fails the test).
func adfFetchExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	t.Fatalf("stat %s: %v", path, err)
	return false
}

// TestAdfFetchOpenFetcherDBHonoursData opens the main database through openFetcherDB
// in a temp root: the DB file lands there and ./data (package dir) is not created.
// It is the only test in this package that opens a database; it never touches the network.
func TestAdfFetchOpenFetcherDBHonoursData(t *testing.T) {
	if euid := os.Geteuid(); euid >= 0 && euid < 1000 {
		t.Skipf("skipping: OpenDatabase refuses euid %d (< 1000)", euid)
	}
	dataBefore := adfFetchExists(t, "./data")

	tmp, err := os.MkdirTemp("", "pugleaf-adf-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tmp); err != nil {
			t.Logf("RemoveAll %s: %v", tmp, err)
		}
	})

	prevNoCache := database.NO_CACHE_BOOT
	database.NO_CACHE_BOOT = true
	t.Cleanup(func() { database.NO_CACHE_BOOT = prevNoCache })

	db, cleanup, err := openFetcherDB(tmp)
	if err != nil {
		t.Fatalf("openFetcherDB(%s): %v", tmp, err)
	}
	if cleanup == nil {
		t.Fatalf("openFetcherDB returned a nil cleanup")
	}
	if db == nil {
		cleanup()
		t.Fatalf("openFetcherDB returned a nil db without error")
	}
	// the providers table is readable through the opened DB
	if _, err := db.GetProviders(); err != nil {
		cleanup()
		t.Fatalf("GetProviders: %v", err)
	}
	cleanup()
	cleanup() // second call is a no-op

	if !adfFetchExists(t, filepath.Join(tmp, "cfg", "pugleaf.sq3")) {
		t.Errorf("%s does not exist after openFetcherDB", filepath.Join(tmp, "cfg", "pugleaf.sq3"))
	}
	if dataAfter := adfFetchExists(t, "./data"); dataAfter != dataBefore {
		t.Errorf("./data existence changed: before=%t after=%t", dataBefore, dataAfter)
	}
}
