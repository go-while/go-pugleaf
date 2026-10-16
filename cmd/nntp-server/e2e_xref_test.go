package main

// TestE2EXrefBinary checks the local Xref: header of plan xref-history (slice xref-e2e-docs)
// against the real binaries. It builds pugleaf-nntp-server (this package), nntpmgr and
// history-rebuild into t.TempDir() (without -race), seeds a scratch data root there (one
// posting user, two active groups), POSTs one crosspost and checks HEAD/ARTICLE/OVER/HDR and
// LIST OVERVIEW.FMT with -xref full, partial and off on the same data, an invalid -xref value,
// and history-rebuild -validate-only on the result.
//
// Every binary runs with its working directory in the scratch root and an absolute -data, so
// the default ./data is never reached. Ports come from the OS. Waits are polls bounded by
// testWait (PUGLEAF_TEST_DEADLINE). Output lines: `PASS|FAIL X<nn> [nntp] <text>` (e2eReport).
//
// Skipped with -short, when PUGLEAF_E2E_ADDR is set (external-server mode), as root (uid <
// 1000: OpenDatabase exits), without the go tool, or when no usable -nntphostname resolves
// (the server resolves it by DNS and exits otherwise). PUGLEAF_E2E_HOST picks the hostname;
// the default tries os.Hostname() and then example.org.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	xrefGroupA = "xref.test.a"
	xrefGroupB = "xref.test.b"
	xrefUser   = "xreftest01"
	xrefPass   = "xreftestpass01"
)

// e2eReport prints the step lines and fails t on FAIL. Shared with the e2e-tagged binary tests
// in e2e_binary_test.go; defined here because this file builds without the tag.
type e2eReport struct{ t *testing.T }

func (r e2eReport) pass(id, text string) { fmt.Printf("PASS %s [nntp] %s\n", id, text) }

func (r e2eReport) fail(id, text, detail string) {
	fmt.Printf("FAIL %s [nntp] %s :: %s\n", id, text, detail)
	r.t.Errorf("%s %s: %s", id, text, detail)
}

func (r e2eReport) check(id, text string, err error) bool {
	if err != nil {
		r.fail(id, text, err.Error())
		return false
	}
	r.pass(id, text)
	return true
}

// xrefHostRe is the hostname format processor.validateHostname accepts (RFC 1123).
var xrefHostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

// xrefHostname returns a hostname the server accepts for -nntphostname: valid format with at
// least one dot, not localhost, not an IP address, and resolvable.
func xrefHostname() (string, error) {
	var cands []string
	if h := os.Getenv("PUGLEAF_E2E_HOST"); h != "" {
		cands = append(cands, h)
	} else {
		if h, err := os.Hostname(); err == nil && h != "" {
			cands = append(cands, h)
		}
		cands = append(cands, "example.org")
	}
	var why []string
	for _, h := range cands {
		l := strings.ToLower(h)
		switch {
		case !xrefHostRe.MatchString(h), !strings.Contains(h, "."), l == "localhost", l == "localhost.localdomain", net.ParseIP(h) != nil:
			why = append(why, h+": not usable as -nntphostname")
			continue
		}
		ips, err := net.LookupIP(h)
		if err != nil || len(ips) == 0 {
			why = append(why, fmt.Sprintf("%s: does not resolve (%v)", h, err))
			continue
		}
		return h, nil
	}
	return "", errors.New(strings.Join(why, "; "))
}

// xrefEnv is the scratch root of one test run.
type xrefEnv struct {
	t                    *testing.T
	server, mgr, rebuild string // binaries
	data, run, host      string
}

// xrefBuild builds the three binaries into dir.
func xrefBuild(t *testing.T, goTool, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*testWait) // cold cgo builds are slow
	defer cancel()
	// the binaries refuse to start with the default appVersion "-unset-"
	cmd := exec.CommandContext(ctx, goTool, "build", "-o", dir+string(filepath.Separator), "-ldflags", "-X main.appVersion=e2e-test",
		"github.com/go-while/go-pugleaf/cmd/nntp-server",
		"github.com/go-while/go-pugleaf/cmd/nntpmgr",
		"github.com/go-while/go-pugleaf/cmd/history-rebuild")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

