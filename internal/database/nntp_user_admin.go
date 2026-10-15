package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/mattn/go-sqlite3"
)

// ErrWebUserHasNNTPAccount: the web user already owns an active NNTP account. Migration 0031
// enforces it with a partial UNIQUE index on nntp_users(web_user_id) for active accounts, so
// the write itself refuses a second mapping (no check-then-write race). Its text is the
// message the admin pages show.
var ErrWebUserHasNNTPAccount = errors.New("web user already has an NNTP account")

// IsWebUserMappingConflict reports whether err is ErrWebUserHasNNTPAccount or the raw UNIQUE
// constraint error of that index (an INSERT, an activation or an update of nntp_users that
// would give a web user a second active NNTP account).
func IsWebUserMappingConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrWebUserHasNNTPAccount) {
		return true
	}
	var se sqlite3.Error
	if !errors.As(err, &se) || se.ExtendedCode != sqlite3.ErrConstraintUnique {
		return false
	}
	return strings.Contains(se.Error(), "nntp_users.web_user_id")
}

// mapWebUserMappingConflict turns the UNIQUE constraint error of the web user index into
// ErrWebUserHasNNTPAccount (wrapping the original); other errors are returned unchanged.
func mapWebUserMappingConflict(err error) error {
	if err == nil || errors.Is(err, ErrWebUserHasNNTPAccount) || !IsWebUserMappingConflict(err) {
		return err
	}
	return fmt.Errorf("%w (%v)", ErrWebUserHasNNTPAccount, err)
}

// DefaultNNTPUserMaxConns is the maxconns value of newly created NNTP users
// (nntpmgr -maxconns default, CreateNNTPUserForWebUser). It is only enforced when
// the NNTP server runs with EnforceUserMaxConns.
const DefaultNNTPUserMaxConns = 3

// GetNNTPUserByUsernameAnyState retrieves an NNTP user by username whether it is
// active or not (admin tools: update, re-activate, delete). Logins must keep using
// GetNNTPUserByUsername / AuthenticateNNTPUser.
func (db *Database) GetNNTPUserByUsernameAnyState(username string) (*models.NNTPUser, error) {
	query := `SELECT id, username, password, maxconns, posting, COALESCE(web_user_id, 0), created_at, updated_at, last_login, is_active
	          FROM nntp_users WHERE username = ?`

	var u models.NNTPUser
	err := RetryableQueryRowScan(db.mainDB, query, []interface{}{username}, &u.ID, &u.Username, &u.Password, &u.MaxConns, &u.Posting, &u.WebUserID,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLogin, &u.IsActive)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CountNNTPUsersByWebUserID counts the NNTP users (active or not) mapped to a web
// user. webUserID <= 0 means "no web user" and counts 0.
func (db *Database) CountNNTPUsersByWebUserID(webUserID int64) (int, error) {
	if webUserID <= 0 {
		return 0, nil
	}
	var count int
	err := RetryableQueryRowScan(db.mainDB, `SELECT COUNT(*) FROM nntp_users WHERE web_user_id = ?`, []interface{}{webUserID}, &count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateNNTPUserWebUserID maps an NNTP user to a web user; webUserID 0 removes the
// mapping (stored as NULL, the column is a foreign key to users(id)). It returns
// sql.ErrNoRows when no NNTP user has this id and an error wrapping ErrWebUserHasNNTPAccount
// when the account is active and the web user already owns another active one (migration
// 0031): callers write and map that error instead of counting first.
func (db *Database) UpdateNNTPUserWebUserID(userID int, webUserID int64) error {
	res, err := RetryableExec(db.mainDB,
		`UPDATE nntp_users SET web_user_id = NULLIF(?, 0), updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		webUserID, userID)
	if err != nil {
		return mapWebUserMappingConflict(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
