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
	"DATE": 0, "NEXT": 0, "LAST": 0, "NEWGROUPS": 3, "OVER": 1, "HDR": 2, "CHECK": 1, "STARTTLS": 0,
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

// loggedIn reports whether an AUTHINFO already succeeded on this connection, as a
// user or as a peer (RFC 4643 §2.3.2: no second AUTHINFO after that).
func (c *ClientConnection) loggedIn() bool {
	return c.authenticated || c.peerLoggedIn
}

// authinfoAdvertised reports whether CAPABILITIES lists AUTHINFO USER now
// (RFC 4643 §2.1: never after a successful login). TLS connections always
// advertise it before login; plaintext ones only with PlaintextAuthAdvertise.
func (c *ClientConnection) authinfoAdvertised() bool {
	if c.loggedIn() {
		return false
	}
	return c.isTLS || (c.server != nil && c.server.PlaintextAuth == PlaintextAuthAdvertise)
}

// postingAllowed reports whether this client may POST: the server has a processor and
// the client is anonymous or an authenticated user with posting rights. It says nothing
// about feeding (plan nntp-peering: nntp_users.posting means POST and nothing else).
func (c *ClientConnection) postingAllowed() bool {
	if c.server == nil || c.server.Processor == nil {
		return false
	}
	return !c.authenticated || c.user == nil || c.user.Posting
}

// feedAllowed reports whether this client may use the feed commands IHAVE, CHECK,
// TAKETHIS and MODE STREAM: the server has a processor and the connection is a peer
// connection (plan nntp-peering, table peers). A user with posting rights may not feed.
func (c *ClientConnection) feedAllowed() bool {
	if c.server == nil || c.server.Processor == nil {
		return false
	}
	return c.peer != nil
}

// feedDeniedCode is the reply code for a feed command on a connection that may not
// feed: 480 while a peer login could still help (an enabled peer with a login whose
// hosts hold this address, or that has no hosts, no AUTHINFO yet, and AUTHINFO is
// possible on this connection at all), else 502. Asking for authentication that this
// connection cannot send would leave the peer with nothing to try (RFC 4643 §2.2:
// a plaintext connection may be refused outright).
func (c *ClientConnection) feedDeniedCode() int {
	if c.server == nil || c.loggedIn() {
		return 502
	}
	if !c.isTLS && c.server.PlaintextAuth == PlaintextAuthRefuse {
		return 502
	}
	if c.server.Peers.LoginPossible(c.remoteIP()) {
		return 480
	}
	return 502
}
