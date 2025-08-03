package nntp

// handleBody handles BODY command
func (c *ClientConnection) handleBody(args []string) error {
	return c.retrieveArticleCommon(args, RetrievalBody)
}
