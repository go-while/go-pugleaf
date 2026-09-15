package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// ErrArticleNotFound is returned by FlagArticleSpamByUser when the article does not exist in the group.
var ErrArticleNotFound = errors.New("article not found")

// FlagArticleSpamByUser records a spam flag of userID for an article and increments the
// article spam counter only when this user had not flagged it before. The INSERT OR IGNORE
// on the user_spam_flags primary key makes concurrent calls by the same user count once.
// It returns true when the flag was new and the counter was incremented.
func (db *Database) FlagArticleSpamByUser(userID int64, groupName string, articleNum int64) (bool, error) {
	newsgroupID, err := db.GetNewsgroupID(groupName)
	if err != nil {
		return false, err
	}

	groupDB, err := db.GetGroupDB(groupName)
	if err != nil {
		return false, fmt.Errorf("failed to get group database: %w", err)
	}
	defer groupDB.Return()

	var one int
	err = RetryableQueryRowScan(groupDB.DB, "SELECT 1 FROM articles WHERE article_num = ?", []interface{}{articleNum}, &one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrArticleNotFound
		}
		return false, fmt.Errorf("failed to check article: %w", err)
	}

	res, err := RetryableExec(db.mainDB,
		"INSERT OR IGNORE INTO user_spam_flags (user_id, newsgroup_id, article_num) VALUES (?, ?, ?)",
		userID, newsgroupID, articleNum)
	if err != nil {
		return false, fmt.Errorf("failed to record user spam flag: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to record user spam flag: %w", err)
	}
	if n == 0 {
		return false, nil // already flagged by this user
	}

	// Increment the counter with the handle we hold; roll the flag back when that fails,
	// so flag row and counter stay consistent.
	if _, err := RetryableExec(groupDB.DB, "UPDATE articles SET spam = spam + 1 WHERE article_num = ?", articleNum); err != nil {
		if _, delErr := RetryableExec(db.mainDB,
			"DELETE FROM user_spam_flags WHERE user_id = ? AND newsgroup_id = ? AND article_num = ?",
			userID, newsgroupID, articleNum); delErr != nil {
			return false, fmt.Errorf("failed to increment spam count: %w (removing flag failed: %v)", err, delErr)
		}
		return false, fmt.Errorf("failed to increment spam count: %w", err)
	}

	// The main spam index is secondary: counter and flag are already consistent.
	if _, err := RetryableExec(db.mainDB, "INSERT OR IGNORE INTO spam (newsgroup_id, article_num) VALUES (?, ?)", newsgroupID, articleNum); err != nil {
		log.Printf("[DATABASE] FlagArticleSpamByUser: add to spam table group=%s article=%d: %v", groupName, articleNum, err)
	}
	return true, nil
}
