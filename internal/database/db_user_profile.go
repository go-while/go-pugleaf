package database

import (
	"database/sql"
	"fmt"
	"unicode/utf8"
)

// Statements of UpdateUserProfile. They are deliberately separate from the per-field
// helpers in queries.go (UpdateUserPassword/UpdateUserEmail/UpdateUserDisplayName), which
// stay as they are for their own callers (web_admin_userfuncs.go, cmd/usermgr).
const (
	query_UpdateUserProfilePassword    = `UPDATE users SET password_hash = ? WHERE id = ?`
	query_UpdateUserProfileEmail       = `UPDATE users SET email = ? WHERE id = ?`
	query_UpdateUserProfileDisplayName = `UPDATE users SET display_name = ? WHERE id = ?`
)

// UpdateUserProfile writes the password hash, the email address and the display name of one
// user in a single transaction. A nil argument leaves that column untouched.
//
// The profile form may change all three at once. Written as three independent statements, a
// transient failure on the second left the first committed: the user saw only "Failed to
// update email" while the password had silently moved. Here either every requested column is
// written or none is, and the retry logic of RetryableTransactionExec retries the whole set.
//
// displayName is limited to 64 runes, the same limit (and the same rune counting, not bytes)
// that UpdateUserDisplayName enforces, so this path accepts exactly what that one accepted.
func (db *Database) UpdateUserProfile(userID int64, passwordHash *string, email *string, displayName *string) error {
	if displayName != nil && utf8.RuneCountInString(*displayName) > 64 {
		return fmt.Errorf("display name is too long")
	}
	if passwordHash == nil && email == nil && displayName == nil {
		return nil // nothing changed
	}
	return RetryableTransactionExec(db.mainDB, func(tx *sql.Tx) error {
		// RetryableTransactionExec rolls the transaction back on every error returned here.
		// Every statement below must stay idempotent: a retryable commit failure re-runs this
		// whole closure on a fresh transaction. Absolute "SET col = ?" is safe; a relative one
		// (see query_UpdateUserPostCount, "post_count = post_count+1") would double-apply.
		if passwordHash != nil {
			if _, err := tx.Exec(query_UpdateUserProfilePassword, *passwordHash, userID); err != nil {
				return fmt.Errorf("update password_hash of user %d: %w", userID, err)
			}
		}
		if email != nil {
			if _, err := tx.Exec(query_UpdateUserProfileEmail, *email, userID); err != nil {
				return fmt.Errorf("update email of user %d: %w", userID, err)
			}
		}
		if displayName != nil {
			if _, err := tx.Exec(query_UpdateUserProfileDisplayName, *displayName, userID); err != nil {
				return fmt.Errorf("update display_name of user %d: %w", userID, err)
			}
		}
		return nil
	})
}
