package database

import (
	"database/sql"
	"fmt"
	"unicode/utf8"
)

// UpdateUserProfile writes the password hash, the email address and the display name of one
// user in a single transaction. A nil argument leaves that column untouched.
//
// The profile form may change all three at once. Written as three independent statements, a
// transient failure on the second left the first committed: the user saw only "Failed to
// update email" while the password had silently moved. Here either every requested column is
// written or none is, and the retry logic of RetryableTransactionExec retries the whole set.
//
// It is the single writer of those three columns: it runs the query_UpdateUser* statements of
// queries.go, and the per-field helpers there (UpdateUserEmail for the admin user edit,
// UpdateUserPassword for cmd/usermgr) delegate to it with the other two fields nil (E8).
//
// displayName is limited to 64 runes - characters, not bytes, so the limit matches the web
// validation and a multi-byte name the form accepted is not rejected here (F6). This is the
// only copy of that limit; UpdateUserDisplayName, which held the other one, is gone with its
// last caller.
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
			if _, err := tx.Exec(query_UpdateUserPassword, *passwordHash, userID); err != nil {
				return fmt.Errorf("update password_hash of user %d: %w", userID, err)
			}
		}
		if email != nil {
			if _, err := tx.Exec(query_UpdateUserEmail, *email, userID); err != nil {
				return fmt.Errorf("update email of user %d: %w", userID, err)
			}
		}
		if displayName != nil {
			if _, err := tx.Exec(query_UpdateUserDisplayName, *displayName, userID); err != nil {
				return fmt.Errorf("update display_name of user %d: %w", userID, err)
			}
		}
		return nil
	})
}
