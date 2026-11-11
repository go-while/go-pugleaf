package nntp

// Upstream-fault tests of plan e2e-gap-tests, slice fetcher-faults: the client half of the
// gap tests G14 (a provider that drops a multi-line reply in the middle, and one that
// answers a reader command with a server fault), G15 (a provider that caps the records of
// an XHDR reply), G17 (MODE READER refused), G18 (hostile TLS) and G19 (NUL and a bare CR
// in a fetched header block). Sources: NewsCache tests/e2e/upstream_test.go
// (TestUpstreamCutOffArticle, TestUpstreamCutOffActive, TestUpstreamArticleErrorNot423,
// TestUpstreamFault403, TestUpstreamOverRangeCap, TestUpstreamConnectRefusals) and
// tests/e2e/tls_test.go.
//
// What the fetcher does with these failures (progress, stored articles) is the processor
// half in internal/processor/proc_DLArt_upstream_test.go.
//
// Every server is a cliStoreServer / newCliFakeServer on 127.0.0.1:0 and never contacts
// any host. There is no wall-clock assertion: cliWait (PUGLEAF_TEST_DEADLINE) and the
// kit's Options.Deadline are hang guards only.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/nntp-testkit/tlscert"
)

// upFaultGroup is the group of these tests; upFaultGroups are the groups of the LIST
// tests (a cut-off active list must not be taken for the whole list).
const upFaultGroup = "upstream.faults"

var upFaultGroups = []string{upFaultGroup, "upstream.faults.two", "upstream.faults.three", "upstream.faults.four"}

// upFaultMsgID is the message-id the kit gives article n of upFaultGroup.
func upFaultMsgID(n int64) string {
	return fmt.Sprintf("<%d.%s@fakeserver.invalid>", n, upFaultGroup)
}

// upFaultStore returns a store with upFaultGroups and three articles in upFaultGroup.
func upFaultStore() *cliFakeStore {
	st := cliFakeNewStore()
	cliQuirkFill(st, upFaultGroup, 1, 3)
	for _, g := range upFaultGroups[1:] {
		st.AddGroup(g, "upstream fault test group")
	}
	return st
}

// upFaultServer starts a store server with the given faults.
func upFaultServer(t *testing.T, f cliFakeFaults) *cliFakeServer {
	t.Helper()
	return cliStoreServer(t, upFaultStore(), cliFakeStoreOpts{}, f)
}

// upFaultBackend is cliBackend without the post-failure sleep (no test here fails a login).
func upFaultBackend(cfg *BackendConfig) {
	cfg.AuthFailureBackoff = -1
}

// upFaultCall is one multi-line reader command of the client, on a bare connection and
// through the pool; both report how many records the call returned next to its error.
type upFaultCall struct {
	name string
	cmd  string // the NNTP command the kit keys the fault on
	conn func(*BackendConn) (int, error)
	pool func(*Pool) (int, error)
}

// upFaultReaders are the multi-line reader commands the fetcher uses: ARTICLE (download),
// XOVER (overview) and LIST (the active file).
func upFaultReaders() []upFaultCall {
	id := upFaultMsgID(1)
	return []upFaultCall{
		{
			name: "article",
			cmd:  "ARTICLE",
			conn: func(c *BackendConn) (int, error) {
				a, err := c.GetArticle(&id, true)
				if a == nil {
					return 0, err
				}
				return 1, err
			},
			pool: func(p *Pool) (int, error) {
				a, err := p.GetArticle(&id, true)
				if a == nil {
					return 0, err
				}
				return 1, err
			},
		},
		{
			name: "xover",
			cmd:  "XOVER",
			conn: func(c *BackendConn) (int, error) {
				ovs, err := c.XOver(upFaultGroup, 1, 3, false)
				return len(ovs), err
			},
			pool: func(p *Pool) (int, error) {
				ovs, err := p.XOver(upFaultGroup, 1, 3, false)
				return len(ovs), err
			},
		},
		{
			name: "list active",
			cmd:  "LIST",
			conn: func(c *BackendConn) (int, error) {
				gs, err := c.ListGroups()
				return len(gs), err
			},
			pool: func(p *Pool) (int, error) {
				gs, err := p.ListNewsgroups()
				return len(gs), err
			},
		},
	}
}

