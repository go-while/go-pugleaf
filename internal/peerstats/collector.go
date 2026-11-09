package peerstats

// The collector (plan peering-stats): Service is the Collector of api.go. The event methods
// only touch in-memory maps under one mutex and never do I/O, so the NNTP command paths and
// the feed sender pay a mutex and a map write per event; one goroutine moves the buckets into
// the Store once a minute, writes the INN-style checkpoint lines once every ten minutes and
// prunes the retention once a day.

import (
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"
)

// Defaults of Config.
const (
	DefaultFlushEvery      = time.Minute
	DefaultCheckpointEvery = 10 * time.Minute
	DefaultRetention       = 90 * 24 * time.Hour
	// pruneEvery is the interval of the retention prune ("once a day").
	pruneEvery = 24 * time.Hour
)

// Config configures New. Every field is optional: without a Store the collector only writes
// log lines, without an INNLogPath it only fills the store, and with neither it is a sink
// that keeps counters nobody reads (use Nop for that instead).
type Config struct {
	// Store takes the hourly buckets. nil: count in memory and never write them.
	Store Store
	// INNLogPath is the INN-style log file. "": no log file.
	INNLogPath string
	// Host is the hostname of the syslog header of the log lines. "": this machine's.
	Host string
	// FlushEvery, CheckpointEvery and Retention default to DefaultFlushEvery,
	// DefaultCheckpointEvery and DefaultRetention.
	FlushEvery      time.Duration
	CheckpointEvery time.Duration
	Retention       time.Duration
	// Now is time.Now in production; the tests pin it.
	Now func() time.Time
}

// withDefaults returns cfg with every unset field filled in.
func (c Config) withDefaults() Config {
	if c.FlushEvery <= 0 {
		c.FlushEvery = DefaultFlushEvery
	}
	if c.CheckpointEvery <= 0 {
		c.CheckpointEvery = DefaultCheckpointEvery
	}
	if c.Retention <= 0 {
		c.Retention = DefaultRetention
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Host == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			c.Host = h
		} else {
			c.Host = "localhost"
		}
	}
	return c
}

// bkey identifies one in-memory bucket: peer, direction, hour. The peer name is not part of
// the key (a rename must not split a bucket); names holds the latest one.
type bkey struct {
	id   int64
	dir  Dir
	hour int64
}

// pdKey identifies one peer and direction, for the two log windows below.
type pdKey struct {
	id  int64
	dir Dir
}

// window are the counters of one peer and direction since start, for a log line: since the
// last checkpoint line (Service.since) or since the first event (Service.total).
type window struct {
	start time.Time
	c     Counters
}

// Service is the Collector of api.go.
type Service struct {
	cfg    Config
	store  Store
	innlog *innLog
	now    func() time.Time

	mu      sync.Mutex
	buckets map[bkey]Counters
	names   map[int64]string // peer id -> latest name
	remotes map[int64]string // peer id -> address of its latest connection
	queue   map[int64]int64  // peer id -> outgoing queue depth (SetQueue)
	since   map[pdKey]*window
	total   map[pdKey]*window
	// lastConn is the connection counter Connected handed out last for a peer and
	// direction; Closed and the checkpoint lines reuse it for the "<peer>:<n>" channel
	// name, and it is kept after the connection closed so a later line of that peer does
	// not fall back to ":0". The contract gives Closed no connection identity, so with
	// several connections of one peer open at once the number in a closed line is the
	// newest, not the one that closed.
	lastConn map[pdKey]int64
	nextConn int64 // connections of this process, innd's file descriptor stand-in
	running  bool
	stopped  bool

	// logq holds the log lines the event methods built: api.go promises they never block
	// on I/O, so they only append here and the background goroutine (and Stop) write the
	// file. The lines carry the time of their event, not of the write.
	logq         []logEntry
	logDropped   int64 // lines dropped because logq was full
	logAfterStop int64 // lines dropped because the log file is already closed
	logClosed    bool

	stopCh chan struct{}
	done   chan struct{}
}

