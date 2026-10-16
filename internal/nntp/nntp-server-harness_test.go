package nntp

// Test harness for the NNTP server (plan nntp-audit-tests, D2).
//
// One database.OpenDatabase per test binary (INIT guard), opened in TestMain.
// Servers are built with NewNNTPServer and Serve on 127.0.0.1:0 with all
// delays set to 0. Package globals are never mutated after TestMain.
//
// Waits for an expected event (dial, reply, close, Stop) use hDeadline as their
// hang guard (PUGLEAF_TEST_DEADLINE, default 60 s; plan test-timing-hardening).
// A passing test never waits on it; only deliberate negative windows stay short.
//
// Identifiers: everything here starts with h, newTestServer, knownBug or
// runIsolated (contract K2).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
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
	"github.com/go-while/go-pugleaf/internal/models"
)

// hDB is the shared test database; nil when the DB could not be opened (root).
var hDB *database.Database

// hDBSkipReason explains why hDB is nil.
var hDBSkipReason string

// hSeq makes unique group/user/message-id names.
var hSeq atomic.Int64

func TestMain(m *testing.M) {
	if os.Getenv("PUGLEAF_TEST_LOG") != "1" {
		log.SetOutput(io.Discard)
	}
	code := hRunMain(m)
	os.Exit(code)
}

func hRunMain(m *testing.M) int {
	// OpenDatabase log.Fatal's for uid < 1000: check first and skip DB tests.
	if os.Geteuid() < 1000 {
		hDBSkipReason = "running as root/system user (uid < 1000): database tests skipped"
		fmt.Fprintln(os.Stderr, "[HARNESS]", hDBSkipReason)
		return m.Run()
	}

	dir := os.Getenv("PUGLEAF_TEST_DATADIR")
	keep := dir != "" // runIsolated children: the parent's t.TempDir cleans up
	if dir == "" {
		d, err := os.MkdirTemp("", "pugleaf-nntptest-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "[HARNESS] MkdirTemp:", err)
			return 2
		}
		dir = d
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[HARNESS] Abs:", err)
		return 2
	}
	tmp, _ := filepath.Abs(os.TempDir())
	if !strings.HasPrefix(absDir, tmp+string(filepath.Separator)) {
		fmt.Fprintf(os.Stderr, "[HARNESS] refusing data dir %q outside %q\n", absDir, tmp)
		return 2
	}

	// Globals, set once before the open (D2).
	database.BatchInterval = 100 * time.Millisecond
	database.ENABLE_ARTICLE_CACHE = false
	database.NO_CACHE_BOOT = true

	cfg := database.DefaultDBConfig()
	cfg.DataDir = absDir
	cfg.BackupDir = filepath.Join(absDir, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[HARNESS] OpenDatabase:", err)
		return 2
	}
	hDB = db

	code := m.Run()

	// Teardown order: StopChan → WG → Shutdown → remove dir.
	close(db.StopChan)
	waited := make(chan struct{})
	go func() { db.WG.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(hDeadline):
		fmt.Fprintf(os.Stderr, "[HARNESS] db.WG.Wait timed out after %s\n", hDeadline)
		if code == 0 {
			code = 1
		}
	}
	if err := db.Shutdown(); err != nil {
		fmt.Fprintln(os.Stderr, "[HARNESS] Shutdown:", err)
	}
	if code == 0 && !keep {
		_ = os.RemoveAll(absDir)
	} else if !keep {
		fmt.Fprintln(os.Stderr, "[HARNESS] test data kept in", absDir)
	}
	return code
}

// hTestDB returns the shared database or skips the test when none is open.
func hTestDB(t *testing.T) *database.Database {
	t.Helper()
	if hDB == nil {
		t.Skipf("no test database: %s", hDBSkipReason)
	}
	return hDB
}

