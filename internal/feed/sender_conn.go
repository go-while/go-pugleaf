package feed

// The protocol side of the outgoing feed (plan nntp-peering wave 3): one connection to a peer,
// built on the internal/nntp client that nntp-transfer uses. A connection is either streaming
// (MODE STREAM with pipelined CHECK/TAKETHIS through the response demuxer, the in-flight bound
// of one batch) or, for a peer without streaming, IHAVE one article at a time.
//
// Articles go out relay-as-stored: the client's transfer header logic keeps the stored header
// block and prepends our identity to the Path exactly once (common.EnsureLocalPathPrefix), so
// a relayed article never gets a second copy of our Path element.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
)

// Outcome is what happens to a queue item after the peer answered.
type Outcome int

const (
	// OutcomeDone is finished: the peer took it (238+239, 335+235), already has it (438, 435)
	// or does not want it (439, 437). The queue row goes away.
	OutcomeDone Outcome = iota
	// OutcomeDefer is temporary (431, 436, 400, a network error): the row stays with one more
	// attempt and a later next_attempt.
	OutcomeDefer
	// OutcomeDrop is an article we cannot put on the wire at all (an unsafe stored header
	// block): finished without counting it as the peer's answer.
	OutcomeDrop
)

// Result is the verdict for one offered article.
type Result struct {
	MessageID string
	Code      int // the peer's status; 0 when nothing was answered
	Outcome   Outcome
	Err       string // the reason of a Defer or a Drop, stored as the item's last_error
}

// Outgoing is one article on its way to a peer: the stored article and the newsgroup it was
// read from (the newsgroup rebuilds the header block of a row without a stored one).
type Outgoing struct {
	Article   *models.Article
	Newsgroup string
}

// wireSize is roughly what this article costs on the wire: the stored header block, the body
// and some room for the command and the dot stuffing. It bounds how much one batch sends
// (Config.BatchBytes), so an exact count is not needed.
func (o Outgoing) wireSize() int {
	if o.Article == nil {
		return 0
	}
	return len(o.Article.HeadersJSON) + len(o.Article.BodyText) + 512
}

// Transport is one open connection to a peer.
type Transport interface {
	// Send offers every article and returns one Result per article, in order (an article
	// without an answer is a Defer). offered is how many CHECK or IHAVE commands were written
	// (Stats.Offered: one per article that reached the wire, never the TAKETHIS on top of its
	// CHECK). A non-nil error means the connection is unusable: the caller closes it, backs
	// off and connects again.
	Send(arts []Outgoing) (res []Result, offered int, err error)
	// Close closes the connection. It may be called from another goroutine while Send runs
	// (that is how a stopping sender unblocks a worker) and more than once.
	Close()
	// Mode is "stream" or "ihave", for the log lines.
	Mode() string
}

// Dialer opens one connection to a peer; the sender's default is DialPeer.
type Dialer func(p *models.Peer) (Transport, error)

// DialOptions are the knobs of DialPeer.
type DialOptions struct {
	Identity       string        // our Path identity
	ConnectTimeout time.Duration // dial, TLS handshake, greeting and AUTHINFO
	ReadTimeout    time.Duration // 0: the client default, < 0: none
	WriteTimeout   time.Duration // 0: the client default, < 0: none
	Batch          int           // the in-flight bound of one streaming batch
}

// DialPeer connects to a peer's out_host/out_port with its TLS and AUTHINFO settings and
// returns a streaming transport. A peer that refuses MODE STREAM but keeps the connection
// (500/501 without streaming, 480 or 502) gets the IHAVE transport instead.
func DialPeer(p *models.Peer, o DialOptions) (Transport, error) {
	if p == nil {
		return nil, fmt.Errorf("feed: no peer")
	}
	if p.OutHost == "" {
		return nil, fmt.Errorf("feed: peer %s has no out_host", p.Name)
	}
	port := p.OutPort
	if port <= 0 {
		port = 119
	}
	batch := o.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	conn := nntp.NewConn(&nntp.BackendConfig{
		Host:         p.OutHost,
		Port:         port,
		SSL:          p.OutTLS,
		Username:     p.OutUsername,
		Password:     p.OutPassword,
		ReadTimeout:  o.ReadTimeout,
		WriteTimeout: o.WriteTimeout,
		// The sender has its own backoff: never sleep inside Connect (it would hold up Stop).
		AuthFailureBackoff: -1,
		ConnectTimeout:     o.ConnectTimeout,
		MaxConns:           1,
	})
	if err := conn.Connect(); err != nil {
		return nil, err
	}
	code, err := conn.ModeStreamCode()
	if err != nil {
		// Only a status the peer answered is a refusal of streaming; a send or read error is
		// the network, and the sender must reconnect with its backoff instead of switching
		// this connection to IHAVE.
		if errors.Is(err, nntp.ErrStreamingUnsupported) || code == 480 || code == 502 {
			log.Printf("[FEED] peer %s: no streaming (%v): using IHAVE", p.Name, err)
			return newIHaveTransport(conn, p, o.Identity), nil
		}
		conn.CloseNow()
		return nil, fmt.Errorf("MODE STREAM: %w", err)
	}
	return newStreamTransport(conn, p, o.Identity, batch), nil
}

