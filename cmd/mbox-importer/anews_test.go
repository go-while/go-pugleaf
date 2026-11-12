package main

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// anewsBatch is the start of the utzoo file news001f1/a13 (two A-news articles, one after
// the other). The first article is verbatim; of the second, the plan quotes the four header
// lines and marks title and body as "follow", so a title and a body line stand in for them.
const anewsBatch = "Aalice.154\n" +
	"net.games\n" +
	"utzoo!decvax!ucbvax!mhtsa!research!alice!ark\n" +
	"Mon Nov 23 23:44:52 1981\n" +
	"Re: Quitting\n" +
	"I know a simple solution to the quitting problem. Just remove\n" +
	"the penalty for getting killed.  This would save a lot of\n" +
	"discussion -- we could probably heat a building with the hot air.\n" +
	"Aalice.184\n" +
	"net.columbia\n" +
	"utzoo!decvax!ucbvax!mhtsa!research!alice!sjb\n" +
	"Mon Nov 30 18:11:37 1981\n" +
	"Columbia launch\n" +
	"The launch is set for tomorrow morning.\n"

// anewsWatmath is the utzoo file news001f1/a18/watmath.1472.txt, verbatim (one article).
const anewsWatmath = "Awatmath.1472\n" +
	"net.jokes.q\n" +
	"utzoo!decvax!watmath!ipe\n" +
	"Tue Jan 12 20:03:13 1982\n" +
	"Pipeline Jokes\n" +
	"Pipeline jokes.....\n"

// anewsArticle is one parsed A-news article.
type anewsArticle struct {
	head []string
	body string
}

// collectANews parses batch and returns the emitted articles.
func collectANews(t *testing.T, batch string) ([]anewsArticle, error) {
	t.Helper()
	return collectANewsReader(t, strings.NewReader(batch))
}

func collectANewsReader(t *testing.T, r io.Reader) ([]anewsArticle, error) {
	t.Helper()
	var got []anewsArticle
	err := parseANews(r, func(head []string, body io.Reader) {
		b, readErr := io.ReadAll(body)
		if readErr != nil {
			t.Fatalf("read body: %v", readErr)
		}
		got = append(got, anewsArticle{head: head, body: string(b)})
	})
	return got, err
}

func TestMboxANewsBatchSample(t *testing.T) {
	want := []anewsArticle{
		{
			head: []string{
				"Path: utzoo!decvax!ucbvax!mhtsa!research!alice!ark",
				"From: alice!ark",
				"Newsgroups: net.games",
				"Subject: Re: Quitting",
				"Date: Mon, 23 Nov 1981 23:44:52 +0000",
				"Message-ID: <anews.Aalice.154>",
			},
			body: "I know a simple solution to the quitting problem. Just remove\n" +
				"the penalty for getting killed.  This would save a lot of\n" +
				"discussion -- we could probably heat a building with the hot air.\n",
		},
		{
			head: []string{
				"Path: utzoo!decvax!ucbvax!mhtsa!research!alice!sjb",
				"From: alice!sjb",
				"Newsgroups: net.columbia",
				"Subject: Columbia launch",
				"Date: Mon, 30 Nov 1981 18:11:37 +0000",
				"Message-ID: <anews.Aalice.184>",
			},
			body: "The launch is set for tomorrow morning.\n",
		},
	}
	got, err := collectANews(t, anewsBatch)
	if err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("articles:\n got %q\nwant %q", got, want)
	}

	// the same batch with CRLF line ends (a DOS-side copy of the CD) parses the same way
	gotCRLF, err := collectANews(t, strings.ReplaceAll(anewsBatch, "\n", "\r\n"))
	if err != nil {
		t.Fatalf("parseANews (CRLF): %v", err)
	}
	for i := range gotCRLF {
		gotCRLF[i].body = strings.ReplaceAll(gotCRLF[i].body, "\r\n", "\n")
	}
	if !reflect.DeepEqual(gotCRLF, want) {
		t.Errorf("CRLF articles:\n got %q\nwant %q", gotCRLF, want)
	}
}