// tool runs a one-shot binary in the scratch root and returns its combined output.
func (e *xrefEnv) tool(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = e.run
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("%s did not exit within %s", filepath.Base(bin), testWait)
	}
	return string(out), err
}

// xrefServer is one running pugleaf-nntp-server process.
type xrefServer struct {
	cmd     *exec.Cmd
	out     *e2eLogBuffer
	addr    string
	done    chan struct{}
	waitErr error // valid after done is closed
}

// xrefFreePort asks the OS for a free TCP port on 127.0.0.1.
func xrefFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	return port, l.Close()
}

// xrefOutput returns the last n lines of a process output buffer.
func xrefOutput(b *e2eLogBuffer, n int) string {
	b.mu.Lock()
	s := b.buf.String()
	b.mu.Unlock()
	return e2eTail(s, n)
}

// start launches the server with -xref mode and waits until it sends a greeting.
// The process is killed at test end if it still runs.
func (e *xrefEnv) start(mode string) *xrefServer {
	e.t.Helper()
	port, err := xrefFreePort()
	if err != nil {
		e.t.Fatalf("free port: %v", err)
	}
	s := &xrefServer{out: &e2eLogBuffer{}, addr: fmt.Sprintf("127.0.0.1:%d", port), done: make(chan struct{})}
	s.cmd = exec.Command(e.server,
		"-data", e.data, "-nntphostname", e.host, "-nntptcpport", strconv.Itoa(port),
		"-xref", mode, "-articledelay", "0", "-groupdelay", "0", "-errordelay", "0",
		// polls by message-id may answer 430 before the commit: do not cache that miss
		"-local430ttl", "1ms")
	s.cmd.Dir = e.run
	s.cmd.Stdout = s.out
	s.cmd.Stderr = s.out
	if err := s.cmd.Start(); err != nil {
		e.t.Fatalf("start server: %v", err)
	}
	go func() { s.waitErr = s.cmd.Wait(); close(s.done) }()
	e.t.Cleanup(func() {
		select {
		case <-s.done:
		default:
			_ = s.cmd.Process.Kill()
			<-s.done
		}
	})
	err = e2eWaitFor(testWait, func() error {
		select {
		case <-s.done:
			return nil // exited: reported below
		default:
		}
		c, err := e2eDialAddr(s.addr, testWait)
		if err != nil {
			return err
		}
		c.close()
		return nil
	})
	select {
	case <-s.done:
		e.t.Fatalf("server -xref %s exited during startup: %v\n%s", mode, s.waitErr, xrefOutput(s.out, 30))
	default:
	}
	if err != nil {
		e.t.Fatalf("server -xref %s not ready: %v\n%s", mode, err, xrefOutput(s.out, 30))
	}
	return s
}

// stop sends SIGINT and waits for the exit (killed after testWait); a non-zero exit is an error.
func (s *xrefServer) stop() error {
	if err := s.cmd.Process.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("SIGINT: %w", err)
	}
	select {
	case <-s.done:
	case <-time.After(testWait):
		_ = s.cmd.Process.Kill()
		<-s.done
		return fmt.Errorf("no exit within %s after SIGINT, killed", testWait)
	}
	if s.waitErr != nil {
		return fmt.Errorf("exit: %v\n%s", s.waitErr, xrefOutput(s.out, 20))
	}
	return nil
}

// dial connects a reader to s (no GROUP selected).
func (s *xrefServer) dial(t *testing.T) *e2eClient {
	t.Helper()
	c, err := e2eDialAddr(s.addr, testWait)
	if err != nil {
		t.Fatalf("dial %s: %v", s.addr, err)
	}
	t.Cleanup(c.close)
	return c
}

// xrefBlock sends cmd, requires the status want and returns the data block.
func xrefBlock(c *e2eClient, want int, format string, args ...interface{}) ([]string, error) {
	cmd := fmt.Sprintf(format, args...)
	code, line, err := c.tryCmd("%s", cmd)
	if err != nil {
		return nil, fmt.Errorf("%s: %v (line %q)", cmd, err, line)
	}
	if code != want {
		return nil, fmt.Errorf("%s: got %q, want %d", cmd, line, want)
	}
	lines, err := c.tryDotLines()
	if err != nil {
		return nil, fmt.Errorf("%s: %v", cmd, err)
	}
	return lines, nil
}

