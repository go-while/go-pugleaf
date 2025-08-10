package nntp

// handleStat handles STAT command
func (c *ClientConnection) handleStat(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalStat)
}
