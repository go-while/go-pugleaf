package main

// End-to-end intake tests for the NNTP server with the real processor, history
// index and batch writer (plan nntp-audit-tests, slice test-nntp-intake-e2e).
//
// One database.OpenDatabase per test binary (INIT guard), opened in TestMain
// together with a history index, a hand-built *processor.Processor (never
// processor.NewProcessor: DNS lookup plus log.Fatal) and one NNTP server on
// 127.0.0.1:0 with all delays set to 0. Articles are committed asynchronously
// by the batch writer (BatchInterval 100 ms), so every check that needs a
// stored article polls with a 20 s deadline.
//
// When PUGLEAF_E2E_ADDR is set (scripts/test-nntp-server.sh runs the binary
// test in e2e_binary_test.go against a real server) nothing is opened and the
// in-process tests skip.
//
// Identifiers use the prefixes e2e / TestE2E (contract K2); groups are t6.*,
// users t6user<seq>, message-ids <t6-<test>-<seq>@test.invalid> (K3). Tests
// added by plan nntp-intake-injection use t7.<test>.<seq> groups and
// <t7-<test>-<seq>@test.invalid> message-ids.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/history"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// e2eHostname is processor.LocalNNTPHostname in the in-process tests.
const e2eHostname = "news.test.invalid"

// e2eCommitWait bounds every poll for an asynchronously committed article.
const e2eCommitWait = 20 * time.Second

// e2eMaxArtSize is the server's NNTP.MaxArtSize (the config default, 32 KiB).
const e2eMaxArtSize = 32768

var (
	e2eDB         *database.Database   // shared database; nil when skipped
	e2eHist       *history.History     // history index of e2eProc
	e2eProc       *processor.Processor // real processor (hand-built)
	e2eSrv        *nntp.NNTPServer     // server under test
	e2eAddr       string               // 127.0.0.1:<port> of e2eSrv
	e2eDataDir    string               // data root of e2eDB
	e2eSkipReason string               // why e2eDB is nil
	e2eSeq        atomic.Int64         // unique names
	e2eLogs       = &e2eLogBuffer{}    // captured log output (BUG-7 check)

	e2eConnsMu sync.Mutex
	e2eConns   []net.Conn // every client connection, closed at teardown
)

func TestMain(m *testing.M) {
	os.Exit(e2eMain(m))
}

func e2eMain(m *testing.M) int {
	if os.Getenv("PUGLEAF_E2E_ADDR") != "" {
		// the binary test (build tag e2e) talks to an external server: no DB here
		e2eSkipReason = "PUGLEAF_E2E_ADDR is set: in-process tests skipped"
		return m.Run()
	}
	// The server logs through the standard logger. Keep the output in memory
	// (TestE2ESizeLimits inspects it); print it too with PUGLEAF_TEST_LOG=1.
	if os.Getenv("PUGLEAF_TEST_LOG") == "1" {
		log.SetOutput(io.MultiWriter(os.Stderr, e2eLogs))
	} else {
		log.SetOutput(e2eLogs)
	}

	// OpenDatabase log.Fatal's for uid < 1000: check first and skip DB tests.
	if os.Geteuid() < 1000 {
		e2eSkipReason = "running as root/system user (uid < 1000): database tests skipped"
		fmt.Fprintln(os.Stderr, "[E2E]", e2eSkipReason)
		return m.Run()
	}

	dir := os.Getenv("PUGLEAF_TEST_DATADIR")
	keep := dir != "" // isolated children: the parent's t.TempDir cleans up
	if dir == "" {
		d, err := os.MkdirTemp("", "pugleaf-e2etest-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "[E2E] MkdirTemp:", err)
			return 2
		}
		dir = d
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[E2E] Abs:", err)
		return 2
	}
	tmp, err := filepath.Abs(os.TempDir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "[E2E] Abs(TempDir):", err)
		return 2
	}
	if !strings.HasPrefix(absDir, tmp+string(filepath.Separator)) {
		fmt.Fprintf(os.Stderr, "[E2E] refusing data dir %q outside %q\n", absDir, tmp)
		return 2
	}

	// Globals, set once before the open (D2). Nothing mutates them later.
	database.BatchInterval = 100 * time.Millisecond
	database.ENABLE_ARTICLE_CACHE = false
	database.NO_CACHE_BOOT = true
	config.AppVersion = "test"
	processor.LocalNNTPHostname = e2eHostname
	database.GlobalDateParser = processor.ParseNNTPDate
	history.ENABLE_HISTORY = true

	cfg := database.DefaultDBConfig()
	cfg.DataDir = absDir
	cfg.BackupDir = filepath.Join(absDir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[E2E] OpenDatabase:", err)
		return 2
	}
	e2eDB = db
	e2eDataDir = absDir

	// What processor.NewProcessor does, minus the hostname DNS check and log.Fatal.
	hist, err := history.NewHistory(&history.HistoryConfig{
		HistoryDir:     filepath.Join(absDir, "history"),
		BatchTimeout:   50, // ms
		MaxConnections: 4,
	}, db.WG)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[E2E] NewHistory:", err)
		return e2eTeardown(2, keep, nil, nil)
	}
	e2eHist = hist
	proc := &processor.Processor{
		DB:            db,
		History:       hist,
		MsgIdCache:    history.NewMsgIdItemCache(), // processArticle uses the global history.MsgIdCache
		ThreadCounter: processor.NewCounter(),
		LegacyCounter: processor.NewCounter(),
	}
	db.Batch.SetProcessor(proc)
	hist.SetDatabaseWorkChecker(db.Batch)
	e2eProc = proc

	srvCfg := &config.ServerConfig{Hostname: e2eHostname}
	srvCfg.NNTP.MaxConns = 100
	srvCfg.NNTP.MaxArtSize = e2eMaxArtSize
	var srvWG sync.WaitGroup
	srv, err := nntp.NewNNTPServer(db, srvCfg, &srvWG, NewProcessorAdapter(proc))
	if err != nil {
		fmt.Fprintln(os.Stderr, "[E2E] NewNNTPServer:", err)
		return e2eTeardown(2, keep, nil, nil)
	}
	srv.ArticleCmdDelay = 0
	srv.GroupCmdDelay = 0
	srv.ErrorDelay = 0
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "[E2E] listen:", err)
		return e2eTeardown(2, keep, nil, nil)
	}
	if err := srv.Serve(l, false); err != nil {
		_ = l.Close()
		fmt.Fprintln(os.Stderr, "[E2E] Serve:", err)
		return e2eTeardown(2, keep, nil, nil)
	}
	e2eSrv = srv
	e2eAddr = l.Addr().String()

	code := m.Run()
	return e2eTeardown(code, keep, srv, &srvWG)
}

// e2eTeardown: Stop → close clients → server goroutines → close(StopChan) →
// proc.Close (30 s) → db.WG.Wait (30 s) → Shutdown → remove the data dir.
func e2eTeardown(code int, keep bool, srv *nntp.NNTPServer, srvWG *sync.WaitGroup) int {
	fail := func(msg string) {
		fmt.Fprintln(os.Stderr, "[E2E]", msg)
		if code == 0 {
			code = 1
		}
	}
	if srv != nil {
		if err := srv.Stop(); err != nil {
			fail("Stop: " + err.Error())
		}
		// Stop() does not close live clients (BUG-7): close ours so the handlers return
		e2eConnsMu.Lock()
		for _, c := range e2eConns {
			_ = c.Close()
		}
		e2eConns = nil
		e2eConnsMu.Unlock()
		if !e2eWaitTimeout(srvWG.Wait, 10*time.Second) {
			fail("server goroutines did not finish within 10s after Stop")
		}
	}
	db := e2eDB
	close(db.StopChan)
	if e2eProc != nil {
		var closeErr error
		if !e2eWaitTimeout(func() { closeErr = e2eProc.Close() }, 30*time.Second) {
			fail("processor Close timed out after 30s")
		} else if closeErr != nil {
			fail("processor Close: " + closeErr.Error())
		}
	} else if e2eHist != nil {
		if err := e2eHist.Close(); err != nil {
			fail("history Close: " + err.Error())
		}
	}
	if !e2eWaitTimeout(db.WG.Wait, 30*time.Second) {
		fail("db.WG.Wait timed out after 30s")
	}
	if err := db.Shutdown(); err != nil {
		fail("Shutdown: " + err.Error())
	}
	if !keep {
		// kept only on request: every PUGLEAF_KNOWN_BUGS=1 run exits non-zero
		if os.Getenv("PUGLEAF_TEST_KEEP") == "1" {
			fmt.Fprintln(os.Stderr, "[E2E] test data kept in", e2eDataDir)
		} else {
			_ = os.RemoveAll(e2eDataDir)
		}
	}
	return code
}