// TestMboxANewsWatmathSample parses the single-article file, with and without the final
// newline, and checks the whole import path: message-id, header rewrite, group, the
// processor's header check and the article the sink would store.
func TestMboxANewsWatmathSample(t *testing.T) {
	wantHead := []string{
		"Path: utzoo!decvax!watmath!ipe",
		"From: watmath!ipe",
		"Newsgroups: net.jokes.q",
		"Subject: Pipeline Jokes",
		"Date: Tue, 12 Jan 1982 20:03:13 +0000",
		"Message-ID: <anews.Awatmath.1472>",
	}
	for _, batch := range []string{anewsWatmath, strings.TrimSuffix(anewsWatmath, "\n")} {
		got, err := collectANews(t, batch)
		if err != nil {
			t.Fatalf("parseANews: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d articles, want 1", len(got))
		}
		if !reflect.DeepEqual(got[0].head, wantHead) {
			t.Fatalf("head:\n got %q\nwant %q", got[0].head, wantHead)
		}
		if got[0].body != "Pipeline jokes.....\n" {
			t.Fatalf("body %q", got[0].body)
		}
	}

	sink := newCaptureSink()
	c := newCounters("watmath.1472.txt")
	if err := parseANews(strings.NewReader(anewsWatmath), func(head []string, body io.Reader) {
		c.Read++
		handleMessage(c, head, body, &runOptions{}, sink)
	}); err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	if c.Read != 1 || c.Stored != 1 || c.skippedTotal() != 0 {
		t.Fatalf("counters: read=%d stored=%d skipped=%s", c.Read, c.Stored, c.skippedDetail())
	}
	art := sink.arts["<anews.Awatmath.1472>"]
	if art == nil {
		t.Fatalf("article not stored: %v", sink.arts)
	}
	if art.Subject != "Pipeline Jokes" || art.FromHeader != "watmath!ipe" {
		t.Errorf("Subject %q FromHeader %q", art.Subject, art.FromHeader)
	}
	if art.Path != "utzoo!decvax!watmath!ipe" || art.BodyText != "Pipeline jokes....." {
		t.Errorf("Path %q BodyText %q", art.Path, art.BodyText)
	}
	// the processor's bulk header check keeps the block as written
	normalized, err := common.CheckHeaderBlock(strings.Split(art.HeadersJSON, "\n"), common.HeaderBlockFetch)
	if err != nil {
		t.Fatalf("CheckHeaderBlock: %v", err)
	}
	if !reflect.DeepEqual(normalized, wantHead) {
		t.Errorf("stored header block:\n got %q\nwant %q", normalized, wantHead)
	}
	// and reads the date the ctime line carried
	date, ok := processor.ParseNNTPDateStrict(art.DateString)
	if want := time.Date(1982, time.January, 12, 20, 3, 13, 0, time.UTC); !ok || !date.Equal(want) {
		t.Errorf("date %v ok=%v, want %v", date, ok, want)
	}
}

// TestMboxANewsIDLineBlanks: a trailing blank on the id line still starts an article (and
// is not part of the Message-ID), so the article does not land in the body before it.
func TestMboxANewsIDLineBlanks(t *testing.T) {
	batch := anewsWatmath +
		"Aalice.184 \n" +
		"net.columbia\n" +
		"utzoo!decvax!ucbvax!mhtsa!research!alice!sjb\n" +
		"Mon Nov 30 18:11:37 1981\n" +
		"Columbia launch\n" +
		"The launch is set for tomorrow morning.\n"
	got, err := collectANews(t, batch)
	if err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d articles, want 2", len(got))
	}
	if got[0].body != "Pipeline jokes.....\n" {
		t.Errorf("first body %q", got[0].body)
	}
	want := []string{
		"Path: utzoo!decvax!ucbvax!mhtsa!research!alice!sjb",
		"From: alice!sjb",
		"Newsgroups: net.columbia",
		"Subject: Columbia launch",
		"Date: Mon, 30 Nov 1981 18:11:37 +0000",
		"Message-ID: <anews.Aalice.184>",
	}
	if !reflect.DeepEqual(got[1].head, want) {
		t.Errorf("second head:\n got %q\nwant %q", got[1].head, want)
	}
}

// TestMboxANewsRnewsFraming: an "#! rnews N" batch framing line is dropped wherever it
// stands, not appended to the body of the article before it.
func TestMboxANewsRnewsFraming(t *testing.T) {
	batch := "#! rnews 274\n" + anewsWatmath + "#! rnews 198\n" + anewsBatch
	got, err := collectANews(t, batch)
	if err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d articles, want 3", len(got))
	}
	if got[0].body != "Pipeline jokes.....\n" {
		t.Errorf("body of the article before the framing line: %q", got[0].body)
	}
	if got[1].head[len(got[1].head)-1] != "Message-ID: <anews.Aalice.154>" {
		t.Errorf("second article: %q", got[1].head)
	}
}