// xrefSelect selects group (211).
func xrefSelect(c *e2eClient, group string) error {
	code, line, err := c.tryCmd("GROUP %s", group)
	if err != nil || code != 211 {
		return fmt.Errorf("GROUP %s: code=%d line=%q err=%v", group, code, line, err)
	}
	return nil
}

// xrefHeaderLines returns the Xref: lines of a header block.
func xrefHeaderLines(hdrs []string) []string {
	var out []string
	for _, l := range hdrs {
		if len(l) >= 5 && strings.EqualFold(l[:5], "Xref:") {
			out = append(out, l)
		}
	}
	return out
}

// xrefLastHeader requires exactly one Xref: line, equal to want and the last header line.
func xrefLastHeader(hdrs []string, want string) error {
	if x := xrefHeaderLines(hdrs); len(x) != 1 {
		return fmt.Errorf("Xref lines %q, want exactly [%q]", x, want)
	}
	if last := hdrs[len(hdrs)-1]; last != want { // hdrs is not empty: it holds the Xref line
		return fmt.Errorf("last header line %q, want %q", last, want)
	}
	return nil
}

// xrefNoHeader requires no Xref: line.
func xrefNoHeader(hdrs []string) error {
	if x := xrefHeaderLines(hdrs); len(x) != 0 {
		return fmt.Errorf("unexpected Xref lines %q", x)
	}
	return nil
}

// xrefHead runs HEAD arg (221) in the selected group (or without one) and returns the headers.
func xrefHead(c *e2eClient, arg string) ([]string, error) {
	return xrefBlock(c, 221, "HEAD %s", arg)
}

// xrefArticleHead runs ARTICLE arg (220) and returns the header part.
func xrefArticleHead(c *e2eClient, arg string) ([]string, error) {
	lines, err := xrefBlock(c, 220, "ARTICLE %s", arg)
	if err != nil {
		return nil, err
	}
	head, body := e2eSplitArticle(lines)
	if len(body) == 0 || body[0] != "xref crosspost body" {
		return nil, fmt.Errorf("ARTICLE %s: body %q", arg, body)
	}
	return head, nil
}

// xrefOver runs OVER 1 in the selected group and returns the TAB fields of the one line.
func xrefOver(c *e2eClient) ([]string, error) {
	lines, err := xrefBlock(c, 224, "OVER 1")
	if err != nil {
		return nil, err
	}
	if len(lines) != 1 {
		return nil, fmt.Errorf("OVER 1: %d lines %q, want 1", len(lines), lines)
	}
	return strings.Split(lines[0], "\t"), nil
}

// xrefHdr runs HDR Xref 1 in the selected group and returns the one line.
func xrefHdr(c *e2eClient) (string, error) {
	lines, err := xrefBlock(c, 225, "HDR Xref 1")
	if err != nil {
		return "", err
	}
	if len(lines) != 1 {
		return "", fmt.Errorf("HDR Xref 1: %d lines %q, want 1", len(lines), lines)
	}
	return lines[0], nil
}

// xrefFmt runs LIST OVERVIEW.FMT.
func xrefFmt(c *e2eClient) ([]string, error) {
	return xrefBlock(c, 215, "LIST OVERVIEW.FMT")
}