// e2eWaitTimeout runs fn in a goroutine and reports whether it returned within d.
func e2eWaitTimeout(fn func(), d time.Duration) bool {
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// e2eNeedDB skips the test when no in-process server runs.
func e2eNeedDB(t *testing.T) {
	t.Helper()
	if e2eDB == nil || e2eSrv == nil {
		t.Skipf("no in-process server: %s", e2eSkipReason)
	}
}

// ---------------------------------------------------------------------------
// Captured log output

// e2eLogBuffer keeps the last e2eLogMax bytes written by the standard logger.
type e2eLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

const e2eLogMax = 4 << 20

func (b *e2eLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if b.buf.Len() > e2eLogMax {
		keep := b.buf.Bytes()[b.buf.Len()-e2eLogMax/2:]
		nb := make([]byte, len(keep))
		copy(nb, keep)
		b.buf.Reset()
		b.buf.Write(nb)
	}
	return len(p), nil
}

// lines returns the captured lines that contain substr.
func (b *e2eLogBuffer) lines(substr string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// knownBug and isolation (same semantics as the package-nntp harness)

// e2eKnownBugAction: "fatal" when the bug no longer reproduces (err == nil),
// "error" when PUGLEAF_KNOWN_BUGS=1 lists open bugs, "skip" otherwise.
func e2eKnownBugAction(id string, err error, envSet bool) (action, msg string) {
	if err == nil {
		return "fatal", fmt.Sprintf("known bug %s no longer reproduces: remove knownBug, keep the assertion", id)
	}
	if envSet {
		return "error", fmt.Sprintf("known bug %s still open: %v", id, err)
	}
	return "skip", fmt.Sprintf("known bug %s: %v", id, err)
}

// e2eKnownBug takes the result of an RFC-correct check.
func e2eKnownBug(t *testing.T, id string, err error) {
	t.Helper()
	action, msg := e2eKnownBugAction(id, err, os.Getenv("PUGLEAF_KNOWN_BUGS") == "1")
	switch action {
	case "fatal":
		t.Fatal(msg)
	case "error":
		t.Error(msg)
	default:
		t.Skip(msg)
	}
}

// e2eIsolatedPattern builds -test.run=^A$/^B$ for a (sub)test name.
func e2eIsolatedPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// e2eIsChild reports whether this process is the isolated child of t.
func e2eIsChild(t *testing.T) bool {
	return os.Getenv("PUGLEAF_ISOLATED") == t.Name()
}

// e2eRunIsolated runs child in a re-executed copy of the test binary with its
// own data dir (and so its own database, processor and server) under timeout.
// In the child process it runs child(t) and returns nil: callers must return
// right after it when e2eIsChild(t). In the parent it returns nil when the
// child passed, or an error with the tail of the child's output.
func e2eRunIsolated(t *testing.T, timeout time.Duration, child func(t *testing.T)) error {
	t.Helper()
	if e2eIsChild(t) {
		child(t)
		return nil
	}
	e2eNeedDB(t)
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+e2eIsolatedPattern(t.Name()), "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(),
		"PUGLEAF_ISOLATED="+t.Name(),
		"PUGLEAF_TEST_DATADIR="+t.TempDir(),
	)
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	start := time.Now()
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %s: %s", time.Since(start).Round(time.Millisecond), e2eTail(out.String(), 20))
	}
	if runErr != nil {
		return fmt.Errorf("child failed: %v: %s", runErr, e2eTail(out.String(), 30))
	}
	return nil
}

// e2eTail returns the last n lines of s.
func e2eTail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Client

// e2eClient is a raw NNTP client with a per-operation deadline.
type e2eClient struct {
	Conn     net.Conn
	R        *bufio.Reader
	Greeting string
	Deadline time.Duration
}

// e2eDialAddr connects to addr and reads the greeting. The connection is
// tracked and closed at teardown.
func e2eDialAddr(addr string, deadline time.Duration) (*e2eClient, error) {
	conn, err := net.DialTimeout("tcp", addr, deadline)
	if err != nil {
		return nil, err
	}
	e2eConnsMu.Lock()
	e2eConns = append(e2eConns, conn)
	e2eConnsMu.Unlock()
	c := &e2eClient{Conn: conn, R: bufio.NewReaderSize(conn, 1<<16), Deadline: deadline}
	line, err := c.readLine()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("greeting: %w", err)
	}
	c.Greeting = line
	return c, nil
}

// e2eDial connects to the in-process server; the client is closed when t ends.
func e2eDial(t *testing.T) *e2eClient {
	t.Helper()
	e2eNeedDB(t)
	c, err := e2eDialAddr(e2eAddr, 10*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", e2eAddr, err)
	}
	t.Cleanup(c.close)
	return c
}

func (c *e2eClient) close() { _ = c.Conn.Close() }

func (c *e2eClient) bump() { _ = c.Conn.SetDeadline(time.Now().Add(c.Deadline)) }