// TestMboxANewsPreUsenetDate: a block with a pre-Usenet date is still an article (its body
// does not end up in the one before it), but it carries no Date line and handleLines skips
// it as no-date. An article without a title is skipped as no-subject.
func TestMboxANewsPreUsenetDate(t *testing.T) {
	batch := anewsWatmath +
		"Aalice.1\n" +
		"net.general\n" +
		"utzoo!alice!ark\n" +
		"Thu Jan  1 00:00:00 1970\n" +
		"Broken clock\n" +
		"body of the article with the broken clock\n" +
		"Aalice.2\n" +
		"net.general\n" +
		"utzoo!alice!ark\n" +
		"Tue Jan 12 20:03:13 1982\n" +
		"\n" +
		"body of the article without a title\n"
	got, err := collectANews(t, batch)
	if err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d articles, want 3", len(got))
	}
	if got[0].body != "Pipeline jokes.....\n" {
		t.Errorf("body of the article before the broken clock: %q", got[0].body)
	}
	wantBroken := []string{
		"Path: utzoo!alice!ark",
		"From: alice!ark",
		"Newsgroups: net.general",
		"Subject: Broken clock",
		"Message-ID: <anews.Aalice.1>",
	}
	if !reflect.DeepEqual(got[1].head, wantBroken) {
		t.Errorf("pre-Usenet head:\n got %q\nwant %q", got[1].head, wantBroken)
	}

	// the two broken ones are skipped, the good one is stored
	sink := newCaptureSink()
	c := newCounters("batch")
	for _, art := range got {
		c.Read++
		handleMessage(c, art.head, strings.NewReader(art.body), &runOptions{}, sink)
	}
	if c.Stored != 1 || c.Skipped[skipNoDate] != 1 || c.Skipped[skipNoSubject] != 1 {
		t.Errorf("counters: stored=%d skipped=%s", c.Stored, c.skippedDetail())
	}
	if sink.arts["<anews.Awatmath.1472>"] == nil {
		t.Errorf("the good article was not stored: %v", sink.arts)
	}
}

// TestMboxANewsOversized: an article whose body passes maxANewsArticle is not emitted, the
// batch goes on with the next block, and a batch with nothing else says so in its error.
func TestMboxANewsOversized(t *testing.T) {
	old := maxANewsArticle
	maxANewsArticle = 64
	defer func() { maxANewsArticle = old }()

	big := strings.Repeat("filler line\n", 20) // well over 64 bytes
	batch := "Aalice.1\n" +
		"net.general\n" +
		"utzoo!alice!ark\n" +
		"Tue Jan 12 20:03:13 1982\n" +
		"Too long\n" +
		big +
		anewsWatmath
	got, err := collectANews(t, batch)
	if err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	if len(got) != 1 || got[0].head[len(got[0].head)-1] != "Message-ID: <anews.Awatmath.1472>" {
		t.Fatalf("got %d articles: %q", len(got), got)
	}
	if got[0].body != "Pipeline jokes.....\n" {
		t.Errorf("body %q", got[0].body)
	}

	// nothing but the oversized article: an error that names it
	got, err = collectANews(t, batch[:len(batch)-len(anewsWatmath)])
	if err == nil || !strings.Contains(err.Error(), "1 over 64 bytes") {
		t.Errorf("error %v, want one naming the oversized article", err)
	}
	if len(got) != 0 {
		t.Errorf("emitted %d articles", len(got))
	}
}

// TestMboxANewsBrokenBlock: a block whose date line is not a ctime date is no article, its
// lines stay in the body of the article before it.
func TestMboxANewsBrokenBlock(t *testing.T) {
	batch := anewsWatmath +
		"Aalice.999\n" +
		"net.games\n" +
		"utzoo!alice!ark\n" +
		"not a date at all\n" +
		"A title\n" +
		"and a body line\n"
	got, err := collectANews(t, batch)
	if err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d articles, want 1", len(got))
	}
	want := "Pipeline jokes.....\nAalice.999\nnet.games\nutzoo!alice!ark\nnot a date at all\nA title\nand a body line\n"
	if got[0].body != want {
		t.Errorf("body:\n got %q\nwant %q", got[0].body, want)
	}

	// an id line with fewer than five lines after it is body text too
	got, err = collectANews(t, anewsWatmath+"Aalice.999\nnet.games\n")
	if err != nil {
		t.Fatalf("parseANews (short tail): %v", err)
	}
	if len(got) != 1 || got[0].body != "Pipeline jokes.....\nAalice.999\nnet.games\n" {
		t.Errorf("short tail: %d articles, body %q", len(got), got[0].body)
	}
}

// TestMboxANewsNoArticle: text before the first block is dropped; a file without any block
// is an error and emits nothing.
func TestMboxANewsNoArticle(t *testing.T) {
	got, err := collectANews(t, "#! rnews 123\nleftover line\n\n"+anewsWatmath)
	if err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	if len(got) != 1 || got[0].head[0] != "Path: utzoo!decvax!watmath!ipe" {
		t.Fatalf("got %d articles: %q", len(got), got)
	}

	for _, batch := range []string{"", "\n\n", "Aalice.154\nnet.games\n"} {
		got, err := collectANews(t, batch)
		if err == nil {
			t.Errorf("batch %q: no error", batch)
		}
		if len(got) != 0 {
			t.Errorf("batch %q: emitted %d articles", batch, len(got))
		}
	}
}

