package nntp

// handleHead handles HEAD command
func (c *ClientConnection) handleHead(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalHead)
}