// hDataDir returns the data root of the shared database (for <DataDir>/db checks).
func hDataDir(t *testing.T) string {
	t.Helper()
	db := hTestDB(t)
	if db.GetDataDir() == "" {
		t.Fatal("test database has no DataDir")
	}
	return db.GetDataDir()
}

// ---------------------------------------------------------------------------
// Server

// hServerOpts configures newTestServer. Zero values: no processor, MaxConns 100,
// default idle timeout, no error delay, plain TCP.
type hServerOpts struct {
	Proc        ArticleProcessor
	MaxConns    int
	IdleTimeout time.Duration
	ErrorDelay  time.Duration
	TLS         *tls.Config
}

// hServer wraps a running NNTPServer bound to 127.0.0.1:0.
type hServer struct {
	S       *NNTPServer
	Addr    string
	TLS     *tls.Config
	wg      sync.WaitGroup
	mu      sync.Mutex
	clients []net.Conn
}

// newTestServer builds a server with NewNNTPServer (own WaitGroup), zero delays,
// and serves on 127.0.0.1:0. Cleanup stops it, closes tracked clients and waits
// at most 10 s for the server goroutines.
func newTestServer(t *testing.T, opts hServerOpts) *hServer {
	t.Helper()
	db := hTestDB(t)
	cfg := &config.ServerConfig{}
	cfg.NNTP.MaxConns = opts.MaxConns
	if cfg.NNTP.MaxConns <= 0 {
		cfg.NNTP.MaxConns = 100
	}
	hs := &hServer{TLS: opts.TLS}
	s, err := NewNNTPServer(db, cfg, &hs.wg, opts.Proc)
	if err != nil {
		t.Fatalf("NewNNTPServer: %v", err)
	}
	s.ArticleCmdDelay = 0
	s.GroupCmdDelay = 0
	s.ErrorDelay = opts.ErrorDelay
	s.IdleTimeout = opts.IdleTimeout
	hs.S = s

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	isTLS := false
	if opts.TLS != nil {
		l = tls.NewListener(l, opts.TLS)
		isTLS = true
	}
	hs.Addr = l.Addr().String()
	if err := s.Serve(l, isTLS); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { hs.stop(t) })
	return hs
}

func (hs *hServer) stop(t *testing.T) {
	t.Helper()
	_ = hs.S.Stop()
	hs.mu.Lock()
	for _, c := range hs.clients {
		_ = c.Close()
	}
	hs.clients = nil
	hs.mu.Unlock()
	done := make(chan struct{})
	go func() { hs.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(hDeadline):
		t.Errorf("server goroutines did not finish within %s after Stop", hDeadline)
	}
}