// upFaultWrongErr reports whether err mistakes a cut reply for a missing article or an
// empty range: the sentinel errors a caller acts on instead of retrying.
func upFaultWrongErr(err error) error {
	for _, sentinel := range []error{ErrArticleNotFound, ErrArticleRemoved, ErrNoArticlesInRange, ErrNewsgroupNotFound} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	return nil
}

// G14: the upstream closes the connection in the middle of a multi-line reply (the status
// line was written, the terminating "." never is). The client must return an error with no
// records, and never one of the sentinel errors that would make the caller treat the
// article as missing or the range as empty.
func TestCliUpDropInMultiline(t *testing.T) {
	for _, call := range upFaultReaders() {
		t.Run(call.name, func(t *testing.T) {
			// 2: the status line and one line of the block, then the close
			faults := cliFakeFaults{DropInMultiline: map[string]int{call.cmd: 2}}

			t.Run("conn", func(t *testing.T) {
				s := upFaultServer(t, faults)
				c, err := cliDial(t, s, upFaultBackend)
				if err != nil {
					t.Fatalf("Connect: %v", err)
				}
				var got int
				err = cliQuirkRun(t, call.cmd+" cut off", func() error {
					var e error
					got, e = call.conn(c)
					return e
				})
				if err == nil {
					t.Fatalf("%s returned %d records and no error after the reply was cut", call.cmd, got)
				}
				if got != 0 {
					t.Errorf("%s returned %d records next to the error, want none (no partial data)", call.cmd, got)
				}
				if sent := upFaultWrongErr(err); sent != nil {
					t.Errorf("cut-off %s reported as %v: %v", call.cmd, sent, err)
				}
				if n := s.Count(call.cmd); n != 1 {
					t.Fatalf("%s reached the server %d times, want 1 (the fault must be the reason for the error)", call.cmd, n)
				}
				// the response was never read to its end: the connection is unusable
				err = cliQuirkRun(t, call.cmd+" after the cut", func() error {
					_, e := call.conn(c)
					return e
				})
				if err == nil {
					t.Errorf("%s succeeded on the connection the server closed", call.cmd)
				}
			})

			t.Run("pool", func(t *testing.T) {
				s := upFaultServer(t, faults)
				pool := cliQuirkPool(t, s)
				for i := 1; i <= 2; i++ {
					var got int
					err := cliQuirkRun(t, fmt.Sprintf("pool %s cut off #%d", call.cmd, i), func() error {
						var e error
						got, e = call.pool(pool)
						return e
					})
					if err == nil {
						t.Fatalf("call %d: pool %s returned %d records and no error", i, call.cmd, got)
					}
					if got != 0 {
						t.Errorf("call %d: pool %s returned %d records next to the error", i, call.cmd, got)
					}
					if sent := upFaultWrongErr(err); sent != nil {
						t.Errorf("call %d: cut-off %s reported as %v: %v", i, call.cmd, sent, err)
					}
				}
				// the closed connection is never handed out again: one dial per call,
				// and no storm of retries inside one call
				if got := s.Accepted(); got != 2 {
					t.Errorf("server accepted %d connections for 2 pool calls, want 2 (one fresh dial each)", got)
				}
			})
		})
	}
}

