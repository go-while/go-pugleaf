package main

// TestE2EPost430Binary checks plan post-430-window (slice post-430-e2e) against the real
// binaries: right after an article arrived in this process, lookups by message-id without a
// selected group must find it and a miss before the commit must not poison the negative cache
// (local430, default TTL 60 s, kept at the default here on purpose).
//
// It builds pugleaf-nntp-server (this package) and nntpmgr into t.TempDir() (without -race),
// seeds a scratch data root there (one posting user, two active groups) and checks:
//   - the default -post-commit-wait 5s: POST answers 240 after the group DB commit, so STAT,
//     HEAD, OVER, HDR and XHDR by message-id answer 2xx at once on the same and a new connection;
//   - IHAVE (no commit wait): STAT on a new connection may answer 430 first, then reaches 223
//     and stays 223 (the miss is not cached);
//   - a POST to a known and an unknown group: 240, STAT 223, no group DB for the unknown name;
//   - -post-commit-wait 0: STAT right after POST may be 430, but reaches 223 and stays 223;
//   - -post-commit-wait -1s exits 1; every server run exits 0 on SIGINT.
//
// It reuses the scratch-root helpers of e2e_xref_test.go (xrefEnv, xrefServer, xrefHostname,
// e2eReport). Waits are polls bounded by testWait (PUGLEAF_TEST_DEADLINE); the POST latency is
// logged, never asserted. Output lines: `PASS|FAIL|INFO P<nn> [nntp] <text>`.
//
// Skipped like TestE2EXrefBinary: -short, PUGLEAF_E2E_ADDR set, uid < 1000, no go tool, or no
// usable -nntphostname.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3"
)

const (
	p430GroupA = "post430.test.a"
	p430GroupB = "post430.test.b"
	p430User   = "post430user01"
	p430Pass   = "post430pass01"
	// p430Stay is the number of extra STAT polls after the first 223 that must all answer 223
	p430Stay = 10
	// p430Poll is the pause between STAT polls
	p430Poll = 100 * time.Millisecond
)

// p430Build builds nntp-server and nntpmgr into dir.
func p430Build(t *testing.T, goTool, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*testWait) // cold cgo builds are slow
	defer cancel()
	// the binaries refuse to start with the default appVersion "-unset-"
	cmd := exec.CommandContext(ctx, goTool, "build", "-o", dir+string(filepath.Separator), "-ldflags", "-X main.appVersion=e2e-test",
		"github.com/go-while/go-pugleaf/cmd/nntp-server",
		"github.com/go-while/go-pugleaf/cmd/nntpmgr")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

// p430Stat sends STAT <id> and returns the status code and line.
func p430Stat(c *e2eClient, id string) (int, string, error) {
	code, line, err := c.tryCmd("STAT %s", id)
	if err != nil {
		return code, line, fmt.Errorf("STAT %s: %v (line %q)", id, err, line)
	}
	return code, line, nil
}

// p430Stat223 requires STAT <id> → "223 0 <id>" (no group selected).
func p430Stat223(c *e2eClient, id string) error {
	code, line, err := p430Stat(c, id)
	if err != nil {
		return err
	}
	if code != 223 || !strings.HasPrefix(line, "223 0 "+id) {
		return fmt.Errorf("STAT %s: got %q, want 223 0 %s", id, line, id)
	}
	return nil
}

// p430PollStat polls STAT <id> on c every p430Poll until it answers 223 (bounded by testWait),
// then polls p430Stay more times and requires 223 each time: a miss before the commit must
// not be cached. It returns the number of non-223 answers before the first 223.
func p430PollStat(c *e2eClient, id string) (int, error) {
	deadline := time.Now().Add(testWait)
	misses := 0
	var last string
	for {
		code, line, err := p430Stat(c, id)
		if err != nil {
			return misses, err
		}
		if code == 223 {
			break
		}
		if code != 430 {
			return misses, fmt.Errorf("STAT %s: got %q, want 430 or 223", id, line)
		}
		misses++
		last = line
		if time.Now().After(deadline) {
			return misses, fmt.Errorf("STAT %s: no 223 within %s (%d answers, last %q)", id, testWait, misses, last)
		}
		time.Sleep(p430Poll)
	}
	for i := 1; i <= p430Stay; i++ {
		time.Sleep(p430Poll)
		if err := p430Stat223(c, id); err != nil {
			return misses, fmt.Errorf("poll %d after the first 223: %v", i, err)
		}
	}
	return misses, nil
}

