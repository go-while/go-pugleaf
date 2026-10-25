package inflate

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// innStream is what INN 2.8.0's nnrpd sent after "206 Compression now active" for DATE,
// a second COMPRESS DEFLATE, HELP and LIST OVERVIEW.FMT: each response ends in a
// Z_PARTIAL_FLUSH (an empty fixed block, not byte-aligned), not in 00 00 ff ff.
var innStream = [][2]string{
	{"32343454303230323334303236b434313030e1e50208", "111 20261023194004\r\n"},
	{"2053032305c79ca2d4c4944a85d2e2ccbc74854485e4fcdc82a2d4e2e2ccfc3c859cc4cad4225e2e8000", "502 Already using a compression layer\r\n"},
	{"32343050f0494d4fcc0149e626e6a514f3720104209c6a7adc8481e87da5fd0ff3079643d5deaaaa0e386089afda269b0a71608b4b506340c65114891f5fdb84ddec4a694f633fbff17b9e190320ca891f6328a561d7ad7822c1dc9fe48b50953b2e7844d26d0605c314fa5a8a39478cc1584fd37950cdcc108b418a5f87baef260965d777baab8f4f467c1cfa49547388534c890fa31a5a551b4aadda9314bd06cff39cc8260b7ede33e0a31c6d484c38c10cca3fe2625597932cc929365e02bc8d11c7160bae31a45991432fce53ab86d368a128a070303512ea9d94aafb56b8fb228c827b2e221ce7369208ed30bc712c1623c65d248c43897c4e0ca53c77c746d6ba9a17c0e324b10f7885fdac48f92d1098744a365e8e385b37052759ca66eb0c53434e58687c519486b89a93cc8088679499250fe6143f33f7eedb5bb31da63b829fbd6dc267566c984f49ee6e7d2355abfba56ae56bd9a05caae308560ea87362b7466d15bb5caacb45caa681c341ca6982324c78752559577055fa3775efaa680dafba769f674b757f14c445c6cdc4f2985dd7fc5ec7f621273cbe698403ff3b04fb8ffa7bd38f35e56386f9055a0bd543b92e9689a6621c94b603ff721472023dc0d7d3247aa1bfff16cda0eacf5f3cf149785a4cfa9bf7f8e03d3efc15", "100 Legal commands\r\n  ARTICLE [message-ID|number]\r\n  AUTHINFO USER name|PASS password|SASL mechanism [initial-response]|GENERIC program [argument ...]\r\n  BODY [message-ID|number]\r\n  CAPABILITIES [keyword]\r\n  COMPRESS DEFLATE\r\n  DATE\r\n  GROUP newsgroup\r\n  HDR header [message-ID|range]\r\n  HEAD [message-ID|number]\r\n  HELP\r\n  IHAVE message-ID\r\n  LAST\r\n  LIST [ACTIVE [wildmat]|ACTIVE.TIMES [wildmat]|COUNTS [wildmat]|DISTRIB.PATS|DISTRIBUTIONS|HEADERS [MSGID|RANGE]|MODERATORS|MOTD|NEWSGROUPS [wildmat]|OVERVIEW.FMT|SUBSCRIPTIONS [wildmat]]\r\n  LISTGROUP [newsgroup [range]]\r\n  MODE READER\r\n  NEWGROUPS [yy]yymmdd hhmmss [GMT]\r\n  NEWNEWS wildmat [yy]yymmdd hhmmss [GMT]\r\n  NEXT\r\n  OVER [range]\r\n  POST\r\n  QUIT\r\n  STARTTLS\r\n  STAT [message-ID|number]\r\n  XGTITLE [wildmat]\r\n  XHDR header [message-ID|range]\r\n  XOVER [range]\r\n  XPAT header message-ID|range pattern [pattern ...]\r\nReport problems to <usenet@fedora45.e2e.test>.\r\n.\r\n"},
	{"404686a60afe45204bf2d314d2325373528a1532f314f2cb528bca3253cb1552124b1293128b5379b9000228b834292b35b9c48a97cbad283f1748b92496a402295fb8e3ac402e484b2d4acd4b4e2d06729c2a4bc0b44f661e988e284a4db34a2bcdc901db0c10", "215 Order of fields in overview database\r\nSubject:\r\nFrom:\r\nDate:\r\nMessage-ID:\r\nReferences:\r\nBytes:\r\nLines:\r\nXref:full\r\n.\r\n"},
}

// stream runs Run on a pipe: feed writes compressed bytes, next waits for decoded bytes.
type stream struct {
	pw   *io.PipeWriter
	got  chan []byte
	done chan error
}

func newStream(t *testing.T) *stream {
	pr, pw := io.Pipe()
	s := &stream{pw: pw, got: make(chan []byte, 1024), done: make(chan error, 1)}
	go func() {
		s.done <- Run(bufio.NewReader(pr), func(b []byte) error {
			s.got <- append([]byte(nil), b...)
			return nil
		})
	}()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	return s
}

func (s *stream) feed(t *testing.T, b []byte) {
	t.Helper()
	go func() { _, _ = s.pw.Write(b) }()
}

// want reads decoded bytes until it has n of them; it fails after a second without them.
func (s *stream) want(t *testing.T, want string) {
	t.Helper()
	var got []byte
	tm := time.NewTimer(5 * time.Second)
	defer tm.Stop()
	for len(got) < len(want) {
		select {
		case b := <-s.got:
			got = append(got, b...)
		case err := <-s.done:
			t.Fatalf("Run ended (%v) with %q, want %q", err, got, want)
		case <-tm.C:
			t.Fatalf("decoded %q, want %q: output held back until more input", got, want)
		}
	}
	if string(got) != want {
		t.Fatalf("decoded %q, want %q", got, want)
	}
}