// G14: a cut-off active list must not be written to the newsgroup list cache. The next run
// would read that file instead of asking again (FileCachedListNewsgroups returns a cached
// list without contacting the provider).
func TestCliUpCutOffActiveNotCached(t *testing.T) {
	s := upFaultServer(t, cliFakeFaults{DropInMultiline: map[string]int{"LIST": 3}})
	pool := cliQuirkPool(t, s)
	dir := t.TempDir()
	var groups []string
	err := cliQuirkRun(t, "FileCachedListNewsgroups", func() error {
		var e error
		groups, e = pool.FileCachedListNewsgroups(dir)
		return e
	})
	if err == nil {
		t.Fatalf("FileCachedListNewsgroups returned %d groups and no error after the cut-off LIST", len(groups))
	}
	if len(groups) != 0 {
		t.Errorf("FileCachedListNewsgroups returned %d groups next to the error", len(groups))
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("ReadDir(%s): %v", dir, rerr)
	}
	for _, e := range entries {
		t.Errorf("cache file %q written from a cut-off active list", filepath.Join(dir, e.Name()))
	}
}

// G14: a server fault on a reader command (403, 503) is an error, never "no such article"
// or "no such group". Source: NewsCache TestUpstreamArticleErrorNot423 / TestUpstreamFault403.
func TestCliUpReaderFaultReplies(t *testing.T) {
	const fault403 = "403 archive server temporarily offline"
	const fault503 = "503 program fault - command not performed"

	for _, tc := range []struct {
		name  string
		cmd   string
		reply string
		run   func(*Pool) (int, error)
	}{
		{"article-403", "ARTICLE", fault403, func(p *Pool) (int, error) {
			id := upFaultMsgID(1)
			a, err := p.GetArticle(&id, true)
			if a == nil {
				return 0, err
			}
			return 1, err
		}},
		{"article-503", "ARTICLE", fault503, func(p *Pool) (int, error) {
			id := upFaultMsgID(1)
			a, err := p.GetArticle(&id, true)
			if a == nil {
				return 0, err
			}
			return 1, err
		}},
		{"group-403", "GROUP", fault403, func(p *Pool) (int, error) {
			gi, err := p.SelectGroup(upFaultGroup)
			if gi == nil {
				return 0, err
			}
			return 1, err
		}},
		{"xover-503", "XOVER", fault503, func(p *Pool) (int, error) {
			ovs, err := p.XOver(upFaultGroup, 1, 3, false)
			return len(ovs), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := upFaultServer(t, cliFakeFaults{Override: map[string][]string{tc.cmd: {tc.reply}}})
			pool := cliQuirkPool(t, s)
			var got int
			err := cliQuirkRun(t, tc.cmd+" fault", func() error {
				var e error
				got, e = tc.run(pool)
				return e
			})
			if err == nil {
				t.Fatalf("%s got %q and returned %d records without an error", tc.cmd, tc.reply, got)
			}
			if got != 0 {
				t.Errorf("%s returned %d records next to the error", tc.cmd, got)
			}
			if sent := upFaultWrongErr(err); sent != nil {
				t.Errorf("%s reported as %v: %v", tc.reply, sent, err)
			}
			code, _, _ := strings.Cut(tc.reply, " ")
			if !strings.Contains(err.Error(), code) {
				t.Errorf("error %v does not name the %s status line", err, code)
			}
		})
	}
}

// upFaultStream runs XHdrStreamed over start-end and returns the article numbers it
// streamed. The stream is drained from another goroutine, as every caller does.
func upFaultStream(t *testing.T, c *BackendConn, group string, start, end int64) []int64 {
	t.Helper()
	ch := make(chan HeaderLine, 1024)
	var nums []int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for h := range ch {
			nums = append(nums, h.ArticleNum)
		}
	}()
	err := cliQuirkRun(t, "XHdrStreamed", func() error {
		return c.XHdrStreamed(group, "message-id", start, end, ch, nil)
	})
	<-done
	if err != nil {
		t.Fatalf("XHdrStreamed %d-%d: %v", start, end, err)
	}
	return nums
}

