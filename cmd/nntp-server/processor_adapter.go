package main

import (
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// ProcessorAdapter adapts the processor.Processor to implement nntp.ArticleProcessor interface
type ProcessorAdapter struct {
	processor *processor.Processor
}

// NewProcessorAdapter creates a new processor adapter
func NewProcessorAdapter(proc *processor.Processor) *ProcessorAdapter {
	return &ProcessorAdapter{processor: proc}
}

// ProcessIncomingArticle processes an incoming article
func (pa *ProcessorAdapter) ProcessIncomingArticle(article *models.Article) (int, error) {
	// Forward the Article directly to the processor
	// No conversions needed since both use models.Article
	return pa.processor.ProcessIncomingArticle(article)
}

// CheckMessageID checks if an offered message-ID is wanted (IHAVE/TAKETHIS)
func (pa *ProcessorAdapter) CheckMessageID(messageID string) int {
	return pa.processor.CheckMessageID(messageID)
}

// FindArticleByMessageID finds an article by message-ID (current group first, then history)
func (pa *ProcessorAdapter) FindArticleByMessageID(messageID, currentGroup string) (*models.Article, error) {
	return pa.processor.FindArticleByMessageID(messageID, currentGroup)
}

// XrefLocations returns the groups and article numbers known for messageID (Xref: header)
func (pa *ProcessorAdapter) XrefLocations(messageID string) ([]nntp.XrefLocation, error) {
	return pa.processor.XrefLocations(messageID)
}

// CheckNoMoreWorkInHistory checks if there's no more work in history
func (pa *ProcessorAdapter) CheckNoMoreWorkInHistory() bool {
	return pa.processor.CheckNoMoreWorkInHistory()
}

// Note: parseArticleData has been removed as we now pass ArticleData
// directly between components, avoiding unnecessary conversions between
// []string and []byte