// errConnLost is the transport's "the connection is gone" error.
var errConnLost = errors.New("feed: connection lost")

// deniedErr marks an error the peer caused by refusing our feed (480 or 502 on CHECK or
// IHAVE): the sender counts it as Denied in the peering statistics (plan peering-stats) and
// otherwise treats it like any other connection error. Its message is the wrapped one, so
// nothing in the log or in the item's last_error changes. A refusal in the greeting is not
// marked: Connect reports it without its status code.
type deniedErr struct{ err error }

func (d *deniedErr) Error() string { return d.err.Error() }
func (d *deniedErr) Unwrap() error { return d.err }

// denied wraps err as a refusal of our feed when code says so, and returns it unchanged
// otherwise.
func denied(code int, err error) error {
	if err == nil || (code != 480 && code != 502) {
		return err
	}
	return &deniedErr{err: err}
}

// isPeerDenied reports whether err is the peer refusing our feed.
func isPeerDenied(err error) bool {
	var d *deniedErr
	return errors.As(err, &d)
}

// sendableMessageID reports whether an id may be put on the wire at all (the client refuses
// the rest before writing anything; an id that reaches here invalid can never be sent).
func sendableMessageID(id string) bool { return common.ValidMessageID(id) }

// streamTransport is a MODE STREAM connection: CHECK for the whole batch, then TAKETHIS for
// the articles the peer wants. The responses are read by the client's demuxer, which keeps one
// reader on the socket, and paired with the commands in order.
type streamTransport struct {
	conn     *nntp.BackendConn
	demux    *nntp.ResponseDemuxer
	errChan  chan struct{}
	rrCheck  chan *nntp.ReadRequest
	rrTake   chan *nntp.ReadRequest
	job      *nntp.CHTTJob
	peerName string
	identity string
}

func newStreamTransport(conn *nntp.BackendConn, p *models.Peer, identity string, batch int) *streamTransport {
	errChan := make(chan struct{}, 16)
	name := p.Name
	t := &streamTransport{
		conn:     conn,
		errChan:  errChan,
		rrCheck:  make(chan *nntp.ReadRequest, batch+1),
		rrTake:   make(chan *nntp.ReadRequest, batch+1),
		job:      &nntp.CHTTJob{Newsgroup: &name},
		peerName: p.Name,
		identity: identity,
	}
	// One batch has at most batch CHECKs and then batch TAKETHISs outstanding; the demuxer's
	// own default buffers (DemuxerQueueDepth) are sized for a bulk transfer of a whole group.
	t.demux = nntp.NewResponseDemuxerSize(conn, errChan, 2*batch+8)
	t.demux.Start()
	return t
}

func (t *streamTransport) Mode() string { return "stream" }

// Close ends the connection at once, also while Send is waiting for an answer on another
// goroutine (CloseNow does not wait for the connection's lock).
func (t *streamTransport) Close() {
	common.SignalErrChan(t.errChan)
	t.conn.CloseNow()
}

