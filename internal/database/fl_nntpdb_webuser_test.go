package database

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fl_nntpdb_webuser_test.go: migration 0031 (plan followup-leftovers, D-C1) repairs duplicate
// active web user mappings (the lowest id keeps it) and adds a partial UNIQUE index, so a web
// user owns at most one active NNTP account and the write itself refuses a second one.

const flNntpdbWebMigration = "migrations/0031_main_nntp_users_unique_web.sql"
const flNntpdbWebIndex = "idx_nntp_users_web_user_active"

// flNntpdbWebUserID reads web_user_id of an NNTP user (0 for NULL).
func flNntpdbWebUserID(t *testing.T, db *Database, id int) int64 {
	t.Helper()
	var webID sql.NullInt64
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT web_user_id FROM nntp_users WHERE id = ?", []interface{}{id}, &webID); err != nil {
		t.Fatalf("read web_user_id of %d: %v", id, err)
	}
	return webID.Int64
}

// flNntpdbWebIndexExists reports whether the partial unique index exists.
func flNntpdbWebIndexExists(t *testing.T, db *Database) bool {
	t.Helper()
	var n int
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?",
		[]interface{}{flNntpdbWebIndex}, &n); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	return n == 1
}

// TestFlNntpdbWebUserMigrationRepair: run on a table with duplicates (index dropped first), the
// migration keeps the mapping of the lowest-id active account per web user, NULLs the other
// active ones, leaves inactive accounts and single mappings alone and creates the index.
func TestFlNntpdbWebUserMigrationRepair(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	if !flNntpdbWebIndexExists(t, db) {
		t.Fatalf("index %s missing: migration 0031 did not run when the test database was opened", flNntpdbWebIndex)
	}
	sqlText, err := EmbeddedMigrationsFS.ReadFile(flNntpdbWebMigration)
	if err != nil {
		t.Fatalf("read embedded %s: %v", flNntpdbWebMigration, err)
	}
	// Registered first, so it runs last (after the users below are deleted): the shared
	// database gets its index back even when the test fails half way.
	t.Cleanup(func() {
		if _, err := RetryableExec(db.GetMainDB(), string(sqlText)); err != nil {
			t.Errorf("restore %s: %v", flNntpdbWebIndex, err)
		}
	})
	if _, err := RetryableExec(db.GetMainDB(), "DROP INDEX "+flNntpdbWebIndex); err != nil {
		t.Fatalf("drop index: %v", err)
	}

	web := w1APIUser(t)
	single := w1APIUser(t)
	u1, _ := nshAuthNewUser(t, true, web.ID)
	u2, _ := nshAuthNewUser(t, true, web.ID)
	u3, _ := nshAuthNewUser(t, false, web.ID) // inactive: keeps its mapping
	u4, _ := nshAuthNewUser(t, true, web.ID)
	u5, _ := nshAuthNewUser(t, true, single.ID)

	if _, err := RetryableExec(db.GetMainDB(), string(sqlText)); err != nil {
		t.Fatalf("run %s: %v", flNntpdbWebMigration, err)
	}
	for _, c := range []struct {
		name string
		id   int
		want int64
	}{
		{"lowest active", u1.ID, web.ID},
		{"second active", u2.ID, 0},
		{"inactive", u3.ID, web.ID},
		{"third active", u4.ID, 0},
		{"single mapping", u5.ID, single.ID},
	} {
		if got := flNntpdbWebUserID(t, db, c.id); got != c.want {
			t.Errorf("%s (id %d): web_user_id %d, want %d", c.name, c.id, got, c.want)
		}
	}
	if !flNntpdbWebIndexExists(t, db) {
		t.Fatalf("index %s not created by the migration", flNntpdbWebIndex)
	}
	if got, err := db.GetNNTPUserByWebUserID(web.ID); err != nil || got.ID != u1.ID {
		t.Errorf("GetNNTPUserByWebUserID after the repair = (%v, %v), want id %d", got, err, u1.ID)
	}
	// the repaired table is consistent with the index: re-running is a no-op
	if _, err := RetryableExec(db.GetMainDB(), string(sqlText)); err != nil {
		t.Errorf("second run of %s: %v", flNntpdbWebMigration, err)
	}
}

// TestFlNntpdbWebUserUniqueMapping: with the index, a second active account of a web user is
// refused by the write itself: UpdateNNTPUserWebUserID returns ErrWebUserHasNNTPAccount, a raw
// insert or an activation fails with an error IsWebUserMappingConflict recognises. Inactive
// accounts may keep the mapping; the account that owns it may be rewritten.
func TestFlNntpdbWebUserUniqueMapping(t *testing.T) {
	adfOptHierSkipLowEUID(t)
	db := w0DB(t)
	web := w1APIUser(t)
	owner, _ := nshAuthNewUser(t, true, web.ID)
	other, _ := nshAuthNewUser(t, true, 0)
	parked, _ := nshAuthNewUser(t, false, web.ID) // inactive with the same mapping: allowed

	err := db.UpdateNNTPUserWebUserID(other.ID, web.ID)
	if !errors.Is(err, ErrWebUserHasNNTPAccount) {
		t.Fatalf("UpdateNNTPUserWebUserID of a second active account: %v, want ErrWebUserHasNNTPAccount", err)
	}
	if got := flNntpdbWebUserID(t, db, other.ID); got != 0 {
		t.Errorf("refused mapping was stored: web_user_id %d", got)
	}
	if err := db.UpdateNNTPUserWebUserID(owner.ID, web.ID); err != nil {
		t.Errorf("rewriting the owner's own mapping: %v", err)
	}

	err = db.InsertNNTPUser(&models.NNTPUser{Username: w0Name("flnntpdbweb"), Password: "pw-flnntpdb-web", MaxConns: 1, WebUserID: web.ID, IsActive: true})
	if !IsWebUserMappingConflict(err) {
		t.Fatalf("InsertNNTPUser of a second active account: %v, want a mapping conflict", err)
	}
	if err := db.ActivateNNTPUser(parked.ID); !IsWebUserMappingConflict(err) {
		t.Fatalf("ActivateNNTPUser of an inactive account of the same web user: %v, want a mapping conflict", err)
	}

	// once the owner is unmapped, the other account may take the web user
	if err := db.UpdateNNTPUserWebUserID(owner.ID, 0); err != nil {
		t.Fatalf("unmap owner: %v", err)
	}
	if err := db.UpdateNNTPUserWebUserID(other.ID, web.ID); err != nil {
		t.Fatalf("map the other account after the owner was unmapped: %v", err)
	}

	for _, e := range []error{nil, sql.ErrNoRows, errors.New("UNIQUE constraint failed: nntp_users.web_user_id")} {
		if IsWebUserMappingConflict(e) {
			t.Errorf("IsWebUserMappingConflict(%v) = true, want false (not a sqlite constraint error)", e)
		}
	}
}
