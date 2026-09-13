package main

import (
	"github.com/go-while/go-pugleaf/internal/models"
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

// CheckNoMoreWorkInHistory checks if there's no more work in history
func (pa *ProcessorAdapter) CheckNoMoreWorkInHistory() bool {
	return pa.processor.CheckNoMoreWorkInHistory()
}
