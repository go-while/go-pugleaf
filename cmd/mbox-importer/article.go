package main

import (
	"io"

	"github.com/go-while/go-pugleaf/internal/models"
)

// skipReason says why an article of the input was not handed to the processor.
type skipReason int

const (
	skipNone skipReason = iota
	skipNoMessageID
	skipBadMessageID
	skipNoBody
	skipBinary
	skipNoDate
	skipNoSubject
	skipTooManyHeaders
	skipNoNewsgroups
	skipBadGroup
)

func (r skipReason) String() string {
	switch r {
	case skipNone:
		return "none"
	case skipNoMessageID:
		return "no-msgid"
	case skipBadMessageID:
		return "bad-msgid"
	case skipNoBody:
		return "no-body"
	case skipBinary:
		return "binary"
	case skipNoDate:
		return "no-date"
	case skipNoSubject:
		return "no-subject"
	case skipTooManyHeaders:
		return "too-many-headers"
	case skipNoNewsgroups:
		return "no-newsgroups"
	case skipBadGroup:
		return "bad-group"
	}
	return "unknown"
}

// readBody reads a message body into lines without line endings; isBinary is set (and the
// body left out) when keepBinary is false and a line starts with "=ybegin".
func readBody(r io.Reader, keepBinary bool) (body []string, isBinary bool) {
	_, _ = io.Copy(io.Discard, r)
	_ = keepBinary
	return nil, false
}

// messageIDFromHeaders finds the article's message-id (lax repairs as in mbox2nntp; strict
// requires common.ValidMessageID).
func messageIDFromHeaders(head []string, strict bool) (msgid string, reason skipReason) {
	_ = head
	_ = strict
	return "", skipNoMessageID
}

// rewriteHead turns the stored header lines into the ones handed to the processor.
func rewriteHead(msgid string, head []string) (newhead []string, reason skipReason) {
	_ = msgid
	_ = head
	return nil, skipNoSubject
}

// firstNewsgroup returns the first group of the Newsgroups header.
func firstNewsgroup(head []string) (group string, reason skipReason) {
	_ = head
	return "", skipNoNewsgroups
}

// buildArticle builds the processor's article from the rewritten head and the body.
func buildArticle(msgid string, head, body []string) (*models.Article, error) {
	_ = msgid
	_ = head
	_ = body
	return nil, errNotImplemented
}
