package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// article_num_cleanup.go removes the main-database rows that hang off a group's article numbers
// (INT-5, AUDIT-2026-10). The main-DB spam and user_spam_flags tables reference articles by
// (newsgroup_id, article_num) without a foreign key into the group database, so they outlive the
// articles: once a number is deleted (expire-news) or the numbering restarts (ResetNewsgroupData)
// those rows must go, or the next article with that number inherits them.

// spamCleanupChunk is how many article numbers one DELETE ... IN (...) statement carries.
const spamCleanupChunk = 500

const (
	query_DeleteSpamRowsForArticles     = `DELETE FROM spam WHERE newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?) AND article_num IN (%s)`
	query_DeleteUserSpamFlagsForArticle = `DELETE FROM user_spam_flags WHERE newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?) AND article_num IN (%s)`
	query_DeleteSpamRowsForGroup        = `DELETE FROM spam WHERE newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?)`
	query_DeleteUserSpamFlagsForGroup   = `DELETE FROM user_spam_flags WHERE newsgroup_id = (SELECT id FROM newsgroups WHERE name = ?)`
)

// DeleteSpamRowsForArticles deletes the main-DB spam and user_spam_flags rows of the given
// article numbers of newsgroup, in chunks of spamCleanupChunk numbers, one retryable
// transaction per chunk. An unknown newsgroup deletes nothing.
func (db *Database) DeleteSpamRowsForArticles(newsgroup string, nums []int64) error {
	for start := 0; start < len(nums); start += spamCleanupChunk {
		end := start + spamCleanupChunk
		if end > len(nums) {
			end = len(nums)
		}
		chunk := nums[start:end]
		in := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]interface{}, 0, len(chunk)+1)
		args = append(args, newsgroup)
		for _, n := range chunk {
			args = append(args, n)
		}
		err := RetryableTransactionExec(db.mainDB, func(tx *sql.Tx) error {
			if _, err := tx.Exec(fmt.Sprintf(query_DeleteSpamRowsForArticles, in), args...); err != nil {
				return fmt.Errorf("delete spam rows: %w", err)
			}
			if _, err := tx.Exec(fmt.Sprintf(query_DeleteUserSpamFlagsForArticle, in), args...); err != nil {
				return fmt.Errorf("delete user_spam_flags rows: %w", err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("DeleteSpamRowsForArticles(%s, %d numbers): %w", newsgroup, len(chunk), err)
		}
	}
	return nil
}

// deleteSpamRowsForGroup deletes every main-DB spam and user_spam_flags row of newsgroup
// (ResetNewsgroupData: the numbering restarts at 1).
func (db *Database) deleteSpamRowsForGroup(newsgroup string) error {
	err := RetryableTransactionExec(db.mainDB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(query_DeleteSpamRowsForGroup, newsgroup); err != nil {
			return fmt.Errorf("delete spam rows: %w", err)
		}
		if _, err := tx.Exec(query_DeleteUserSpamFlagsForGroup, newsgroup); err != nil {
			return fmt.Errorf("delete user_spam_flags rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("deleteSpamRowsForGroup(%s): %w", newsgroup, err)
	}
	return nil
}
