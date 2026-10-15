// Package adfreopen tests that database.OpenDatabase can fail, succeed, shut down and
// open again in one process (AUDIT-2026-10 OPS-1a). It is its own package because
// OpenDatabase may only run once at a time per process and the database package's tests
// keep one Database open for the whole test binary.
package adfreopen

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
)

var adfReopenRoot string

// testWait is the hang guard of this package's tests (the database package has its own; this
// package cannot see it). It only stops a hung test; a passing test never waits on it.
// Override with PUGLEAF_TEST_DEADLINE (a Go duration, e.g. 120s). Plan test-timing-hardening D3.
var testWait = testWaitFromEnv(60 * time.Second)

func testWaitFromEnv(def time.Duration) time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// adfReopenCronBudget is how long (e) waits for CronDB to exit: it only notices shutdown on
// its next 10 s tick, so the budget is 12x the tick (or testWait if that is longer). The pass
// path still ends at the first tick.
func adfReopenCronBudget() time.Duration {
	if b := 2 * time.Minute; b > testWait {
		return b
	}
	return testWait
}

func TestMain(m *testing.M) {
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		fmt.Println("adfreopen: skipped: OpenDatabase refuses to run with euid < 1000")
		os.Exit(0)
	}
	database.NO_CACHE_BOOT = true
	dir, err := os.MkdirTemp("", "pugleaf-adf-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	adfReopenRoot = dir
	code := m.Run()
	if code == 0 {
		_ = os.RemoveAll(dir)
	} else {
		fmt.Fprintln(os.Stderr, "test data kept in", dir)
	}
	os.Exit(code)
}

// adfReopenConfig returns a DB config whose data and backup dirs are below dataDir.
func adfReopenConfig(dataDir string) *database.DBConfig {
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dataDir
	cfg.BackupDir = filepath.Join(dataDir, "backups")
	return cfg
}

// adfReopenTeardown runs the teardown every binary uses: close StopChan, wait for WG
// (bounded), Shutdown.
func adfReopenTeardown(t *testing.T, db *database.Database) {
	t.Helper()
	close(db.StopChan)
	done := make(chan struct{})
	go func() {
		db.WG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatalf("db.WG.Wait() did not return within %v", testWait)
	}
	if err := db.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// adfReopenStacks returns the stack of every goroutine keyed by goroutine id.
func adfReopenStacks() (map[int]string, string) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	all := string(buf)
	stacks := make(map[int]string)
	for _, g := range strings.Split(all, "\n\n") {
		var id int
		if _, err := fmt.Sscanf(g, "goroutine %d ", &id); err == nil {
			stacks[id] = g
		}
	}
	return stacks, all
}

