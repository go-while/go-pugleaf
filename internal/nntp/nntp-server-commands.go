package nntp

// Command table and per-command gates used by handleCommand. Wave 0 of plan
// nntp-server-hardening installs these stubs; slice nntp-session fills in the
// argument-count table, the reader gate and the plaintext AUTHINFO policy.

// argCountOK reports whether the number of arguments is acceptable for command.
// Unknown commands are accepted (the dispatch answers 500).
func (c *ClientConnection) argCountOK(command string, args []string) bool {
	return true
}

// readGateOK reports whether command may run in the current authentication state.
func (c *ClientConnection) readGateOK(command string) bool {
	return true
}

// authinfoAdvertised reports whether CAPABILITIES lists AUTHINFO USER now
// (RFC 4643 §2.1: never after a successful login).
func (c *ClientConnection) authinfoAdvertised() bool {
	return !c.authenticated && c.isTLS
}

// postingAllowed reports whether this client may POST/IHAVE: the server has a
// processor and the client is anonymous or an authenticated user with posting rights.
func (c *ClientConnection) postingAllowed() bool {
	if c.server == nil || c.server.Processor == nil {
		return false
	}
	return !c.authenticated || c.user == nil || c.user.Posting
}
