package nntp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// DefaultNNTPcliconnTimeout is the idle timeout of a client connection and the
// write timeout per flush (RFC 3977 §3.1: an inactivity timer of at least 3 minutes).
var DefaultNNTPcliconnTimeout = 3 * time.Minute

// ClientConnection represents a client connection to the NNTP server
type ClientConnection struct {
	conn           net.Conn
	textConn       *textproto.Conn
	server         *NNTPServer
	isTLS          bool
	authenticated  bool
	user           *models.NNTPUser // Currently authenticated NNTP user
	authUsername   string           // Username from AUTHINFO USER command
	currentGroup   string
	currentFirst   int64
	currentLast    int64
	currentArticle int64
}

// NewClientConnection creates a new client connection
func NewClientConnection(conn net.Conn, server *NNTPServer, isTLS bool) *ClientConnection {
	return &ClientConnection{
		conn:     conn,
		textConn: textproto.NewConn(conn),
		server:   server,
		isTLS:    isTLS,
	}
}

// UpdateDeadlines refreshes the read (idle) deadline before the next command.
// Write deadlines are set per flush by setWriteDeadline.
func (c *ClientConnection) UpdateDeadlines() {
	if c.conn == nil {
		return
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(c.ioTimeout()))
}

// Handle processes the client connection. It returns nil for a normal end
// (QUIT, client EOF, closed by Stop, overlong line closed with 400) and the
// error otherwise.
func (c *ClientConnection) Handle() error {
	defer c.textConn.Close()

	// Send welcome message
	if err := c.sendWelcome(); err != nil {
		if errors.Is(err, net.ErrClosed) {
			return nil // closed by Stop before the greeting went out
		}
		return fmt.Errorf("failed to send welcome: %w", err)
	}

	// Process commands
	for {
		line, err := c.readCommandLine()
		switch {
		case errors.Is(err, errCommandTooLong):
			// RFC 3977 §3.1: 512 octets including CRLF; the session continues
			c.rateLimitOnError()
			if err := c.sendResponse(501, "Command line too long"); err != nil {
				return err
			}
			c.UpdateDeadlines()
			continue
		case errors.Is(err, errCommandOverflow):
			// no line end within the read buffer: stop reading this client
			_ = c.sendResponse(400, "Command line too long")
			return nil
		case err != nil:
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("failed to read command: %w", err)
		}
		c.UpdateDeadlines()

		// A handler error (usually a failed write) ends the connection without a 500.
		if err := c.handleCommand(line); err != nil {
			if errors.Is(err, errQuit) || errors.Is(err, net.ErrClosed) {
				return nil // QUIT, or the socket was closed by Stop mid-reply
			}
			return err
		}
	}
}

// sendWelcome sends the initial greeting: 200 when this client may post, else
// 201 (RFC 3977 §5.1.1), with the configured hostname.
func (c *ClientConnection) sendWelcome() error {
	hostname := "go-pugleaf"
	if c.server != nil && c.server.Config != nil && c.server.Config.Hostname != "" {
		hostname = c.server.Config.Hostname
	}
	code := 201
	if c.postingAllowed() {
		code = 200
	}
	if c.isTLS {
		return c.sendResponse(code, fmt.Sprintf("%s NNTP server ready (TLS)", hostname))
	}
	return c.sendResponse(code, fmt.Sprintf("%s NNTP server ready", hostname))
}

// handleCommand parses and dispatches a command
func (c *ClientConnection) handleCommand(line string) error {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		c.rateLimitOnError()
		return c.sendResponse(500, "Empty command")
	}

	command := strings.ToUpper(parts[0])
	args := parts[1:]

	// Track command execution (unknown words are folded into one bucket, SEC-4)
	c.server.Stats.CommandExecuted(command)

	if !c.argCountOK(command, args) {
		c.rateLimitOnError()
		return c.sendResponse(501, "Syntax error: too many arguments")
	}
	if !c.readGateOK(command) {
		return c.sendResponse(480, "Authentication required")
	}

	// Dispatch to appropriate handler
	switch command {
	case "CAPABILITIES":
		return c.handleCapabilities()
	case "MODE":
		return c.handleMode(args)
	case "AUTHINFO":
		return c.handleAuthInfo(args, line)
	case "QUIT":
		return c.handleQuit(args)
	case "HELP":
		return c.handleHelp()
	case "LIST":
		return c.handleList(args)
	case "GROUP":
		return c.handleGroup(args)
	case "LISTGROUP":
		return c.handleListGroup(args)
	case "STAT":
		return c.handleStat(args)
	case "HEAD":
		return c.handleHead(args)
	case "BODY":
		return c.handleBody(args)
	case "ARTICLE":
		return c.handleArticle(args)
	case "XOVER":
		return c.handleXOver(args)
	case "XHDR":
		return c.handleXHdr(args)
	case "POST":
		return c.handlePost()
	case "IHAVE":
		return c.handleIHave(args)
	case "TAKETHIS":
		return c.handleTakeThis(args)
	default:
		c.rateLimitOnError()
		return c.sendResponse(500, "Command not recognized")
	}
}

// sendResponse sends a single-line response
func (c *ClientConnection) sendResponse(code int, message string) error {
	c.setWriteDeadline()
	return c.textConn.PrintfLine("%d %s", code, message)
}

// sendLine writes one raw line plus CRLF and flushes.
func (c *ClientConnection) sendLine(line string) error {
	c.setWriteDeadline()
	if _, err := c.textConn.W.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return c.textConn.W.Flush()
}

// getServerCapabilities returns the capability list for the current connection state
// (RFC 3977 §5.2: the list may change with state; RFC 4643 §2.1: no AUTHINFO after login).
func (c *ClientConnection) getServerCapabilities() []string {
	capabilities := []string{"VERSION 2", "READER"}
	if c.authinfoAdvertised() {
		capabilities = append(capabilities, "AUTHINFO USER")
	}
	capabilities = append(capabilities, "LIST ACTIVE NEWSGROUPS", "XOVER", "XHDR")
	if c.postingAllowed() {
		capabilities = append(capabilities, "POST", "IHAVE")
	}
	return capabilities
}
