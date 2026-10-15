package processor

// Commit barrier of plan followup-leftovers (D-B, A-tools-5b) through the processor: bulk
// processArticle (the fetcher's path) counts every capture before the capture goroutine runs,
// so a WaitCommitted after the last processArticle returned waits for exactly those articles
// and returns nil once the batch writer committed them.
//
// The test opens its own database (OpenDatabase may run once per process at a time; nothing
// else in this package opens one) in a temp dir and shuts it down at the end.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// flNntpdbOpenDB opens a database in a temp dir; the cleanup stops its background work and
// shuts it down.
func flNntpdbOpenDB(t *testing.T) *database.Database {
	t.Helper()
	if runtime.GOOS != "windows" {
		if euid := os.Geteuid(); euid >= 0 && euid < 1000 {
			t.Skipf("skipping: OpenDatabase refuses euid %d (< 1000)", euid)
		}
	}
	dir := t.TempDir()
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() {
		close(db.StopChan)
		done := make(chan struct{})
		go func() {
			db.WG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(testWait):
			t.Errorf("db.WG.Wait did not return within %v", testWait)
		}
		if err := db.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return db
}

// flNntpdbArticleLines is a fetched article as the NNTP client hands it to processArticle.
func flNntpdbArticleLines(group, msgid string, i int) []string {
	return []string{
		"Path: peer.test.invalid!not-for-mail",
		"From: Tester <tester@test.invalid>",
		"Newsgroups: " + group,
		fmt.Sprintf("Subject: commit barrier %d", i),
		"Date: Sun, 11 Oct 2026 12:00:00 +0000",
		"Message-ID: " + msgid,
		"",
		fmt.Sprintf("body %d", i),
	}
}

func TestFlNntpdbBulkProcessThenWaitCommitted(t *testing.T) {
	db := flNntpdbOpenDB(t)
	// processArticle uses the global history.MsgIdCache: NewMsgIdItemCache creates it once
	proc := &Processor{DB: db, MsgIdCache: history.NewMsgIdItemCache()}
	group := fmt.Sprintf("flnntpdb.bulk.t%d", time.Now().UnixNano())
	mark := db.Batch.CommitMark(group)

	const n = 25
	for i := 0; i < n; i++ {
		msgid := fmt.Sprintf("<flnntpdb-bulk-%d-%d@test.invalid>", time.Now().UnixNano(), i)
		art, err := nntp.ParseLegacyArticleLines(msgid, flNntpdbArticleLines(group, msgid, i), true)
		if err != nil {
			t.Fatalf("ParseLegacyArticleLines %d: %v", i, err)
		}
		if _, err := proc.processArticle(art, group, true); err != nil {
			t.Fatalf("processArticle %d: %v", i, err)
		}
	}
	if err := db.Batch.WaitCommitted(mark, nil, testWait); err != nil {
		t.Fatalf("WaitCommitted after %d bulk articles: %v", n, err)
	}

	// committed means stored: every article is in the group database now
	groupDB, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB(%s): %v", group, err)
	}
	defer groupDB.Return()
	var stored int
	if err := database.RetryableQueryRowScan(groupDB.DB, "SELECT COUNT(*) FROM articles", nil, &stored); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if stored != n {
		t.Fatalf("%d articles stored when WaitCommitted returned, want %d", stored, n)
	}

	// a second run of the same articles stores nothing new and has nothing to wait for
	if err := db.Batch.WaitCommitted(db.Batch.CommitMark(group), nil, testWait); err != nil {
		t.Errorf("WaitCommitted with nothing captured: %v", err)
	}
}