// readLine reads one line without its CRLF (or LF).
func (c *e2eClient) readLine() (string, error) {
	c.bump()
	line, err := c.R.ReadString('\n')
	if err != nil {
		return line, err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// send writes raw bytes (no CRLF appended).
func (c *e2eClient) send(raw string) error {
	c.bump()
	_, err := io.WriteString(c.Conn, raw)
	return err
}

// readStatus reads the next status line.
func (c *e2eClient) readStatus() (int, string, error) {
	line, err := c.readLine()
	if err != nil {
		return 0, line, err
	}
	code, err := e2eParseCode(line)
	return code, line, err
}

// tryCmd sends one command line and reads the status line.
func (c *e2eClient) tryCmd(format string, args ...interface{}) (int, string, error) {
	if err := c.send(fmt.Sprintf(format, args...) + "\r\n"); err != nil {
		return 0, "", err
	}
	return c.readStatus()
}

// cmd is tryCmd that fails the test on I/O or parse errors.
func (c *e2eClient) cmd(t *testing.T, format string, args ...interface{}) (int, string) {
	t.Helper()
	code, line, err := c.tryCmd(format, args...)
	if err != nil {
		t.Fatalf("cmd %q: %v (line %q)", fmt.Sprintf(format, args...), err, line)
	}
	return code, line
}

// tryDotLines reads a dot-terminated block, un-stuffs leading dots, strips CRLF.
func (c *e2eClient) tryDotLines() ([]string, error) {
	var out []string
	for {
		line, err := c.readLine()
		if err != nil {
			return out, fmt.Errorf("reading block: %w after %d lines", err, len(out))
		}
		if line == "." {
			return out, nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		out = append(out, line)
	}
}

func (c *e2eClient) readDotLines(t *testing.T) []string {
	t.Helper()
	out, err := c.tryDotLines()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// rawUntilDot returns the exact bytes up to and including the ".\r\n" line.
func (c *e2eClient) rawUntilDot(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	for {
		c.bump()
		line, err := c.R.ReadBytes('\n')
		buf.Write(line)
		if err != nil {
			t.Fatalf("rawUntilDot: %v after %d bytes", err, buf.Len())
		}
		if bytes.Equal(line, []byte(".\r\n")) || bytes.Equal(line, []byte(".\n")) {
			return buf.Bytes()
		}
	}
}

// tryAuth runs AUTHINFO USER/PASS and returns the final status.
func (c *e2eClient) tryAuth(user, pass string) (int, string, error) {
	code, line, err := c.tryCmd("AUTHINFO USER %s", user)
	if err != nil || code != 381 {
		return code, line, err
	}
	return c.tryCmd("AUTHINFO PASS %s", pass)
}

// auth authenticates or fails the test (RFC 4643 §2.3: 381 then 281).
func (c *e2eClient) auth(t *testing.T, user, pass string) {
	t.Helper()
	code, line, err := c.tryAuth(user, pass)
	if err != nil || code != 281 {
		t.Fatalf("AUTHINFO %s: code=%d line=%q err=%v", user, code, line, err)
	}
}

// tryPost runs POST with a ready wire article (e2eWire) and returns the final status.
func (c *e2eClient) tryPost(wire string) (int, string, error) {
	code, line, err := c.tryCmd("POST")
	if err != nil {
		return code, line, err
	}
	if code != 340 {
		return code, line, fmt.Errorf("POST: got %q, want 340", line)
	}
	if err := c.send(wire); err != nil {
		return 0, "", err
	}
	return c.readStatus()
}

// post is tryPost that fails the test on I/O errors.
func (c *e2eClient) post(t *testing.T, wire string) (int, string) {
	t.Helper()
	code, line, err := c.tryPost(wire)
	if err != nil {
		t.Fatalf("POST: %v (line %q)", err, line)
	}
	return code, line
}

// e2eParseCode extracts the 3-digit status code at the start of line.
func e2eParseCode(line string) (int, error) {
	if len(line) < 3 || (len(line) > 3 && line[3] != ' ') {
		return 0, fmt.Errorf("bad status line %q", line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil || code < 100 || code > 599 {
		return 0, fmt.Errorf("bad status line %q", line)
	}
	return code, nil
}

// ---------------------------------------------------------------------------
// Articles

// e2eWire builds the wire form of an article: CRLF line ends, dot-stuffing and
// the terminating ".\r\n".
func e2eWire(head, body []string) string {
	var b strings.Builder
	w := func(l string) {
		if strings.HasPrefix(l, ".") {
			b.WriteString(".")
		}
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	for _, l := range head {
		w(l)
	}
	b.WriteString("\r\n")
	for _, l := range body {
		w(l)
	}
	b.WriteString(".\r\n")
	return b.String()
}

// e2eHead returns a complete RFC 5536 header block. Empty msgid or date leave
// that header out.
func e2eHead(msgid, groups, subject, date string) []string {
	h := []string{
		"Path: e2e.client.invalid!not-for-mail",
		"From: E2E Tester <tester@test.invalid>",
		"Newsgroups: " + groups,
		"Subject: " + subject,
	}
	if date != "" {
		h = append(h, "Date: "+date)
	}
	if msgid != "" {
		h = append(h, "Message-ID: "+msgid)
	}
	return h
}

// e2eNow is an RFC 5322 date the processor accepts.
func e2eNow() string { return time.Now().UTC().Format(time.RFC1123Z) }

// e2eMsgID returns a unique <t6-<test>-<seq>@test.invalid>.
func e2eMsgID(test string) string {
	return fmt.Sprintf("<t6-%s-%d@test.invalid>", test, e2eSeq.Add(1))
}

// e2eGroup inserts an active newsgroup t6.<test>.<seq> and returns its name.
func e2eGroup(t *testing.T, test string) string {
	t.Helper()
	e2eNeedDB(t)
	name := fmt.Sprintf("t6.%s.%d", test, e2eSeq.Add(1))
	if err := e2eDB.InsertNewsgroup(&models.Newsgroup{
		Name: name, Description: "e2e group " + name, Active: true,
		Status: "y", LowWater: 1, Hierarchy: "t6",
	}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	return name
}

// e2eUser inserts an active NNTP user t6user<seq> and returns name and password.
func e2eUser(t *testing.T, posting bool) (user, pass string) {
	t.Helper()
	e2eNeedDB(t)
	user = fmt.Sprintf("t6user%04d", e2eSeq.Add(1)) // >= 10 chars
	pass = "pw-" + user
	if err := e2eDB.InsertNNTPUser(&models.NNTPUser{
		Username: user, Password: pass, MaxConns: 10, Posting: posting, WebUserID: 0, IsActive: true,
	}); err != nil {
		t.Fatalf("InsertNNTPUser %s: %v", user, err)
	}
	return user, pass
}

// e2ePosterUser/e2ePosterPass: one posting user shared by the tests. bcrypt is
// slow under -race; after the first login the auth cache answers.
var (
	e2ePosterMu   sync.Mutex
	e2ePosterUser string
	e2ePosterPass string
)

// e2ePoster dials and authenticates the shared posting user.
func e2ePoster(t *testing.T) *e2eClient {
	t.Helper()
	c := e2eDial(t)
	e2ePosterMu.Lock()
	if e2ePosterUser == "" {
		e2ePosterUser, e2ePosterPass = e2eUser(t, true)
	}
	user, pass := e2ePosterUser, e2ePosterPass
	e2ePosterMu.Unlock()
	c.auth(t, user, pass)
	return c
}

// e2eArticleSQL inserts one article with raw SQL (history does not learn about
// it) and updates the newsgroups row. Returns the article number.
func e2eArticleSQL(t *testing.T, group, msgid, subject, body string) int64 {
	t.Helper()
	gdb, err := e2eDB.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	date := time.Now().UTC()
	headers := strings.Join([]string{
		"Path: test.invalid!not-for-mail",
		"From: Seeder <seeder@test.invalid>",
		"Newsgroups: " + group,
		"Subject: " + subject,
		"Date: " + date.Format(time.RFC1123Z),
		"Message-ID: " + msgid,
	}, "\n")
	res, err := gdb.DB.Exec(
		`INSERT INTO articles (message_id, subject, from_header, date_sent, date_string, "references",
		                       bytes, lines, reply_count, path, headers_json, body_text, downloaded, imported_at)
		 VALUES (?, ?, ?, ?, ?, '', ?, ?, 0, ?, ?, ?, 1, CURRENT_TIMESTAMP)`,
		msgid, subject, "Seeder <seeder@test.invalid>", date.Format("2006-01-02 15:04:05"), date.Format(time.RFC1123Z),
		len(body), strings.Count(body, "\n")+1, "test.invalid!not-for-mail", headers, body,
	)
	if err != nil {
		t.Fatalf("insert %s into %s: %v", msgid, group, err)
	}
	num, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	var cnt, hi int64
	if err := gdb.DB.QueryRow(`SELECT COUNT(*), COALESCE(MAX(article_num),0) FROM articles`).Scan(&cnt, &hi); err != nil {
		t.Fatalf("count %s: %v", group, err)
	}
	if _, err := e2eDB.GetMainDB().Exec(
		`UPDATE newsgroups SET last_article = ?, message_count = ?, high_water = ? WHERE name = ?`,
		hi, cnt, hi, group,
	); err != nil {
		t.Fatalf("update newsgroups %s: %v", group, err)
	}
	return num
}

// ---------------------------------------------------------------------------
// Polling

// e2eWaitFor calls fn every 50 ms until it returns nil or d passes; it returns
// the last error on timeout.
func e2eWaitFor(d time.Duration, fn func() error) error {
	deadline := time.Now().Add(d)
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("after %s: %w", d, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// e2eStored reports whether msgid is committed in every group and recorded for
// every group in the history index. It returns the article number per group.
func e2eStored(msgid string, groups ...string) (map[string]int64, error) {
	nums := make(map[string]int64, len(groups))
	ids, err := e2eHist.LookupGroups(msgid)
	if err != nil {
		return nil, fmt.Errorf("history lookup: %w", err)
	}
	for _, g := range groups {
		gdb, err := e2eDB.GetGroupDB(g)
		if err != nil {
			return nil, fmt.Errorf("GetGroupDB %s: %w", g, err)
		}
		a, err := e2eDB.GetArticleByMessageID(gdb, msgid)
		gdb.Return()
		if err != nil || a == nil {
			return nil, fmt.Errorf("%s not yet in %s: %v", msgid, g, err)
		}
		nums[g] = a.DBArtNum
		ng, err := e2eDB.MainDBGetNewsgroup(g)
		if err != nil {
			return nil, fmt.Errorf("MainDBGetNewsgroup %s: %w", g, err)
		}
		found := false
		for _, id := range ids {
			found = found || id == ng.ID
		}
		if !found {
			return nil, fmt.Errorf("history has %v for %s, want group id %d (%s)", ids, msgid, ng.ID, g)
		}
	}
	return nums, nil
}

// e2eWaitStored polls e2eStored with the commit deadline and fails the test on timeout.
func e2eWaitStored(t *testing.T, msgid string, groups ...string) map[string]int64 {
	t.Helper()
	var nums map[string]int64
	err := e2eWaitFor(e2eCommitWait, func() error {
		var err error
		nums, err = e2eStored(msgid, groups...)
		return err
	})
	if err != nil {
		t.Fatalf("article %s not committed: %v", msgid, err)
	}
	return nums
}

// e2eWaitGroupCount polls GROUP until it reports count articles; returns the 211 line.
func (c *e2eClient) waitGroupCount(group string, count int) (string, error) {
	var last string
	err := e2eWaitFor(e2eCommitWait, func() error {
		code, line, err := c.tryCmd("GROUP %s", group)
		if err != nil {
			return err
		}
		last = line
		f := strings.Fields(line)
		if code != 211 || len(f) < 5 || f[1] != strconv.Itoa(count) {
			return fmt.Errorf("GROUP %s: %q, want count %d", group, line, count)
		}
		return nil
	})
	return last, err
}

// e2eHeaderValue returns the value of the first header name in lines ("" if absent).
func e2eHeaderValue(lines []string, name string) string {
	for _, l := range lines {
		if i := strings.IndexByte(l, ':'); i > 0 && strings.EqualFold(l[:i], name) {
			return strings.TrimSpace(l[i+1:])
		}
	}
	return ""
}

// e2eSplitArticle splits ARTICLE lines at the first empty line.
func e2eSplitArticle(lines []string) (head, body []string) {
	for i, l := range lines {
		if l == "" {
			return lines[:i], lines[i+1:]
		}
	}
	return lines, nil
}

// e2eExpectLines compares two line slices.
func e2eExpectLines(got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("got %d lines, want %d: got=%q want=%q", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tests

// TestE2EPost: POST with all headers is stored and served (RFC 3977 §6.3.1,
// §6.2.1), and the served Path carries the local path identity (RFC 5537 §3.2.1, INT-1).
func TestE2EPost(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroup(t, "post")
	id := e2eMsgID("post")
	body := []string{"first line of the e2e post", "", "last line"}
	c := e2ePoster(t)

	// RFC 3977 §6.3.1.2: 240 article received OK
	if code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E post", e2eNow()), body)); code != 240 {
		t.Fatalf("POST: %q, want 240", line)
	}
	e2eWaitStored(t, id, g)

	// ARTICLE <id> with no group selected (RFC 3977 §6.2.1.2: 220 0 <id>, found via history)
	code, line := c.cmd(t, "ARTICLE %s", id)
	if code != 220 || !strings.Contains(line, id) {
		t.Fatalf("ARTICLE %s: %q, want 220 ... %s", id, line, id)
	}
	head, gotBody := e2eSplitArticle(c.readDotLines(t))
	if err := e2eExpectLines(gotBody, body); err != nil {
		t.Fatalf("ARTICLE body: %v", err)
	}
	if v := e2eHeaderValue(head, "Message-ID"); v != id {
		t.Fatalf("ARTICLE Message-ID header %q, want %q", v, id)
	}

	// HEAD <id> (RFC 3977 §6.2.2: 221 and the headers only)
	code, line = c.cmd(t, "HEAD %s", id)
	if code != 221 {
		t.Fatalf("HEAD %s: %q, want 221", id, line)
	}
	hdrs := c.readDotLines(t)
	if v := e2eHeaderValue(hdrs, "Subject"); v != "E2E post" {
		t.Fatalf("HEAD Subject %q, want %q (headers %q)", v, "E2E post", hdrs)
	}
	if v := e2eHeaderValue(hdrs, "Newsgroups"); v != g {
		t.Fatalf("HEAD Newsgroups %q, want %q", v, g)
	}

	// GROUP reports the article (async newsgroups counter update), ARTICLE 1 works
	if line, err := c.waitGroupCount(g, 1); err != nil {
		t.Fatal(err)
	} else if f := strings.Fields(line); f[3] != "1" {
		t.Fatalf("GROUP %s: %q, want high 1", g, line)
	}
	code, line = c.cmd(t, "ARTICLE 1")
	if code != 220 || !strings.HasPrefix(line, "220 1 "+id) {
		t.Fatalf("ARTICLE 1: %q, want 220 1 %s", line, id)
	}
	_, gotBody = e2eSplitArticle(c.readDotLines(t))
	if err := e2eExpectLines(gotBody, body); err != nil {
		t.Fatalf("ARTICLE 1 body: %v", err)
	}

	// RFC 5537 §3.2.1: the injecting/relaying agent prepends its path identity;
	// the served Path header must start with it. Today the prefix is stored only
	// in the path column and HEAD serves the original header (INT-1).
	var pathErr error
	if p := e2eHeaderValue(hdrs, "Path"); !strings.HasPrefix(p, e2eHostname+"!") {
		pathErr = fmt.Errorf("served Path %q does not start with %q", p, e2eHostname+"!")
	}
	e2eKnownBug(t, "INT-1", pathErr)
}

// TestE2EPostGeneratedHeaders: POST without Message-ID or Date must be accepted,
// the injecting agent adds them (RFC 5537 §3.5 steps 5/6; RFC 3977 §6.3.1). Today 441 (INT-1).
func TestE2EPostGeneratedHeaders(t *testing.T) {
	e2eNeedDB(t)
	t.Run("no-message-id", func(t *testing.T) {
		g := e2eGroup(t, "genmid")
		c := e2ePoster(t)
		code, line := c.post(t, e2eWire(e2eHead("", g, "E2E no message-id", e2eNow()), []string{"body"}))
		var err error
		if code != 240 {
			err = fmt.Errorf("POST without Message-ID: %q, want 240", line)
		} else if _, werr := c.waitGroupCount(g, 1); werr != nil {
			err = werr
		} else if code, line, rerr := c.tryCmd("HEAD 1"); rerr != nil || code != 221 {
			err = fmt.Errorf("HEAD 1: %q %v", line, rerr)
		} else if hdrs, rerr := c.tryDotLines(); rerr != nil {
			err = rerr
		} else if v := e2eHeaderValue(hdrs, "Message-ID"); !strings.HasPrefix(v, "<") || !strings.Contains(v, "@") {
			err = fmt.Errorf("stored article has no generated Message-ID (headers %q)", hdrs)
		}
		e2eKnownBug(t, "INT-1", err)
	})
	t.Run("no-date", func(t *testing.T) {
		g := e2eGroup(t, "gendate")
		id := e2eMsgID("gendate")
		c := e2ePoster(t)
		code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E no date", ""), []string{"body"}))
		var err error
		if code != 240 {
			err = fmt.Errorf("POST without Date: %q, want 240", line)
		} else if serr := e2eWaitFor(e2eCommitWait, func() error { _, e := e2eStored(id, g); return e }); serr != nil {
			err = serr
		} else if code, line, rerr := c.tryCmd("HEAD %s", id); rerr != nil || code != 221 {
			err = fmt.Errorf("HEAD %s: %q %v", id, line, rerr)
		} else if hdrs, rerr := c.tryDotLines(); rerr != nil {
			err = rerr
		} else if processor.ParseNNTPDate(e2eHeaderValue(hdrs, "Date")).IsZero() {
			err = fmt.Errorf("stored article has no valid generated Date (headers %q)", hdrs)
		}
		e2eKnownBug(t, "INT-1", err)
	})
}

// TestE2EIHave: IHAVE <new> → 335, article → 235; the same id again → 435
// (RFC 3977 §6.3.2.2). 436 ("try again later") is accepted while the first
// copy is still being committed.
func TestE2EIHave(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroup(t, "ihave")
	id := e2eMsgID("ihave")
	c := e2ePoster(t)
	if code, line := c.cmd(t, "IHAVE %s", id); code != 335 {
		t.Fatalf("IHAVE %s: %q, want 335", id, line)
	}
	if err := c.send(e2eWire(e2eHead(id, g, "E2E ihave", e2eNow()), []string{"ihave body"})); err != nil {
		t.Fatal(err)
	}
	if code, line, err := c.readStatus(); err != nil || code != 235 {
		t.Fatalf("IHAVE article: code=%d line=%q err=%v, want 235", code, line, err)
	}
	e2eWaitStored(t, id, g)
	err := e2eWaitFor(e2eCommitWait, func() error {
		code, line := c.cmd(t, "IHAVE %s", id)
		switch code {
		case 435:
			return nil
		case 436:
			return fmt.Errorf("IHAVE %s again: %q", id, line)
		case 335:
			t.Fatalf("IHAVE %s again: %q, the stored article is wanted again", id, line)
		}
		t.Fatalf("IHAVE %s again: %q, want 435", id, line)
		return nil
	})
	if err != nil {
		t.Fatalf("IHAVE of a stored article never answered 435: %v", err)
	}
}

// TestE2EIHaveDuplicateNotInHistory (INT-2): an article already stored in every
// target group but unknown to the history index must be refused after the
// transfer: 335 then 437 (RFC 3977 §6.3.2.2), and history learns the id, so the
// next offer gets 435.
func TestE2EIHaveDuplicateNotInHistory(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroup(t, "dup")
	id := e2eMsgID("dup")
	e2eArticleSQL(t, g, id, "E2E seeded duplicate", "seeded body")
	c := e2ePoster(t)
	if code, line := c.cmd(t, "IHAVE %s", id); code != 335 {
		// history does not know the id, so 335 is the expected offer reply
		t.Fatalf("IHAVE %s: %q, want 335", id, line)
	}
	if err := c.send(e2eWire(e2eHead(id, g, "E2E seeded duplicate", e2eNow()), []string{"seeded body"})); err != nil {
		t.Fatal(err)
	}
	code, line, err := c.readStatus()
	if err != nil {
		t.Fatalf("IHAVE article: %v", err)
	}
	if code != 437 {
		t.Fatalf("IHAVE of an article already in %s: %q, want 437", g, line)
	}
	// history self-heal: the id is known from now on
	if err := e2eWaitFor(e2eCommitWait, func() error {
		if code, line := c.cmd(t, "IHAVE %s", id); code != 435 {
			if code == 335 {
				t.Fatalf("IHAVE %s again: %q, the stored article is wanted again", id, line)
			}
			return fmt.Errorf("IHAVE %s again: %q, want 435", id, line)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestE2ETakeThis: a pipelined stream of new, duplicate and mismatched articles
// gets 239, 439, 439 in order, each carrying its message-id (RFC 4644 §2.5).
func TestE2ETakeThis(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroup(t, "takethis")
	pre := e2eMsgID("takethis")
	c := e2ePoster(t)

	// a committed article for the duplicate case
	if err := c.send("TAKETHIS " + pre + "\r\n" + e2eWire(e2eHead(pre, g, "E2E takethis pre", e2eNow()), []string{"pre"})); err != nil {
		t.Fatal(err)
	}
	if code, line, err := c.readStatus(); err != nil || code != 239 {
		t.Fatalf("TAKETHIS %s: code=%d line=%q err=%v, want 239", pre, code, line, err)
	}
	e2eWaitStored(t, pre, g)

	fresh, mism, other := e2eMsgID("takethis"), e2eMsgID("takethis"), e2eMsgID("takethis")
	stream := "TAKETHIS " + fresh + "\r\n" + e2eWire(e2eHead(fresh, g, "E2E takethis new", e2eNow()), []string{"new"}) +
		"TAKETHIS " + pre + "\r\n" + e2eWire(e2eHead(pre, g, "E2E takethis dup", e2eNow()), []string{"dup"}) +
		"TAKETHIS " + mism + "\r\n" + e2eWire(e2eHead(other, g, "E2E takethis mismatch", e2eNow()), []string{"mismatch"})
	if err := c.send(stream); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		code int
		id   string
	}{{239, fresh}, {439, pre}, {439, mism}}
	for i, w := range want {
		code, line, err := c.readStatus()
		if err != nil {
			t.Fatalf("reply %d: %v (line %q)", i+1, err, line)
		}
		f := strings.Fields(line)
		if code != w.code || len(f) < 2 || f[1] != w.id {
			t.Fatalf("reply %d: %q, want %d %s", i+1, line, w.code, w.id)
		}
	}
	e2eWaitStored(t, fresh, g)
	if _, err := e2eStored(other, g); err == nil {
		t.Fatalf("the mismatched article %s was stored", other)
	}
}

// TestE2ERejectMissingNewsgroups: an article without Newsgroups is rejected:
// POST 441 (RFC 3977 §6.3.1.2), IHAVE 437 (§6.3.2.2; today 436, RFC-5).
func TestE2ERejectMissingNewsgroups(t *testing.T) {
	e2eNeedDB(t)
	head := []string{
		"Path: e2e.client.invalid!not-for-mail",
		"From: E2E Tester <tester@test.invalid>",
		"Subject: E2E no newsgroups",
		"Date: " + e2eNow(),
	}
	t.Run("post", func(t *testing.T) {
		c := e2ePoster(t)
		id := e2eMsgID("nong")
		code, line := c.post(t, e2eWire(append(head[:len(head):len(head)], "Message-ID: "+id), []string{"body"}))
		if code != 441 {
			t.Fatalf("POST without Newsgroups: %q, want 441", line)
		}
		// the connection stays usable
		if code, line := c.cmd(t, "MODE READER"); code != 200 && code != 201 {
			t.Fatalf("MODE READER after the rejection: %q", line)
		}
	})
	t.Run("ihave", func(t *testing.T) {
		c := e2ePoster(t)
		id := e2eMsgID("nong")
		if code, line := c.cmd(t, "IHAVE %s", id); code != 335 {
			t.Fatalf("IHAVE %s: %q, want 335", id, line)
		}
		if err := c.send(e2eWire(append(head[:len(head):len(head)], "Message-ID: "+id), []string{"body"})); err != nil {
			t.Fatal(err)
		}
		code, line, err := c.readStatus()
		if err != nil {
			t.Fatalf("IHAVE article: %v", err)
		}
		if code != 436 && code != 437 {
			t.Fatalf("IHAVE without Newsgroups: %q, want a rejection", line)
		}
		var bugErr error
		if code != 437 {
			bugErr = fmt.Errorf("IHAVE without Newsgroups: %q, want 437 (permanent reject)", line)
		}
		e2eKnownBug(t, "RFC-5", bugErr)
	})
}

// TestE2ERejectUnknownGroup (SEC-2 intake): POST to a group that does not exist
// must be rejected (441) without creating a newsgroups row or a group DB file.
func TestE2ERejectUnknownGroup(t *testing.T) {
	e2eNeedDB(t)
	g := fmt.Sprintf("t6.nosuch.group%d", e2eSeq.Add(1))
	id := e2eMsgID("nosuch")
	file := database.SanitizeGroupName(g) + ".db"
	countFiles := func() int {
		n := 0
		_ = filepath.WalkDir(filepath.Join(e2eDataDir, "db"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == file {
				n++
			}
			return nil
		})
		return n
	}
	if n := countFiles(); n != 0 {
		t.Fatalf("%s exists before the test (%d)", file, n)
	}
	c := e2ePoster(t)
	code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E unknown group", e2eNow()), []string{"body"}))
	var errs []string
	if code != 441 {
		errs = append(errs, fmt.Sprintf("POST to %s: %q, want 441", g, line))
	}
	if n := countFiles(); n != 0 {
		errs = append(errs, fmt.Sprintf("%d group DB file(s) %s created under <DataDir>/db", n, file))
	}
	// the newsgroups row is upserted by the async batch writer: let a sentinel
	// article in an existing group pass through the batch before checking it
	sg := e2eGroup(t, "sentinel")
	sid := e2eMsgID("sentinel")
	if code, line := c.post(t, e2eWire(e2eHead(sid, sg, "E2E sentinel", e2eNow()), []string{"sentinel"})); code != 240 {
		t.Fatalf("POST sentinel to %s: %q, want 240", sg, line)
	}
	e2eWaitStored(t, sid, sg)
	if _, err := e2eDB.MainDBGetNewsgroup(g); !errors.Is(err, sql.ErrNoRows) {
		errs = append(errs, fmt.Sprintf("newsgroups row for %s: err=%v, want sql.ErrNoRows", g, err))
	}
	if len(errs) > 0 {
		t.Fatal(strings.Join(errs, "; "))
	}
}

// TestE2ESizeLimits: oversized articles get a clean rejection.
func TestE2ESizeLimits(t *testing.T) {
	e2eNeedDB(t)
	// 20 000 lines exceed the 16384-line cap of readArticleLines. RFC 3977
	// §6.3.1.2 wants a 441 status; the server may then close the connection.
	// Today it closes the connection first and then writes 441 and 500 to the
	// closed socket, so the client never gets a status (BUG-7; the missing
	// byte limit is SEC-3). Runs in a child process: an oversized article must
	// not take down the shared server.
	t.Run("lines-20000", func(t *testing.T) {
		err := e2eRunIsolated(t, 120*time.Second, e2eOversizeChild)
		if e2eIsChild(t) {
			return
		}
		// only the child's BUG-7 assertions carry the marker; any other child
		// failure (setup, auth, a -race report, timeout) is a real failure
		if err != nil && !strings.Contains(err.Error(), e2eBug7Marker) {
			t.Fatal(err)
		}
		e2eKnownBug(t, "BUG-7", err)
	})
	// MaxArtSize (Server.NNTP.MaxArtSize, 32 KiB here) is never read: a 64 KiB
	// article must be refused with 441 (SEC-3), today it is accepted with 240.
	t.Run("bytes-over-MaxArtSize", func(t *testing.T) {
		g := e2eGroup(t, "size")
		id := e2eMsgID("size")
		body := make([]string, 0, 1000)
		for i := 0; i < 1000; i++ {
			body = append(body, fmt.Sprintf("%04d %s", i, strings.Repeat("x", 60)))
		}
		c := e2ePoster(t)
		code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E big", e2eNow()), body))
		if code != 240 && code != 441 {
			t.Fatalf("POST of %d bytes: %q, want 441 (or 240 while SEC-3 is open)", 1000*65, line)
		}
		if code, line := c.cmd(t, "MODE READER"); code != 200 && code != 201 {
			t.Fatalf("MODE READER after the big article: %q", line)
		}
		var bugErr error
		if code != 441 {
			bugErr = fmt.Errorf("POST of a %d-byte article with MaxArtSize %d: %q, want 441", 1000*65, e2eMaxArtSize, line)
		}
		e2eKnownBug(t, "SEC-3", bugErr)
	})
}

// e2eBug7Marker prefixes the child's BUG-7 failures (see lines-20000).
const e2eBug7Marker = "BUG-7-OPEN:"

// e2eOversizeChild posts a 20 000-line article and requires a status line
// (441 or another 4xx/5xx), then either a usable connection or a close after
// the status, and no server write to a closed socket.
func e2eOversizeChild(t *testing.T) {
	g := e2eGroup(t, "lines")
	id := e2eMsgID("lines")
	c := e2ePoster(t)
	local := c.Conn.LocalAddr().String()
	if code, line := c.cmd(t, "POST"); code != 340 {
		t.Fatalf("POST: %q, want 340", line)
	}
	body := make([]string, 20000)
	for i := range body {
		body[i] = fmt.Sprintf("line %05d", i)
	}
	wireErr := c.send(e2eWire(e2eHead(id, g, "E2E 20000 lines", e2eNow()), body)) // may fail: server closed
	code, line, err := c.readStatus()
	statusOK := err == nil && code >= 400 && code < 600
	if statusOK {
		// a status was sent: the connection is either still usable or closed after it
		code2, line2, err2 := c.tryCmd("MODE READER")
		if err2 == nil && code2 != 200 && code2 != 201 {
			t.Fatalf("after the rejection %q: MODE READER %q", line, line2)
		}
	}
	// wait for the server side of this connection to end, then look for writes after close
	_ = c.Conn.Close()
	if werr := e2eWaitFor(e2eCommitWait, func() error {
		if len(e2eLogs.lines("Connection error from "+local)) == 0 {
			return fmt.Errorf("handler of %s still running", local)
		}
		return nil
	}); werr != nil && !statusOK {
		t.Logf("no handler end logged for %s: %v", local, werr)
	}
	// Note for the fix plan: today the client sees "connection reset by peer"
	// because the server closes with ~3,600 unread article lines in its receive
	// buffer (the kernel answers them with RST). A fix must drain the rest of
	// the article (or CloseWrite and drain under a deadline) before closing,
	// otherwise the 441 is lost even when it is written before the close.
	var closedWrites []string
	for _, l := range e2eLogs.lines(local) {
		// writes only: a read loop hitting the closed conn after a correct 441+close is fine
		if strings.Contains(l, "use of closed network connection") && strings.Contains(l, "write tcp") {
			closedWrites = append(closedWrites, l)
		}
	}
	switch {
	case !statusOK && len(closedWrites) > 0:
		t.Fatalf(e2eBug7Marker+" 20000-line article: no status line (read: code=%d line=%q err=%v, write err=%v); the server wrote to the closed socket: %q",
			code, line, err, wireErr, closedWrites)
	case !statusOK:
		t.Fatalf(e2eBug7Marker+" 20000-line article: no status line (read: code=%d line=%q err=%v, write err=%v)", code, line, err, wireErr)
	case len(closedWrites) > 0:
		t.Fatalf(e2eBug7Marker+" 20000-line article: %q, but the server wrote to a closed socket: %q", line, closedWrites)
	}
}

// TestE2EDotStuffingRoundTrip: body lines starting with "." survive POST and
// BODY byte-exactly (RFC 3977 §3.1.1 dot-stuffing in both directions).
func TestE2EDotStuffingRoundTrip(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroup(t, "dots")
	id := e2eMsgID("dots")
	body := []string{".leading", "..leading", ".", "..", "plain", "", ". space", "end"}
	c := e2ePoster(t)
	if code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E dots", e2eNow()), body)); code != 240 {
		t.Fatalf("POST: %q, want 240", line)
	}
	e2eWaitStored(t, id, g)
	if code, line := c.cmd(t, "GROUP %s", g); code != 211 {
		t.Fatalf("GROUP %s: %q", g, line)
	}
	code, line := c.cmd(t, "BODY %s", id)
	if code != 222 {
		t.Fatalf("BODY %s: %q, want 222", id, line)
	}
	got := c.rawUntilDot(t)
	wire := e2eWire(nil, body)
	want := wire[len("\r\n"):] // e2eWire(nil, …) starts with the empty separator line
	if string(got) != want {
		t.Fatalf("BODY bytes differ:\n got %q\nwant %q", got, want)
	}
}

// TestE2EDotHeaderLine (A-nntp-rfc-2, A-intake-2): a header line ".." (un-stuffed
// ".") is not a header field: intake refuses the article with 441.
func TestE2EDotHeaderLine(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroup(t, "dothdr")
	id := e2eMsgID("dothdr")
	head := e2eHead(id, g, "E2E dot header", e2eNow())
	head = append(head[:2], append([]string{"."}, head[2:]...)...) // sent as ".." by e2eWire
	c := e2ePoster(t)
	code, line := c.post(t, e2eWire(head, []string{"body"}))
	switch code {
	case 441:
		// refused at intake (strict header block check)
	case 240:
		e2eWaitStored(t, id, g)
		if code, line := c.cmd(t, "HEAD %s", id); code != 221 {
			t.Fatalf("POST accepted (want 441); HEAD %s: %q", id, line)
		}
		t.Fatalf("POST with a \"..\" header line was accepted (want 441); served head: %q", c.rawUntilDot(t))
	default:
		t.Fatalf("POST with a \"..\" header line: %q, want 441", line)
	}
	if code, line := c.cmd(t, "MODE READER"); code != 200 && code != 201 {
		t.Fatalf("MODE READER after the rejection: %q", line)
	}
	if _, err := e2eStored(id, g); err == nil {
		t.Fatalf("the refused article %s was stored", id)
	}
}

// TestE2ECrosspost: a crosspost gets a number in each group; ARTICLE <id> in a
// selected group gives that group's number, with no group selected 0
// (RFC 3977 §6.2.1.2, found through the history index).
func TestE2ECrosspost(t *testing.T) {
	e2eNeedDB(t)
	ga, gb := e2eGroup(t, "xa"), e2eGroup(t, "xb")
	// two older articles in gb, so the crosspost has different numbers in a and b
	e2eArticleSQL(t, gb, e2eMsgID("xb"), "E2E older 1", "older 1")
	e2eArticleSQL(t, gb, e2eMsgID("xb"), "E2E older 2", "older 2")
	id := e2eMsgID("xpost")
	c := e2ePoster(t)
	if code, line := c.post(t, e2eWire(e2eHead(id, ga+","+gb, "E2E crosspost", e2eNow()), []string{"crosspost body"})); code != 240 {
		t.Fatalf("POST: %q, want 240", line)
	}
	nums := e2eWaitStored(t, id, ga, gb)
	if nums[ga] <= 0 || nums[gb] <= 0 || nums[ga] == nums[gb] {
		t.Fatalf("article numbers %v: want a distinct positive number in each group", nums)
	}

	// no group selected: number 0
	code, line := c.cmd(t, "ARTICLE %s", id)
	if code != 220 || !strings.HasPrefix(line, "220 0 "+id) {
		t.Fatalf("ARTICLE %s without a group: %q, want 220 0 %s", id, line, id)
	}
	c.readDotLines(t)

	// GROUP b: b's number
	if code, line := c.cmd(t, "GROUP %s", gb); code != 211 {
		t.Fatalf("GROUP %s: %q", gb, line)
	}
	code, line = c.cmd(t, "ARTICLE %s", id)
	if want := fmt.Sprintf("220 %d %s", nums[gb], id); code != 220 || !strings.HasPrefix(line, want) {
		t.Fatalf("ARTICLE %s in %s: %q, want %q", id, gb, line, want)
	}
	c.readDotLines(t)
	code, line = c.cmd(t, "STAT %s", id)
	if want := fmt.Sprintf("223 %d %s", nums[gb], id); code != 223 || !strings.HasPrefix(line, want) {
		t.Fatalf("STAT %s in %s: %q, want %q", id, gb, line, want)
	}

	// GROUP a: a's number
	if code, line := c.cmd(t, "GROUP %s", ga); code != 211 {
		t.Fatalf("GROUP %s: %q", ga, line)
	}
	code, line = c.cmd(t, "STAT %s", id)
	if want := fmt.Sprintf("223 %d %s", nums[ga], id); code != 223 || !strings.HasPrefix(line, want) {
		t.Fatalf("STAT %s in %s: %q, want %q", id, ga, line, want)
	}
}

// TestE2EHelpers covers the pure helpers.
func TestE2EHelpers(t *testing.T) {
	open := errors.New("got 436, want 437")
	for _, tc := range []struct {
		err    error
		env    bool
		action string
		substr string
	}{
		{nil, false, "fatal", "no longer reproduces"},
		{nil, true, "fatal", "no longer reproduces"},
		{open, false, "skip", "known bug X-1: got 436"},
		{open, true, "error", "known bug X-1 still open"},
	} {
		action, msg := e2eKnownBugAction("X-1", tc.err, tc.env)
		if action != tc.action || !strings.Contains(msg, tc.substr) {
			t.Errorf("e2eKnownBugAction(%v, %v) = (%q, %q), want %q containing %q", tc.err, tc.env, action, msg, tc.action, tc.substr)
		}
	}
	if got := e2eIsolatedPattern("TestA/sub.1/x+y"); got != `^TestA$/^sub\.1$/^x\+y$` {
		t.Errorf("e2eIsolatedPattern: %q", got)
	}
	if got, want := e2eWire([]string{"A: b"}, []string{".x", "y"}), "A: b\r\n\r\n..x\r\ny\r\n.\r\n"; got != want {
		t.Errorf("e2eWire: %q, want %q", got, want)
	}
	for _, tc := range []struct {
		line string
		code int
		ok   bool
	}{{"240 ok", 240, true}, {"240", 240, true}, {"24", 0, false}, {"2400 x", 0, false}, {"abc x", 0, false}, {"099 x", 0, false}} {
		code, err := e2eParseCode(tc.line)
		if (err == nil) != tc.ok || code != tc.code {
			t.Errorf("e2eParseCode(%q) = %d, %v", tc.line, code, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Intake validation and policy (plan nntp-intake-injection, slice S3).
// Groups t7.<test>.<seq>, message-ids <t7-<test>-<seq>@test.invalid>.

// e2eGroupWith inserts a newsgroup t7.<test>.<seq> with the given active flag,
// status and max_art_size (0 = no per-group limit) and returns its name.
func e2eGroupWith(t *testing.T, test string, active bool, status string, maxArtSize int) string {
	t.Helper()
	e2eNeedDB(t)
	name := fmt.Sprintf("t7.%s.%d", test, e2eSeq.Add(1))
	if err := e2eDB.InsertNewsgroup(&models.Newsgroup{
		Name: name, Description: "e2e group " + name, Active: active,
		Status: status, MaxArtSize: maxArtSize, LowWater: 1, Hierarchy: "t7",
	}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	return name
}

// TestE2EHeaderBlockRejects (A-intake-2): POST and IHAVE refuse a malformed header block.
func TestE2EHeaderBlockRejects(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroupWith(t, "hdrblock", true, "y", 0)
	c := e2ePoster(t)
	for _, tc := range []struct {
		name  string
		extra string
	}{
		{"two-subjects", "Subject: a second subject"},
		{"colon-less", "this line has no colon"},
		{"nul-byte", "X-Bad: nul\x00byte"},
	} {
		id := fmt.Sprintf("<t7-hdrblock-%d@test.invalid>", e2eSeq.Add(1))
		head := append(e2eHead(id, g, "E2E header block "+tc.name, e2eNow()), tc.extra)
		if code, line := c.post(t, e2eWire(head, []string{"body"})); code != 441 {
			t.Fatalf("%s: POST %q, want 441", tc.name, line)
		}
		if _, err := e2eStored(id, g); err == nil {
			t.Fatalf("%s: the refused article %s was stored", tc.name, id)
		}
	}
	if code, line := c.cmd(t, "MODE READER"); code != 200 && code != 201 {
		t.Fatalf("MODE READER after the rejections: %q", line)
	}

	// IHAVE with two From headers: 335, then a rejection. S4 maps CaseError to 437;
	// until then the server answers 436.
	id := fmt.Sprintf("<t7-hdrblock-%d@test.invalid>", e2eSeq.Add(1))
	if code, line := c.cmd(t, "IHAVE %s", id); code != 335 {
		t.Fatalf("IHAVE %s: %q, want 335", id, line)
	}
	head := append(e2eHead(id, g, "E2E header block two from", e2eNow()), "From: Second <second@test.invalid>")
	if err := c.send(e2eWire(head, []string{"body"})); err != nil {
		t.Fatal(err)
	}
	code, line, err := c.readStatus()
	if err != nil {
		t.Fatalf("IHAVE article: %v", err)
	}
	if code != 437 && code != 436 {
		t.Fatalf("IHAVE with two From headers: %q, want 437 (or 436 before S4)", line)
	}
	if _, err := e2eStored(id, g); err == nil {
		t.Fatalf("the refused article %s was stored", id)
	}
}

// TestE2EGroupPolicy (SEC-2, A-intake-8, D1/D2/D8): intake never creates groups and
// honours active, status and moderation.
func TestE2EGroupPolicy(t *testing.T) {
	e2eNeedDB(t)
	c := e2ePoster(t)
	newID := func() string { return fmt.Sprintf("<t7-policy-%d@test.invalid>", e2eSeq.Add(1)) }
	post := func(t *testing.T, id, groups string, extra ...string) (int, string) {
		t.Helper()
		head := append(e2eHead(id, groups, "E2E group policy", e2eNow()), extra...)
		return c.post(t, e2eWire(head, []string{"policy body"}))
	}
	countFiles := func(name string) int {
		file := database.SanitizeGroupName(name) + ".db"
		n := 0
		_ = filepath.WalkDir(filepath.Join(e2eDataDir, "db"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == file {
				n++
			}
			return nil
		})
		return n
	}
	countRows := func(t *testing.T, name string) int {
		t.Helper()
		var n int
		if err := e2eDB.GetMainDB().QueryRow(`SELECT COUNT(*) FROM newsgroups WHERE name = ?`, name).Scan(&n); err != nil {
			t.Fatalf("count newsgroups rows %s: %v", name, err)
		}
		return n
	}

	t.Run("inactive", func(t *testing.T) {
		g := e2eGroupWith(t, "policy", false, "y", 0)
		if code, line := post(t, newID(), g); code != 441 {
			t.Fatalf("POST to inactive %s: %q, want 441", g, line)
		}
	})
	t.Run("status-x", func(t *testing.T) {
		g := e2eGroupWith(t, "policy", true, "x", 0)
		if code, line := post(t, newID(), g); code != 441 {
			t.Fatalf("POST to status x %s: %q, want 441", g, line)
		}
	})
	t.Run("crosspost-unknown", func(t *testing.T) {
		g := e2eGroupWith(t, "policy", true, "y", 0)
		unknown := fmt.Sprintf("t7.nosuch.%d", e2eSeq.Add(1))
		id := newID()
		if code, line := post(t, id, g+","+unknown); code != 240 {
			t.Fatalf("POST to %s,%s: %q, want 240", g, unknown, line)
		}
		e2eWaitStored(t, id, g)
		if n := countFiles(unknown); n != 0 {
			t.Fatalf("%d group DB file(s) created for the unknown %s", n, unknown)
		}
		if n := countRows(t, unknown); n != 0 {
			t.Fatalf("%d newsgroups row(s) created for the unknown %s", n, unknown)
		}
	})
	t.Run("space-separated", func(t *testing.T) {
		g1 := e2eGroupWith(t, "policy", true, "y", 0)
		g2 := e2eGroupWith(t, "policy", true, "y", 0)
		if code, line := post(t, newID(), g1+" "+g2); code != 441 {
			t.Fatalf("POST with Newsgroups %q: %q, want 441", g1+" "+g2, line)
		}
	})
	t.Run("reserved-junk", func(t *testing.T) {
		rows, files := countRows(t, "junk"), countFiles("junk")
		if code, line := post(t, newID(), "junk"); code != 441 {
			t.Fatalf("POST to junk: %q, want 441", line)
		}
		if n := countRows(t, "junk"); n != rows {
			t.Fatalf("junk rows %d -> %d", rows, n)
		}
		if n := countFiles("junk"); n != files {
			t.Fatalf("junk group DB files %d -> %d", files, n)
		}
	})
	t.Run("moderated", func(t *testing.T) {
		g := e2eGroupWith(t, "policy", true, "m", 0)
		if code, line := post(t, newID(), g); code != 441 {
			t.Fatalf("POST to moderated %s without Approved: %q, want 441", g, line)
		}
		id := newID()
		if code, line := post(t, id, g, "Approved: x"); code != 240 {
			t.Fatalf("POST to moderated %s with Approved: %q, want 240", g, line)
		}
		e2eWaitStored(t, id, g)
	})
	t.Run("ihave-status-n", func(t *testing.T) {
		g := e2eGroupWith(t, "policy", true, "n", 0)
		id := newID()
		if code, line := c.cmd(t, "IHAVE %s", id); code != 335 {
			t.Fatalf("IHAVE %s: %q, want 335", id, line)
		}
		if err := c.send(e2eWire(e2eHead(id, g, "E2E feed to status n", e2eNow()), []string{"feed body"})); err != nil {
			t.Fatal(err)
		}
		if code, line, err := c.readStatus(); err != nil || code != 235 {
			t.Fatalf("IHAVE to status n %s: code=%d line=%q err=%v, want 235", g, code, line, err)
		}
		e2eWaitStored(t, id, g)
	})
	t.Run("group-max-art-size", func(t *testing.T) {
		small := e2eGroupWith(t, "policy", true, "y", 200)
		if code, line := post(t, newID(), small); code != 441 {
			t.Fatalf("POST over max_art_size 200 of %s: %q, want 441", small, line)
		}
	})
}

// TestE2ENoNullHierarchyRow (A-db-1 end to end): intake never writes a newsgroups row
// without hierarchy, so MainDBGetAllNewsgroups keeps working.
func TestE2ENoNullHierarchyRow(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroupWith(t, "nullhier", true, "y", 0)
	c := e2ePoster(t)
	id := fmt.Sprintf("<t7-nullhier-%d@test.invalid>", e2eSeq.Add(1))
	if code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E null hierarchy", e2eNow()), []string{"body"})); code != 240 {
		t.Fatalf("POST to %s: %q, want 240", g, line)
	}
	unknown := fmt.Sprintf("t7.nullhier.nosuch%d", e2eSeq.Add(1))
	uid := fmt.Sprintf("<t7-nullhier-%d@test.invalid>", e2eSeq.Add(1))
	if code, line := c.post(t, e2eWire(e2eHead(uid, unknown, "E2E null hierarchy unknown", e2eNow()), []string{"body"})); code != 441 {
		t.Fatalf("POST to %s: %q, want 441", unknown, line)
	}
	e2eWaitStored(t, id, g)
	if _, err := e2eDB.MainDBGetAllNewsgroups(); err != nil {
		t.Fatalf("MainDBGetAllNewsgroups after intake: %v", err)
	}
}

// TestE2EHistoryAfterIntake (INT-6): articles stored in a group whose row has a NULL
// hierarchy reach the history index (two batches), so later offers get 435.
func TestE2EHistoryAfterIntake(t *testing.T) {
	e2eNeedDB(t)
	g := fmt.Sprintf("t7.histnull.%d", e2eSeq.Add(1))
	if _, err := e2eDB.GetMainDB().Exec(`INSERT INTO newsgroups(name,description,last_article,message_count,active,status,low_water,high_water) VALUES(?, '', 0, 0, 1, 'y', 1, 0)`, g); err != nil {
		t.Fatalf("insert %s: %v", g, err)
	}
	// readers of NULL-hierarchy rows are fixed elsewhere (audit-delta-fixes): give the row
	// a hierarchy afterwards so later tests reading every row are not affected
	t.Cleanup(func() {
		if _, err := e2eDB.GetMainDB().Exec(`UPDATE newsgroups SET hierarchy = 't7' WHERE name = ?`, g); err != nil {
			t.Errorf("cleanup hierarchy of %s: %v", g, err)
		}
	})
	c := e2ePoster(t)
	var ids []string
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("<t7-histnull-%d@test.invalid>", e2eSeq.Add(1))
		if code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E history after intake", e2eNow()), []string{"body"})); code != 240 {
			t.Fatalf("POST %d to %s: %q, want 240", i+1, g, line)
		}
		// wait for the history entry before the next post: two separate batches
		if err := e2eWaitFor(e2eCommitWait, func() error {
			ok, err := e2eHist.Exists(id)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%s not in history", id)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if code, line := c.cmd(t, "IHAVE %s", id); code != 435 {
			t.Fatalf("IHAVE %s: %q, want 435", id, line)
		}
	}
}

// TestE2EMessageIDSyntax (A-intake-3): POST with a syntactically invalid Message-ID is refused.
func TestE2EMessageIDSyntax(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroupWith(t, "msgid", true, "y", 0)
	c := e2ePoster(t)
	noBrackets := fmt.Sprintf("t7-msgid-%d@test.invalid", e2eSeq.Add(1))
	prefix := fmt.Sprintf("<t7-msgid-%d-", e2eSeq.Add(1))
	long := prefix + strings.Repeat("a", 251-len(prefix)-len("@test.invalid>")) + "@test.invalid>"
	if len(long) != 251 {
		t.Fatalf("test bug: id length %d", len(long))
	}
	for _, id := range []string{noBrackets, long} {
		if code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E message-id syntax", e2eNow()), []string{"body"})); code != 441 {
			t.Fatalf("POST with Message-ID %q: %q, want 441", id, line)
		}
		if _, err := e2eStored(id, g); err == nil {
			t.Fatalf("the refused article %q was stored", id)
		}
	}
}

// TestE2EDateChecks (INT-3, A-intake-7): unparseable dates and dates more than 24 h in
// the future are refused.
func TestE2EDateChecks(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroupWith(t, "date", true, "y", 0)
	c := e2ePoster(t)
	now := time.Now().UTC()
	for _, tc := range []struct {
		date string
		want int
	}{
		{"garbage", 441},
		{now.Add(24*time.Hour + 30*time.Minute).Format(time.RFC1123Z), 441},
		{now.Add(23 * time.Hour).Format(time.RFC1123Z), 240},
	} {
		id := fmt.Sprintf("<t7-date-%d@test.invalid>", e2eSeq.Add(1))
		if code, line := c.post(t, e2eWire(e2eHead(id, g, "E2E date check", tc.date), []string{"body"})); code != tc.want {
			t.Fatalf("POST with Date %q: %q, want %d", tc.date, line, tc.want)
		}
		if tc.want == 240 {
			e2eWaitStored(t, id, g)
		}
	}
}

// TestE2EControlXref (INT-4, A-intake-6, D3): control messages are refused, Supersedes
// is stored as an ordinary article, a foreign Xref is not stored.
func TestE2EControlXref(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroupWith(t, "ctlxref", true, "y", 0)
	c := e2ePoster(t)

	ctl := fmt.Sprintf("<t7-ctlxref-%d@test.invalid>", e2eSeq.Add(1))
	head := append(e2eHead(ctl, g, "cmsg cancel <x@y>", e2eNow()), "Control: cancel <x@y>")
	if code, line := c.post(t, e2eWire(head, []string{"cancel"})); code != 441 {
		t.Fatalf("POST with Control: %q, want 441", line)
	}
	if n := len(e2eLogs.lines("[INTAKE] control message " + ctl + " (cancel) refused: control messages are not processed")); n == 0 {
		t.Fatalf("no D3 log line for %s", ctl)
	}

	sup := fmt.Sprintf("<t7-ctlxref-%d@test.invalid>", e2eSeq.Add(1))
	head = append(e2eHead(sup, g, "E2E supersedes", e2eNow()), "Supersedes: <x@y>")
	if code, line := c.post(t, e2eWire(head, []string{"superseding"})); code != 240 {
		t.Fatalf("POST with Supersedes: %q, want 240", line)
	}
	e2eWaitStored(t, sup, g)
	if n := len(e2eLogs.lines("[INTAKE] Supersedes ignored for " + sup)); n == 0 {
		t.Fatalf("no D3 Supersedes log line for %s", sup)
	}

	// a foreign Xref by IHAVE (S4 refuses POST with Xref as a forged trace)
	xref := fmt.Sprintf("<t7-ctlxref-%d@test.invalid>", e2eSeq.Add(1))
	if code, line := c.cmd(t, "IHAVE %s", xref); code != 335 {
		t.Fatalf("IHAVE %s: %q, want 335", xref, line)
	}
	head = append(e2eHead(xref, g, "E2E foreign xref", e2eNow()), "Xref: other.example g:1")
	if err := c.send(e2eWire(head, []string{"xref body"})); err != nil {
		t.Fatal(err)
	}
	if code, line, err := c.readStatus(); err != nil || code != 235 {
		t.Fatalf("IHAVE with Xref: code=%d line=%q err=%v, want 235", code, line, err)
	}
	e2eWaitStored(t, xref, g)
	gdb, err := e2eDB.GetGroupDB(g)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", g, err)
	}
	var stored string
	err = gdb.DB.QueryRow(`SELECT headers_json FROM articles WHERE message_id = ?`, xref).Scan(&stored)
	gdb.Return()
	if err != nil {
		t.Fatalf("read headers_json of %s: %v", xref, err)
	}
	for _, l := range strings.Split(stored, "\n") {
		if strings.HasPrefix(strings.ToLower(l), "xref:") {
			t.Fatalf("stored headers_json keeps the foreign Xref: %q", stored)
		}
	}
	if !strings.Contains(stored, "Message-ID: "+xref) {
		t.Fatalf("stored headers_json %q lacks the Message-ID", stored)
	}
}

// TestE2EInFlightRetry (INT-2 in flight): an article whose message-id is claimed by another
// transfer gets 436 (retry later), and a later offer is accepted.
func TestE2EInFlightRetry(t *testing.T) {
	e2eNeedDB(t)
	g := e2eGroupWith(t, "inflight", true, "y", 0)
	c := e2ePoster(t)
	id := fmt.Sprintf("<t7-inflight-%d@test.invalid>", e2eSeq.Add(1))
	if code, line := c.cmd(t, "IHAVE %s", id); code != 335 {
		t.Fatalf("IHAVE %s: %q, want 335", id, line)
	}
	// the one allowed global touch (K3): this test's own message-id item
	item := history.MsgIdCache.GetORCreate(id)
	item.Mux.Lock()
	item.Response = history.CaseLock
	item.Mux.Unlock()
	if err := c.send(e2eWire(e2eHead(id, g, "E2E in flight", e2eNow()), []string{"body"})); err != nil {
		t.Fatal(err)
	}
	code, line, err := c.readStatus()
	if err != nil || code != 436 {
		t.Fatalf("IHAVE of an in-flight id: code=%d line=%q err=%v, want 436", code, line, err)
	}
	item.Mux.Lock()
	item.Response = 0
	item.Mux.Unlock()
	if code, line := c.cmd(t, "IHAVE %s", id); code != 335 {
		t.Fatalf("IHAVE %s again: %q, want 335", id, line)
	}
	if err := c.send(e2eWire(e2eHead(id, g, "E2E in flight", e2eNow()), []string{"body"})); err != nil {
		t.Fatal(err)
	}
	if code, line, err := c.readStatus(); err != nil || code != 235 {
		t.Fatalf("IHAVE retry: code=%d line=%q err=%v, want 235", code, line, err)
	}
	e2eWaitStored(t, id, g)
}

// TestE2EGroupDBOpenFailure (A-intake-9): when no group database can be opened nothing is
// stored and the sender is told to retry (POST 441, IHAVE 436), not success.
func TestE2EGroupDBOpenFailure(t *testing.T) {
	e2eNeedDB(t)
	// a 255-octet name: valid, but its database file name exceeds NAME_MAX
	prefix := fmt.Sprintf("t7.dbfail.%d.", e2eSeq.Add(1))
	g := prefix + strings.Repeat("a", 255-len(prefix))
	if err := e2eDB.InsertNewsgroup(&models.Newsgroup{
		Name: g, Description: "e2e group too long for a file name", Active: true,
		Status: "y", LowWater: 1, Hierarchy: "t7",
	}); err != nil {
		t.Fatalf("InsertNewsgroup: %v", err)
	}
	t.Cleanup(func() {
		// the row stays unusable (its DB file can never be opened): deactivate it so a
		// later test that walks every active group does not trip over it
		if _, err := e2eDB.GetMainDB().Exec(`UPDATE newsgroups SET active = 0 WHERE name = ?`, g); err != nil {
			t.Logf("cleanup: deactivating %s: %v", g, err)
		}
	})
	c := e2ePoster(t)
	id := fmt.Sprintf("<t7-dbfail-%d@test.invalid>", e2eSeq.Add(1))
	wire := e2eWire(e2eHead(id, g, "E2E group DB open failure", e2eNow()), []string{"body"})
	if code, line := c.post(t, wire); code != 441 {
		t.Fatalf("POST to %s: %q, want 441", g, line)
	}
	// the temporary failure released the claim: the same id is offered again
	if code, line := c.cmd(t, "IHAVE %s", id); code != 335 {
		t.Fatalf("IHAVE %s after the temporary failure: %q, want 335", id, line)
	}
	if err := c.send(wire); err != nil {
		t.Fatal(err)
	}
	if code, line, err := c.readStatus(); err != nil || code != 436 {
		t.Fatalf("IHAVE to %s: code=%d line=%q err=%v, want 436", g, code, line, err)
	}
	if ok, err := e2eHist.Exists(id); err != nil || ok {
		t.Fatalf("history Exists(%s) = %v, %v; want false", id, ok, err)
	}
	if n := len(e2eLogs.lines("[INTAKE] article " + id + " not stored: no group database could be opened")); n == 0 {
		t.Fatalf("no [INTAKE] log line for %s", id)
	}
}

// TestE2EWebCrosspost (A-intake-5): a web post queued for two groups is stored in both.
func TestE2EWebCrosspost(t *testing.T) {
	e2eNeedDB(t)
	g1 := e2eGroupWith(t, "webxpost", true, "y", 0)
	g2 := e2eGroupWith(t, "webxpost", true, "y", 0)
	w := e2eProc.NewPostQueueWorker()
	w.Start()
	defer w.Stop()
	id := fmt.Sprintf("<t7-webxpost-%d@test.invalid>", e2eSeq.Add(1))
	body := "web crosspost body"
	from := "E2E Web <noreply@pugleaf.net.invalid>"
	// header block built like web/web_sitePostPage.go
	headers := []string{
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=\"UTF-8\"",
		"Content-Transfer-Encoding: 8bit",
		"X-pugleaf-Trace: " + e2eHostname + ";",
		" nonce=\"1\"; mail-complaints-to=\"abuse@test.invalid\";",
		" posting-account=\"e2e\";",
		"From: " + from,
		"Newsgroups: " + g1 + "," + g2,
		"Lines: 1",
		"Bytes: " + strconv.Itoa(len(body)-1),
	}
	article := &models.Article{
		MessageID:   id,
		Subject:     "E2E web crosspost",
		HeadersJSON: strings.Join(headers, "\n"),
		FromHeader:  from,
		DateString:  time.Now().Format(time.RFC1123Z),
		BodyText:    body,
		IsThrRoot:   true,
		Lines:       1,
		Bytes:       len(body) - 1,
		Path:        ".POSTED!not-for-mail",
		ArticleNums: make(map[*string]int64),
		RefSlice:    []string{},
		Headers:     map[string][]string{"newsgroups": {g1 + "," + g2}},
	}
	select {
	case models.PostQueueChannel <- article:
	case <-time.After(10 * time.Second):
		t.Fatal("post queue full")
	}
	e2eWaitStored(t, id, g1, g2)
}

// TestE2ECaptureSlotReleased (A-tools-1): rejected, duplicate and accepted articles leave
// every capture slot free again.
func TestE2ECaptureSlotReleased(t *testing.T) {
	e2eNeedDB(t)
	ga := e2eGroupWith(t, "slots", true, "y", 0)
	gb := e2eGroupWith(t, "slots", true, "y", 0)
	c := e2ePoster(t)
	unknown := fmt.Sprintf("t7.slots.nosuch%d", e2eSeq.Add(1))
	rejected := fmt.Sprintf("<t7-slots-%d@test.invalid>", e2eSeq.Add(1))
	if code, line := c.post(t, e2eWire(e2eHead(rejected, unknown, "E2E slots rejected", e2eNow()), []string{"body"})); code != 441 {
		t.Fatalf("POST to %s: %q, want 441", unknown, line)
	}
	xpost := fmt.Sprintf("<t7-slots-%d@test.invalid>", e2eSeq.Add(1))
	wire := e2eWire(e2eHead(xpost, ga+","+gb, "E2E slots crosspost", e2eNow()), []string{"body"})
	if code, line := c.post(t, wire); code != 240 {
		t.Fatalf("POST crosspost: %q, want 240", line)
	}
	e2eWaitStored(t, xpost, ga, gb)
	if code, line := c.post(t, wire); code != 441 {
		t.Fatalf("POST duplicate: %q, want 441", line)
	}
	if err := e2eWaitFor(e2eCommitWait, func() error {
		if n := processor.CaptureInFlight(); n != 0 {
			return fmt.Errorf("%d capture slots still taken", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
