package nntp

// handleArticle handles ARTICLE command
func (c *ClientConnection) handleArticle(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalArticle)
}
