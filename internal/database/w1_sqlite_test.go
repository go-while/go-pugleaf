package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

// Every connection of a pool opened with driverNameMain runs the stored pragmas.
func TestW1SQLiteConnectHookAllConns(t *testing.T) {
	w0DB(t) // OpenDatabase stored the main pragma list
	if mainConnPragmas.Load() == nil {
		t.Fatal("main pragma list not stored")
	}
	tdb, err := sql.Open(driverNameMain, filepath.Join(t.TempDir(), "hook.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	tdb.SetMaxOpenConns(8)

	ctx := context.Background()
	conns := make([]*sql.Conn, 0, 8)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < 8; i++ {
		c, err := tdb.Conn(ctx)
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	if st := tdb.Stats(); st.OpenConnections != 8 {
		t.Fatalf("open connections = %d, want 8", st.OpenConnections)
	}
	for i, c := range conns {
		var fk, busy int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn %d foreign_keys: %v", i, err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("conn %d busy_timeout: %v", i, err)
		}
		if fk != 1 || busy != 30000 {
			t.Errorf("conn %d: foreign_keys=%d busy_timeout=%d, want 1 and 30000", i, fk, busy)
		}
	}
}

func TestW1SQLiteNewGroupDBIsWAL(t *testing.T) {
	db := w0DB(t)
	g, err := db.GetGroupDB(w0Name("w1sqlite.wal"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Return()
	var mode string
	var syncMode int
	if err := g.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := g.DB.QueryRow("PRAGMA synchronous").Scan(&syncMode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || syncMode != 1 {
		t.Fatalf("journal_mode=%q synchronous=%d, want wal and 1", mode, syncMode)
	}
}

// Concurrent GetGroupDB/Return while idle cleanup closes every unused group DB must
// never hand out a closed *sql.DB.
func TestW1SQLiteConcurrentAcquireAndCleanup(t *testing.T) {
	db := w0DB(t)
	names := make([]string, 8)
	for i := range names {
		names[i] = w0Name("w1sqlite.conc")
	}

	stop := make(chan struct{})
	var cleanupWG sync.WaitGroup
	cleanupWG.Add(1)
	go func() {
		defer cleanupWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			db.cleanupIdleGroupsWith(0)
			time.Sleep(time.Millisecond)
		}
	}()

	var failures atomic.Int64
	var firstErr atomic.Value
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				name := names[(w+i)%len(names)]
				g, err := db.GetGroupDB(name)
				if err != nil {
					failures.Add(1)
					firstErr.CompareAndSwap(nil, fmt.Sprintf("GetGroupDB(%s): %v", name, err))
					continue
				}
				var one int
				if err := g.DB.QueryRow("SELECT 1").Scan(&one); err != nil {
					failures.Add(1)
					firstErr.CompareAndSwap(nil, fmt.Sprintf("SELECT 1 on %s: %v", name, err))
				}
				g.Return()
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	cleanupWG.Wait()

	if n := failures.Load(); n > 0 {
		t.Fatalf("%d failures, first: %v", n, firstErr.Load())
	}
	db.cleanupIdleGroupsWith(0)
}

// When group DB initialization fails, all concurrent callers return an error.
func TestW1SQLiteInitFailureWaitersReturn(t *testing.T) {
	db := w0DB(t)
	name := w0Name("w1sqlite.fail")
	dbDir := filepath.Join(db.GetDataDir(), "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dbDir, GroupHashMap.GroupToHash(name))
	if _, err := os.Stat(blocker); err == nil {
		t.Fatalf("%s already exists", blocker)
	}
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(blocker) })

	const callers = 8
	errs := make(chan error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			g, err := db.GetGroupDB(name)
			if err == nil {
				g.Return()
			}
			errs <- err
		}()
	}
	close(start)
	timeout := time.After(5 * time.Second)
	for i := 0; i < callers; i++ {
		select {
		case err := <-errs:
			if err == nil {
				t.Errorf("caller %d: GetGroupDB succeeded, want an error", i)
			}
		case <-timeout:
			t.Fatalf("only %d of %d callers returned within 5s", i, callers)
		}
	}
	db.MainMutex.RLock()
	_, exists := db.groupDB[name]
	db.MainMutex.RUnlock()
	if exists {
		t.Fatal("failed group DB entry left in the map")
	}
}

// A failing migration leaves neither its schema changes nor its schema_migrations row.
func TestW1SQLiteMigrationAtomic(t *testing.T) {
	dir := t.TempDir()
	tdb, err := sql.Open("sqlite3", filepath.Join(dir, "atomic.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	if err := ensureMigrationsTable(tdb, "main"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "9999_main_w1atomic.sql")
	if err := os.WriteFile(path, []byte("PRAGMA foreign_keys = ON;\nCREATE TABLE w1a(x);\nCREATE TABLE w1a(x);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &MigrationFile{FileName: "9999_main_w1atomic.sql", Version: 9999, Type: MigrationTypeMain, FilePath: path}
	if err := applyMigration(tdb, m, "main"); err == nil {
		t.Fatal("applyMigration succeeded, want an error")
	}
	var n int
	if err := tdb.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'w1a'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("table w1a exists after failed migration")
	}
	if err := tdb.QueryRow("SELECT count(*) FROM schema_migrations WHERE filename = ?", m.FileName).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("schema_migrations row recorded for failed migration")
	}
}

// All embedded migrations apply to empty main and group databases.
func TestW1SQLiteFreshInstallAllMigrations(t *testing.T) {
	shared := w0DB(t)
	dir := t.TempDir()
	migrations, err := getMigrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	var wantMain, wantGroup int
	for _, m := range migrations {
		switch m.Type {
		case MigrationTypeMain:
			wantMain++
		case MigrationTypeGroup:
			wantGroup++
		}
	}

	mainDB, err := sql.Open(driverNameMain, filepath.Join(dir, "main.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	fresh := &Database{mainDB: mainDB, dbconfig: shared.dbconfig}
	if err := fresh.migrateMainDB(); err != nil {
		t.Fatalf("main migrations: %v", err)
	}
	var n int
	if err := mainDB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != wantMain {
		t.Errorf("main schema_migrations rows = %d, want %d", n, wantMain)
	}
	var fk int
	if err := mainDB.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d after migrations, want 1", fk)
	}

	shared.ensureGroupConnPragmas()
	groupDB, err := sql.Open(driverNameGroup, filepath.Join(dir, "group.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer groupDB.Close()
	g := &GroupDB{Newsgroup: w0Name("w1sqlite.fresh"), DB: groupDB, Idle: time.Now()}
	if err := fresh.migrateGroupDB(g, false); err != nil {
		t.Fatalf("group migrations: %v", err)
	}
	if err := groupDB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != wantGroup {
		t.Errorf("group schema_migrations rows = %d, want %d", n, wantGroup)
	}
	var mode string
	if err := groupDB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("group journal_mode = %q after migrations, want wal", mode)
	}
}

func TestW1SQLiteRetryMatcher(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{sqlite3.Error{Code: sqlite3.ErrBusy}, true},
		{sqlite3.Error{Code: sqlite3.ErrLocked}, true},
		{fmt.Errorf("wrapped: %w", sqlite3.Error{Code: sqlite3.ErrBusy}), true},
		{sqlite3.Error{Code: sqlite3.ErrConstraint}, false},
		{errors.New("database is locked"), true},
		{errors.New("database table is locked"), true},
		{errors.New("UNIQUE constraint failed: users.locked"), false},
		{errors.New("user is busy"), false},
	}
	for _, c := range cases {
		if got := isRetryableSQLiteError(c.err); got != c.want {
			t.Errorf("isRetryableSQLiteError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestW1SQLiteStripMigrationPragmas(t *testing.T) {
	in := "PRAGMA foreign_keys = ON;\n  pragma synchronous = OFF;    -- comment\nCREATE TABLE a(x); -- PRAGMA foo;\n"
	out := stripMigrationPragmas(in)
	if strings.Contains(strings.ToUpper(strings.SplitN(out, "CREATE", 2)[0]), "PRAGMA") {
		t.Errorf("pragma lines not stripped: %q", out)
	}
	if !strings.Contains(out, "CREATE TABLE a(x); -- PRAGMA foo;") {
		t.Errorf("non-pragma line changed: %q", out)
	}
}
