package database

import (
	"database/sql"

	"github.com/go-while/go-pugleaf/internal/models"
)

// NNTP reader-side reads (plan nntp-server-hardening, slice nntp-read).
//
// Every article query carries "hide = 0": an article with hide > 0 is never served over
// NNTP (it is absent from LISTGROUP, the GROUP marks and ARTICLE/HEAD/BODY/STAT). None of
// these reads uses ArticleCache, so a hide set by the web admin takes effect at once.

// NNTPListGroupChunk is the number of article numbers one NNTPArticleNumbers call returns
// by default; LISTGROUP pages through a group in chunks of this size.
const NNTPListGroupChunk = 10000

// NNTPArticleByNum returns the visible (hide = 0) article num of groupDB with DBArtNum,
// ArticleNums and NewsgroupsPtr set. It returns sql.ErrNoRows when the article is absent
// or hidden. NULL text columns are read as empty strings.
func (db *Database) NNTPArticleByNum(groupDB *GroupDB, num int64) (*models.Article, error) {
	const query = `SELECT article_num, message_id, COALESCE(subject, ''), COALESCE(from_header, ''), date_sent,
		COALESCE(date_string, ''), COALESCE("references", ''), COALESCE(bytes, 0), COALESCE(lines, 0),
		COALESCE(reply_count, 0), COALESCE(path, ''), COALESCE(headers_json, ''), COALESCE(body_text, ''), imported_at
		FROM articles WHERE article_num = ? AND hide = 0`
	var a models.Article
	var artnum int64
	var dateSent, importedAt sql.NullTime
	if err := RetryableQueryRowScan(groupDB.DB, query, []interface{}{num},
		&artnum, &a.MessageID, &a.Subject, &a.FromHeader, &dateSent, &a.DateString, &a.References,
		&a.Bytes, &a.Lines, &a.ReplyCount, &a.Path, &a.HeadersJSON, &a.BodyText, &importedAt); err != nil {
		return nil, err
	}
	a.DateSent = dateSent.Time
	a.ImportedAt = importedAt.Time
	a.DBArtNum = artnum
	a.ArticleNums = map[*string]int64{groupDB.NewsgroupPtr: artnum}
	a.NewsgroupsPtr = []*string{groupDB.NewsgroupPtr}
	return &a, nil
}

// NNTPArticleByMessageID returns the visible (hide = 0) article with messageID in groupDB,
// like NNTPArticleByNum. It returns sql.ErrNoRows when the article is absent or hidden.
func (db *Database) NNTPArticleByMessageID(groupDB *GroupDB, messageID string) (*models.Article, error) {
	var num int64
	if err := RetryableQueryRowScan(groupDB.DB,
		`SELECT article_num FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`,
		[]interface{}{messageID}, &num); err != nil {
		return nil, err
	}
	return db.NNTPArticleByNum(groupDB, num)
}

// NNTPArticleMessageID returns the message-id of the visible (hide = 0) article num of
// groupDB (STAT). It returns sql.ErrNoRows when the article is absent or hidden.
func (db *Database) NNTPArticleMessageID(groupDB *GroupDB, num int64) (string, error) {
	var messageID string
	if err := RetryableQueryRowScan(groupDB.DB,
		`SELECT message_id FROM articles WHERE article_num = ? AND hide = 0`,
		[]interface{}{num}, &messageID); err != nil {
		return "", err
	}
	return messageID, nil
}

// NNTPGroupWatermarks returns the lowest and highest number of the visible (hide = 0)
// articles in groupDB; both are 0 for a group without visible articles.
func (db *Database) NNTPGroupWatermarks(groupDB *GroupDB) (low, high int64, err error) {
	err = RetryableQueryRowScan(groupDB.DB,
		`SELECT COALESCE(MIN(article_num), 0), COALESCE(MAX(article_num), 0) FROM articles WHERE hide = 0`,
		nil, &low, &high)
	if err != nil {
		return 0, 0, err
	}
	return low, high, nil
}

// NNTPGroupNextArticleNum returns the number the next article stored in groupDB gets: the
// expression the inserts use (migration 0010: max(article_num_seq.last_num, MAX(article_num)) + 1),
// so it stays above every number ever handed out, also after all articles were expired. A group
// DB without the sequence row (not migrated) falls back to MAX(article_num) + 1; an unused group
// gives 1.
func (db *Database) NNTPGroupNextArticleNum(groupDB *GroupDB) (int64, error) {
	var next int64
	err := RetryableQueryRowScan(groupDB.DB,
		`SELECT MAX(COALESCE((SELECT last_num FROM article_num_seq WHERE id = 1), 0),
		            COALESCE((SELECT MAX(article_num) FROM articles), 0)) + 1`,
		nil, &next)
	if err != nil {
		return 0, err
	}
	return next, nil
}

// NNTPArticleNumbers returns up to limit numbers of visible (hide = 0) articles in
// first..last (inclusive), ascending. limit <= 0 means NNTPListGroupChunk.
func (db *Database) NNTPArticleNumbers(groupDB *GroupDB, first, last int64, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = NNTPListGroupChunk
	}
	rows, err := RetryableQuery(groupDB.DB,
		`SELECT article_num FROM articles WHERE hide = 0 AND article_num >= ? AND article_num <= ?
		 ORDER BY article_num ASC LIMIT ?`, first, last, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0, 64)
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// NNTPActiveNewsgroups returns the active newsgroups from the main DB, ordered by name,
// with Name, Description, LastArticle, LowWater, Status and Active set (LIST ACTIVE and
// LIST NEWSGROUPS). Every nullable column is read through COALESCE, so a row with NULL
// fields is listed instead of aborting the list; a NULL status reads as "y".
func (db *Database) NNTPActiveNewsgroups() ([]*models.Newsgroup, error) {
	rows, err := RetryableQuery(db.mainDB,
		`SELECT name, COALESCE(description, ''), COALESCE(last_article, 0), COALESCE(low_water, 0), COALESCE(status, 'y')
		 FROM newsgroups WHERE active = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Newsgroup
	for rows.Next() {
		g := &models.Newsgroup{Active: true}
		if err := rows.Scan(&g.Name, &g.Description, &g.LastArticle, &g.LowWater, &g.Status); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
