package nntp

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

var DefaultNNTPcliconnTimeout = time.Duration(60 * time.Second)

// ClientConnection represents a client connection to the NNTP server
type ClientConnection struct {
	conn           net.Conn
	textConn       *textproto.Conn
	writer         *bufio.Writer
	server         *NNTPServer
	isTLS          bool
	authenticated  bool
	user           *models.NNTPUser // Currently authenticated NNTP user
	authUsername   string           // Username from AUTHINFO USER command
	currentGroup   string
	currentFirst   int64
	currentLast    int64
	currentArticle int64
	capabilities   []string
	created        time.Time
	lastCommand    time.Time
}

// NewClientConnection creates a new client connection
func NewClientConnection(conn net.Conn, server *NNTPServer, isTLS bool) *ClientConnection {
	textConn := textproto.NewConn(conn)

	client := &ClientConnection{
		conn:         conn,
		textConn:     textConn,
		writer:       bufio.NewWriter(conn),
		server:       server,
		isTLS:        isTLS,
		capabilities: []string{}, // Will be set dynamically via getServerCapabilities()
		created:      time.Now(),
		lastCommand:  time.Now(),
	}

	return client
}

func (c *ClientConnection) UpdateDeadlines() {
	// Set read deadline for command reading
	c.conn.SetReadDeadline(time.Now().Add(DefaultNNTPcliconnTimeout))
	// Set write deadline for large responses
	c.conn.SetWriteDeadline(time.Now().Add(DefaultNNTPcliconnTimeout))
}

// Handle processes the client connection
func (c *ClientConnection) Handle() error {
	defer c.textConn.Close()

	// Send welcome message
	if err := c.sendWelcome(); err != nil {
		return fmt.Errorf("failed to send welcome: %w", err)
	}

	// Process commands
	for {
		// Read command line
		line, err := c.textConn.ReadLine()
		if err != nil {
			return fmt.Errorf("failed to read command: %w", err)
		}
		c.UpdateDeadlines()
		c.lastCommand = time.Now()

		// Parse and handle command
		if err := c.handleCommand(line); err != nil {
			log.Printf("Command error from %s: %v", c.conn.RemoteAddr(), err)
			c.rateLimitOnError()
			// Send error response but continue
			c.sendResponse(500, "Internal server error")
			c.Close()
			return err
		}
	}
}

// sendWelcome sends the initial welcome message
func (c *ClientConnection) sendWelcome() error {
	hostname := "go-pugleaf"
	if c.isTLS {
		return c.sendResponse(200, fmt.Sprintf("%s NNTP server ready (TLS)", hostname))
	}
	return c.sendResponse(200, fmt.Sprintf("%s NNTP server ready", hostname))
}

// handleCommand parses and dispatches a command
func (c *ClientConnection) handleCommand(line string) error {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return c.sendResponse(500, "Empty command")
	}

	command := strings.ToUpper(parts[0])
	args := parts[1:]

	// Track command execution
	c.server.Stats.CommandExecuted(command)

	// Dispatch to appropriate handler
	switch command {
	case "CAPABILITIES":
		return c.handleCapabilities()
	case "MODE":
		return c.handleMode(args)
	case "AUTHINFO":
		return c.handleAuthInfo(args)
	case "QUIT":
		return c.handleQuit()
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
		return c.sendResponse(500, fmt.Sprintf("Command not recognized: %s", command))
	}
}

// sendResponse sends a single-line response
func (c *ClientConnection) sendResponse(code int, message string) error {
	response := fmt.Sprintf("%d %s", code, message)
	return c.textConn.PrintfLine("%s", response)
} // sendResponse sends a single-line response

func (c *ClientConnection) sendLine(line string) error {
	if _, err := c.writer.WriteString(line + "\r\n"); err != nil {
		return err
	}
	if err := c.writer.Flush(); err != nil {
		return err
	}
	return nil
}

// sendMultilineResponse sends a multi-line response
func (c *ClientConnection) sendMultilineResponse(code int, message string, lines []string) error {
	// Send status line
	if err := c.sendResponse(code, message); err != nil {
		return err
	}

	// Send data lines
	dw := c.textConn.DotWriter()
	writer := bufio.NewWriter(dw)

	for _, line := range lines {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return err
		}
	}

	if err := writer.Flush(); err != nil {
		return err
	}

	return dw.Close()
}

// getServerCapabilities returns the list of server capabilities
func (c *ClientConnection) getServerCapabilities() []string {
	capabilities := []string{
		"VERSION 2",
		"READER",
		"AUTHINFO USER",
		"LIST ACTIVE NEWSGROUPS",
		"XOVER",
		"XHDR",
		"MODE-READER",
	}

	// Add posting capabilities if processor is available
	if c.server.Processor != nil {
		capabilities = append(capabilities, "POST", "IHAVE", "TAKETHIS")
	}

	return capabilities
}

// RemoteAddr returns the remote address of the connection
func (c *ClientConnection) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}