// upFaultWantRun fails unless nums is the unbroken run lo..hi.
func upFaultWantRun(t *testing.T, nums []int64, lo, hi int64) {
	t.Helper()
	if int64(len(nums)) != hi-lo+1 {
		t.Fatalf("XHDR streamed %d numbers, want %d (%d..%d)", len(nums), hi-lo+1, lo, hi)
	}
	for i, got := range nums {
		if want := lo + int64(i); got != want {
			t.Fatalf("number %d of the stream is %d, want %d", i, got, want)
		}
	}
}

// G15: a provider that serves only the first N records of an XHDR range and terminates the
// reply normally. The client must ask again after the last record instead of skipping the
// rest of the range (the fetcher would move its progress past articles it never saw).
// Source: NewsCache TestUpstreamOverRangeCap.
func TestCliUpXHdrRecordCap(t *testing.T) {
	const group = "upstream.cap"

	// A provider with a cap of 100 records answers a 250-number range in three requests.
	t.Run("capped-records", func(t *testing.T) {
		const high = int64(250)
		const recs = 100
		st := cliFakeNewStore()
		st.AddGenerated(group, "capped provider", cliFakeGenerator{Low: 1, High: high})
		so := cliFakeStoreOpts{HdrLines: func(conn int, g, field string, lines []string) []string {
			if len(lines) > recs {
				return lines[:recs]
			}
			return lines
		}}
		s := cliStoreServer(t, st, so, cliFakeFaults{})
		c, err := cliDial(t, s, upFaultBackend)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		upFaultWantRun(t, upFaultStream(t, c, group, 1, high), 1, high)
		if n := s.Count("XHDR"); n != 3 {
			t.Errorf("%d XHDR commands, want 3 (%d records each for %d numbers)", n, recs, high)
		}
	})

	// The articles above 40 are gone while GROUP still reports 60: the re-ask gets the RFC
	// 3977 423 and ends the batch, it does not fail the stream. This is what bounds the
	// re-asking.
	t.Run("trailing-hole", func(t *testing.T) {
		const last = int64(40)
		const high = int64(60)
		st := cliFakeNewStore()
		cliQuirkFill(st, group, 1, last)
		nums := cliFakeGroupNums(func(conn int, g cliFakeGroup) cliFakeGroup {
			g.High = high
			return g
		})
		s := cliStoreServer(t, st, cliFakeStoreOpts{GroupNumbers: nums}, cliFakeFaults{})
		c, err := cliDial(t, s, upFaultBackend)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		upFaultWantRun(t, upFaultStream(t, c, group, 1, high), 1, last)
		if n := s.Count("XHDR"); n != 2 {
			t.Errorf("%d XHDR commands, want 2 (one re-ask that finds the range empty)", n)
		}
	})

	// A reply with gaps is a range whose articles are gone, not a capped reply: the client
	// must not ask again (that would cost a round trip per run and find nothing). Pinned
	// because it is what keeps the re-asking from firing on every ordinary hole.
	t.Run("gaps-are-not-a-cap", func(t *testing.T) {
		const high = int64(20)
		st := cliFakeNewStore()
		cliQuirkFill(st, group, 1, high)
		for _, n := range []int64{3, 9, 14, 15, 16, 17, 18, 19, 20} {
			if !st.RemoveArticle(group, n) {
				t.Fatalf("RemoveArticle(%s, %d) failed", group, n)
			}
		}
		nums := cliFakeGroupNums(func(conn int, g cliFakeGroup) cliFakeGroup {
			g.High = high
			return g
		})
		s := cliStoreServer(t, st, cliFakeStoreOpts{GroupNumbers: nums}, cliFakeFaults{})
		c, err := cliDial(t, s, upFaultBackend)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		got := upFaultStream(t, c, group, 1, high)
		want := []int64{1, 2, 4, 5, 6, 7, 8, 10, 11, 12, 13}
		if len(got) != len(want) {
			t.Fatalf("XHDR streamed %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("XHDR streamed %v, want %v", got, want)
			}
		}
		if n := s.Count("XHDR"); n != 1 {
			t.Errorf("%d XHDR commands, want 1 (a hole is not a record cap)", n)
		}
	})
}

