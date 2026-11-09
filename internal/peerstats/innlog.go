package peerstats

// The optional INN-style log file (plan peering-stats): the lines are byte-for-byte the lines
// innd and innfeed write into news.notice, wrapped in the syslog header, so INN's innreport
// parses our log file unchanged. Every format string below is copied from the INN source in
// /tank0/efs/GO/src/github.com/go-while/inn with the C verbs adapted to Go (%lu/%llu/%u/%ld
// -> %d, %.0f on an integral value -> %d), and the comment above it names file:line.
//
// Fields go-pugleaf has no counter for are written as 0, which is what innreport expects of a
// feeder that does not spool (it only adds them up).

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// innfeedHighwater is innfeed's default host-queue-highwater (innfeed/innfeed.h:52,
// "#define HOST_HIGHWATER 10"), the denominator of the queue histogram below.
const innfeedHighwater = 10

// logLabel is the peer name as it goes into a log line. innd and innfeed write one
// whitespace-free token there (the peer name of incoming.conf / innfeed.conf), and
// innreport's regexps match on \S+, so a name with a space in it would shift every field.
func logLabel(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return UnknownPeer.Name
	}
	return strings.Join(strings.Fields(name), "_")
}

// syslogLine wraps msg the way syslogd writes innd's and innfeed's notices into news.notice:
// "Mmm _d hh:mm:ss <host> <tag>[<pid>]: <msg>". innreport skips that header and parses msg.
func syslogLine(t time.Time, host, tag string, pid int, msg string) string {
	return fmt.Sprintf("%s %s %s[%d]: %s", t.Format("Jan _2 15:04:05"), host, tag, pid, msg)
}

// inndConnectedLine is innd's notice when a peer opens a channel.
//
// innd/rc.c:662: notice("%s connected %d streaming %s", name ? name : addr, new->fd,
//
//	(!StreamingOff && new->Streaming) ? "allowed" : "not allowed");
//
// n takes the place of the file descriptor: a counter of the connections of this process,
// so that "<peer> connected <n>" and the "<peer>:<n> closed ..." line below pair up the way
// innreport (scripts/innreport_inn.pm:665) expects.
func inndConnectedLine(peer string, n int64, streaming bool) string {
	allowed := "not allowed"
	if streaming {
		allowed = "allowed"
	}
	return fmt.Sprintf("%s connected %d streaming %s", logLabel(peer), n, allowed)
}

// inndStatsLine is innd's incoming checkpoint and closed line. status is "checkpoint" or
// "closed", name the channel name, which is "<host>:<fd>" (CHANname, innd/chan.c:514) for an
// NNTP channel -- innreport_inn.pm:689 matches "\S+:\d+" there.
//
// innd/nc.c:275 (checkpoint, every incominglogfrequency articles) and
// innd/chan.c:321 (checkpoint) / :332 (closed), one format string for all three:
//
//	"%s checkpoint seconds %lu accepted %lu refused %lu rejected %lu duplicate %lu"
//	" accepted size %llu duplicate size %llu rejected size %llu"
func inndStatsLine(name, status string, seconds int64, c Counters) string {
	return fmt.Sprintf("%s %s seconds %d accepted %d refused %d rejected %d duplicate %d"+
		" accepted size %d duplicate size %d rejected size %d",
		name, status, seconds, c.Accepted, c.Refused, c.Rejected, c.Duplicate,
		c.AccBytes, c.DupBytes, c.RejBytes)
}

// inndChanName is the "<host>:<fd>" channel name of innd/chan.c:514 with our connection
// counter in place of the descriptor.
func inndChanName(peer string, n int64) string {
	return fmt.Sprintf("%s:%d", logLabel(peer), n)
}

// innfeedQueueHist is the six percentages of innfeed's "queue %.1f/%u:%.0f,..." field. innfeed
// samples the backlog once a second (gCalcHostBlStat, innfeed/host.c:3831) and prints how
// much of the period the queue was empty, in each quartile below the high water mark, and at
// it; we have one sample per checkpoint, so one of the six is 100 and the rest 0 (an empty
// queue prints "100,0,0,0,0,0", which is what innfeed prints when nothing was ever queued).
func innfeedQueueHist(depth int64) string {
	slot := 0 // blNone
	switch {
	case depth <= 0:
		slot = 0
	case depth >= innfeedHighwater:
		slot = 5 // blFull
	default:
		slot = 1 + int((4*depth)/innfeedHighwater) // blQuartile[0..3]
	}
	var p [6]int
	p[slot] = 100
	return fmt.Sprintf("%d,%d,%d,%d,%d,%d", p[0], p[1], p[2], p[3], p[4], p[5])
}