// p430Post runs POST with wire on c and requires 240. It returns the time between sending
// the article (with its terminating dot) and reading the reply.
func p430Post(c *e2eClient, wire string) (time.Duration, error) {
	code, line, err := c.tryCmd("POST")
	if err != nil || code != 340 {
		return 0, fmt.Errorf("POST: code=%d line=%q err=%v, want 340", code, line, err)
	}
	start := time.Now()
	if err := c.send(wire); err != nil {
		return 0, err
	}
	code, line, err = c.readStatus()
	took := time.Since(start)
	if err != nil || code != 240 {
		return took, fmt.Errorf("POST article: code=%d line=%q err=%v, want 240", code, line, err)
	}
	return took, nil
}

// p430GroupDBFiles walks dir and returns the paths whose base name starts with one of prefixes.
func p430GroupDBFiles(dir string, prefixes ...string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, p := range prefixes {
			if strings.HasPrefix(d.Name(), p) {
				out = append(out, path)
				break
			}
		}
		return nil
	})
	return out, err
}

// p430Auth opens a new connection to srv and authenticates the posting user.
func p430Auth(t *testing.T, srv *xrefServer) *e2eClient {
	t.Helper()
	c := srv.dial(t)
	if code, line, err := c.tryAuth(p430User, p430Pass); err != nil || code != 281 {
		t.Fatalf("AUTHINFO: code=%d line=%q err=%v\n%s", code, line, err, xrefOutput(srv.out, 30))
	}
	return c
}

