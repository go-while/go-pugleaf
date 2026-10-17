package nntp

// Scripted fake NNTP server and fake streaming peer for the client tests of
// plan nntp-client-transfer (wave 0), now provided by
// github.com/go-while/nntp-testkit/fakeserver (plan nntp-testkit). This file
// keeps the go-pugleaf names as aliases plus the glue that needs
// BackendConfig: cliBackend and cliDial. The self-tests of the fake moved
// into the kit (TestScript, TestOpenCount, TestPeerStream).
//
// Identifiers: cliFake*, cliPeer*, cliBackend, cliDial, cliUnstuff (contract
// K2). The server listens on 127.0.0.1:0 unless a test passes another
// address; it never contacts any host.

import (
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/nntp-testkit/client"
	"github.com/go-while/nntp-testkit/fakeserver"
)

// cliWait is the hang guard of the client tests: the outer deadline of every
// blocking step, the WaitOpen/WaitLines polls and the raw test connections.
// It only stops a hung test; a passing test never waits on it. It is
// hDeadline of the server harness (PUGLEAF_TEST_DEADLINE, a Go duration,
// e.g. 120s), passed into the kit as Options.Deadline by newCliFakeServer.
// Plan test-timing-hardening D3.
var cliWait = hDeadline

type (
	cliFakeOpts   = fakeserver.Options
	cliFakeStep   = fakeserver.Step
	cliFakeServer = fakeserver.Server
	cliFakeConn   = fakeserver.Conn
	cliPeerOpts   = fakeserver.PeerOptions
)

var (
	cliPeerHandler   = fakeserver.PeerHandler
	cliUnstuff       = client.Unstuff
	cliFakeTLSConfig = fakeserver.TLSConfig
)

// newCliFakeServer starts a fakeserver with cliWait as its hang guard (unless
// o.Deadline is set) and registers t.Cleanup(s.Close).
func newCliFakeServer(t *testing.T, o cliFakeOpts) *cliFakeServer {
	t.Helper()
	if o.Deadline == 0 {
		o.Deadline = cliWait
	}
	return fakeserver.New(t, o)
}

// cliBackend returns a fresh client config for s. ConnectTimeout is 30 s
// (config.DefaultConnectTimeout) so a loaded machine never trips it on the
// happy path; the tests that exercise the timeout set 300 ms themselves.
func cliBackend(s *cliFakeServer) *BackendConfig {
	return &BackendConfig{
		Host:           s.Host(),
		Port:           s.Port(),
		MaxConns:       2,
		ConnectTimeout: 30 * time.Second,
		Provider:       &config.Provider{Name: "cli-fake", Host: s.Host(), Port: s.Port()},
	}
}

// cliDial connects a BackendConn to s with cliBackend(s) after mutate. On
// success the connection is closed by t.Cleanup.
func cliDial(t *testing.T, s *cliFakeServer, mutate func(*BackendConfig)) (*BackendConn, error) {
	t.Helper()
	cfg := cliBackend(s)
	if mutate != nil {
		mutate(cfg)
	}
	c := NewConn(cfg)
	if err := c.Connect(); err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = c.CloseFromPoolOnly() })
	return c, nil
}
