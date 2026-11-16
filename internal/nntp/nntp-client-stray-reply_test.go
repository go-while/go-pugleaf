package nntp

// Stray reply lines (plan client-stray-reply-lines; cross-project finding of NewsCache PR #381):
// a server answers a command with a one-line status the pool treats as healthy (430, 411, 423)
// and then sends lines that no such reply has. Pool.Put must not hand that connection out
// again: the next command would read the leftover lines as its own reply and every later reply
// on the connection would be off by one. Every server is a cliStoreServer on 127.0.0.1:0; the
// fault applies once per connection (Faults.OverrideTimes), so a fresh connection is clean.
// No wall-clock assertion: cliWait and the kit's Options.Deadline are hang guards only.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-while/nntp-testkit/fakeserver"
)

const strayGroup = "stray.reply"

// strayMsgID is the message-id the kit gives article n of strayGroup.
func strayMsgID(n int64) string {
	return fmt.Sprintf("<%d.%s@fakeserver.invalid>", n, strayGroup)
}

// strayServer starts a store server with three articles in strayGroup whose first reply to cmd
// on each connection is lines.
func strayServer(t *testing.T, cmd string, lines []string) *cliFakeServer {
	t.Helper()
	st := cliFakeNewStore()
	cliQuirkFill(st, strayGroup, 1, 3)
	f := cliFakeFaults{
		Override:      map[string][]string{cmd: lines},
		OverrideTimes: map[string]int{cmd: 1},
	}
	return cliStoreServer(t, st, cliFakeStoreOpts{}, f)
}

// strayPool is a pool of one connection to s, so a reused connection is the same one.
func strayPool(t *testing.T, s *cliFakeServer) *Pool {
	t.Helper()
	return cliCmdPool(t, s, func(cfg *BackendConfig) { cfg.MaxConns = 1 })
}

// strayWantGroup checks that the next command through the pool gets a correct GROUP reply.
func strayWantGroup(t *testing.T, pool *Pool) {
	t.Helper()
	gi, err := pool.SelectGroup(strayGroup)
	if err != nil {
		t.Fatalf("GROUP after the stray lines: %v (the connection read the leftover lines)", err)
	}
	if gi == nil || gi.Count != 3 || gi.First != 1 || gi.Last != 3 {
		t.Fatalf("GROUP after the stray lines: %+v, want 3 articles 1-3", gi)
	}
}

// strayWantNewConn checks that the pool dropped the dirty connection and dialled a new one.
func strayWantNewConn(t *testing.T, pool *Pool, wantCreated int64) {
	t.Helper()
	if got := pool.Stats().TotalCreated; got != wantCreated {
		t.Fatalf("pool created %d connections, want %d", got, wantCreated)
	}
}

// 430 with a block after it (the kit's reply corpus case article-430-block).
func TestCliStrayReplyArticle430Block(t *testing.T) {
	rc, ok := fakeserver.ReplyCaseByName("article-430-block")
	if !ok {
		t.Fatal("reply corpus has no article-430-block")
	}
	s := strayServer(t, rc.Cmd, rc.Lines)
	pool := strayPool(t, s)

	id := strayMsgID(1)
	if _, err := pool.GetArticle(&id, false); !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("ARTICLE with the 430 block: err %v, want ErrArticleNotFound", err)
	}
	strayWantGroup(t, pool)
	strayWantNewConn(t, pool, 2)
}

// 411 with a block after it.
func TestCliStrayReplyGroup411Block(t *testing.T) {
	s := strayServer(t, "GROUP", []string{"411 no such group", "stray line", "."})
	pool := strayPool(t, s)

	if _, err := pool.SelectGroup(strayGroup); !errors.Is(err, ErrNewsgroupNotFound) {
		t.Fatalf("GROUP with the 411 block: err %v, want ErrNewsgroupNotFound", err)
	}
	id := strayMsgID(2)
	art, err := pool.GetArticle(&id, false)
	if err != nil || art == nil {
		t.Fatalf("ARTICLE after the stray lines: %v (the connection read the leftover lines)", err)
	}
	if art.MessageID != id {
		t.Fatalf("ARTICLE after the stray lines: got %q, want %q", art.MessageID, id)
	}
	strayWantNewConn(t, pool, 2)
}

