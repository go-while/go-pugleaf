package processor

import (
	"strings"
	"sync/atomic"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// ReuseCrossposts enables copying crossposted articles already stored in another group
// (found via the history index) instead of downloading them again. Set by the fetcher flag.
var ReuseCrossposts = true

// ReuseStats counts reuse attempts (no logging per miss)
var ReuseStats struct {
	Hits   atomic.Int64 // article rebuilt from another group DB
	Misses atomic.Int64 // history knew the message-id but no usable copy was found
	Errors atomic.Int64 // history lookup / group db / parse errors
}

// reuseStoredArticle returns a freshly parsed article for messageID if the history index
// says it is already stored in another group and that copy is usable.
// Returns nil if reuse is disabled, not possible or failed: the caller downloads the article.
func (proc *Processor) reuseStoredArticle(messageID string, currentGroupID int64, currentGroup string) *models.Article {
	if !ReuseCrossposts || proc == nil || !proc.History.Enabled() || proc.DB == nil || messageID == "" {
		return nil
	}
	ids, err := proc.History.LookupGroups(messageID)
	if err != nil {
		ReuseStats.Errors.Add(1)
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	for i, id := range ids {
		if id <= 0 || id == currentGroupID {
			continue
		}
		if reuseSeenBefore(ids[:i], id) {
			continue // try each ID once
		}
		name, ok := database.NewsgroupDBsIDcache.GetNewsgroupNameByID(id, proc.DB)
		if !ok || name == "" || name == currentGroup {
			continue
		}
		stored := proc.reuseGetStored(name, messageID)
		if stored == nil {
			continue
		}
		lines, ok := storedArticleLines(stored.HeadersJSON, stored.BodyText)
		if !ok {
			continue
		}
		art, err := nntp.ParseLegacyArticleLines(messageID, lines, true)
		if err != nil || art == nil {
			ReuseStats.Errors.Add(1)
			continue
		}
		ReuseStats.Hits.Add(1)
		return art
	}
	ReuseStats.Misses.Add(1)
	return nil
}

// reuseGetStored loads messageID from the group DB of name. Returns nil if not usable.
func (proc *Processor) reuseGetStored(name string, messageID string) *models.Article {
	groupDB, err := proc.DB.GetGroupDB(name)
	if err != nil || groupDB == nil {
		ReuseStats.Errors.Add(1)
		return nil
	}
	stored, err := proc.DB.GetArticleByMessageID(groupDB, messageID)
	groupDB.Return()
	if err != nil || stored == nil || stored.MessageID != messageID || stored.HeadersJSON == "" {
		return nil
	}
	return stored
}

// reuseSeenBefore reports whether id is already in prev (dedup without allocating)
func reuseSeenBefore(prev []int64, id int64) bool {
	for _, p := range prev {
		if p == id {
			return true
		}
	}
	return false
}

// storedArticleLines rebuilds the wire lines (headers, "", body) of a stored article,
// as expected by nntp.ParseLegacyArticleLines.
// headersJSON holds the raw header lines joined by "\n", bodyText the body joined by "\n".
// Trailing "\r" is stripped from every line. Returns false if the headers are unusable.
func storedArticleLines(headersJSON string, bodyText string) ([]string, bool) {
	if strings.TrimSpace(headersJSON) == "" {
		return nil, false
	}
	headers := strings.Split(strings.TrimRight(headersJSON, "\r\n"), "\n")
	var body []string
	if bodyText != "" {
		body = strings.Split(bodyText, "\n")
	}
	lines := make([]string, 0, len(headers)+1+len(body))
	for _, line := range headers {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			// an empty line inside the headers would end the header block early
			return nil, false
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	for _, line := range body {
		lines = append(lines, strings.TrimSuffix(line, "\r"))
	}
	return lines, true
}
