package main

import (
	"github.com/go-while/go-pugleaf/internal/history"
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

// Lookup checks if a message-ID exists in history
func (pa *ProcessorAdapter) Lookup(msgIdItem *history.MessageIdItem) (int, error) {
	return pa.processor.History.Lookup(msgIdItem)
}

// CheckNoMoreWorkInHistory checks if there's no more work in history
func (pa *ProcessorAdapter) CheckNoMoreWorkInHistory() bool {
	return pa.processor.CheckNoMoreWorkInHistory()
}
