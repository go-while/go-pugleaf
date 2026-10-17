package main

// Tests of the audit-delta-fixes slice adf-opt-ops-tools (A-ops-10): the web password
// rule, the end of the web session on a password change, and rune-safe truncation. The
// database lives below t.TempDir(); ./data is never used, and usermgrExit (it ends the
// process) is never called.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// adfOptOpenDB opens a scratch main database below t.TempDir() and tears it down at the
// end of the test (close StopChan, WG.Wait, Shutdown).
func adfOptOpenDB(t *testing.T) *database.Database {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		t.Skip("OpenDatabase refuses to run with euid < 1000")
	}
	dir := t.TempDir()
	database.NO_CACHE_BOOT = true
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() {
		close(db.StopChan)
		db.WG.Wait()
		if err := db.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return db
}

func TestAdfOptUsermgrValidatePassword(t *testing.T) {
	cases := []struct {
		pw string
		ok bool
	}{
		{pw: "", ok: false},
		{pw: "abcdef", ok: false},                      // the old 6-character minimum
		{pw: strings.Repeat("a", 11), ok: false},       // one below the web minimum
		{pw: strings.Repeat("a", 12), ok: true},        // web minimum
		{pw: strings.Repeat("a", 72), ok: true},        // bcrypt maximum
		{pw: strings.Repeat("a", 73), ok: false},       // bcrypt rejects it
		{pw: strings.Repeat("ä", 6), ok: true},         // 12 bytes: the rule counts bytes like the web
		{pw: strings.Repeat("ä", 36) + "a", ok: false}, // 73 bytes
	}
	for _, tc := range cases {
		err := validateCLIPassword([]byte(tc.pw))
		if (err == nil) != tc.ok {
			t.Fatalf("validateCLIPassword(%d bytes) = %v, want ok=%v", len(tc.pw), err, tc.ok)
		}
	}
}

func TestAdfOptUsermgrTruncateRunes(t *testing.T) {
	if got := truncate("short", 20); got != "short" {
		t.Fatalf("truncate kept-short = %q", got)
	}
	in := strings.Repeat("äöü", 10) // 30 runes, 60 bytes
	got := truncate(in, 20)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate split a rune: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 20 {
		t.Fatalf("truncate gave %d runes, want 20: %q", n, got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncate lost the ellipsis: %q", got)
	}
	// 20 runes of 2 bytes each fit in maxLen runes: kept whole
	if got := truncate(strings.Repeat("ä", 20), 20); got != strings.Repeat("ä", 20) {
		t.Fatalf("truncate cut a string of maxLen runes: %q", got)
	}
}

func TestAdfOptUsermgrPasswordUpdateEndsSession(t *testing.T) {
	db := adfOptOpenDB(t)
	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertUser(&models.User{Username: "adfoptuser", Email: "adfopt@example.invalid",
		DisplayName: "adfopt", PasswordHash: string(oldHash), Verified: 1}); err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	user, err := db.GetUserByUsername("adfoptuser")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	token, err := db.CreateUserSession(int64(user.ID), "192.0.2.1")
	if err != nil {
		t.Fatalf("CreateUserSession: %v", err)
	}
	if _, err := db.ValidateUserSession(token); err != nil {
		t.Fatalf("session not valid before the password change: %v", err)
	}

	// a too short password is refused and changes nothing
	if err := applyPasswordUpdate(db, user, []byte("short-pw")); err == nil {
		t.Fatalf("applyPasswordUpdate accepted an 8-byte password")
	}
	if _, err := db.ValidateUserSession(token); err != nil {
		t.Fatalf("refused password change ended the session: %v", err)
	}

	newPW := []byte("new-password-4567")
	if err := applyPasswordUpdate(db, user, newPW); err != nil {
		t.Fatalf("applyPasswordUpdate: %v", err)
	}
	if _, err := db.ValidateUserSession(token); err == nil {
		t.Fatalf("session still valid after the password change")
	}
	after, err := db.GetUserByUsername("adfoptuser")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), newPW); err != nil {
		t.Fatalf("stored hash does not match the new password: %v", err)
	}
}