// logEntry is one queued log line.
type logEntry struct {
	t   time.Time
	tag string // "innd" or "innfeed"
	msg string
}

// maxLogQueue bounds logq: a log file on a wedged disk must not grow the heap without end.
// At ~2 lines per connection and per checkpoint this is far more than a minute of traffic.
const maxLogQueue = 4096

// Service is the implementation the Collector contract in api.go describes.
var _ Collector = (*Service)(nil)

// New builds the collector. It opens the INN log file when one is configured; the store is
// opened by the caller (Open) and closed by the caller after Stop.
func New(cfg Config) (*Service, error) {
	cfg = cfg.withDefaults()
	s := &Service{
		cfg:      cfg,
		store:    cfg.Store,
		now:      cfg.Now,
		buckets:  make(map[bkey]Counters),
		names:    make(map[int64]string),
		remotes:  make(map[int64]string),
		queue:    make(map[int64]int64),
		since:    make(map[pdKey]*window),
		total:    make(map[pdKey]*window),
		lastConn: make(map[pdKey]int64),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	if cfg.INNLogPath != "" {
		l, err := openINNLog(cfg.INNLogPath, cfg.Host)
		if err != nil {
			return nil, err
		}
		s.innlog = l
		log.Printf("[PEERSTATS] inn log=%q host=%s", cfg.INNLogPath, cfg.Host)
	}
	return s, nil
}

// Start launches the one background goroutine. It is idempotent and does nothing after Stop.
func (s *Service) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.running || s.stopped {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	go s.loop()
}

// Stop stops the goroutine, flushes what is buffered, writes the final log lines and closes
// the log file. It is idempotent and safe without Start. The store stays open: the caller
// closes it after Stop returned.
func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	running := s.running
	s.mu.Unlock()
	close(s.stopCh)
	if running {
		<-s.done
	}
	s.flush()
	s.finalLines()
	// The last drain: whatever the event methods queued and never got written, plus the
	// final lines. This runs whether or not Start was ever called.
	s.drainLog()
	s.mu.Lock()
	s.logClosed = true
	s.mu.Unlock()
	if err := s.innlog.Close(); err != nil {
		log.Printf("[PEERSTATS] closing the inn log: %v", err)
	}
}

// loop is the one goroutine: flush, checkpoint, prune.
func (s *Service) loop() {
	defer close(s.done)
	flushT := time.NewTicker(s.cfg.FlushEvery)
	defer flushT.Stop()
	cpT := time.NewTicker(s.cfg.CheckpointEvery)
	defer cpT.Stop()
	pruneT := time.NewTicker(pruneEvery)
	defer pruneT.Stop()
	// Prune once at startup: a server that is restarted every day would otherwise never
	// reach the daily tick and keep every bucket for ever.
	s.prune()
	for {
		select {
		case <-s.stopCh:
			return
		case <-flushT.C:
			s.flush()
			s.drainLog()
		case <-cpT.C:
			s.checkpointTick()
		case <-pruneT.C:
			s.prune()
		}
	}
}

// ---- events (no I/O) ----

// Connected records the start of a connection and queues the INN connect line.
func (s *Service) Connected(p PeerRef, d Dir, remote string) {
	now := s.now()
	s.mu.Lock()
	s.nextConn++
	n := s.nextConn
	s.lastConn[pdKey{id: p.ID, dir: d}] = n
	if remote != "" {
		s.remotes[p.ID] = remote
	}
	s.addLocked(p, d, now, Counters{Conns: 1})
	label := s.labelLocked(p)
	var warn bool
	switch d {
	case In:
		// innd/rc.c:662
		warn = s.enqueueLocked(now, "innd", inndConnectedLine(label, n, true))
	case Out:
		// innfeed logs "<peer>:<n> connected" per connection
		// (innreport_inn.pm:1005 matches it).
		warn = s.enqueueLocked(now, "innfeed", fmt.Sprintf("%s connected", inndChanName(label, n)))
	}
	s.mu.Unlock()
	warnAfterStop(warn)
}

