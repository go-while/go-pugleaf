package database

import (
	"database/sql"
	"fmt"

	"github.com/go-while/go-pugleaf/internal/models"
)

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
// sql.ErrNoRows when no NNTP user has this id.
func (db *Database) UpdateNNTPUserWebUserID(userID int, webUserID int64) error {
	res, err := RetryableExec(db.mainDB,
		`UPDATE nntp_users SET web_user_id = NULLIF(?, 0), updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		webUserID, userID)
	if err != nil {
		return err
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