// errReader returns data and then err.
type errReader struct {
	data string
	err  error
	done bool
}

func (e *errReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, e.err
	}
	e.done = true
	n := copy(p, e.data)
	if n < len(e.data) {
		e.data = e.data[n:]
		e.done = false
		return n, nil
	}
	return n, nil
}

func TestMboxANewsReadError(t *testing.T) {
	boom := errors.New("boom")
	got, err := collectANewsReader(t, &errReader{data: anewsWatmath, err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("error %v, want %v", err, boom)
	}
	if len(got) != 1 {
		t.Errorf("emitted %d articles before the error, want 1", len(got))
	}
}

func TestMboxANewsFrom(t *testing.T) {
	cases := map[string]string{
		"utzoo!decvax!ucbvax!mhtsa!research!alice!ark": "alice!ark",
		"utzoo!decvax!watmath!ipe":                     "watmath!ipe",
		"alice!ark":                                    "alice!ark",
		"ark":                                          "ark",
		"utzoo!alice!ark!":                             "alice!ark",
		"!":                                            "",
		"":                                             "",
	}
	for path, want := range cases {
		if got := anewsFrom(path); got != want {
			t.Errorf("anewsFrom(%q) = %q, want %q", path, got, want)
		}
	}

	// without a path there is no Path and no From line
	got, err := collectANews(t, "Aalice.154\nnet.games\n\nMon Nov 23 23:44:52 1981\nTitle\nbody\n")
	if err != nil {
		t.Fatalf("parseANews: %v", err)
	}
	want := []string{
		"Newsgroups: net.games",
		"Subject: Title",
		"Date: Mon, 23 Nov 1981 23:44:52 +0000",
		"Message-ID: <anews.Aalice.154>",
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].head, want) {
		t.Errorf("head:\n got %q\nwant %q", got, want)
	}
}

func TestMboxANewsParseCtime(t *testing.T) {
	want := time.Date(1981, time.November, 23, 23, 44, 52, 0, time.UTC)
	for _, line := range []string{
		"Mon Nov 23 23:44:52 1981",
		"Mon Nov 23 23:44:52 GMT 1981",
		"Mon Nov 23 23:44:52 1981 GMT",
		"Mon Nov  23  23:44:52 1981",
	} {
		got, ok := parseCtime(line)
		if !ok || !got.Equal(want) {
			t.Errorf("parseCtime(%q) = %v %v, want %v", line, got, ok, want)
		}
		if ok && got.Format(rfc5322DateLayout) != "Mon, 23 Nov 1981 23:44:52 +0000" {
			t.Errorf("parseCtime(%q) formats as %q", line, got.Format(rfc5322DateLayout))
		}
	}
	for _, line := range []string{
		"", "not a date", "Mon Nov 23 1981", "Xyz Nov 23 23:44:52 1981",
		"Mon, 23 Nov 1981 23:44:52 +0000",
	} {
		if got, ok := parseCtime(line); ok {
			t.Errorf("parseCtime(%q) = %v, want no match", line, got)
		}
	}
}

func TestMboxANewsBnewsMessageID(t *testing.T) {
	cases := []struct {
		head []string
		want string
		ok   bool
	}{
		{[]string{"Article-I.D.: decvax.123"}, "<bnews.decvax.123>", true},
		{[]string{"article-i.d.:watmath.1472"}, "<bnews.watmath.1472>", true},
		{[]string{"Article-ID: utzoo.3104"}, "<bnews.utzoo.3104>", true},
		{[]string{"Article-I.D.: decvax.123", "Message-ID:"}, "<bnews.decvax.123>", true},
		// a real Message-ID wins, whatever the article-id says
		{[]string{"Message-ID: <a@b.example>", "Article-I.D.: decvax.123"}, "", false},
		// values that are not the "site.number" form
		{[]string{"Article-I.D.: <decvax.123@decvax.uucp>"}, "", false},
		{[]string{"Article-I.D.:"}, "", false},
		{[]string{"Article-I.D.: decvax"}, "", false},
		{[]string{"Article-I.D.: decvax.12a"}, "", false},
		{[]string{"Article-I.D.: decvax.123 (resent)"}, "", false},
		{[]string{"Article-I.D.: decvax.123", " .456"}, "", false},
		{[]string{"X-Article-I.D.: decvax.123"}, "", false},
		{nil, "", false},
	}
	for _, tc := range cases {
		got, ok := bnewsMessageID(tc.head)
		if got != tc.want || ok != tc.ok {
			t.Errorf("bnewsMessageID(%q) = %q %v, want %q %v", tc.head, got, ok, tc.want, tc.ok)
		}
	}
}