func (hs *hServer) track(c net.Conn) {
	hs.mu.Lock()
	hs.clients = append(hs.clients, c)
	hs.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Client

// hClient is a raw NNTP client with a per-connection deadline.
type hClient struct {
	Conn     net.Conn
	R        *bufio.Reader
	Greeting string
	Deadline time.Duration
}

// hDeadline is the hang guard of the test client: the per-command read/write deadline and the dial
// timeout. It only stops a hung test; a passing test never waits on it. Override with
// PUGLEAF_TEST_DEADLINE (a Go duration, e.g. 120s). Plan test-timing-hardening D3.
var hDeadline = hDeadlineFromEnv(60 * time.Second)

func hDeadlineFromEnv(def time.Duration) time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// dial connects, reads the greeting (c.Greeting) and tracks the connection.
func (hs *hServer) dial(t *testing.T) *hClient {
	t.Helper()
	c, err := hs.tryDial(hDeadline)
	if err != nil {
		t.Fatalf("dial %s: %v", hs.Addr, err)
	}
	return c
}

// tryDial connects without failing the test; the greeting is read with deadline.
// The deadline is only a hang guard, so a shorter value is raised to hDeadline:
// every caller expects the dial (or the refusal) to happen at once.
func (hs *hServer) tryDial(deadline time.Duration) (*hClient, error) {
	deadline = max(deadline, hDeadline)
	var conn net.Conn
	var err error
	if hs.TLS != nil {
		d := &net.Dialer{Timeout: deadline}
		conn, err = tls.DialWithDialer(d, "tcp", hs.Addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test client
	} else {
		conn, err = net.DialTimeout("tcp", hs.Addr, deadline)
	}
	if err != nil {
		return nil, err
	}
	hs.track(conn)
	c := &hClient{Conn: conn, R: bufio.NewReaderSize(conn, 1<<20), Deadline: deadline}
	line, err := c.readLine()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("greeting: %w", err)
	}
	c.Greeting = line
	return c, nil
}

func (c *hClient) bump() {
	_ = c.Conn.SetDeadline(time.Now().Add(c.Deadline))
}

// readLine reads one line and strips the CRLF (or LF).
func (c *hClient) readLine() (string, error) {
	c.bump()
	line, err := c.R.ReadString('\n')
	if err != nil {
		return line, err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// send writes raw bytes (no CRLF appended): use it for pipelining.
func (c *hClient) send(raw string) error {
	c.bump()
	_, err := io.WriteString(c.Conn, raw)
	return err
}

// tryCmd sends one command line and returns the status code and full line.
func (c *hClient) tryCmd(format string, args ...interface{}) (int, string, error) {
	if err := c.send(fmt.Sprintf(format, args...) + CRLF); err != nil {
		return 0, "", err
	}
	return c.readStatus()
}

// readStatus reads the next status line.
func (c *hClient) readStatus() (int, string, error) {
	line, err := c.readLine()
	if err != nil {
		return 0, line, err
	}
	code, perr := hParseCode(line)
	if perr != nil {
		return 0, line, perr
	}
	return code, line, nil
}

// cmd is tryCmd that fails the test on I/O errors.
func (c *hClient) cmd(t *testing.T, format string, args ...interface{}) (int, string) {
	t.Helper()
	code, line, err := c.tryCmd(format, args...)
	if err != nil {
		t.Fatalf("cmd %q: %v (line %q)", fmt.Sprintf(format, args...), err, line)
	}
	return code, line
}

// readDotLines reads a dot-terminated block, un-stuffs leading dots and strips CRLF.
func (c *hClient) readDotLines(t *testing.T) []string {
	t.Helper()
	var out []string
	for {
		line, err := c.readLine()
		if err != nil {
			t.Fatalf("readDotLines: %v after %d lines", err, len(out))
		}
		if line == "." {
			return out
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		out = append(out, line)
	}
}

// rawUntilDot returns the exact bytes up to and including the "\r\n.\r\n"
// terminator (or ".\r\n" when the block is empty). No un-stuffing.
func (c *hClient) rawUntilDot(t *testing.T) []byte {
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

// close closes the client connection.
func (c *hClient) close() {
	_ = c.Conn.Close()
}

// hParseCode extracts the 3-digit status code at the start of line.
func hParseCode(line string) (int, error) {
	if len(line) < 3 {
		return 0, fmt.Errorf("short status line %q", line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil || code < 100 || code > 599 {
		return 0, fmt.Errorf("bad status line %q", line)
	}
	if len(line) > 3 && line[3] != ' ' {
		return 0, fmt.Errorf("bad status line %q", line)
	}
	return code, nil
}

// hExpectCode returns an error unless code is one of want.
func hExpectCode(code int, line string, want ...int) error {
	for _, w := range want {
		if code == w {
			return nil
		}
	}
	return fmt.Errorf("got %d (%q), want %v", code, line, want)
}

// hExpectLines compares two line slices.
func hExpectLines(got, want []string) error {
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
// Seeding (raw SQL, synchronous: no waiting needed)

// hArticle is one seeded article. Headers is the raw header block (joined with
// "\n"); when empty, hGroup builds it from the other fields. Skip leaves the
// article number out (a "deleted" number).
type hArticle struct {
	Num        int64
	MessageID  string
	Subject    string
	From       string
	DateString string
	DateSent   time.Time
	References string
	Path       string
	Headers    string
	Body       string
	Bytes      int64
	Lines      int64
	Skip       bool
}

// hGroupName returns a unique group name "<prefix>.<seq>".
func hGroupName(prefix string) string {
	return fmt.Sprintf("%s.%d", prefix, hSeq.Add(1))
}

// hMessageID returns "<t-prefix-seq@test.invalid>" style ids.
func hMessageID(group string, n int64) string {
	return fmt.Sprintf("<%s-%d@test.invalid>", strings.ReplaceAll(group, ".", "-"), n)
}

// hDefaultArticle builds the default article n of group.
func hDefaultArticle(group string, n int64) hArticle {
	return hArticle{
		Num:        n,
		MessageID:  hMessageID(group, n),
		Subject:    fmt.Sprintf("Test article %d", n),
		From:       fmt.Sprintf("Tester %d <tester%d@test.invalid>", n, n),
		DateString: fmt.Sprintf("Sat, 11 Oct 2026 10:%02d:00 +0000", n%60),
		DateSent:   time.Date(2026, 10, 11, 10, int(n%60), 0, 0, time.UTC),
		Path:       "test.invalid!not-for-mail",
		Body:       fmt.Sprintf("body of article %d\nline two", n),
	}
}

// hGroup inserts a newsgroup with articles 1..n (mutate may change or Skip
// each) and returns the unique group name. The newsgroups row gets
// last_article, message_count, high_water and low_water from what was inserted.
func hGroup(t *testing.T, prefix string, n int, mutate func(i int, a *hArticle)) string {
	t.Helper()
	db := hTestDB(t)
	name := hGroupName(prefix)
	hier := name
	if i := strings.IndexByte(name, '.'); i > 0 {
		hier = name[:i]
	}
	if err := db.InsertNewsgroup(&models.Newsgroup{
		Name: name, Description: "harness group " + name, Active: true,
		Status: "y", LowWater: 1, Hierarchy: hier,
	}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", name, err)
	}
	var articles []hArticle
	for i := 1; i <= n; i++ {
		a := hDefaultArticle(name, int64(i))
		if mutate != nil {
			mutate(i, &a)
		}
		if a.Skip {
			continue
		}
		articles = append(articles, a)
	}
	if len(articles) > 0 {
		hArticleSQL(t, name, articles...)
	}
	return name
}

// hArticleSQL inserts articles with raw SQL into group's DB and updates the
// newsgroups row (last_article, message_count, high_water, low_water).
func hArticleSQL(t *testing.T, group string, articles ...hArticle) {
	t.Helper()
	db := hTestDB(t)
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	for _, a := range articles {
		headers := a.Headers
		if headers == "" {
			headers = strings.Join([]string{
				"Path: " + a.Path,
				"From: " + a.From,
				"Newsgroups: " + group,
				"Subject: " + a.Subject,
				"Date: " + a.DateString,
				"Message-ID: " + a.MessageID,
			}, "\n")
			if a.References != "" {
				headers += "\nReferences: " + a.References
			}
		}
		lines := a.Lines
		if lines == 0 {
			lines = int64(strings.Count(a.Body, "\n") + 1)
			if a.Body == "" {
				lines = 0
			}
		}
		bytesN := a.Bytes
		if bytesN == 0 {
			bytesN = int64(len(a.Body))
		}
		if _, err := gdb.DB.Exec(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references",
			                       bytes, lines, reply_count, path, headers_json, body_text, downloaded, imported_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, 1, CURRENT_TIMESTAMP)`,
			a.Num, a.MessageID, a.Subject, a.From, a.DateSent.UTC().Format("2006-01-02 15:04:05"), a.DateString,
			a.References, bytesN, lines, a.Path, headers, a.Body,
		); err != nil {
			t.Fatalf("insert article %d into %s: %v", a.Num, group, err)
		}
	}
	var cnt, lo, hi int64
	if err := gdb.DB.QueryRow(`SELECT COUNT(*), COALESCE(MIN(article_num),0), COALESCE(MAX(article_num),0) FROM articles`).Scan(&cnt, &lo, &hi); err != nil {
		t.Fatalf("count articles in %s: %v", group, err)
	}
	if lo == 0 {
		lo = 1
	}
	if _, err := db.GetMainDB().Exec(
		`UPDATE newsgroups SET last_article = ?, message_count = ?, high_water = ?, low_water = ?, updated_at = CURRENT_TIMESTAMP WHERE name = ?`,
		hi, cnt, hi, lo, group,
	); err != nil {
		t.Fatalf("update newsgroups %s: %v", group, err)
	}
}

// hUser inserts an NNTP user and returns username and plaintext password.
func hUser(t *testing.T, posting, active bool) (user, pass string) {
	t.Helper()
	db := hTestDB(t)
	user = fmt.Sprintf("t0user%04d", hSeq.Add(1)) // >= 10 chars
	pass = "pw-" + user
	if err := db.InsertNNTPUser(&models.NNTPUser{
		Username: user, Password: pass, MaxConns: 10, Posting: posting, WebUserID: 0, IsActive: active,
	}); err != nil {
		t.Fatalf("InsertNNTPUser %s: %v", user, err)
	}
	return user, pass
}

// ---------------------------------------------------------------------------
// Fake processor (same behavior as wiringTestProcessor, kept separate)

// hFakeProcessor is a scriptable ArticleProcessor. The *Fn hooks, when set,
// take precedence over the fixed results.
type hFakeProcessor struct {
	mu            sync.Mutex
	checkResult   int
	processResult int
	processErr    error
	findArticle   *models.Article
	findErr       error
	checkFn       func(messageID string) int
	processFn     func(article *models.Article) (int, error)
	findFn        func(messageID, currentGroup string) (*models.Article, error)
	processed     []*models.Article
	checked       []string
	findCalls     int
	lastFindGroup string
	lastFindID    string
	xrefLocs      map[string][]XrefLocation // XrefLocations result per message-id (nil: none known)
	xrefErr       error                     // XrefLocations error (takes precedence over xrefLocs)
	xrefCalls     int
}

func (p *hFakeProcessor) ProcessIncomingArticle(article *models.Article) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.processed = append(p.processed, article)
	if p.processFn != nil {
		return p.processFn(article)
	}
	return p.processResult, p.processErr
}

func (p *hFakeProcessor) CheckMessageID(messageID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked = append(p.checked, messageID)
	if p.checkFn != nil {
		return p.checkFn(messageID)
	}
	return p.checkResult
}

func (p *hFakeProcessor) FindArticleByMessageID(messageID, currentGroup string) (*models.Article, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.findCalls++
	p.lastFindGroup = currentGroup
	p.lastFindID = messageID
	if p.findFn != nil {
		return p.findFn(messageID, currentGroup)
	}
	return p.findArticle, p.findErr
}

func (p *hFakeProcessor) processedArticles() []*models.Article {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*models.Article(nil), p.processed...)
}

func (p *hFakeProcessor) findCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.findCalls
}

// ---------------------------------------------------------------------------
// knownBug

// hKnownBugAction decides what knownBug does: "fatal" when the bug no longer
// reproduces (err == nil), "error" when PUGLEAF_KNOWN_BUGS=1 lists open bugs,
// "skip" otherwise.
func hKnownBugAction(id string, err error, envSet bool) (action, msg string) {
	if err == nil {
		return "fatal", fmt.Sprintf("known bug %s no longer reproduces: remove knownBug, keep the assertion", id)
	}
	if envSet {
		return "error", fmt.Sprintf("known bug %s still open: %v", id, err)
	}
	return "skip", fmt.Sprintf("known bug %s: %v", id, err)
}

// knownBug takes the result of an RFC-correct check. A nil err means the bug is
// fixed and the marker must be removed (the test fails). With PUGLEAF_KNOWN_BUGS=1
// an open bug is reported with t.Errorf; otherwise the test is skipped.
func knownBug(t *testing.T, id string, err error) {
	t.Helper()
	action, msg := hKnownBugAction(id, err, os.Getenv("PUGLEAF_KNOWN_BUGS") == "1")
	switch action {
	case "fatal":
		t.Fatal(msg)
	case "error":
		t.Error(msg)
	default:
		t.Skip(msg)
	}
}

// ---------------------------------------------------------------------------
// runIsolated

// hIsolatedRunPattern builds -test.run=^A$/^B$ for a (sub)test name.
func hIsolatedRunPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// runIsolated runs child in a re-executed copy of the test binary with its own
// data dir, under timeout. In the child process it just runs child(t). It
// returns nil when the child passed, an error with the tail of its output when
// it failed, or "timed out after …" when it had to be killed.
func runIsolated(t *testing.T, timeout time.Duration, child func(t *testing.T)) error {
	t.Helper()
	if os.Getenv("PUGLEAF_ISOLATED") == t.Name() {
		child(t)
		return nil
	}
	if hDB == nil {
		t.Skipf("no test database: %s", hDBSkipReason)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+hIsolatedRunPattern(t.Name()), "-test.count=1", "-test.v")
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
		return fmt.Errorf("timed out after %s: %s", time.Since(start).Round(time.Millisecond), hTail(out.String(), 20))
	}
	if runErr != nil {
		return fmt.Errorf("child failed: %v: %s", runErr, hChildOutput(out.String(), 40))
	}
	return nil
}

// hTail returns the last n lines of s.
func hTail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// hChildOutput returns the last n lines of a child's output, preceded by the
// lines around the first "panic:" (a Go panic prints at the top, before the
// goroutine dump, which the tail alone would hide).
func hChildOutput(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "panic:") || strings.Contains(l, "fatal error:") {
			end := i + 12
			if end > len(lines) {
				end = len(lines)
			}
			return strings.Join(lines[i:end], "\n") + "\n[...]\n" + hTail(s, n)
		}
	}
	return hTail(s, n)
}

// ---------------------------------------------------------------------------
// Self-tests

func TestHarnessSmoke(t *testing.T) {
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	if code, err := hParseCode(c.Greeting); err != nil || (code != 200 && code != 201) {
		t.Fatalf("greeting %q: code=%d err=%v", c.Greeting, code, err)
	}
	code, line := c.cmd(t, "CAPABILITIES")
	if err := hExpectCode(code, line, 101); err != nil {
		t.Fatal(err)
	}
	caps := c.readDotLines(t)
	if len(caps) == 0 || caps[0] != "VERSION 2" {
		t.Fatalf("CAPABILITIES lines %q: want first line VERSION 2", caps)
	}
	code, line = c.cmd(t, "QUIT")
	if err := hExpectCode(code, line, 205); err != nil {
		t.Fatal(err)
	}
	if _, err := c.readLine(); err == nil {
		t.Fatal("expected EOF after QUIT")
	}
}

func TestHarnessSeed(t *testing.T) {
	g := hGroup(t, "t0.seed", 3, nil)
	hs := newTestServer(t, hServerOpts{})
	c := hs.dial(t)
	code, line := c.cmd(t, "GROUP %s", g)
	if err := hExpectCode(code, line, 211); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[1] != "3" || fields[4] != g {
		t.Fatalf("GROUP reply %q: want '211 3 <low> <high> %s'", line, g)
	}
	db := hTestDB(t)
	gdb, err := db.GetGroupDB(g)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Return()
	a, err := db.GetArticleByNum(gdb, 2)
	if err != nil {
		t.Fatalf("GetArticleByNum 2: %v", err)
	}
	if a.MessageID != hMessageID(g, 2) {
		t.Fatalf("article 2 message-id %q, want %q", a.MessageID, hMessageID(g, 2))
	}
	user, pass := hUser(t, true, true)
	if _, err := db.AuthenticateNNTPUser(user, pass); err != nil {
		t.Fatalf("AuthenticateNNTPUser(%s): %v", user, err)
	}
}

func TestHarnessKnownBug(t *testing.T) {
	open := errors.New("got 500, want 501")
	cases := []struct {
		name   string
		err    error
		envSet bool
		action string
		substr string
	}{
		{"fixed", nil, false, "fatal", "no longer reproduces"},
		{"fixed-env", nil, true, "fatal", "no longer reproduces"},
		{"open-skip", open, false, "skip", "known bug X-1: got 500"},
		{"open-list", open, true, "error", "known bug X-1 still open"},
	}
	for _, tc := range cases {
		action, msg := hKnownBugAction("X-1", tc.err, tc.envSet)
		if action != tc.action || !strings.Contains(msg, tc.substr) {
			t.Errorf("%s: got (%q, %q), want action %q containing %q", tc.name, action, msg, tc.action, tc.substr)
		}
	}
	if got := hIsolatedRunPattern("TestA/sub.1/x+y"); got != `^TestA$/^sub\.1$/^x\+y$` {
		t.Errorf("hIsolatedRunPattern: %q", got)
	}
	long := "=== RUN TestX\npanic: runtime error: nil deref\n\ngoroutine 7 [running]:\n" + strings.Repeat("frame\n", 100) + "exit status 2"
	if got := hChildOutput(long, 5); !strings.HasPrefix(got, "panic: runtime error") || !strings.HasSuffix(got, "exit status 2") {
		t.Errorf("hChildOutput lost the panic line or the tail: %q", got)
	}
}

func TestHarnessIsolatedTimeout(t *testing.T) {
	if hDB == nil {
		t.Skipf("no test database: %s", hDBSkipReason)
	}
	const timeout = 3 * time.Second
	start := time.Now()
	err := runIsolated(t, timeout, func(t *testing.T) {
		// The child never ends on its own (far above every guard below): only the
		// runIsolated timeout can stop it.
		time.Sleep(10 * time.Minute)
	})
	if os.Getenv("PUGLEAF_ISOLATED") == t.Name() {
		return // child
	}
	took := time.Since(start)
	// Structural: runIsolated reports the kill by its timeout ("timed out after ...").
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timed out error, got %v", err)
	}
	// Lower bound: the child was killed by the timeout, not by an early exit.
	if took < timeout {
		t.Fatalf("child ended after %s, before the %s timeout", took, timeout)
	}
	// Hang guard only (the kill plus WaitDelay ends near timeout+2s): a kill that
	// is ignored runs into the 10-minute child sleep.
	if took > timeout+hDeadline {
		t.Fatalf("timeout took %s, want the kill soon after %s", took, timeout)
	}
}

func TestHarnessIsolatedPass(t *testing.T) {
	if hDB == nil {
		t.Skipf("no test database: %s", hDBSkipReason)
	}
	err := runIsolated(t, 3*hDeadline, func(t *testing.T) {
		hs := newTestServer(t, hServerOpts{})
		c := hs.dial(t)
		if code, line := c.cmd(t, "QUIT"); code != 205 {
			t.Fatalf("QUIT: %q", line)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// XrefLocations implements ArticleProcessor: xrefErr when set, else the configured
// xrefLocs[messageID] (nil, nil when none are configured).
func (p *hFakeProcessor) XrefLocations(messageID string) ([]XrefLocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.xrefCalls++
	if p.xrefErr != nil {
		return nil, p.xrefErr
	}
	return p.xrefLocs[messageID], nil
}

// xrefCount returns the number of XrefLocations calls.
func (p *hFakeProcessor) xrefCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.xrefCalls
}
