package main

// TestE2EUlimitBinary checks design point 4 of plan fetcher-hostile-peer (slice server-ulimit):
// the real pugleaf-nntp-server binary under a low file-descriptor limit keeps serving the
// connections it already has, logs the accept error with a backoff instead of spinning, accepts
// again once connections close, and still shuts down cleanly on SIGTERM.
//
// It builds the server into t.TempDir() (without -race), starts it through the kit's process
// runner (github.com/go-while/nntp-testkit/proc) with Ulimit []string{"-n <ulFDLimit>"} — /bin/sh
// sets the limit and execs the binary, so the pid stays the binary's — on a scratch data root
// under t.TempDir() and a port the OS handed out. The server creates its main database and runs
// the migrations itself; -history=false keeps the history index out of the fd budget.
//
// The fd budget is measured, not assumed: connections are opened one by one until the server's
// output shows the accept error, so the test works whatever the binary's own fd usage is (about
// 10 descriptors at idle here, so roughly 14 client connections at -n 24).
//
// No wall-clock assertion: every deadline is a hang guard (testWait, PUGLEAF_TEST_DEADLINE) and
// the one fixed pause (ulHold) is how long the exhausted state is held, not something asserted.
// The accept-error line count over the exhausted window bounds the log volume: the rate-limited
// backoff writes about one line per second, a 50 ms spin without a rate limit wrote 20.
//
// The server has no log-file flag, so proc.Options.Logs stays empty; the kit captures the
// binary's stdout and stderr and dumps them with the test output on failure, and the assertions
// read the same capture through Process.Stderr().
//
// Skipped like TestE2EXrefBinary: -short, PUGLEAF_E2E_ADDR set (external-server mode), uid <
// 1000, no go tool, no usable -nntphostname — plus a non-Unix GOOS or a missing /bin/sh, which
// the kit needs for the ulimit and for SIGTERM.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-while/nntp-testkit/proc"
)

const (
	// ulFDLimit is the "ulimit -n" the server runs with: enough for its own files (main
	// database plus WAL and SHM, the listener, the pipes to the test, /dev/null) and a
	// handful of client connections.
	ulFDLimit = 24
	// ulMaxDial bounds the probe loop; reaching it means the limit never bit.
	ulMaxDial = 40
	// ulMinConns is the smallest number of served connections the test insists on before it
	// calls the measurement usable.
	ulMinConns = 3
	// ulHold is how long the exhausted state is held before the accept-error lines are
	// counted. It is not an assertion; the count is.
	ulHold = 2 * time.Second
	// ulMaxAcceptLines bounds the accept-error lines over the whole exhausted window. The
	// rate-limited backoff writes roughly one per second; a spin with a flat 50 ms pause and
	// no rate limit wrote 20 per second, so a window of at least ulHold gave 40+.
	ulMaxAcceptLines = 30
	// ulPoll is the pause between polls of the server output while a greeting is outstanding.
	ulPoll = 20 * time.Millisecond
	// ulEMFILE is the accept error of an exhausted descriptor table (accept4: EMFILE).
	ulEMFILE = "too many open files"
)

// ulAcceptErrs counts the accept-error lines of the fd limit in the captured server output.
func ulAcceptErrs(p *proc.Process) int {
	n := 0
	for _, line := range strings.Split(p.Stderr(), "\n") {
		if strings.Contains(line, "Error accepting connection") && strings.Contains(line, ulEMFILE) {
			n++
		}
	}
	return n
}

// ulGreet is the outcome of the greeting read of one probe connection.
type ulGreet struct {
	line string
	err  error
}