// Add adds delta to the current hour of p and d.
func (s *Service) Add(p PeerRef, d Dir, delta Counters) {
	if delta.IsZero() {
		return
	}
	now := s.now()
	s.mu.Lock()
	s.addLocked(p, d, now, delta)
	s.mu.Unlock()
}

// Closed records the end of a connection and queues the per-connection line. Both lines are
// per-connection shapes innreport does not add up (innreport_inn.pm:718, :1022); what it
// sums are the periodic checkpoints, so a connection is never counted twice.
func (s *Service) Closed(p PeerRef, d Dir, conn Counters, dur time.Duration) {
	now := s.now()
	secs := int64(dur / time.Second)
	if secs < 0 {
		secs = 0
	}
	s.mu.Lock()
	// conn was already counted through Add; only the duration is new.
	s.addLocked(p, d, now, Counters{ConnSeconds: secs})
	n := s.lastConn[pdKey{id: p.ID, dir: d}]
	label := s.labelLocked(p)
	var warn bool
	switch d {
	case In:
		// innd/chan.c:332: the totals of the connection that closed.
		warn = s.enqueueLocked(now, "innd", inndStatsLine(inndChanName(label, n), "closed", secs, conn))
	case Out:
		// innfeed/connection.c:976: innfeed's per-connection total. The host-wide
		// "<peer> checkpoint" line of innfeedStatsLine must not be used here: innreport
		// adds every one of those up (innreport_inn.pm:1031), and the periodic
		// checkpoint already carries this connection's articles.
		warn = s.enqueueLocked(now, "innfeed", innfeedConnFinalLine(label, n, secs, conn))
	}
	s.mu.Unlock()
	warnAfterStop(warn)
}

// SetQueue reports the outgoing queue depth of p.
func (s *Service) SetQueue(p PeerRef, depth int64) {
	if depth < 0 {
		depth = 0
	}
	s.mu.Lock()
	s.queue[p.ID] = depth
	if p.Name != "" {
		s.names[p.ID] = p.Name
	}
	s.mu.Unlock()
}

// Queue is the last depth SetQueue reported for p (the admin page reads the store; this is
// for the log lines and for tests).
func (s *Service) Queue(id int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queue[id]
}

// addLocked adds delta to the hour bucket of p and d and to both log windows.
func (s *Service) addLocked(p PeerRef, d Dir, now time.Time, delta Counters) {
	if p.Name != "" {
		s.names[p.ID] = p.Name
	}
	k := bkey{id: p.ID, dir: d, hour: HourOf(now)}
	c := s.buckets[k]
	c.Add(delta)
	s.buckets[k] = c
	pk := pdKey{id: p.ID, dir: d}
	for _, m := range []map[pdKey]*window{s.since, s.total} {
		w := m[pk]
		if w == nil {
			w = &window{start: now}
			m[pk] = w
		}
		w.c.Add(delta)
	}
}

// labelLocked is the log name of p: its peer name, or the address of its latest connection
// when we have no name (feed commands from a host that matches no peer row).
func (s *Service) labelLocked(p PeerRef) string {
	if name := s.names[p.ID]; name != "" && name != UnknownPeer.Name {
		return name
	}
	if r := s.remotes[p.ID]; r != "" {
		return r
	}
	if p.Name != "" {
		return p.Name
	}
	return UnknownPeer.Name
}

// ---- background work ----

