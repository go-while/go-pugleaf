package database

import (
	"database/sql"
	"strings"
)

// OverviewRangeChunk is the number of rows one GetOverviewsRange/GetHeaderFieldRange call
// returns by default. NNTP range commands (XOVER, XHDR) page through a range in chunks of
// this size instead of loading or walking the whole range at once.
const OverviewRangeChunk = 1000

// HeaderFieldValue is one article's value of a header field (XHDR/HDR).
// Value is empty when the article has no such header.
type HeaderFieldValue struct {
	ArticleNum int64
	Value      string
}

// HeaderValueFromJSON returns the value of the first header named field (case-insensitive)
// in headersJSON, the raw header block as stored in articles.headers_json (header lines
// joined with "\n"). Continuation lines (leading SP or TAB) are unfolded with one space;
// the result is trimmed. It returns "" when the header is absent.
func HeaderValueFromJSON(headersJSON, field string) string {
	if headersJSON == "" || field == "" {
		return ""
	}
	var b strings.Builder
	found := false
	for _, line := range strings.Split(headersJSON, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if found {
			if line != "" && (line[0] == ' ' || line[0] == '\t') {
				if part := strings.TrimSpace(line); part != "" {
					if b.Len() > 0 {
						b.WriteByte(' ')
					}
					b.WriteString(part)
				}
				continue
			}
			break
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || !strings.EqualFold(line[:colon], field) {
			continue
		}
		found = true
		b.WriteString(strings.TrimSpace(line[colon+1:]))
	}
	return strings.TrimSpace(b.String())
}

// NNTPHeaderByMessageID returns the article number and the value of field for the visible
// (hide = 0) article with messageID in groupDB. The 7 overview fields come from their
// columns, any other field from headers_json (see HeaderValueFromJSON). It returns
// sql.ErrNoRows when the article is absent or hidden.
func (db *Database) NNTPHeaderByMessageID(groupDB *GroupDB, field, messageID string) (articleNum int64, value string, err error) {
	var query string
	fromJSON := false
	switch strings.ToLower(field) {
	case "subject":
		query = `SELECT article_num, subject FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`
	case "from":
		query = `SELECT article_num, from_header FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`
	case "date":
		query = `SELECT article_num, date_string FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`
	case "message-id":
		query = `SELECT article_num, message_id FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`
	case "references":
		query = `SELECT article_num, "references" FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`
	case "bytes":
		query = `SELECT article_num, bytes FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`
	case "lines":
		query = `SELECT article_num, lines FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`
	default:
		query = `SELECT article_num, headers_json FROM articles WHERE message_id = ? AND hide = 0 LIMIT 1`
		fromJSON = true
	}
	var raw sql.NullString // NULL gives an empty value; integers convert to decimal text
	if err := RetryableQueryRowScan(groupDB.DB, query, []interface{}{messageID}, &articleNum, &raw); err != nil {
		return 0, "", err
	}
	if fromJSON {
		return articleNum, HeaderValueFromJSON(raw.String, field), nil
	}
	return articleNum, raw.String, nil
}