// 423 to XOVER with a block after it.
func TestCliStrayReplyXOver423Block(t *testing.T) {
	s := strayServer(t, "XOVER", []string{"423 no articles in that range", "stray line", "."})
	pool := strayPool(t, s)

	if _, err := pool.XOver(strayGroup, 10, 20, false); !errors.Is(err, ErrNoArticlesInRange) {
		t.Fatalf("XOVER with the 423 block: err %v, want ErrNoArticlesInRange", err)
	}
	strayWantGroup(t, pool)
	strayWantNewConn(t, pool, 2)
}

// 423 to XHDR with a block after it (the streamed path gives the connection back in streamXHdr).
func TestCliStrayReplyXHdr423Block(t *testing.T) {
	s := strayServer(t, "XHDR", []string{"423 no articles in that range", "stray line", "."})
	pool := strayPool(t, s)

	ch := make(chan HeaderLine, 16)
	done := make(chan []HeaderLine, 1)
	go func() {
		var got []HeaderLine
		for h := range ch {
			got = append(got, h)
		}
		done <- got
	}()
	if err := pool.XHdrStreamedWait(strayGroup, "Subject", 2, 3, ch, nil); !errors.Is(err, ErrNoArticlesInRange) {
		t.Fatalf("XHDR with the 423 block: err %v, want ErrNoArticlesInRange", err)
	}
	<-done
	strayWantGroup(t, pool)
	strayWantNewConn(t, pool, 2)
}

// Clean replies keep the connection: no false positive from the check.
func TestCliStrayReplyCleanKeepsConn(t *testing.T) {
	s := strayServer(t, "HELP", []string{"100 help", "."}) // never sent
	pool := strayPool(t, s)

	id := strayMsgID(3)
	if _, err := pool.GetArticle(&id, false); err != nil {
		t.Fatalf("ARTICLE: %v", err)
	}
	missing := "<missing@stray.invalid>"
	if _, err := pool.GetArticle(&missing, false); !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("ARTICLE of a missing id: err %v, want ErrArticleNotFound", err)
	}
	if _, err := pool.XOver(strayGroup, 10, 20, false); !errors.Is(err, ErrNoArticlesInRange) {
		t.Fatalf("XOVER past the end: err %v, want ErrNoArticlesInRange", err)
	}
	strayWantGroup(t, pool)
	strayWantNewConn(t, pool, 1)
}

// The same 430 block on a COMPRESS DEFLATE connection: the leftover octets may sit in the
// textproto reader or, inflated but not read yet, in the deflate layer; both count.
func TestCliStrayReplyArticle430BlockCompressed(t *testing.T) {
	rc, ok := fakeserver.ReplyCaseByName("article-430-block")
	if !ok {
		t.Fatal("reply corpus has no article-430-block")
	}
	s := cliCompServer(t, 3, cliFakeFaults{
		Override:      map[string][]string{rc.Cmd: rc.Lines},
		OverrideTimes: map[string]int{rc.Cmd: 1},
	})
	pool := cliCmdPool(t, s, func(cfg *BackendConfig) {
		cfg.MaxConns = 1
		cfg.Compress = true
	})

	id := cliCompMsgID(1)
	if _, err := pool.GetArticle(&id, false); !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("ARTICLE with the 430 block: err %v, want ErrArticleNotFound", err)
	}
	gi, err := pool.SelectGroup(cliCompGroup)
	if err != nil {
		t.Fatalf("GROUP after the stray lines: %v (the connection read the leftover lines)", err)
	}
	if gi == nil || gi.Count != 3 {
		t.Fatalf("GROUP after the stray lines: %+v, want 3 articles", gi)
	}
	strayWantNewConn(t, pool, 2)
}