// adfReopenWaitNoStack polls until no goroutine stack contains needle.
func adfReopenWaitNoStack(t *testing.T, needle string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		found := false
		stacks, all := adfReopenStacks()
		for _, s := range stacks {
			if strings.Contains(s, needle) {
				found = true
				break
			}
		}
		if !found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine %q still running after %v:\n%s", needle, within, all)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Goroutines OpenDatabase starts that must be gone after the teardown.
var adfReopenForbidden = []string{
	"database.(*GHmap).GHmapCron",
	"database.(*GroupSectionDBCache).CronClean",
	"database.(*Database).CronDB",
	"database/sql.(*DB).connectionOpener", // a main or group *sql.DB left open
}

// Known leftovers without a stop path, at most one per OpenDatabase (two opens here).
// Tightened by adf-db-batch: BatchDivider, ExpireCache and CleanCron stop on Shutdown now;
// only NNTPAuthCache.cleanupExpired (hardening-owned) is left.
var adfReopenAllowed = []struct {
	needle string
	max    int
}{
	{"database.(*NNTPAuthCache).cleanupExpired", 2},
}

// adfReopenViolations lists the goroutines started after baseline that are forbidden,
// not on the allow-list, or over an allow-list limit.
func adfReopenViolations(baseline map[int]string) ([]string, string) {
	stacks, all := adfReopenStacks()
	var out []string
	counts := make(map[string]int)
	for id, s := range stacks {
		if _, old := baseline[id]; old {
			continue
		}
		forbidden := false
		for _, f := range adfReopenForbidden {
			if strings.Contains(s, f) {
				out = append(out, fmt.Sprintf("goroutine %d still runs %s", id, f))
				forbidden = true
				break
			}
		}
		if forbidden {
			continue
		}
		allowed := false
		for _, a := range adfReopenAllowed {
			if strings.Contains(s, a.needle) {
				counts[a.needle]++
				allowed = true
				break
			}
		}
		if !allowed {
			first := s
			if i := strings.Index(s, "\n"); i > 0 && i+1 < len(s) {
				first = s[i+1:]
				if j := strings.Index(first, "\n"); j > 0 {
					first = first[:j]
				}
			}
			out = append(out, fmt.Sprintf("goroutine %d not on the allow-list: %s", id, first))
		}
	}
	for _, a := range adfReopenAllowed {
		if counts[a.needle] > a.max {
			out = append(out, fmt.Sprintf("%d goroutines of %s, allowed %d", counts[a.needle], a.needle, a.max))
		}
	}
	return out, all
}

func TestAdfReopenAfterFailureAndShutdown(t *testing.T) {
	baseline, _ := adfReopenStacks()
	t.Logf("goroutines before: %d", runtime.NumGoroutine())
	// a fresh root per run, so -count=N does not find the previous run's databases
	root, err := os.MkdirTemp(adfReopenRoot, "run-")
	if err != nil {
		t.Fatal(err)
	}

	// (a) DataDir is a regular file: initMainDB fails; error, no panic, no exit
	notADir := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if db, err := database.OpenDatabase(adfReopenConfig(notADir)); err == nil {
		t.Fatalf("OpenDatabase on a regular file succeeded (db=%p), want an error", db)
	} else {
		t.Logf("(a) expected error: %v", err)
	}
	adfReopenWaitNoStack(t, "database/sql.(*DB).connectionOpener", testWait)

	// (a2) a failure after initMainDB (migrations): the main DB is closed again
	badDir := filepath.Join(root, "bad-migrations")
	if err := os.MkdirAll(filepath.Join(badDir, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite3", filepath.Join(badDir, "cfg", "pugleaf.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	// schema_migrations without db_type: getAppliedMigrations fails
	if _, err := raw.Exec("CREATE TABLE schema_migrations (id INTEGER PRIMARY KEY, filename TEXT)"); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err := database.OpenDatabase(adfReopenConfig(badDir)); err == nil {
		t.Fatalf("OpenDatabase with a broken schema_migrations succeeded (db=%p), want an error", db)
	} else {
		t.Logf("(a2) expected error: %v", err)
	}
	adfReopenWaitNoStack(t, "database/sql.(*DB).connectionOpener", testWait)

	// (b)+(c) open (INIT was not left set by the failures), teardown
	db, err := database.OpenDatabase(adfReopenConfig(filepath.Join(root, "first")))
	if err != nil {
		t.Fatalf("(b) OpenDatabase after failed opens: %v", err)
	}
	if _, err := database.OpenDatabase(adfReopenConfig(filepath.Join(root, "second-too-early"))); err == nil {
		t.Fatal("second OpenDatabase while one is open succeeded, want an error")
	}
	adfReopenTeardown(t, db)

	// (d) open again (Shutdown reset INIT), teardown
	db, err = database.OpenDatabase(adfReopenConfig(filepath.Join(root, "second")))
	if err != nil {
		t.Fatalf("(d) OpenDatabase after Shutdown: %v", err)
	}
	adfReopenTeardown(t, db)

	// (e) background goroutines are gone; CronDB only exits on its next 10 s tick
	budget := adfReopenCronBudget()
	deadline := time.Now().Add(budget)
	for {
		violations, all := adfReopenViolations(baseline)
		if len(violations) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines left %v after two open/shutdown cycles:\n  %s\n\nall goroutines:\n%s",
				budget, strings.Join(violations, "\n  "), all)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("goroutines after: %d", runtime.NumGoroutine())
}