func TestE2EPost430Binary(t *testing.T) {
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
		server: filepath.Join(root, "bin", "nntp-server"),
		mgr:    filepath.Join(root, "bin", "nntpmgr"),
		data:   filepath.Join(root, "data"),
		run:    filepath.Join(root, "run"),
	}
	if err := os.MkdirAll(e.run, 0o755); err != nil {
		t.Fatal(err)
	}
	p430Build(t, goTool, filepath.Join(root, "bin"))

	// seed: nntpmgr creates the schema and the posting user, then the two groups by SQL
	if out, err := e.tool(e.mgr, "-data", e.data, "-create", "-username", p430User, "-password", p430Pass, "-posting", "-maxconns", "4"); err != nil {
		t.Fatalf("nntpmgr -create: %v\n%s", err, e2eTail(out, 20))
	}
	mainDB, err := sql.Open("sqlite3", filepath.Join(e.data, "cfg", "pugleaf.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = mainDB.Exec(`INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status,low_water,high_water)
		VALUES(?,'',0,0,1,'post430','y',1,0),(?,'',0,0,1,'post430','y',1,0)`, p430GroupA, p430GroupB)
	if cerr := mainDB.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("seed groups: %v", err)
	}

	r := e2eReport{t: t}
	ga, gb := p430GroupA, p430GroupB
	stamp := time.Now().UnixNano()
	newID := func(n string) string { return fmt.Sprintf("<post430-%s-%d@test.invalid>", n, stamp) }
	idPost, idIHave, idCross, idNoWait := newID("post"), newID("ihave"), newID("cross"), newID("nowait")
	subject := "Post430 e2e post"
	nonexistent := fmt.Sprintf("t5.nonexistent.n%d", stamp)
	testStart := time.Now()

	// ---------------------------------------------------------------- default -post-commit-wait (5s)
	// start() passes -local430ttl 1ms for the xref polls; set the default 60 s back, so a
	// cached 430 would stick for the whole test
	srv := e.start("partial", "-local430ttl", "60s")
	r.check("P01", "default run: startup log has post-commit-wait=5s and local430ttl=1m0s", func() error {
		for _, want := range []string{"post-commit-wait=5s", "local430ttl=1m0s"} {
			if !xrefLogHas(srv.out, want) {
				return fmt.Errorf("startup log has no %q:\n%s", want, xrefOutput(srv.out, 20))
			}
		}
		return nil
	}())

	c := p430Auth(t, srv)
	if !r.check("P02", "POST to "+ga+" 240", func() error {
		took, err := p430Post(c, e2eWire(e2eHead(idPost, ga, subject, e2eNow()), []string{"post430 body"}))
		if err != nil {
			return err
		}
		fmt.Printf("INFO P02 [nntp] POST dot to 240: %s (-post-commit-wait 5s)\n", took.Round(time.Millisecond))
		return nil
	}()) {
		t.Fatalf("server output:\n%s", xrefOutput(srv.out, 40))
	}

	// same connection, no GROUP, no polling: the 240 means the group DB has the row
	r.check("P03", "same connection without GROUP: STAT <id> 223 at once", p430Stat223(c, idPost))
	r.check("P04", "same connection without GROUP: HEAD <id> 221", func() error {
		hdrs, err := xrefHead(c, idPost)
		if err != nil {
			return err
		}
		if v := e2eHeaderValue(hdrs, "Message-ID"); v != idPost {
			return fmt.Errorf("Message-ID %q, want %q", v, idPost)
		}
		return nil
	}())
	r.check("P05", "same connection without GROUP: OVER <id> 224", func() error {
		lines, err := xrefBlock(c, 224, "OVER %s", idPost)
		if err != nil {
			return err
		}
		if len(lines) != 1 || !strings.Contains(lines[0], "\t"+idPost+"\t") {
			return fmt.Errorf("lines %q, want one overview line with %s", lines, idPost)
		}
		return nil
	}())
	r.check("P06", "same connection without GROUP: HDR Subject <id> 225", func() error {
		lines, err := xrefBlock(c, 225, "HDR Subject %s", idPost)
		if err != nil {
			return err
		}
		if len(lines) != 1 || !strings.HasSuffix(lines[0], " "+subject) {
			return fmt.Errorf("lines %q, want one line ending with %q", lines, subject)
		}
		return nil
	}())
	r.check("P07", "same connection without GROUP: XHDR Subject <id> 221", func() error {
		lines, err := xrefBlock(c, 221, "XHDR Subject %s", idPost)
		if err != nil {
			return err
		}
		if len(lines) != 1 || !strings.HasSuffix(lines[0], " "+subject) {
			return fmt.Errorf("lines %q, want one line ending with %q", lines, subject)
		}
		return nil
	}())
	r.check("P08", "new connection without GROUP: STAT <id> 223 at once", func() error {
		c0 := srv.dial(t)
		defer c0.close()
		return p430Stat223(c0, idPost)
	}())

	// IHAVE does not wait for the commit: the reader may see uncached 430s first
	r.check("P10", "IHAVE to "+gb+": 335 then 235", func() error {
		code, line, err := c.tryCmd("IHAVE %s", idIHave)
		if err != nil || code != 335 {
			return fmt.Errorf("IHAVE: code=%d line=%q err=%v, want 335", code, line, err)
		}
		if err := c.send(e2eWire(e2eHead(idIHave, gb, "Post430 e2e ihave", e2eNow()), []string{"post430 ihave"})); err != nil {
			return err
		}
		code, line, err = c.readStatus()
		if err != nil || code != 235 {
			return fmt.Errorf("IHAVE article: code=%d line=%q err=%v, want 235", code, line, err)
		}
		return nil
	}())
	// first STAT before the commit (usually 430: not cached during the arrival grace). The
	// waiting POST of P12 asks the batch writer to flush every pending batch, which commits
	// the IHAVE article too without sitting out BatchInterval; P14 then polls on c1.
	c1 := srv.dial(t)
	r.check("P11", "after IHAVE, new connection: first STAT <id> 430 (uncached) or 223", func() error {
		code, line, err := p430Stat(c1, idIHave)
		if err != nil {
			return err
		}
		if code != 223 && code != 430 {
			return fmt.Errorf("STAT %s: got %q, want 223 or 430", idIHave, line)
		}
		fmt.Printf("INFO P11 [nntp] IHAVE, first STAT: %q\n", line)
		return nil
	}())

	r.check("P12", "POST to "+ga+","+nonexistent+" 240, STAT <id> 223", func() error {
		took, err := p430Post(c, e2eWire(e2eHead(idCross, ga+","+nonexistent, "Post430 e2e unknown group", e2eNow()), []string{"post430 cross"}))
		if err != nil {
			return err
		}
		fmt.Printf("INFO P12 [nntp] POST dot to 240: %s\n", took.Round(time.Millisecond))
		return p430Stat223(c, idCross)
	}())
	r.check("P13", "no group DB for "+nonexistent+" under db/ (the "+ga+" DB is there)", func() error {
		dbDir := filepath.Join(e.data, "db")
		bad, err := p430GroupDBFiles(dbDir, "t5_nonexistent")
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			return fmt.Errorf("unexpected files %q", bad)
		}
		// the group DB path is db/<md5(name)>/<sanitized>.db: the hash directory must not
		// exist either (a regression that only creates the directory would pass the file walk)
		if _, serr := os.Stat(filepath.Join(dbDir, database.MD5Hash(nonexistent))); !errors.Is(serr, fs.ErrNotExist) {
			return fmt.Errorf("hash directory of %s exists or cannot be checked: %v", nonexistent, serr)
		}
		good, err := p430GroupDBFiles(dbDir, "post430_test_a.db")
		if err != nil {
			return err
		}
		if len(good) == 0 {
			return fmt.Errorf("no post430_test_a.db under %s: the walk proves nothing", dbDir)
		}
		return nil
	}())
	r.check("P14", fmt.Sprintf("after IHAVE, same reader connection: STAT <id> reaches 223 and stays 223 (%d more polls)", p430Stay), func() error {
		start := time.Now()
		misses, err := p430PollStat(c1, idIHave)
		if err != nil {
			return err
		}
		fmt.Printf("INFO P14 [nntp] IHAVE: %d more uncached 430 before the first 223, polled %s\n", misses, time.Since(start).Round(time.Millisecond))
		return nil
	}())
	c1.close()
	c.close()
	r.check("P15", "default run: SIGINT, exit 0", srv.stop())

	// ---------------------------------------------------------------- -post-commit-wait 0
	srv = e.start("partial", "-local430ttl", "60s", "-post-commit-wait", "0")
	r.check("P20", "-post-commit-wait 0: startup log has post-commit-wait=0s", func() error {
		if !xrefLogHas(srv.out, "post-commit-wait=0s") {
			return fmt.Errorf("startup log has no %q:\n%s", "post-commit-wait=0s", xrefOutput(srv.out, 20))
		}
		return nil
	}())
	c = p430Auth(t, srv)
	r.check("P21", "-post-commit-wait 0: POST to "+ga+" 240, STAT <id> at once 223 or 430", func() error {
		took, err := p430Post(c, e2eWire(e2eHead(idNoWait, ga, "Post430 e2e no wait", e2eNow()), []string{"post430 nowait"}))
		if err != nil {
			return err
		}
		code, line, err := p430Stat(c, idNoWait)
		if err != nil {
			return err
		}
		if code != 223 && code != 430 {
			return fmt.Errorf("STAT %s: got %q, want 223 or 430", idNoWait, line)
		}
		fmt.Printf("INFO P21 [nntp] POST dot to 240: %s (-post-commit-wait 0), then STAT: %q\n", took.Round(time.Millisecond), line)
		return nil
	}())
	r.check("P22", fmt.Sprintf("-post-commit-wait 0, new connection: STAT <id> reaches 223 and stays 223 (%d more polls)", p430Stay), func() error {
		c0 := srv.dial(t)
		defer c0.close()
		start := time.Now()
		misses, err := p430PollStat(c0, idNoWait)
		if err != nil {
			return err
		}
		fmt.Printf("INFO P22 [nntp] no wait: %d uncached 430 before the first 223, polled %s\n", misses, time.Since(start).Round(time.Millisecond))
		return nil
	}())
	c.close()
	r.check("P23", "-post-commit-wait 0: SIGINT, exit 0", srv.stop())

	// ---------------------------------------------------------------- negative wait
	r.check("P30", "-post-commit-wait -1s exits 1", func() error {
		out, err := e.tool(e.server, "-data", e.data, "-nntphostname", host, "-nntptcpport", "0", "-post-commit-wait", "-1s")
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return fmt.Errorf("err=%v, want exit 1; output:\n%s", err, e2eTail(out, 10))
		}
		if !strings.Contains(out, "-post-commit-wait must not be negative") {
			return fmt.Errorf("output has no usage error:\n%s", e2eTail(out, 10))
		}
		return nil
	}())
	fmt.Printf("INFO P99 [nntp] server runs and checks took %s\n", time.Since(testStart).Round(time.Millisecond))
}