// TestINNPartialFlush: every response of INN's nnrpd is decoded before the next one is sent.
func TestINNPartialFlush(t *testing.T) {
	s := newStream(t)
	for _, v := range innStream {
		b, err := hex.DecodeString(v[0])
		if err != nil {
			t.Fatal(err)
		}
		s.feed(t, b)
		s.want(t, v[1])
	}
}

// TestRoundTrip: compress/flate output at every level, sync-flushed per chunk, decodes chunk
// by chunk; a large chunk spans many blocks and windows.
func TestRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	text := func(n int, alpha string) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alpha[r.IntN(len(alpha))]
		}
		return string(b)
	}
	chunks := []string{
		"200 hi\r\n", "x", strings.Repeat("abc", 25000) + "\r\n",
		text(140000, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"),
		text(70000, "\x00\x01\x02\xfe\xff"), text(40000, string(rune(0x7f))+"0123456789"), "",
		strings.Repeat("224 overview follows\r\n1\tsubject\tfrom\r\n", 3000),
	}
	for _, level := range []int{flate.NoCompression, flate.BestSpeed, flate.DefaultCompression, flate.BestCompression, flate.HuffmanOnly} {
		s := newStream(t)
		var buf bytes.Buffer
		zw, err := flate.NewWriter(&buf, level)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range chunks {
			_, _ = zw.Write([]byte(c))
			_ = zw.Flush()
			s.feed(t, append([]byte(nil), buf.Bytes()...))
			buf.Reset()
			s.want(t, c)
		}
		_ = zw.Close() // a final block ends Run with io.EOF
		s.feed(t, append([]byte(nil), buf.Bytes()...))
		if err := <-s.done; err != io.EOF {
			t.Fatalf("level %d: after the final block: %v", level, err)
		}
	}
}

// bitWriter writes DEFLATE bits, the first one lowest; codes are written most significant
// bit first.
type bitWriter struct {
	out  []byte
	bits uint32
	nb   uint
}

func (w *bitWriter) put(v uint32, n uint) {
	w.bits |= v << w.nb
	w.nb += n
	for w.nb >= 8 {
		w.out = append(w.out, byte(w.bits))
		w.bits >>= 8
		w.nb -= 8
	}
}

func (w *bitWriter) code(v uint32, n uint) {
	var r uint32
	for i := uint(0); i < n; i++ {
		r = r<<1 | (v>>i)&1
	}
	w.put(r, n)
}

// lit writes a fixed-code literal (0..143).
func (w *bitWriter) lit(s string) {
	for _, c := range []byte(s) {
		w.code(0x30+uint32(c), 8)
	}
}

func (w *bitWriter) eob() { w.code(0, 7) }

// bytes returns the whole bytes written; the bits of a partial last byte are left out.
func (w *bitWriter) bytes() []byte { return w.out }

// TestPartialFlushByHand: a fixed block, then the 10 bits of an empty fixed block, of which
// the last ones are not sent yet: the literals are decoded at once.
func TestPartialFlushByHand(t *testing.T) {
	var w bitWriter
	w.put(0, 1) // not final
	w.put(1, 2) // fixed
	w.lit("111 x\r\n")
	w.eob()
	w.put(0, 1)
	w.put(1, 2)
	w.eob()
	s := newStream(t)
	s.feed(t, w.bytes())
	s.want(t, "111 x\r\n")
}

func TestCorrupt(t *testing.T) {
	var far bitWriter
	far.put(1, 1)
	far.put(1, 2)
	far.lit("a")
	far.code(1, 7) // length 3
	far.code(1, 5) // distance 2: only 1 byte decoded
	far.eob()
	far.put(0, 8)
	cases := []struct {
		name string
		in   []byte
		why  string
	}{
		{"block type 3", []byte{0x07}, "invalid block type"},
		{"stored length", []byte{0x01, 0x05, 0x00, 0x00, 0x00}, "complement"},
		{"distance", far.bytes(), "too far back"},
		{"not deflate", []byte("\xff\xff\xff\xffDATE\r\n"), "invalid block type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(bufio.NewReader(bytes.NewReader(tc.in)), func([]byte) error { return nil })
			var ce flate.CorruptInputError
			if !errors.As(err, &ce) || !strings.Contains(err.Error(), tc.why) {
				t.Fatalf("err = %v, want a flate.CorruptInputError (%s)", err, tc.why)
			}
		})
	}
}

// TestEnd: the end of the input is io.EOF, the output decoded before it is passed on, and an
// error of out stops Run without another call of out.
func TestEnd(t *testing.T) {
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.BestSpeed)
	_, _ = zw.Write([]byte("205 bye\r\n"))
	_ = zw.Flush()
	var got []byte
	err := Run(bufio.NewReader(bytes.NewReader(buf.Bytes())), func(b []byte) error {
		got = append(got, b...)
		return nil
	})
	if err != io.EOF || string(got) != "205 bye\r\n" {
		t.Fatalf("Run = %q %v", got, err)
	}
	stop := errors.New("stop")
	calls := 0
	if err := Run(bufio.NewReader(bytes.NewReader(buf.Bytes())), func([]byte) error { calls++; return stop }); err != stop || calls != 1 {
		t.Fatalf("out error: %v after %d calls of out, want 1", err, calls)
	}
}
