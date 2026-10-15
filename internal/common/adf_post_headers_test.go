package common

import (
	"strings"
	"testing"
)

// audit-delta-fixes (D8 follow-up): a web post stores Injection-Info and Injection-Date in
// its header block. The POST path (withPath == false) must drop both so the remote injecting
// agent does not reject the proto-article; relays (withPath == true) keep them.
func TestAdfPostHeadersDropInjectionOnPost(t *testing.T) {
	host := cliHdrHost
	block := cliHdrLines(
		"Newsgroups: a.b",
		"Injection-Date: Wed, 15 Oct 2026 10:00:00 +0000",
		"Injection-Info: news.test.invalid;",
		" posting-account=\"deadbeef\";",
		" logging-data=\"1760522400\"",
		"X-Custom: keep",
	)
	a := cliHdrArticle(block, "poster!not-for-mail")

	post, err := ReconstructHeaders(a, false, &host, "")
	if err != nil {
		t.Fatalf("withPath=false: %v", err)
	}
	joined := strings.Join(post, "\n")
	for _, bad := range []string{"Injection-Info:", "Injection-Date:", "posting-account=", "logging-data="} {
		if strings.Contains(joined, bad) {
			t.Errorf("POST headers contain %q: %q", bad, post)
		}
	}
	if got := cliHdrWithPrefix(post, "X-Custom:"); len(got) != 1 {
		t.Errorf("POST headers X-Custom lines = %q, want one", got)
	}

	relay, err := ReconstructHeaders(a, true, &host, "")
	if err != nil {
		t.Fatalf("withPath=true: %v", err)
	}
	joined = strings.Join(relay, "\n")
	for _, want := range []string{"Injection-Info: news.test.invalid;", " posting-account=\"deadbeef\";", "Injection-Date:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("relay headers lack %q: %q", want, relay)
		}
	}
}