// ulDial opens one connection and waits for its greeting, bounded by testWait. When the server
// cannot accept it — a new accept-error line appears in its output — it returns the connection
// as blocked and no client: the connection stays open on purpose, so the accept queue keeps the
// pending entry and the accept loop keeps failing while the test looks at the log.
func ulDial(p *proc.Process, addr string) (c *e2eClient, blocked net.Conn, err error) {
	base := ulAcceptErrs(p)
	conn, err := net.DialTimeout("tcp", addr, testWait)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(testWait)); err != nil {
		return nil, nil, err
	}
	r := bufio.NewReaderSize(conn, 1<<16)
	ch := make(chan ulGreet, 1)
	// The reader goroutine ends with the read: on the greeting, on the testWait deadline of
	// the connection, or when the test closes it. Nothing touches r after a blocked return.
	go func() {
		line, err := r.ReadString('\n')
		ch <- ulGreet{line: strings.TrimRight(line, "\r\n"), err: err}
	}()
	deadline := time.Now().Add(testWait)
	for {
		select {
		case g := <-ch:
			if g.err != nil {
				return nil, nil, fmt.Errorf("greeting: %w", g.err)
			}
			ok = true
			return &e2eClient{Conn: conn, R: r, Greeting: g.line, Deadline: testWait}, nil, nil
		case <-time.After(ulPoll):
		}
		if ulAcceptErrs(p) > base {
			ok = true
			return nil, conn, nil
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("neither a greeting nor an accept error within %s", testWait)
		}
	}
}

// ulCaps runs CAPABILITIES on c and requires 101 plus a capability list starting with VERSION 2.
func ulCaps(c *e2eClient) error {
	code, line, err := c.tryCmd("CAPABILITIES")
	if err != nil || code != 101 {
		return fmt.Errorf("CAPABILITIES: code=%d line=%q err=%v", code, line, err)
	}
	caps, err := c.tryDotLines()
	if err != nil {
		return fmt.Errorf("CAPABILITIES list: %w", err)
	}
	if len(caps) == 0 || caps[0] != "VERSION 2" {
		return fmt.Errorf("CAPABILITIES list %q", caps)
	}
	return nil
}

// ulQuit runs QUIT on c and requires 205.
func ulQuit(c *e2eClient) error {
	code, line, err := c.tryCmd("QUIT")
	if err != nil || code != 205 {
		return fmt.Errorf("QUIT: code=%d line=%q err=%v", code, line, err)
	}
	return nil
}

