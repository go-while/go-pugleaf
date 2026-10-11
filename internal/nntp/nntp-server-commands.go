package nntp

// Command table and per-command gates used by handleCommand.

// commandMaxArgs is the maximum number of arguments per command (RFC 3977
// §3.2.1: surplus arguments are a syntax error, 501). -1 means "not checked
// here": QUIT ignores surplus arguments (a client that wants to leave is never
// kept), TAKETHIS must consume its article, AUTHINFO takes the raw rest of the line.
var commandMaxArgs = map[string]int{
	"CAPABILITIES": 1, "MODE": 1, "AUTHINFO": -1, "QUIT": -1, "HELP": 0, "LIST": 2, "GROUP": 1,
	"LISTGROUP": 2, "STAT": 1, "HEAD": 1, "BODY": 1, "ARTICLE": 1, "XOVER": 1, "XHDR": 2,
	"POST": 0, "IHAVE": 1, "TAKETHIS": -1,
}

// readerCommands need authentication when NNTPServer.ReadRequiresAuth is set.
var readerCommands = map[string]bool{
	"GROUP": true, "LISTGROUP": true, "LIST": true, "STAT": true, "HEAD": true, "BODY": true,
	"ARTICLE": true, "XOVER": true, "XHDR": true, "NEXT": true, "LAST": true, "OVER": true,
	"HDR": true, "NEWGROUPS": true, "NEWNEWS": true,
}

// argCountOK reports whether the number of arguments is acceptable for command.
// Unknown commands are accepted (the dispatch answers 500).
func (c *ClientConnection) argCountOK(command string, args []string) bool {
	limit, ok := commandMaxArgs[command]
	return !ok || limit < 0 || len(args) <= limit
}

// readGateOK reports whether command may run in the current authentication state.
func (c *ClientConnection) readGateOK(command string) bool {
	if c.server == nil || !c.server.ReadRequiresAuth || c.authenticated {
		return true
	}
	return !readerCommands[command]
}

// authinfoAdvertised reports whether CAPABILITIES lists AUTHINFO USER now
// (RFC 4643 §2.1: never after a successful login). TLS connections always
// advertise it before login; plaintext ones only with PlaintextAuthAdvertise.
func (c *ClientConnection) authinfoAdvertised() bool {
	if c.authenticated {
		return false
	}
	return c.isTLS || (c.server != nil && c.server.PlaintextAuth == PlaintextAuthAdvertise)
}

// postingAllowed reports whether this client may POST/IHAVE: the server has a
// processor and the client is anonymous or an authenticated user with posting rights.
func (c *ClientConnection) postingAllowed() bool {
	if c.server == nil || c.server.Processor == nil {
		return false
	}
	return !c.authenticated || c.user == nil || c.user.Posting
}
