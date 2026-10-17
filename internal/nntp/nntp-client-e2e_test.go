//go:build e2e

package nntp

// End-to-end helpers for plan nntp-client-transfer, driven by
// scripts/test-nntp-client-transfer.sh (build tag e2e). Each test skips
// unless its environment is set. Result lines go to stdout with fmt.Println
// ("PASS|FAIL|INFO <id> ..."): TestMain discards the log package's output and
// t.Log output is indented, so neither would match the script's greps.
//
//	TestCliE2EFakePeer: the fake streaming peer (or a silent one) the transfer talks to
//	TestCliE2ESeed:     POSTs the three test articles to the scratch nntp-server
//	TestCliE2EVerify:   checks what the peer received (E03, E04, E05, E15; phase 2: E07b)

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
)

// cliE2EPass/cliE2EFail print one result line; cliE2EFail also fails t.
func cliE2EPass(id, tag, text string) {
	fmt.Println(fmt.Sprintf("PASS %s [%s] %s", id, tag, text))
}

func cliE2EFail(t *testing.T, id, tag, text, detail string) {
	t.Helper()
	fmt.Println(fmt.Sprintf("FAIL %s [%s] %s :: %s", id, tag, text, detail))
	t.Errorf("%s %s: %s", id, text, detail)
}

// cliE2EWireName is the file the peer writes for a TAKETHIS payload.
func cliE2EWireName(dir, msgID string) string {
	return filepath.Join(dir, hex.EncodeToString([]byte(msgID))+".wire")
}

// cliE2ELineLog appends the server's new client lines to commands.log, one
// %q-quoted line each.
type cliE2ELineLog struct {
	mu   sync.Mutex
	path string
	srv  *cliFakeServer
	n    int
}

func (l *cliE2ELineLog) setServer(s *cliFakeServer) {
	l.mu.Lock()
	l.srv = s
	l.mu.Unlock()
}

