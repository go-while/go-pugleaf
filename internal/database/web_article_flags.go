package database

// Spam/hide flags of one article, read straight from the group DB.
// The web handlers use them to decide visibility (hide > 0 = gone for non-admins) and to show
// the counters. They never come from the article cache: a cached *Article may be stale and
// the cached copy is shared between requests.

const query_GetArticleFlags = `SELECT spam, hide FROM articles WHERE article_num = ?`

const query_GetArticleFlagsByMessageID = `SELECT article_num, spam, hide FROM articles WHERE message_id = ?`

// GetArticleFlags returns the spam and hide counters of articleNum in groupDB.
// A missing article returns sql.ErrNoRows unwrapped.
func (db *Database) GetArticleFlags(groupDB *GroupDB, articleNum int64) (spam, hide int, err error) {
	err = RetryableQueryRowScan(groupDB.DB, query_GetArticleFlags, []interface{}{articleNum}, &spam, &hide)
	if err != nil {
		return 0, 0, err
	}
	return spam, hide, nil
}

// GetArticleFlagsByMessageID returns the article number and the spam and hide counters of
// messageID in groupDB. A missing article returns sql.ErrNoRows unwrapped.
func (db *Database) GetArticleFlagsByMessageID(groupDB *GroupDB, messageID string) (articleNum int64, spam, hide int, err error) {
	err = RetryableQueryRowScan(groupDB.DB, query_GetArticleFlagsByMessageID, []interface{}{messageID}, &articleNum, &spam, &hide)
	if err != nil {
		return 0, 0, 0, err
	}
	return articleNum, spam, hide, nil
}
