package database

// NNTP NEXT/LAST reads (plan nntp-rfc-commands, slice rfc-reader). Like every NNTP read
// they see only visible articles (hide = 0). article_num is the rowid primary key, so
// each call is one index seek.

const query_NNTPNextArticle = `SELECT article_num, message_id FROM articles WHERE article_num > ? AND hide = 0 ORDER BY article_num ASC LIMIT 1`
const query_NNTPPrevArticle = `SELECT article_num, message_id FROM articles WHERE article_num < ? AND hide = 0 ORDER BY article_num DESC LIMIT 1`

// NNTPNextArticle returns the number and message-id of the first visible article of
// groupDB with a number above cur. It returns sql.ErrNoRows when there is none.
func (db *Database) NNTPNextArticle(groupDB *GroupDB, cur int64) (num int64, messageID string, err error) {
	if err = RetryableQueryRowScan(groupDB.DB, query_NNTPNextArticle, []interface{}{cur}, &num, &messageID); err != nil {
		return 0, "", err
	}
	return num, messageID, nil
}

// NNTPPrevArticle returns the number and message-id of the last visible article of
// groupDB with a number below cur. It returns sql.ErrNoRows when there is none.
func (db *Database) NNTPPrevArticle(groupDB *GroupDB, cur int64) (num int64, messageID string, err error) {
	if err = RetryableQueryRowScan(groupDB.DB, query_NNTPPrevArticle, []interface{}{cur}, &num, &messageID); err != nil {
		return 0, "", err
	}
	return num, messageID, nil
}
