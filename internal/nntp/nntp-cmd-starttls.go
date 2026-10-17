package nntp

import (
	"crypto/tls"
	"log"
	"net/textproto"
	"sync"
	"time"
)

// STARTTLS (RFC 4642): upgrade a plaintext connection to TLS in place.

// starttlsHandshakeTimeout bounds the TLS handshake after the 382 reply.
const starttlsHandshakeTimeout = 30 * time.Second

// starttlsEntry is one cached certificate load (success or failure).
type starttlsEntry struct {
	once sync.Once
	cfg  *tls.Config
	err  error
}

// starttlsCache holds one entry per (cert, key) path pair for the process lifetime.
var starttlsCache sync.Map // string -> *starttlsEntry

// starttlsConfigFor returns the server TLS config for certFile/keyFile. The pair
// is loaded once per process; rotation needs a restart (as for the TLS port).
func starttlsConfigFor(certFile, keyFile string) (*tls.Config, error) {
	v, _ := starttlsCache.LoadOrStore(certFile+"\x00"+keyFile, &starttlsEntry{})
	e := v.(*starttlsEntry)
	e.once.Do(func() {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			e.err = err
			return
		}
		e.cfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	})
	return e.cfg, e.err
}

// starttlsAvailable reports whether STARTTLS may be used (and advertised) now:
// a certificate is configured, the connection is plaintext and not authenticated
// (RFC 4642 §2.2.2).
func (c *ClientConnection) starttlsAvailable() bool {
	return !c.isTLS && !c.authenticated && c.server != nil && c.server.Config != nil &&
		c.server.Config.NNTP.TLSCert != "" && c.server.Config.NNTP.TLSKey != ""
}

// resetSessionState discards everything learned from the client before the TLS
// layer was negotiated (RFC 4642 §2.2.2). No per-user connection slot can be
// held here: STARTTLS is refused after authentication.
func (c *ClientConnection) resetSessionState() {
	c.authenticated = false
	c.user = nil
	c.authUsername = ""
	c.currentGroup = ""
	c.currentFirst = 0
	c.currentLast = 0
	c.currentArticle = 0
}

// handleStartTLS handles STARTTLS (RFC 4642 §2.2): 382, then the TLS handshake
// on the raw connection. A failed handshake closes the connection (errQuit, no
// reply); a successful one resets the session and sends no greeting.
func (c *ClientConnection) handleStartTLS(args []string) error {
	if len(args) != 0 {
		c.rateLimitOnError()
		return c.sendResponse(501, "STARTTLS takes no arguments")
	}
	if c.isTLS {
		return c.sendResponse(502, "STARTTLS not allowed with active TLS layer")
	}
	if c.authenticated {
		return c.sendResponse(502, "STARTTLS not allowed after authentication")
	}
	if !c.starttlsAvailable() {
		return c.sendResponse(502, "STARTTLS not available")
	}
	cfg, err := starttlsConfigFor(c.server.Config.NNTP.TLSCert, c.server.Config.NNTP.TLSKey)
	if err != nil {
		log.Printf("[NNTP] STARTTLS certificate: %v", err)
		return c.sendResponse(580, "Can not initiate TLS negotiation")
	}
	// RFC 4642 §2.2.1: STARTTLS must not be pipelined. Bytes sent after it in
	// plaintext are dropped unexecuted (no plaintext injection into the TLS session).
	if n := c.textConn.R.Buffered(); n > 0 {
		log.Printf("[NNTP] STARTTLS from %s: discarding %d pipelined bytes", c.conn.RemoteAddr(), n)
		_, _ = c.textConn.R.Discard(n)
	}
	if err := c.sendResponse(382, "Continue with TLS negotiation"); err != nil {
		return err
	}

	raw := c.conn
	_ = raw.SetDeadline(time.Now().Add(starttlsHandshakeTimeout))
	tc := tls.Server(raw, cfg)
	if err := tc.Handshake(); err != nil {
		log.Printf("[NNTP] STARTTLS handshake with %s failed: %v", raw.RemoteAddr(), err)
		_ = raw.Close()
		return errQuit
	}
	_ = raw.SetDeadline(time.Time{})
	c.conn = tc
	c.textConn = textproto.NewConn(tc)
	c.isTLS = true
	c.resetSessionState()
	c.UpdateDeadlines()
	return nil
}