func TestE2EXrefBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs the server binary")
	}
	if os.Getenv("PUGLEAF_E2E_ADDR") != "" {
		t.Skip("PUGLEAF_E2E_ADDR is set: external-server mode")
	}
	if os.Geteuid() < 1000 {
		t.Skip("running as root/system user (uid < 1000): the binaries refuse to open a database")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go tool not found: %v", err)
	}
	host, err := xrefHostname()
	if err != nil {
		t.Skipf("no usable -nntphostname: %v", err)
	}

	root := t.TempDir()
	e := &xrefEnv{
		t: t, host: host,
		server:  filepath.Join(root, "bin", "nntp-server"),
		mgr:     filepath.Join(root, "bin", "nntpmgr"),
		rebuild: filepath.Join(root, "bin", "history-rebuild"),
		data:    filepath.Join(root, "data"),
		run:     filepath.Join(root, "run"),
	}
	if err := os.MkdirAll(e.run, 0o755); err != nil {
		t.Fatal(err)
	}
	xrefBuild(t, goTool, filepath.Join(root, "bin"))

	// seed: nntpmgr creates the schema and the posting user, then the two groups by SQL
	if out, err := e.tool(e.mgr, "-data", e.data, "-create", "-username", xrefUser, "-password", xrefPass, "-posting", "-maxconns", "4"); err != nil {
		t.Fatalf("nntpmgr -create: %v\n%s", err, e2eTail(out, 20))
	}
	mainDB, err := sql.Open("sqlite3", filepath.Join(e.data, "cfg", "pugleaf.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = mainDB.Exec(`INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status,low_water,high_water)
		VALUES(?,'',0,0,1,'xref','y',1,0),(?,'',0,0,1,'xref','y',1,0)`, xrefGroupA, xrefGroupB)
	if cerr := mainDB.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("seed groups: %v", err)
	}

	r := e2eReport{t: t}
	ga, gb := xrefGroupA, xrefGroupB
	id := fmt.Sprintf("<xref-e2e-%d@test.invalid>", time.Now().UnixNano())
	full := map[string]string{ // serving group first
		ga: fmt.Sprintf("Xref: %s %s:1 %s:1", host, ga, gb),
		gb: fmt.Sprintf("Xref: %s %s:1 %s:1", host, gb, ga),
	}

	// ---------------------------------------------------------------- -xref full
	srv := e.start("full")
	c := srv.dial(t)
	if code, line, err := c.tryAuth(xrefUser, xrefPass); err != nil || code != 281 {
		t.Fatalf("AUTHINFO: code=%d line=%q err=%v\n%s", code, line, err, xrefOutput(srv.out, 30))
	}
	if !r.check("X01", "POST crosspost "+ga+","+gb+" 240", func() error {
		code, line, err := c.tryPost(e2eWire(e2eHead(id, ga+","+gb, "Xref e2e crosspost", e2eNow()), []string{"xref crosspost body"}))
		if err != nil || code != 240 {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		return nil
	}()) {
		t.FailNow()
	}

	// the group commit and then the history entry are asynchronous: poll HEAD <id> in each
	// group until it is stored there and the history lists both numbers
	if !r.check("X02", "HEAD <id> 221 in both groups with the full Xref", func() error {
		for _, g := range []string{ga, gb} {
			if err := xrefSelect(c, g); err != nil {
				return err
			}
			if err := e2eWaitFor(testWait, func() error {
				hdrs, err := xrefHead(c, id)
				if err != nil {
					return err
				}
				return xrefLastHeader(hdrs, full[g])
			}); err != nil {
				return fmt.Errorf("in %s: %v", g, err)
			}
		}
		return nil
	}()) {
		t.Fatalf("server output:\n%s", xrefOutput(srv.out, 40))
	}

	for _, g := range []string{ga, gb} {
		r.check("X03", "full: HEAD 1 and ARTICLE 1 in "+g+" end with "+full[g], func() error {
			if err := xrefSelect(c, g); err != nil {
				return err
			}
			hdrs, err := xrefHead(c, "1")
			if err != nil {
				return err
			}
			if err := xrefLastHeader(hdrs, full[g]); err != nil {
				return fmt.Errorf("HEAD 1: %v", err)
			}
			hdrs, err = xrefArticleHead(c, "1")
			if err != nil {
				return err
			}
			if err := xrefLastHeader(hdrs, full[g]); err != nil {
				return fmt.Errorf("ARTICLE 1: %v", err)
			}
			fmt.Printf("INFO X03 [nntp] %s: %s\n", g, full[g])
			return nil
		}())
	}

	r.check("X04", "full: HEAD <id> without GROUP lists both groups", func() error {
		c0 := srv.dial(t)
		defer c0.close()
		code, line, err := c0.tryCmd("HEAD %s", id)
		if err != nil || code != 221 || !strings.HasPrefix(line, "221 0 "+id) {
			return fmt.Errorf("code=%d line=%q err=%v", code, line, err)
		}
		hdrs, err := c0.tryDotLines()
		if err != nil {
			return err
		}
		x := xrefHeaderLines(hdrs)
		if len(x) != 1 || hdrs[len(hdrs)-1] != x[0] {
			return fmt.Errorf("Xref lines %q (last header %q)", x, hdrs[len(hdrs)-1])
		}
		f := strings.Fields(x[0])
		if len(f) != 4 || f[0] != "Xref:" || f[1] != host {
			return fmt.Errorf("Xref line %q", x[0])
		}
		got := map[string]bool{f[2]: true, f[3]: true}
		if !got[ga+":1"] || !got[gb+":1"] {
			return fmt.Errorf("Xref line %q, want %s:1 and %s:1", x[0], ga, gb)
		}
		fmt.Printf("INFO X04 [nntp] no group: %s\n", x[0])
		return nil
	}())

	r.check("X05", "full: OVER 1 in "+ga+" has 9 fields, the 9th "+full[ga], func() error {
		if err := xrefSelect(c, ga); err != nil {
			return err
		}
		f, err := xrefOver(c)
		if err != nil {
			return err
		}
		if len(f) != 9 || f[8] != full[ga] || f[4] != id {
			return fmt.Errorf("fields %q", f)
		}
		return nil
	}())

	r.check("X06", "full: LIST OVERVIEW.FMT ends with Xref:full", func() error {
		lines, err := xrefFmt(c)
		if err != nil {
			return err
		}
		if len(lines) == 0 || lines[len(lines)-1] != "Xref:full" {
			return fmt.Errorf("lines %q", lines)
		}
		return nil
	}())

	r.check("X07", "full: HDR Xref 1 in "+ga, func() error {
		line, err := xrefHdr(c)
		if err != nil {
			return err
		}
		if want := fmt.Sprintf("1 %s %s:1 %s:1", host, ga, gb); line != want {
			return fmt.Errorf("got %q, want %q", line, want)
		}
		fmt.Printf("INFO X07 [nntp] HDR: %q\n", line)
		return nil
	}())
	c.close()
	r.check("X08", "full: SIGINT, exit 0", srv.stop())

	// ---------------------------------------------------------------- -xref partial
	srv = e.start("partial")
	c = srv.dial(t)
	for _, g := range []string{ga, gb} {
		want := fmt.Sprintf("Xref: %s %s:1", host, g)
		r.check("X10", "partial: HEAD 1 and HEAD <id> in "+g+" end with "+want, func() error {
			if err := xrefSelect(c, g); err != nil {
				return err
			}
			for _, arg := range []string{"1", id} {
				hdrs, err := xrefHead(c, arg)
				if err != nil {
					return err
				}
				if err := xrefLastHeader(hdrs, want); err != nil {
					return fmt.Errorf("HEAD %s: %v", arg, err)
				}
			}
			fmt.Printf("INFO X10 [nntp] %s: %s\n", g, want)
			return nil
		}())
	}

	r.check("X11", "partial: HEAD <id> without GROUP has no Xref line", func() error {
		c0 := srv.dial(t)
		defer c0.close()
		hdrs, err := xrefHead(c0, id)
		if err != nil {
			return err
		}
		return xrefNoHeader(hdrs)
	}())

	r.check("X12", "partial: OVER 1, HDR Xref 1 and LIST OVERVIEW.FMT in "+ga, func() error {
		if err := xrefSelect(c, ga); err != nil {
			return err
		}
		f, err := xrefOver(c)
		if err != nil {
			return err
		}
		if want := fmt.Sprintf("Xref: %s %s:1", host, ga); len(f) != 9 || f[8] != want {
			return fmt.Errorf("OVER fields %q, want 9th %q", f, want)
		}
		line, err := xrefHdr(c)
		if err != nil {
			return err
		}
		if want := fmt.Sprintf("1 %s %s:1", host, ga); line != want {
			return fmt.Errorf("HDR: got %q, want %q", line, want)
		}
		lines, err := xrefFmt(c)
		if err != nil {
			return err
		}
		if len(lines) == 0 || lines[len(lines)-1] != "Xref:full" {
			return fmt.Errorf("LIST OVERVIEW.FMT %q", lines)
		}
		return nil
	}())
	c.close()
	r.check("X13", "partial: SIGINT, exit 0", srv.stop())

	// ---------------------------------------------------------------- -xref off
	srv = e.start("off")
	c = srv.dial(t)
	r.check("X20", "off: HEAD 1, ARTICLE 1 in "+ga+" and HEAD <id> without GROUP have no Xref line", func() error {
		c0 := srv.dial(t)
		defer c0.close()
		hdrs, err := xrefHead(c0, id)
		if err != nil {
			return err
		}
		if err := xrefNoHeader(hdrs); err != nil {
			return fmt.Errorf("HEAD <id> without GROUP: %v", err)
		}
		if err := xrefSelect(c, ga); err != nil {
			return err
		}
		if hdrs, err = xrefHead(c, "1"); err != nil {
			return err
		}
		if err := xrefNoHeader(hdrs); err != nil {
			return fmt.Errorf("HEAD 1: %v", err)
		}
		if hdrs, err = xrefArticleHead(c, "1"); err != nil {
			return err
		}
		if err := xrefNoHeader(hdrs); err != nil {
			return fmt.Errorf("ARTICLE 1: %v", err)
		}
		return nil
	}())

	r.check("X21", "off: LIST OVERVIEW.FMT without Xref:full, OVER 1 has 8 fields", func() error {
		lines, err := xrefFmt(c)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if strings.EqualFold(l, "Xref:full") {
				return fmt.Errorf("LIST OVERVIEW.FMT %q", lines)
			}
		}
		f, err := xrefOver(c)
		if err != nil {
			return err
		}
		if len(f) != 8 {
			return fmt.Errorf("OVER fields %q, want 8", f)
		}
		return nil
	}())

	// empty value: the server sends the number alone (hdrLine; RFC 3977 §8.5.2 lets the
	// space after the number be retained or omitted)
	r.check("X22", "off: HDR Xref 1 gives an empty value", func() error {
		line, err := xrefHdr(c)
		if err != nil {
			return err
		}
		if line != "1" {
			return fmt.Errorf("got %q, want %q", line, "1")
		}
		return nil
	}())
	c.close()
	r.check("X23", "off: SIGINT, exit 0", srv.stop())

	// ---------------------------------------------------------------- invalid mode
	r.check("X30", "-xref maybe exits non-zero", func() error {
		out, err := e.tool(e.server, "-data", e.data, "-nntphostname", host, "-nntptcpport", "0", "-xref", "maybe")
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() == 0 {
			return fmt.Errorf("err=%v, want a non-zero exit; output:\n%s", err, e2eTail(out, 10))
		}
		if !strings.Contains(out, `invalid -xref value "maybe"`) {
			return fmt.Errorf("output has no usage error:\n%s", e2eTail(out, 10))
		}
		return nil
	}())

	// ---------------------------------------------------------------- history-rebuild -validate-only
	r.check("X31", "history-rebuild -validate-only: 2 found, 0 missing, 0 without/wrong number", func() error {
		out, err := e.tool(e.rebuild, "-data", e.data, "-validate-only")
		if err != nil {
			return fmt.Errorf("%v\n%s", err, e2eTail(out, 20))
		}
		for _, want := range []*regexp.Regexp{
			regexp.MustCompile(`(?m)^Found in History:\s+2$`),
			regexp.MustCompile(`(?m)^Missing:\s+0$`),
			regexp.MustCompile(`(?m)^Group not in entry:\s+0$`),
			regexp.MustCompile(`(?m)^Without number:\s+0 `),
			regexp.MustCompile(`(?m)^Wrong number:\s+0 `),
		} {
			if !want.MatchString(out) {
				return fmt.Errorf("no line matching %s in:\n%s", want, e2eTail(out, 20))
			}
		}
		return nil
	}())
}
