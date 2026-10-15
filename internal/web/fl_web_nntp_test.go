package web

// Test for slice fl-web of plan followup-leftovers: an NNTP account still mapped to a web user
// that no longer exists (a legacy row from before foreign keys were enforced) can be updated;
// the dead mapping is treated as "no mapping" and the admin gets a notice.

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// flWebLegacyMapping points the NNTP user id at web user webUserID without the foreign key
// check (on one connection, switched back before the connection returns to the pool), the way
// rows written before foreign key enforcement look.
func flWebLegacyMapping(t *testing.T, id int, webUserID int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	conn, err := w0DB(t).GetMainDB().Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("conn.Close: %v", err)
		}
	}()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("foreign_keys=OFF: %v", err)
	}
	_, upErr := conn.ExecContext(ctx, "UPDATE nntp_users SET web_user_id = ? WHERE id = ?", webUserID, id)
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("foreign_keys=ON: %v", err)
	}
	if upErr != nil {
		t.Fatalf("legacy mapping: %v", upErr)
	}
}

func TestFlWebNNTPLegacyMappingDoesNotBlockUpdate(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	u := adfNNTPUsersCreate(t, cookie, url.Values{"maxconns": {"2"}})
	const deleted = int64(987654321)
	flWebLegacyMapping(t, u.ID, deleted)
	if v := adfNNTPUsersWebUserID(t, u.ID); !v.Valid || v.Int64 != deleted {
		t.Fatalf("fixture: web_user_id = %+v, want %d", v, deleted)
	}

	// The edit form submits the current (dead) mapping together with the other fields.
	success, failure := adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{
		"web_user_id": {strconv.FormatInt(deleted, 10)}, "maxconns": {"5"}, "posting": {"off"}})
	if failure != "" {
		t.Fatalf("update with a dead mapping refused: %q", failure)
	}
	if !strings.Contains(success, "no longer exists") {
		t.Errorf("success flash %q lacks the notice about the removed mapping", success)
	}
	if v := adfNNTPUsersWebUserID(t, u.ID); v.Valid {
		t.Errorf("dead mapping kept: web_user_id = %d, want NULL", v.Int64)
	}
	if got := adfNNTPUsersGet(t, u.ID).MaxConns; got != 5 {
		t.Errorf("maxconns = %d, want 5", got)
	}

	// Another missing web user (not the current mapping) is still refused.
	_, failure = adfNNTPUsersUpdate(t, cookie, u.ID, url.Values{"web_user_id": {strconv.FormatInt(deleted+1, 10)}})
	if !strings.Contains(failure, "web user not found") {
		t.Errorf("mapping to a missing web user: error flash %q, want \"web user not found\"", failure)
	}
}
