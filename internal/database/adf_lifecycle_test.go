package database

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// adfLifeGroupDBFile returns the path of the group database file of name.
func adfLifeGroupDBFile(db *Database, name string) string {
	return filepath.Join(db.GetDataDir(), "db", GroupHashMap.GroupToHash(name), SanitizeGroupName(name)+".db")
}

// adfLifeOpenCount opens the group database, reads the articles table and closes it again.
func adfLifeOpenCount(t *testing.T, db *Database, name string) {
	t.Helper()
	g, err := db.GetGroupDB(name)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", name, err)
	}
	var n int
	if err := g.DB.QueryRow("SELECT count(*) FROM articles").Scan(&n); err != nil {
		_ = db.ForceCloseGroupDB(g)
		t.Fatalf("SELECT count(*) FROM articles: %v", err)
	}
	if err := db.ForceCloseGroupDB(g); err != nil {
		t.Fatalf("ForceCloseGroupDB(%s): %v", name, err)
	}
}

// A-db-6: a group database whose file was deleted gets its schema again on the next open
// (the migration cache keyed by group name used to skip it).
func TestAdfLifeGroupDBRecreated(t *testing.T) {
	db := w0DB(t)
	name := w0Name("adflife.recreate")
	// two cycles: the old cache only remembered a group after an open that found every
	// migration already applied
	adfLifeOpenCount(t, db, name)
	adfLifeOpenCount(t, db, name)

	path := adfLifeGroupDBFile(db, name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("group database file: %v", err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	adfLifeOpenCount(t, db, name)
}

// A-db-12: an unknown hash returns ok == false instead of dereferencing nil.
func TestAdfLifeGetGroupFromHashUnknown(t *testing.T) {
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	h := NewGHmapStop(stop)
	if g, ok := h.GetGroupFromHash("0123456789abcdef0123456789abcdef"); ok || g != "" {
		t.Fatalf("GetGroupFromHash(unknown) = %q, %v; want \"\", false", g, ok)
	}
	name := w0Name("adflife.hash")
	hash := h.GroupToHash(name)
	if g, ok := h.GetGroupFromHash(hash); !ok || g != name {
		t.Fatalf("GetGroupFromHash(known) = %q, %v; want %q, true", g, ok, name)
	}
	// a nil entry (not produced by GroupToHash, but possible through the exported map)
	h.mux.Lock()
	h.Groups["nilentry"] = nil
	h.mux.Unlock()
	if g, ok := h.GetGroupFromHash("nilentry"); ok || g != "" {
		t.Fatalf("GetGroupFromHash(nil entry) = %q, %v; want \"\", false", g, ok)
	}
	h.mux.Lock()
	delete(h.Groups, "nilentry")
	h.mux.Unlock()
}

// adfLifeCountStacks returns the number of goroutines whose stack contains needle.
func adfLifeCountStacks(needle string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, needle) {
			count++
		}
	}
	return count
}

// The cron loops of NewGHmapStop and newGroupSectionDBCacheStop end when stop is closed.
func TestAdfLifeCronLoopsStop(t *testing.T) {
	const ghNeedle = "database.(*GHmap).GHmapCron"
	const scNeedle = "database.(*GroupSectionDBCache).CronClean"
	ghBefore, scBefore := adfLifeCountStacks(ghNeedle), adfLifeCountStacks(scNeedle)

	stop := make(chan struct{})
	_ = NewGHmapStop(stop)
	_ = newGroupSectionDBCacheStop(stop)
	deadline := time.Now().Add(5 * time.Second)
	for adfLifeCountStacks(ghNeedle) != ghBefore+1 || adfLifeCountStacks(scNeedle) != scBefore+1 {
		if time.Now().After(deadline) {
			t.Fatalf("cron loops not started: GHmapCron %d (before %d), CronClean %d (before %d)",
				adfLifeCountStacks(ghNeedle), ghBefore, adfLifeCountStacks(scNeedle), scBefore)
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(stop)
	deadline = time.Now().Add(5 * time.Second)
	for adfLifeCountStacks(ghNeedle) != ghBefore || adfLifeCountStacks(scNeedle) != scBefore {
		if time.Now().After(deadline) {
			t.Fatalf("cron loops still running after stop: GHmapCron %d (want %d), CronClean %d (want %d)",
				adfLifeCountStacks(ghNeedle), ghBefore, adfLifeCountStacks(scNeedle), scBefore)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stopBackground is a no-op on a bare Database and closes bgStop exactly once otherwise.
func TestAdfLifeStopBackground(t *testing.T) {
	bare := &Database{}
	bare.stopBackground()
	if bare.backgroundStop() != nil {
		t.Fatal("bare Database has a background stop channel")
	}

	d := &Database{bgStop: make(chan struct{})}
	d.stopBackground()
	d.stopBackground() // second call must not panic
	select {
	case <-d.backgroundStop():
	default:
		t.Fatal("backgroundStop() not closed after stopBackground()")
	}
}