// flush moves every buffered bucket into the store. On a write error the buckets go back
// into the map, so the next tick tries them again (merged with whatever arrived meanwhile).
func (s *Service) flush() {
	if s.store == nil {
		// Log-only mode: drop what was counted instead of building and sorting a slice
		// nobody takes, but do empty the map so it cannot grow for ever.
		s.mu.Lock()
		if len(s.buckets) > 0 {
			s.buckets = make(map[bkey]Counters)
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	if len(s.buckets) == 0 {
		s.mu.Unlock()
		return
	}
	bs := make([]Bucket, 0, len(s.buckets))
	for k, c := range s.buckets {
		bs = append(bs, Bucket{
			Peer:     PeerRef{ID: k.id, Name: s.names[k.id]},
			Dir:      k.dir,
			Hour:     k.hour,
			Counters: c,
		})
	}
	s.buckets = make(map[bkey]Counters, len(bs))
	s.mu.Unlock()
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].Hour != bs[j].Hour {
			return bs[i].Hour < bs[j].Hour
		}
		if bs[i].Peer.ID != bs[j].Peer.ID {
			return bs[i].Peer.ID < bs[j].Peer.ID
		}
		return bs[i].Dir < bs[j].Dir
	})
	if err := s.store.AddBuckets(bs); err != nil {
		log.Printf("[PEERSTATS] keeping %d buckets after a failed flush: %v", len(bs), err)
		s.mu.Lock()
		for _, b := range bs {
			k := bkey{id: b.Peer.ID, dir: b.Dir, hour: b.Hour}
			c := s.buckets[k]
			c.Add(b.Counters)
			s.buckets[k] = c
		}
		s.mu.Unlock()
	}
}

// prune drops the buckets that fell out of the retention.
func (s *Service) prune() {
	if s.store == nil {
		return
	}
	before := HourOf(s.now().Add(-s.cfg.Retention))
	n, err := s.store.Prune(before)
	if err != nil {
		log.Printf("[PEERSTATS] prune before hour %d: %v", before, err)
		return
	}
	if n > 0 {
		log.Printf("[PEERSTATS] pruned %d buckets older than %s", n,
			time.Unix(before, 0).UTC().Format(time.RFC3339))
	}
}

// ---- the log line queue ----

// enqueueLocked appends one log line. The caller holds s.mu. It returns true when this is the
// first line dropped because the log file is already closed, so the caller can log that once
// after it unlocked.
func (s *Service) enqueueLocked(t time.Time, tag, msg string) bool {
	if s.innlog == nil || msg == "" {
		return false
	}
	if s.logClosed {
		s.logAfterStop++
		return s.logAfterStop == 1
	}
	if len(s.logq) >= maxLogQueue {
		s.logDropped++
		return false
	}
	s.logq = append(s.logq, logEntry{t: t, tag: tag, msg: msg})
	return false
}

// warnAfterStop logs the one line enqueueLocked asked for.
func warnAfterStop(warn bool) {
	if warn {
		log.Printf("[PEERSTATS] the collector is stopped: dropping further inn log lines")
	}
}

// drainLog writes every queued line. It is called from the background goroutine and from
// Stop, never from an event method.
func (s *Service) drainLog() {
	if s.innlog == nil {
		return
	}
	s.mu.Lock()
	q := s.logq
	s.logq = nil
	dropped := s.logDropped
	s.logDropped = 0
	s.mu.Unlock()
	if dropped > 0 {
		log.Printf("[PEERSTATS] dropped %d inn log lines: the queue was full", dropped)
	}
	for _, e := range q {
		s.innlog.write(e.t, e.tag, e.msg)
	}
}

// checkpointTick is the checkpoint timer's work: notice a log file that was rotated away,
// build the checkpoint lines and write out everything queued, in event order.
func (s *Service) checkpointTick() {
	s.innlog.reopenIfRotated()
	s.checkpointLines()
	s.drainLog()
}

// lineReq is one log line to write, built under the mutex and written without it.
type lineReq struct {
	dir   Dir
	label string
	secs  int64
	c     Counters
	queue int64
	n     int64
}

