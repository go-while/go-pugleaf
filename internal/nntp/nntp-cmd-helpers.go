package nntp

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// Helper functions
func (c *ClientConnection) rateLimitOnError() {
	// Implement rate limiting logic here if needed
	// For now, just a placeholder
	time.Sleep(time.Second)
}

// parseArticleHeaders parses headers from an article
func (c *ClientConnection) parseArticleHeadersFull(article *models.Article) []string {
	result := strings.Split(article.HeadersJSON, "\n")
	return result
}

func (c *ClientConnection) parseArticleHeadersShort(article *models.Article) []string {
	headers := []string{}

	// Basic headers
	headers = append(headers, fmt.Sprintf("Subject: %s", article.Subject))
	headers = append(headers, fmt.Sprintf("From: %s", article.FromHeader))
	headers = append(headers, fmt.Sprintf("Date: %s", article.DateString))
	headers = append(headers, fmt.Sprintf("Message-ID: %s", article.MessageID))

	if article.References != "" {
		headers = append(headers, fmt.Sprintf("References: %s", article.References))
	}

	if article.Path != "" {
		headers = append(headers, fmt.Sprintf("Path: %s", article.Path))
	}

	// Additional headers from HeadersJSON if available
	// TODO: Parse HeadersJSON for additional headers if needed

	return headers
}

// parseArticleBody parses the body from an article
func (c *ClientConnection) parseArticleBody(article *models.Article) []string {
	if article.BodyText == "" {
		return []string{""}
	}

	// Split body into lines
	lines := strings.Split(article.BodyText, "\n")

	// Handle dot-stuffing: lines starting with "." need to be escaped as ".."
	for i, line := range lines {
		if strings.HasPrefix(line, DOT) {
			lines[i] = DOT + line
		}
	}

	return lines
}

// formatOverviewLine formats an overview entry for XOVER response
func (c *ClientConnection) formatOverviewLine(overview *models.Overview) string {
	// XOVER format: number\tsubject\tfrom\tdate\tmessage-id\treferences\tbytes\tlines\txref
	return fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t",
		overview.ArticleNum,
		overview.Subject,
		overview.FromHeader,
		overview.DateString,
		overview.MessageID,
		overview.References,
		overview.Bytes,
		overview.Lines,
	)
}