// G17: a provider that refuses MODE READER (503, 500). The client must report a clean
// error, and the pool must dial exactly once per Get: no reconnect storm.
func TestCliUpModeReaderRefused(t *testing.T) {
	for _, reply := range []string{"503 program fault - command not performed", "500 unknown command"} {
		code, _, _ := strings.Cut(reply, " ")
		t.Run(code, func(t *testing.T) {
			faults := cliFakeFaults{OverrideLine: map[string][]string{"MODE READER": {reply}}}

			t.Run("conn", func(t *testing.T) {
				s := upFaultServer(t, faults)
				// Connect itself sends no MODE READER: the greeting is answered
				c, err := cliDial(t, s, upFaultBackend)
				if err != nil {
					t.Fatalf("Connect: %v", err)
				}
				err = cliQuirkRun(t, "SwitchToModeReader", c.SwitchToModeReader)
				if err == nil {
					t.Fatalf("SwitchToModeReader accepted %q", reply)
				}
				if !strings.Contains(err.Error(), code) {
					t.Errorf("error %v does not name the %s reply", err, code)
				}
				if c.ModeReader {
					t.Error("ModeReader is set although MODE READER was refused")
				}
			})

			t.Run("pool", func(t *testing.T) {
				s := upFaultServer(t, faults)
				pool := cliQuirkPool(t, s)
				const gets = 3
				for i := 1; i <= gets; i++ {
					var conn *BackendConn
					err := cliQuirkRun(t, fmt.Sprintf("Get #%d", i), func() error {
						var e error
						conn, e = pool.Get(MODE_READER_MV)
						return e
					})
					if err == nil {
						_ = pool.Put(conn)
						t.Fatalf("Get #%d returned a connection although MODE READER is refused", i)
					}
					if !strings.Contains(err.Error(), code) {
						t.Errorf("Get #%d error %v does not name the %s reply", i, err, code)
					}
				}
				// one dial per Get: the refusal is not retried on new connections
				if got := s.Accepted(); got != gets {
					t.Errorf("server accepted %d connections for %d Gets, want %d (no reconnect storm)", got, gets, gets)
				}
				// a reader command through the pool fails with the same refusal
				var ovs []OverviewLine
				err := cliQuirkRun(t, "XOver", func() error {
					var e error
					ovs, e = pool.XOver(upFaultGroup, 1, 3, false)
					return e
				})
				if err == nil {
					t.Fatalf("Pool.XOver returned %d lines although MODE READER is refused", len(ovs))
				}
				if got := s.Accepted(); got != gets+1 {
					t.Errorf("server accepted %d connections after the XOver, want %d", got, gets+1)
				}
			})
		})
	}
}