// Send runs one streaming round: CHECK every article, TAKETHIS the wanted ones.
func (t *streamTransport) Send(arts []Outgoing) ([]Result, int, error) {
	res := make([]Result, len(arts))
	for i := range arts {
		res[i] = Result{MessageID: arts[i].Article.MessageID, Outcome: OutcomeDefer, Err: "not offered"}
	}
	if len(arts) == 0 {
		return res, 0, nil
	}

	// Only ids that may be written are offered; the rest can never be sent.
	ids := make([]*string, 0, len(arts))
	checked := make([]int, 0, len(arts))
	for i := range arts {
		id := arts[i].Article.MessageID
		if !sendableMessageID(id) {
			res[i] = Result{MessageID: id, Outcome: OutcomeDrop, Err: "invalid message-id"}
			continue
		}
		ids = append(ids, &arts[i].Article.MessageID)
		checked = append(checked, i)
	}
	if len(ids) == 0 {
		return res, 0, nil
	}

	sent, err := t.conn.SendCheckMultiple(ids, t.rrCheck, t.job, t.demux)
	offered := int(sent)
	if err != nil {
		t.fail(res, checked, err)
		return res, offered, fmt.Errorf("CHECK: %w", err)
	}

	// CHECK answers, in command order.
	wanted := make([]int, 0, len(checked))
	for n := 0; n < int(sent); n++ {
		i := checked[n]
		id, code, _, rerr := t.readResult(t.rrCheck, t.demux.GetCheckResponseChan())
		if rerr != nil {
			t.fail(res, checked[n:], rerr)
			return res, offered, rerr
		}
		if id != arts[i].Article.MessageID {
			err := fmt.Errorf("feed: peer %s: CHECK answer for %s, expected %s", t.peerName, id, arts[i].Article.MessageID)
			t.fail(res, checked[n:], err)
			return res, offered, err
		}
		switch code {
		case 238: // wanted
			wanted = append(wanted, i)
		case 438, 435: // the peer has it
			res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDone}
		case 431, 400: // try again later
			res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDefer, Err: fmt.Sprintf("CHECK %d", code)}
		case 439, 437: // refused by id
			res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDone}
		default:
			// 480, 500, 501, 502, 403: the peer does not take CHECK from us (any more).
			err := denied(code, fmt.Errorf("feed: peer %s: CHECK %s answered %d", t.peerName, id, code))
			t.fail(res, checked[n:], err)
			return res, offered, err
		}
	}

	// TAKETHIS the wanted articles. The connection is locked for the whole run: the client
	// writes the command and the article in two steps and nothing else may write between them.
	taken := make([]int, 0, len(wanted))
	var sendErr error
	t.conn.Lock()
	for n, i := range wanted {
		_, _, err, doContinue := t.conn.SendTakeThisArticleStreaming(
			arts[i].Article, &t.identity, arts[i].Newsgroup, t.demux, t.rrTake, t.job, n, len(wanted))
		if err != nil {
			if !doContinue {
				sendErr = err
				break
			}
			// Nothing was written: the stored header block or the id is unusable.
			res[i] = Result{MessageID: arts[i].Article.MessageID, Outcome: OutcomeDrop, Err: err.Error()}
			continue
		}
		taken = append(taken, i)
	}
	t.conn.Unlock()

	// TAKETHIS answers, in command order.
	for n, i := range taken {
		id, code, _, rerr := t.readResult(t.rrTake, t.demux.GetTakeThisResponseChan())
		if rerr != nil {
			t.fail(res, taken[n:], rerr)
			return res, offered, rerr
		}
		if id != arts[i].Article.MessageID {
			err := fmt.Errorf("feed: peer %s: TAKETHIS answer for %s, expected %s", t.peerName, id, arts[i].Article.MessageID)
			t.fail(res, taken[n:], err)
			return res, offered, err
		}
		switch code {
		case 239, 235: // transferred
			res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDone}
		case 439, 437: // rejected: do not offer it again
			res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDone}
		case 438, 435: // the peer got it meanwhile
			res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDone}
		case 431, 436, 400:
			res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDefer, Err: fmt.Sprintf("TAKETHIS %d", code)}
		default:
			err := fmt.Errorf("feed: peer %s: TAKETHIS %s answered %d", t.peerName, id, code)
			t.fail(res, taken[n:], err)
			return res, offered, err
		}
	}
	if sendErr != nil {
		return res, offered, sendErr
	}
	return res, offered, nil
}

// fail marks every article of idx that has no answer yet as deferred with reason.
func (t *streamTransport) fail(res []Result, idx []int, reason error) {
	msg := reason.Error()
	for _, i := range idx {
		if res[i].Code != 0 || res[i].Outcome == OutcomeDrop {
			continue
		}
		res[i] = Result{MessageID: res[i].MessageID, Outcome: OutcomeDefer, Err: msg}
	}
}

