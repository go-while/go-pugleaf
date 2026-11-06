package main

// A-news batches and early B-news Message-IDs (plan mbox-import-discs). The oldest utzoo
// files (1981-83) hold A-news articles: "A<site>.<n>", newsgroups, path, ctime date, title,
// body, one after the other. They get Google Groups' Message-ID forms, so the same article
// from another archive is a duplicate: <anews.A<site>.<n>> and, for a B-news article with
// only "Article-I.D.: site.n", <bnews.site.n>.

import (
	"errors"
	"io"
)

// errANewsNotImplemented is returned by the wave-0 stub of parseANews.
var errANewsNotImplemented = errors.New("A-news parser not implemented yet")

// parseANews reads an A-news batch from r and calls emit once per article with an RFC 5322
// header block (Path, From, Newsgroups, Subject, Date, Message-ID) and the body.
func parseANews(r io.Reader, emit func(head []string, body io.Reader)) error {
	_, _ = r, emit
	return errANewsNotImplemented
}

// bnewsMessageID returns <bnews.ID> for a header block that has an "Article-I.D.: ID" field
// and no Message-ID field.
func bnewsMessageID(head []string) (string, bool) {
	_ = head
	return "", false
}
