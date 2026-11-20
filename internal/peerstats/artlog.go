package peerstats

// The optional INN-style article log (plan inn-reject-log): one line per article a peer fed us
// and we rejected (437 after IHAVE, 439 after TAKETHIS), in the format of innd's "news" log,
// so the reason of a reject can be looked up by message-id. innd writes one line per article
// there, accepted ones included; this log holds the rejections only (the accepted ones would
// cost a line per article and say nothing the counters do not). It is a file of its own: the
// news.notice-style INN log of innlog.go stays what innreport parses, and innreport would list
// these lines as unknown there.
//
// The file is an innLog without the syslog header (writeLines). Like the INN log, the event
// method only queues the line; the background goroutine writes the queue once a second and
// Stop writes the rest, reopens the file after logrotate moved it away, and nothing is written
// after Stop.

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-while/go-pugleaf/internal/common"
)

// artRejectCode is innd's code character of a rejected article (innd/art.c:28,
// "#define ART_REJECT '-'").
const artRejectCode = '-'

// artTextMax caps the reply text of one line, so a long reason cannot make the log line
// unbounded. innd's reply texts are far shorter (cp->Error is a fixed buffer).
const artTextMax = 256

// maxArtQueue bounds artq like maxLogQueue bounds logq. A peer that streams duplicates gets
// one line per article, so the queue is drained every artDrainEvery: this is that many lines
// per second before lines are dropped (and counted).
const maxArtQueue = 8192

// artDrainEvery is how often the background goroutine writes the queued article log lines.
const artDrainEvery = time.Second

// artTime is the timestamp of innd's news log: ctime() without the weekday (the "%.15s" of
// ctime(&t) + 4, "Nov 20 15:17:19", the day padded with a space) and the milliseconds,
// truncated as innd's Now.tv_usec / 1000 is.
func artTime(t time.Time) string {
	return t.Format("Jan _2 15:04:05.000")
}

// artMessageID is the message-id field: innd prints "(null)" when the article has no
// Message-ID (innd/art.c:304); an invalid one, which could hold anything, is printed the same.
func artMessageID(id string) string {
	if !common.ValidMessageID(id) {
		return "(null)"
	}
	return id
}

// artText is the reply text of a line: the text the peer was sent after the code, without a
// leading message-id (a TAKETHIS reply carries "<id> Rejected (...)", and innd's text is the
// reply without it, e.g. "439 Illegal path element", innd/art.c:2050), on one line (control
// characters become spaces) and at most artTextMax bytes, cut at a rune boundary.
func artText(id, text string) string {
	if id != "" {
		text = strings.TrimPrefix(text, id+" ")
	}
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if len(text) > artTextMax {
		n := artTextMax
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
		text = text[:n]
	}
	return text
}

// artRejectLine is one line of innd's news log for an article it rejected.
//
// innd/art.c:304 (ARTlog):
//
//	fprintf(Log, "%.15s.%03d %c %s %s %s%s", ctime(&t) + 4,
//	        (int) (Now.tv_usec / 1000), code,
//	        data->Feedsite != NULL ? data->Feedsite : "(null)",
//	        HDR_FOUND(HDR__MESSAGE_ID) ? HDR(HDR__MESSAGE_ID) : "(null)",
//	        text, Done ? "" : "\n");
//
// code is ART_REJECT ('-'), the Feedsite our peer label (innd takes the first Path element or
// the host name of the channel; we have the peer name) and text cp->Error, the reply innd sent,
// code first: "437 Unwanted newsgroup ...". The trailing newline is added by writeLines.
func artRejectLine(t time.Time, peer, messageID string, code int, text string) string {
	reply := fmt.Sprintf("%d", code)
	if txt := artText(messageID, text); txt != "" {
		reply += " " + txt
	}
	return fmt.Sprintf("%s %c %s %s %s", artTime(t), artRejectCode, logLabel(peer), artMessageID(messageID), reply)
}

// artRejected reports whether code is a rejection that goes into the article log.
func artRejected(code int) bool { return code == 437 || code == 439 }

// Article queues the article log line of one rejected article. It does no I/O (api.go).
func (s *Service) Article(p PeerRef, messageID string, code int, text string) {
	if s.artlog == nil || !artRejected(code) {
		return
	}
	line := artRejectLine(s.now(), p.Name, messageID, code, text)
	s.mu.Lock()
	warn := s.enqueueArtLocked(line)
	s.mu.Unlock()
	if warn {
		log.Printf("[PEERSTATS] the collector is stopped: dropping further article log lines")
	}
}

// enqueueArtLocked appends one article log line. The caller holds s.mu. It returns true when
// this is the first line dropped because the log file is already closed.
func (s *Service) enqueueArtLocked(line string) bool {
	if s.logClosed {
		s.artAfterStop++
		return s.artAfterStop == 1
	}
	if len(s.artq) >= maxArtQueue {
		s.artDropped++
		return false
	}
	s.artq = append(s.artq, line)
	return false
}

// drainArtLog writes every queued article log line. It is called from the background goroutine
// and from Stop, never from an event method. A file that logrotate moved away is reopened
// before lines are written, so they do not go into the rotated file (which may be compressed
// already); the checkpoint tick checks it as well.
func (s *Service) drainArtLog() {
	if s.artlog == nil {
		return
	}
	s.mu.Lock()
	q := s.artq
	s.artq = nil
	dropped := s.artDropped
	s.artDropped = 0
	s.mu.Unlock()
	if dropped > 0 {
		log.Printf("[PEERSTATS] dropped %d article log lines: the queue was full", dropped)
	}
	if len(q) == 0 {
		return
	}
	s.artlog.reopenIfRotated()
	s.artlog.writeLines(q)
}

// openArtLog opens (and creates) the article log file at path.
func openArtLog(path string) (*innLog, error) {
	l := &innLog{path: path}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.openLocked(); err != nil {
		return nil, err
	}
	return l, nil
}