// readResult pairs the next command of rrChan with the next answer of respChan (the demuxer
// keeps both in command order) and returns the answer. Both pooled structs are recycled.
func (t *streamTransport) readResult(rrChan chan *nntp.ReadRequest, respChan chan *nntp.ResponseData) (msgID string, code int, line string, err error) {
	var rr *nntp.ReadRequest
	select {
	case rr = <-rrChan:
	case <-t.errChan:
		common.SignalErrChan(t.errChan)
		return "", 0, "", errConnLost
	}
	if rr == nil || rr.MsgID == nil {
		return "", 0, "", fmt.Errorf("feed: peer %s: empty read request", t.peerName)
	}
	msgID = *rr.MsgID
	cmdID := rr.CmdID

	var rd *nntp.ResponseData
	select {
	case rd = <-respChan:
	case <-t.errChan:
		common.SignalErrChan(t.errChan)
		rr.ClearReadRequest(nil)
		return msgID, 0, "", errConnLost
	}
	code, line, err = rd.Code, rd.Line, rd.Err
	mismatch := rd.CmdID != cmdID
	wantID := msgID
	rr.ClearReadRequest(rd) // recycles the read request and the response

	if mismatch {
		return wantID, 0, "", fmt.Errorf("feed: peer %s: answer out of order (%s)", t.peerName, wantID)
	}
	if code == 0 && err != nil {
		return wantID, 0, "", fmt.Errorf("feed: peer %s: reading the answer for %s: %w", t.peerName, wantID, err)
	}
	// "238 <id> ..." names the id; peers that answer without it keep wantID.
	if parts := strings.Fields(line); len(parts) > 0 && strings.HasPrefix(parts[0], "<") {
		return parts[0], code, line, nil
	}
	return wantID, code, line, nil
}

// ihaveTransport offers one article at a time with IHAVE: the fallback for a peer without
// streaming.
type ihaveTransport struct {
	conn     *nntp.BackendConn
	peerName string
	identity string
}

func newIHaveTransport(conn *nntp.BackendConn, p *models.Peer, identity string) *ihaveTransport {
	return &ihaveTransport{conn: conn, peerName: p.Name, identity: identity}
}

func (t *ihaveTransport) Mode() string { return "ihave" }

// Close ends the connection at once: IHave holds the connection's lock for a whole exchange,
// so only CloseNow unblocks a worker that is waiting for the answer to one article.
func (t *ihaveTransport) Close() { t.conn.CloseNow() }

func (t *ihaveTransport) Send(arts []Outgoing) ([]Result, int, error) {
	res := make([]Result, len(arts))
	for i := range arts {
		res[i] = Result{MessageID: arts[i].Article.MessageID, Outcome: OutcomeDefer, Err: "not offered"}
	}
	offered := 0
	for i := range arts {
		id := arts[i].Article.MessageID
		if !sendableMessageID(id) {
			res[i] = Result{MessageID: id, Outcome: OutcomeDrop, Err: "invalid message-id"}
			continue
		}
		code, _, err := t.conn.IHave(arts[i].Article, &t.identity, arts[i].Newsgroup)
		switch {
		case err == nil:
			offered++
			switch code {
			case 235:
				res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDone}
			case 435, 437:
				res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDone}
			case 436:
				res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDefer, Err: "IHAVE 436"}
			default:
				res[i] = Result{MessageID: id, Code: code, Outcome: OutcomeDefer, Err: fmt.Sprintf("IHAVE %d", code)}
			}
		case errors.Is(err, common.ErrRelayHeadersInvalid), errors.Is(err, nntp.ErrInvalidCommandArg):
			// Refused before anything was written: the connection stays usable.
			res[i] = Result{MessageID: id, Outcome: OutcomeDrop, Err: err.Error()}
		default:
			// ErrIHaveUnsupported, ErrAuthRequired and every I/O error: stop offering on this
			// connection (reconnecting into the same refusal would loop).
			if code != 0 {
				offered++
			}
			perr := denied(code, fmt.Errorf("feed: peer %s: IHAVE %s: %w", t.peerName, id, err))
			res[i] = Result{MessageID: id, Code: 0, Outcome: OutcomeDefer, Err: perr.Error()}
			return res, offered, perr
		}
	}
	return res, offered, nil
}