// ulBuild builds the server binary into dir and returns its path.
func ulBuild(t *testing.T, goTool, dir string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*testWait) // cold cgo builds are slow
	defer cancel()
	bin := filepath.Join(dir, "pugleaf-nntp-server")
	// the binary refuses to start with the default appVersion "-unset-"
	cmd := exec.CommandContext(ctx, goTool, "build", "-o", bin, "-ldflags", "-X main.appVersion=e2e-test",
		"github.com/go-while/go-pugleaf/cmd/nntp-server")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func TestE2EUlimitBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs the server binary")
	}
	if os.Getenv("PUGLEAF_E2E_ADDR") != "" {
		t.Skip("PUGLEAF_E2E_ADDR is set: external-server mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("needs a Unix system: ulimit through /bin/sh and SIGTERM")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh not available: %v", err)
	}
	if os.Geteuid() < 1000 {
		t.Skip("running as root/system user (uid < 1000): the binary refuses to open a database")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go tool not found: %v", err)
	}
	host, err := xrefHostname()
	if err != nil {
		t.Skipf("no usable -nntphostname: %v", err)
	}

	tmp := t.TempDir()
	bin := ulBuild(t, goTool, tmp)
	data := filepath.Join(tmp, "data-test-ulimit")
	run := filepath.Join(tmp, "run")
	for _, d := range []string{data, run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	port, err := xrefFreePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	p := proc.StartTCP(t, proc.Options{
		Path: bin,
		Args: []string{
			"-data", data, "-nntphostname", host,
			"-nntpbind", "127.0.0.1", "-nntptcpport", strconv.Itoa(port), "-nntptlsport", "0",
			"-maxconnections", "100", "-history=false",
			"-articledelay", "0", "-groupdelay", "0", "-errordelay", "0",
		},
		Dir:         run,
		Ulimit:      []string{fmt.Sprintf("-n %d", ulFDLimit)},
		Deadline:    testWait,
		StopTimeout: testWait,
	}, addr)

	// 1. Measure the fd budget: open connections until the server's accept hits the limit.
	var held []*e2eClient
	var blocked net.Conn
	for i := 0; i < ulMaxDial && blocked == nil; i++ {
		c, b, err := ulDial(p, addr)
		if err != nil {
			t.Fatalf("connection %d: %v\n%s", len(held), err, e2eTail(p.Stderr(), 20))
		}
		if b != nil {
			blocked = b
			break
		}
		if !strings.HasPrefix(c.Greeting, "200 ") && !strings.HasPrefix(c.Greeting, "201 ") {
			t.Fatalf("connection %d greeting %q", len(held), c.Greeting)
		}
		held = append(held, c)
	}
	for _, c := range held {
		defer c.close()
	}
	if blocked == nil {
		t.Fatalf("the fd limit of %d never stopped accept: %d connections served", ulFDLimit, len(held))
	}
	defer func() { _ = blocked.Close() }()
	t.Logf("fd budget: ulimit -n %d served %d client connections before accept failed", ulFDLimit, len(held))
	if len(held) < ulMinConns {
		t.Fatalf("only %d connections were served under ulimit -n %d, want at least %d",
			len(held), ulFDLimit, ulMinConns)
	}
	errsAtExhaustion := ulAcceptErrs(p)

	// 2. While the limit is exhausted, every connection that got a greeting keeps working.
	for i, c := range held {
		if err := ulCaps(c); err != nil {
			t.Fatalf("connection %d while the fd limit is exhausted: %v", i, err)
		}
	}

	// 3. The accept loop logs the error and backs off instead of spinning. The pending
	// connection is still open, so accept keeps failing for the whole hold.
	time.Sleep(ulHold)
	if n := ulAcceptErrs(p); n == 0 {
		t.Fatalf("no accept error for the fd limit in the server output\n%s", e2eTail(p.Stderr(), 30))
	}
	spin := ulAcceptErrs(p) - errsAtExhaustion
	t.Logf("accept errors: %d over the exhausted window (hold %s)", spin, ulHold)
	if spin > ulMaxAcceptLines {
		t.Errorf("accept loop spins: %d accept-error lines over the exhausted window, want at most %d\n%s",
			spin, ulMaxAcceptLines, e2eTail(p.Stderr(), 20))
	}
	// The fd limit must not be answered as the MaxConns cap (-maxconnections 100 is far away).
	if strings.Contains(p.Stderr(), "Connection limit") {
		t.Errorf("the fd limit was taken for the MaxConns cap (400 Too many connections)\n%s", e2eTail(p.Stderr(), 20))
	}

	// 4. Closing connections frees descriptors: a new connection is accepted and served.
	if err := blocked.Close(); err != nil {
		t.Fatalf("close the blocked connection: %v", err)
	}
	half := len(held) / 2
	if half < 1 {
		half = 1
	}
	for _, c := range held[:half] {
		c.close()
	}
	held = held[half:]
	var fresh *e2eClient
	if err := e2eWaitFor(testWait, func() error {
		c, b, err := ulDial(p, addr)
		if err != nil {
			return err
		}
		if b != nil {
			_ = b.Close()
			return fmt.Errorf("still not accepted")
		}
		fresh = c
		return nil
	}); err != nil {
		t.Fatalf("no connection accepted after closing %d of the held ones: %v\n%s",
			half, err, e2eTail(p.Stderr(), 20))
	}
	defer fresh.close()
	if err := ulCaps(fresh); err != nil {
		t.Errorf("connection after the limit eased: %v", err)
	}
	if !strings.Contains(p.Stderr(), "accepting again after") {
		t.Errorf("no recovery line after the accept errors\n%s", e2eTail(p.Stderr(), 20))
	}

	// 5. The connections held through the exhausted period end cleanly, then so does the server.
	if err := ulQuit(fresh); err != nil {
		t.Errorf("fresh connection: %v", err)
	}
	for i, c := range held {
		if err := ulQuit(c); err != nil {
			t.Errorf("held connection %d: %v", i, err)
		}
	}
	p.Stop() // SIGTERM, SIGKILL after StopTimeout
	if err := p.Wait(); err != nil {
		t.Errorf("server did not exit cleanly on SIGTERM: %v\n%s", err, e2eTail(p.Stderr(), 20))
	}
	for _, want := range []string{"Shutting down NNTP server", "NNTP server stopped"} {
		if !strings.Contains(p.Stderr(), want) {
			t.Errorf("shutdown line %q missing from the server output\n%s", want, e2eTail(p.Stderr(), 30))
		}
	}
}