// hasLogLineContent reports whether c has anything the INN stats lines show. Conns and
// ConnSeconds appear in no field of them, so a window that only saw a connection start or
// end would otherwise produce a line of zeros.
func hasLogLineContent(c Counters) bool {
	c.Conns, c.ConnSeconds = 0, 0
	return !c.IsZero()
}

// takeWindows returns one line request per peer and direction with activity in m, sorted so
// the log is deterministic. reset moves the window's start to now and zeroes its counters.
func (s *Service) takeWindows(m map[pdKey]*window, now time.Time, reset bool) []lineReq {
	s.mu.Lock()
	var reqs []lineReq
	for k, w := range m {
		if !hasLogLineContent(w.c) {
			continue
		}
		secs := int64(now.Sub(w.start) / time.Second)
		if secs < 0 {
			secs = 0
		}
		reqs = append(reqs, lineReq{
			dir:   k.dir,
			label: s.labelLocked(PeerRef{ID: k.id, Name: s.names[k.id]}),
			secs:  secs,
			c:     w.c,
			queue: s.queue[k.id],
			n:     s.lastConn[k],
		})
		if reset {
			w.start = now
			w.c = Counters{}
		}
	}
	s.mu.Unlock()
	sort.Slice(reqs, func(i, j int) bool {
		if reqs[i].dir != reqs[j].dir {
			return reqs[i].dir < reqs[j].dir
		}
		return reqs[i].label < reqs[j].label
	})
	return reqs
}

// checkpointLines queues one checkpoint line per peer and direction with activity since the
// last checkpoint, and starts a new window. These are the lines innreport adds up
// (innreport_inn.pm:689 for innd, :1031 for innfeed), so every article must appear in exactly
// one of them: nothing else ever uses the "checkpoint" shape with cumulative numbers.
func (s *Service) checkpointLines() {
	if s.innlog == nil {
		return
	}
	now := s.now()
	reqs := s.takeWindows(s.since, now, true)
	s.mu.Lock()
	var warn bool
	for _, r := range reqs {
		switch r.dir {
		case In:
			// innd/nc.c:275
			if s.enqueueLocked(now, "innd", inndStatsLine(inndChanName(r.label, r.n), "checkpoint", r.secs, r.c)) {
				warn = true
			}
		case Out:
			// innfeed/host.c:2890
			if s.enqueueLocked(now, "innfeed", innfeedStatsLine(r.label, "checkpoint", r.secs, r.c, r.queue)) {
				warn = true
			}
		}
	}
	s.mu.Unlock()
	warnAfterStop(warn)
}

// finalLines are the lines Stop writes: first the tail since the last checkpoint, as a
// checkpoint line like any other (writing the totals as a second checkpoint would make
// innreport count every article twice), then innfeed's "<peer> global" per outgoing peer and
// its "ME global" process total, which innreport only matches to skip (innreport_inn.pm:1024).
// innd has no process-total line; the per-connection "closed" lines carry the incoming ones.
func (s *Service) finalLines() {
	if s.innlog == nil {
		return
	}
	s.checkpointLines()
	now := s.now()
	reqs := s.takeWindows(s.total, now, true)
	var me Counters
	var meSecs int64
	s.mu.Lock()
	var warn bool
	for _, r := range reqs {
		if r.dir != Out {
			continue
		}
		// innfeed/host.c:2005 / :2472
		if s.enqueueLocked(now, "innfeed", innfeedGlobalLine(logLabel(r.label), r.secs, r.c)) {
			warn = true
		}
		me.Add(r.c)
		if r.secs > meSecs {
			meSecs = r.secs
		}
	}
	if !me.IsZero() {
		// innfeed/host.c:2021: the process total under the literal name "ME".
		if s.enqueueLocked(now, "innfeed", innfeedGlobalLine("ME", meSecs, me)) {
			warn = true
		}
	}
	s.mu.Unlock()
	warnAfterStop(warn)
}