// innfeedStatsLine is innfeed's per-host checkpoint and final line. status is "checkpoint" or
// "final".
//
// innfeed/host.c:2890 (checkpoint) and :2929 (final), the same fields:
//
//	"%s checkpoint seconds %ld offered %u accepted %u refused %u"
//	" rejected %u missing %u accsize %.0f rejsize %.0f spooled %u"
//	" on_close %u unspooled %u deferred %u/%.1f requeued %u"
//	" queue %.1f/%u:%.0f,%.0f,%.0f,%.0f,%.0f,%.0f"
//
// spooled, on_close, unspooled and requeued are 0: the go-pugleaf sender has a durable queue
// instead of innfeed's tape files. The second deferred field is innfeed's average defer
// length, which we do not sample, so 0.0.
func innfeedStatsLine(peer, status string, seconds int64, c Counters, queue int64) string {
	return fmt.Sprintf("%s %s seconds %d offered %d accepted %d refused %d"+
		" rejected %d missing %d accsize %d rejsize %d spooled %d"+
		" on_close %d unspooled %d deferred %d/%.1f requeued %d"+
		" queue %.1f/%d:%s",
		logLabel(peer), status, seconds, c.Offered, c.Accepted, c.Refused,
		c.Rejected, c.Missing, c.AccBytes, c.RejBytes, 0,
		0, 0, c.Deferred, 0.0, 0,
		float64(queue), innfeedHighwater, innfeedQueueHist(queue))
}

// innfeedGlobalLine is innfeed's per-host total, written when the host goes away, and the
// process total of the same shape under the name "ME".
//
// innfeed/host.c:2005 (host gone) and :2472 (gHostStats), with msgstr of :2002 inlined:
//
//	snprintf(msgstr, sizeof(msgstr), "accsize %.0f rejsize %.0f", ...);
//	notice("%s global seconds %ld offered %u accepted %u refused %u"
//	       " rejected %u missing %u %s spooled %u unspooled %u", ...)
//
// innfeed/host.c:2021 writes the process total with the literal name "ME" and the same
// fields. spooled and unspooled are 0 (see innfeedStatsLine).
func innfeedGlobalLine(peer string, seconds int64, c Counters) string {
	return fmt.Sprintf("%s global seconds %d offered %d accepted %d refused %d"+
		" rejected %d missing %d accsize %d rejsize %d spooled %d unspooled %d",
		peer, seconds, c.Offered, c.Accepted, c.Refused,
		c.Rejected, c.Missing, c.AccBytes, c.RejBytes, 0, 0)
}

// innLog is the append-only log file. A nil *innLog swallows every write, so the collector
// does not have to care whether a log file was configured.
type innLog struct {
	mu   sync.Mutex
	path string
	host string
	pid  int
	f    *os.File
	// fi is the stat of the open file, so a rename (logrotate) is seen as a different
	// inode on the path and the file is reopened. copytruncate needs no reopen: the file
	// is opened O_APPEND, so the next write lands at the new end of the file.
	fi os.FileInfo
}

// openINNLog opens (and creates) the log file at path. host is the hostname of the syslog
// header; it falls back to this machine's name.
func openINNLog(path, host string) (*innLog, error) {
	if strings.TrimSpace(host) == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			host = h
		} else {
			host = "localhost"
		}
	}
	l := &innLog{path: path, host: logLabel(host), pid: os.Getpid()}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.openLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

// openLocked opens the file for appending. The directory has to exist already.
func (l *innLog) openLocked() error {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("peerstats inn log %s: %w", l.path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		if cerr := f.Close(); cerr != nil {
			log.Printf("[PEERSTATS] closing %s after a failed stat: %v", l.path, cerr)
		}
		return fmt.Errorf("peerstats inn log %s: %w", l.path, err)
	}
	l.f, l.fi = f, fi
	return nil
}

// write appends one line for tag ("innd" or "innfeed").
func (l *innLog) write(t time.Time, tag, msg string) {
	if l == nil || msg == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		if err := l.openLocked(); err != nil {
			log.Printf("[PEERSTATS] %v", err)
			return
		}
	}
	if _, err := fmt.Fprintln(l.f, syslogLine(t, l.host, tag, l.pid, msg)); err != nil {
		log.Printf("[PEERSTATS] writing to %s: %v", l.path, err)
	}
}

// innd and innfeed write one line of the respective daemon.
func (l *innLog) innd(t time.Time, msg string)    { l.write(t, "innd", msg) }
func (l *innLog) innfeed(t time.Time, msg string) { l.write(t, "innfeed", msg) }

// reopenIfRotated reopens the file when the path no longer names the open file (logrotate
// moved it away) or cannot be stat'ed. The collector calls it on every checkpoint tick.
func (l *innLog) reopenIfRotated() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		fi, err := os.Stat(l.path)
		if err == nil && os.SameFile(fi, l.fi) {
			return
		}
		if cerr := l.f.Close(); cerr != nil {
			log.Printf("[PEERSTATS] closing the rotated %s: %v", l.path, cerr)
		}
		l.f, l.fi = nil, nil
	}
	if err := l.openLocked(); err != nil {
		log.Printf("[PEERSTATS] reopening %s: %v", l.path, err)
		return
	}
	log.Printf("[PEERSTATS] reopened inn log %s", l.path)
}

// Close closes the file. It is safe to call on a nil *innLog and twice.
func (l *innLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.f
	l.f, l.fi = nil, nil
	if f == nil {
		return nil
	}
	return f.Close()
}