func (l *cliE2ELineLog) flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.srv == nil {
		return nil
	}
	lines := l.srv.Lines()
	if len(lines) <= l.n {
		return nil
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, line := range lines[l.n:] {
		fmt.Fprintf(&b, "%q\n", line)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	l.n = len(lines)
	return f.Close()
}

func TestCliE2EFakePeer(t *testing.T) {
	addr := os.Getenv("PUGLEAF_E2E_PEER_ADDR")
	dir := os.Getenv("PUGLEAF_E2E_PEER_DIR")
	if addr == "" || dir == "" {
		t.Skip("PUGLEAF_E2E_PEER_ADDR/PUGLEAF_E2E_PEER_DIR not set (run scripts/test-nntp-client-transfer.sh)")
	}
	mode := os.Getenv("PUGLEAF_E2E_PEER_MODE")
	if mode == "" {
		mode = "peer"
	}
	lineLog := &cliE2ELineLog{path: filepath.Join(dir, "commands.log")}
	if err := os.WriteFile(lineLog.path, nil, 0o644); err != nil {
		t.Fatalf("create commands.log: %v", err)
	}
	o := cliFakeOpts{Addr: addr}
	switch mode {
	case "silent":
		o.RawStall = true
	case "peer":
		var stall time.Duration
		if v := os.Getenv("PUGLEAF_E2E_PEER_STALL"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				t.Fatalf("PUGLEAF_E2E_PEER_STALL: %v", err)
			}
			stall = d
		}
		defer431 := make(map[string]bool)
		for _, id := range strings.Split(os.Getenv("PUGLEAF_E2E_PEER_431"), ",") {
			if id = strings.TrimSpace(id); id != "" {
				defer431[id] = true
			}
		}
		o.Handler = cliPeerHandler(&cliPeerOpts{
			User:            os.Getenv("PUGLEAF_E2E_PEER_USER"),
			Pass:            os.Getenv("PUGLEAF_E2E_PEER_PASS"),
			Have:            make(map[string]bool),
			Defer431Once:    defer431,
			StallModeStream: stall,
			OnArticle: func(msgID string, wire []string) {
				// the TAKETHIS line is already recorded: flush it before the 239 goes out
				if err := lineLog.flush(); err != nil {
					t.Errorf("commands.log: %v", err)
				}
				if err := os.WriteFile(cliE2EWireName(dir, msgID), []byte(strings.Join(wire, "\r\n")), 0o644); err != nil {
					t.Errorf("write wire file for %s: %v", msgID, err)
				}
			},
		})
	default:
		t.Fatalf("PUGLEAF_E2E_PEER_MODE %q: want peer or silent", mode)
	}
	s := newCliFakeServer(t, o)
	lineLog.setServer(s)
	if err := os.WriteFile(filepath.Join(dir, "ready"), []byte(net.JoinHostPort(s.Host(), strconv.Itoa(s.Port()))+"\n"), 0o644); err != nil {
		t.Fatalf("write ready: %v", err)
	}
	fmt.Println(fmt.Sprintf("INFO peer %s listening on %s:%d", mode, s.Host(), s.Port()))
	stop := filepath.Join(dir, "STOP")
	deadline := time.Now().Add(14 * time.Minute) // below the script's go test -timeout 15m
	for time.Now().Before(deadline) {
		if err := lineLog.flush(); err != nil {
			t.Errorf("commands.log: %v", err)
		}
		if _, err := os.Stat(stop); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.Close()
	if err := lineLog.flush(); err != nil {
		t.Errorf("commands.log: %v", err)
	}
	fmt.Println(fmt.Sprintf("INFO peer %s stopped: accepted=%d articles=%d mismatches=%d", mode, s.Accepted(), len(s.Articles()), len(s.Mismatches())))
}

// cliE2ESeedArticle is one article POSTed by TestCliE2ESeed.
type cliE2ESeedArticle struct {
	id   string
	head []string
	body []string
}

func cliE2ESeedArticles(date string) []cliE2ESeedArticle {
	return []cliE2ESeedArticle{
		{
			id: "<ct-1@e2e.invalid>",
			head: []string{
				"Path: poster.e2e.invalid!not-for-mail",
				"From: E2E Poster <poster@e2e.invalid>",
				"Newsgroups: e2e.ct.a,",
				" e2e.ct.b",
				"Subject: ct one",
				"Date: " + date,
				"Message-ID: <ct-1@e2e.invalid>",
				"Organization: E2E",
			},
			body: []string{"line1", ".dotline", ""},
		},
		{
			id: "<ct-2@e2e.invalid>",
			head: []string{
				"Path: poster.e2e.invalid!not-for-mail",
				"From: E2E Poster <poster@e2e.invalid>",
				"Newsgroups: e2e.ct.a",
				"Subject: ct two",
				"Date: " + date,
				"Message-ID: <ct-2@e2e.invalid>",
			},
			body: []string{"two"},
		},
		{
			id: "<ct-3@e2e.invalid>",
			head: []string{
				"Message-ID: <ct-3@e2e.invalid>",
				"Date: " + date,
				"Subject: ct three",
				"From: E2E Poster <poster@e2e.invalid>",
				"X-Custom: keep",
				"Newsgroups: e2e.ct.a",
				"Path: poster.e2e.invalid!not-for-mail",
			},
			body: []string{"three"},
		},
	}
}

func TestCliE2ESeed(t *testing.T) {
	addr := os.Getenv("PUGLEAF_E2E_ADDR")
	if addr == "" {
		t.Skip("PUGLEAF_E2E_ADDR not set (run scripts/test-nntp-client-transfer.sh)")
	}
	user, pass := os.Getenv("PUGLEAF_E2E_USER"), os.Getenv("PUGLEAF_E2E_PASS")
	nc, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	_ = nc.SetDeadline(time.Now().Add(60 * time.Second))
	tc := textproto.NewConn(nc)
	defer tc.Close()
	if code, msg, err := tc.ReadCodeLine(200); err != nil {
		t.Fatalf("greeting: %d %s: %v", code, msg, err)
	}
	cmd := func(want int, format string, args ...any) (int, string, error) {
		if err := tc.PrintfLine(format, args...); err != nil {
			return 0, "", err
		}
		return tc.ReadCodeLine(want)
	}
	if user != "" {
		if code, msg, err := cmd(381, "AUTHINFO USER %s", user); err != nil {
			t.Fatalf("AUTHINFO USER: %d %s: %v", code, msg, err)
		}
		if code, msg, err := cmd(281, "AUTHINFO PASS %s", pass); err != nil {
			t.Fatalf("AUTHINFO PASS: %d %s: %v", code, msg, err)
		}
	}
	date := time.Now().Format(time.RFC1123Z)
	for i, a := range cliE2ESeedArticles(date) {
		id := fmt.Sprintf("S%02d", i+1)
		err := func() error {
			if code, msg, err := cmd(340, "POST"); err != nil {
				return fmt.Errorf("POST: %d %s: %v", code, msg, err)
			}
			lines := append(append(append([]string{}, a.head...), ""), a.body...)
			for _, l := range lines {
				if strings.HasPrefix(l, ".") {
					l = "." + l
				}
				if err := tc.PrintfLine("%s", l); err != nil {
					return err
				}
			}
			if err := tc.PrintfLine("."); err != nil {
				return err
			}
			if code, msg, err := tc.ReadCodeLine(240); err != nil {
				return fmt.Errorf("after article: %d %s: %v", code, msg, err)
			}
			return nil
		}()
		if err != nil {
			fmt.Println(fmt.Sprintf("FAIL %s POST %s :: %v", id, a.id, err))
			t.Errorf("%s POST %s: %v", id, a.id, err)
			continue
		}
		fmt.Println(fmt.Sprintf("PASS %s POST %s 240", id, a.id))
	}
	_, _, _ = cmd(205, "QUIT")
}

// cliE2EDropFields removes the named header fields (case-insensitive) with
// their continuation lines.
func cliE2EDropFields(lines []string, names ...string) []string {
	out := []string{}
	skipping := false
	for _, l := range lines {
		if l != "" && (l[0] == ' ' || l[0] == '\t') {
			if !skipping {
				out = append(out, l)
			}
			continue
		}
		skipping = false
		name := l
		if i := strings.IndexByte(l, ':'); i >= 0 {
			name = l[:i]
		}
		for _, n := range names {
			if strings.EqualFold(strings.TrimSpace(name), n) {
				skipping = true
			}
		}
		if !skipping {
			out = append(out, l)
		}
	}
	return out
}

// cliE2EReadWire returns the wire lines of a .wire file split into head and body.
func cliE2EReadWire(path string) (head, body []string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(string(b), "\r\n")
	for i, l := range lines {
		if l == "" {
			return lines[:i], lines[i+1:], nil
		}
	}
	return lines, nil, nil
}

func TestCliE2EVerify(t *testing.T) {
	dir := os.Getenv("PUGLEAF_E2E_PEER_DIR")
	phase := os.Getenv("PUGLEAF_E2E_PHASE")
	if dir == "" || phase == "" {
		t.Skip("PUGLEAF_E2E_PEER_DIR/PUGLEAF_E2E_PHASE not set (run scripts/test-nntp-client-transfer.sh)")
	}
	switch phase {
	case "1":
		cliE2EVerifyPhase1(t, dir)
	case "2":
		id := "<ct-2@e2e.invalid>"
		if _, err := os.Stat(cliE2EWireName(dir, id)); err == nil {
			cliE2EPass("E07b", "cli-tools", "peer now holds "+id)
		} else {
			cliE2EFail(t, "E07b", "cli-tools", "peer now holds "+id, err.Error())
		}
	default:
		t.Fatalf("PUGLEAF_E2E_PHASE %q: want 1 or 2", phase)
	}
}

func cliE2EVerifyPhase1(t *testing.T, dir string) {
	host := os.Getenv("PUGLEAF_E2E_LOCALHOST")
	gdb := os.Getenv("PUGLEAF_E2E_GROUPDB")
	wires, err := filepath.Glob(filepath.Join(dir, "*.wire"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(wires)

	// E03: exactly one Path line, prefixed once with the local host, no .TX
	func() {
		const text = "one Path line with the local identity once"
		if host == "" {
			cliE2EFail(t, "E03", "cli-commands", text, "PUGLEAF_E2E_LOCALHOST not set")
			return
		}
		if len(wires) == 0 {
			cliE2EFail(t, "E03", "cli-commands", text, "no .wire files (peer received no article)")
			return
		}
		var bad []string
		for _, w := range wires {
			head, _, err := cliE2EReadWire(w)
			if err != nil {
				bad = append(bad, err.Error())
				continue
			}
			var paths []string
			for _, l := range head {
				if strings.HasPrefix(strings.ToLower(l), "path:") {
					paths = append(paths, l)
				}
			}
			switch {
			case len(paths) != 1:
				bad = append(bad, fmt.Sprintf("%s: %d Path lines %q", filepath.Base(w), len(paths), paths))
			case !strings.HasPrefix(paths[0], "Path: "+host+"!"):
				bad = append(bad, fmt.Sprintf("%s: %q", filepath.Base(w), paths[0]))
			case strings.Count(paths[0], host+"!") != 1:
				bad = append(bad, fmt.Sprintf("%s: %s! repeated in %q", filepath.Base(w), host, paths[0]))
			case strings.Contains(paths[0], "!.TX!"):
				bad = append(bad, fmt.Sprintf("%s: .TX in %q", filepath.Base(w), paths[0]))
			}
		}
		if len(bad) > 0 {
			cliE2EFail(t, "E03", "cli-commands", text, strings.Join(bad, "; "))
			return
		}
		cliE2EPass("E03", "cli-commands", fmt.Sprintf("%s (%d articles)", text, len(wires)))
	}()

	// E04: relayed header block equals the stored one minus Path and Xref
	func() {
		const text = "header block relayed as stored (minus Path/Xref), ct-1 continuation once"
		if gdb == "" {
			cliE2EFail(t, "E04", "cli-commands", text, "PUGLEAF_E2E_GROUPDB not set")
			return
		}
		db, err := sql.Open("sqlite3", "file:"+gdb+"?mode=ro")
		if err != nil {
			cliE2EFail(t, "E04", "cli-commands", text, err.Error())
			return
		}
		defer db.Close()
		var bad []string
		for _, id := range []string{"<ct-1@e2e.invalid>", "<ct-3@e2e.invalid>"} {
			var stored sql.NullString
			if err := db.QueryRow("SELECT headers_json FROM articles WHERE message_id = ?", id).Scan(&stored); err != nil {
				bad = append(bad, fmt.Sprintf("%s: stored row: %v", id, err))
				continue
			}
			want := strings.Split(stored.String, "\n")
			for i := range want {
				want[i] = strings.TrimSuffix(want[i], "\r")
			}
			for len(want) > 0 && want[len(want)-1] == "" {
				want = want[:len(want)-1]
			}
			want = cliE2EDropFields(want, "Path", "Xref")
			head, _, err := cliE2EReadWire(cliE2EWireName(dir, id))
			if err != nil {
				bad = append(bad, fmt.Sprintf("%s: %v", id, err))
				continue
			}
			got := cliE2EDropFields(cliUnstuff(head), "Path")
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				bad = append(bad, fmt.Sprintf("%s: wire %q != stored %q", id, got, want))
				continue
			}
			if id == "<ct-1@e2e.invalid>" {
				n := 0
				for _, l := range head {
					if l == " e2e.ct.b" {
						n++
					}
				}
				if n != 1 {
					bad = append(bad, fmt.Sprintf("%s: continuation \" e2e.ct.b\" %d times", id, n))
				}
			}
		}
		if len(bad) > 0 {
			cliE2EFail(t, "E04", "cli-commands", text, strings.Join(bad, "; "))
			return
		}
		cliE2EPass("E04", "cli-commands", text)
	}()

	// E05: ct-1's body is sent byte-exact and dot-stuffed
	func() {
		const text = "ct-1 body on the wire is line1, ..dotline, empty line"
		_, body, err := cliE2EReadWire(cliE2EWireName(dir, "<ct-1@e2e.invalid>"))
		if err != nil {
			cliE2EFail(t, "E05", "cli-commands", text, err.Error())
			return
		}
		want := []string{"line1", "..dotline", ""}
		if strings.Join(body, "\x00") != strings.Join(want, "\x00") || len(body) != len(want) {
			cliE2EFail(t, "E05", "cli-commands", text, fmt.Sprintf("got %q", body))
			return
		}
		cliE2EPass("E05", "cli-commands", text)
	}()

	// E15: CHECK/TAKETHIS arguments are valid message-ids; no CR/NUL in any line
	func() {
		const text = "CHECK/TAKETHIS arguments are valid message-ids, no CR/NUL on the wire"
		b, err := os.ReadFile(filepath.Join(dir, "commands.log"))
		if err != nil {
			cliE2EFail(t, "E15", "cli-hygiene", text, err.Error())
			return
		}
		var bad []string
		n := 0
		for i, q := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			if q == "" {
				continue
			}
			line, err := strconv.Unquote(q)
			if err != nil {
				bad = append(bad, fmt.Sprintf("line %d: unquote %s: %v", i+1, q, err))
				continue
			}
			if strings.ContainsAny(line, "\r\x00") {
				bad = append(bad, fmt.Sprintf("line %d: CR/NUL in %q", i+1, line))
			}
			verb, arg, _ := strings.Cut(line, " ")
			if v := strings.ToUpper(verb); v == "CHECK" || v == "TAKETHIS" {
				n++
				if !common.ValidMessageID(arg) {
					bad = append(bad, fmt.Sprintf("line %d: invalid message-id in %q", i+1, line))
				}
			}
		}
		if len(bad) > 0 {
			cliE2EFail(t, "E15", "cli-hygiene", text, strings.Join(bad, "; "))
			return
		}
		cliE2EPass("E15", "cli-hygiene", fmt.Sprintf("%s (%d commands checked)", text, n))
	}()
}