// G18: hostile upstream TLS: a certificate for another host, an expired certificate and a
// server that closes right after the handshake. Connect must fail cleanly and promptly
// (cliWait is the hang guard) and the pool must dial once per Get.
//
// The client builds its own tls.Config (nntp-client.go: ServerName + MinVersion, the system
// root pool) and has no hook for custom roots or for skipping verification, so all three
// servers fail at certificate verification and the test cannot tell the reasons apart: it
// pins the clean failure, not the reason. Same situation as TestCliConnTLSUnknownCA.
func TestCliUpTLSHostile(t *testing.T) {
	ca := tlscert.NewCA(t)
	local := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	for _, tc := range []struct {
		name  string
		leaf  *tlscert.Leaf
		close bool // close the connection right after the handshake
	}{
		{"wrong-name", ca.Issue(t, tlscert.Options{CommonName: "elsewhere.invalid", DNSNames: []string{"elsewhere.invalid"}}), false},
		{"expired", ca.Issue(t, tlscert.Options{Expired: true, IPs: local}), false},
		{"closed-after-handshake", ca.Issue(t, tlscert.Options{IPs: local}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := cliFakeOpts{TLS: tc.leaf.ServerConfig()}
			if tc.close {
				o.Handler = func(c *cliFakeConn) {} // returning closes the connection
			}
			s := newCliFakeServer(t, o)
			cfg := cliBackend(s)
			cfg.SSL = true
			upFaultBackend(cfg)
			_, _, err := cliConnConnect(t, cfg)
			if err == nil {
				t.Fatal("Connect accepted the hostile TLS server")
			}
			if cliConnIsTimeout(err) {
				t.Errorf("Connect error %v is a timeout, want a handshake failure", err)
			}

			// the pool reports the same failure and dials once per Get
			pool := cliConnPool(t, cfg)
			const gets = 2
			for i := 1; i <= gets; i++ {
				var conn *BackendConn
				gerr := cliQuirkRun(t, fmt.Sprintf("Get #%d", i), func() error {
					var e error
					conn, e = pool.Get(MODE_READER_MV)
					return e
				})
				if gerr == nil {
					_ = pool.Put(conn)
					t.Fatalf("Get #%d succeeded against the hostile TLS server", i)
				}
			}
			if got := s.Accepted(); got != 1+gets {
				t.Errorf("server accepted %d connections for 1 Connect and %d Gets, want %d", got, gets, 1+gets)
			}
		})
	}
}

// G19: a fetched article whose header block holds a bare CR and a NUL octet. The client
// stores both verbatim (nothing is turned into a space or dropped); the fetch-mode header
// check is what refuses such an article (common.CheckHeaderBlock rule 1, in both modes),
// so nothing reaches the database. Source: NewsCache TestUpstreamArticleNULAndCR.
func TestCliUpArticleNULAndCR(t *testing.T) {
	const id = "<nulcr.1.upstream.faults@fakeserver.invalid>"
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"bare-cr", "Subject: before\rafter"},
		{"nul", "Subject: before\x00after"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := cliFakeNewStore()
			st.AddGroup(upFaultGroup, "upstream fault test group")
			st.AddArticle(upFaultGroup, cliFakeArticle{
				Number:    1,
				MessageID: id,
				Headers:   []string{"From: tester@upstream.invalid", tc.header},
				Body:      []string{"body"},
			})
			s := cliStoreServer(t, st, cliFakeStoreOpts{}, cliFakeFaults{})
			c, err := cliDial(t, s, upFaultBackend)
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			msgID := id
			var art *models.Article
			err = cliQuirkRun(t, "GetArticle", func() error {
				var e error
				art, e = c.GetArticle(&msgID, true)
				return e
			})
			// pinned: the client reads the article, defect octets and all
			if err != nil {
				t.Fatalf("GetArticle: %v", err)
			}
			if art == nil {
				t.Fatal("GetArticle returned no article and no error")
			}
			if !strings.Contains(art.HeadersJSON, tc.header) {
				t.Errorf("the stored header block does not hold %q verbatim: %q", tc.header, art.HeadersJSON)
			}
			if want := strings.TrimPrefix(tc.header, "Subject: "); art.Subject != want {
				t.Errorf("Subject = %q, want %q (the client normalizes nothing)", art.Subject, want)
			}
			// the fetch-mode check refuses the block: the fetcher stores nothing
			// (internal/processor/proc_DLArt_upstream_test.go runs the same case through
			// DownloadArticles)
			_, cerr := common.CheckHeaderBlock(strings.Split(art.HeadersJSON, "\n"), common.HeaderBlockFetch)
			if !errors.Is(cerr, common.ErrIntakeRejected) {
				t.Errorf("CheckHeaderBlock(fetch) = %v, want ErrIntakeRejected for %q", cerr, tc.header)
			}
		})
	}
}
