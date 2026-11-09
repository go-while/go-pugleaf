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
	// direction; Closed reuses it for the "<peer>:<n>" channel name. The contract gives
	// Closed no connection identity, so with several connections of one peer open at
	// once the number in a closed line is the newest, not the one that closed.
	lastConn map[pdKey]int64
	nextConn int64 // connections of this process, innd's file descriptor stand-in
	running  bool
	stopped  bool

	stopCh chan struct{}
	done   chan struct{}
}

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
		case <-cpT.C:
			s.innlog.reopenIfRotated()
			s.checkpointLines()
		case <-pruneT.C:
			s.prune()
		}
	}
}

// ---- events (no I/O) ----

// Connected records the start of a connection and writes the INN connect line.
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
	s.mu.Unlock()
	if s.innlog == nil {
		return
	}
	switch d {
	case In:
		// innd/rc.c:662
		s.innlog.innd(now, inndConnectedLine(label, n, true))
	case Out:
		// innfeed logs "<peer>:<n> connected" per connection
		// (innreport_inn.pm:1005 matches it).
		s.innlog.innfeed(now, fmt.Sprintf("%s connected", inndChanName(label, n)))
	}
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

// Closed records the end of a connection and writes the per-connection line.
func (s *Service) Closed(p PeerRef, d Dir, conn Counters, dur time.Duration) {
	now := s.now()
	secs := int64(dur / time.Second)
	if secs < 0 {
		secs = 0
	}
	s.mu.Lock()
	// conn was already counted through Add; only the duration is new.
	s.addLocked(p, d, now, Counters{ConnSeconds: secs})
	k := pdKey{id: p.ID, dir: d}
	n := s.lastConn[k]
	delete(s.lastConn, k)
	label := s.labelLocked(p)
	queue := s.queue[p.ID]
	s.mu.Unlock()
	if s.innlog == nil {
		return
	}
	switch d {
	case In:
		// innd/chan.c:332: the totals of the connection that closed.
		s.innlog.innd(now, inndStatsLine(inndChanName(label, n), "closed", secs, conn))
	case Out:
		// innfeed/host.c:2890: innfeed has no per-connection closed line of this
		// shape; the host checkpoint carries what the connection did.
		s.innlog.innfeed(now, innfeedStatsLine(label, "checkpoint", secs, conn, queue))
	}
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
	if s.store == nil {
		return
	}
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

// lineReq is one log line to write, built under the mutex and written without it.
type lineReq struct {
	dir   Dir
	label string
	secs  int64
	c     Counters
	queue int64
	n     int64
}

// takeWindows returns one line request per peer and direction with activity in m, sorted so
// the log is deterministic. reset moves the window's start to now and zeroes its counters.
func (s *Service) takeWindows(m map[pdKey]*window, now time.Time, reset bool) []lineReq {
	s.mu.Lock()
	var reqs []lineReq
	for k, w := range m {
		if w.c.IsZero() {
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

// checkpointLines writes one checkpoint line per peer and direction with activity since the
// last checkpoint, and starts a new window.
func (s *Service) checkpointLines() {
	if s.innlog == nil {
		return
	}
	now := s.now()
	for _, r := range s.takeWindows(s.since, now, true) {
		switch r.dir {
		case In:
			// innd/nc.c:275
			s.innlog.innd(now, inndStatsLine(inndChanName(r.label, r.n), "checkpoint", r.secs, r.c))
		case Out:
			// innfeed/host.c:2890
			s.innlog.innfeed(now, innfeedStatsLine(r.label, "checkpoint", r.secs, r.c, r.queue))
		}
	}
}

// finalLines writes the totals of this process at Stop: an innd checkpoint per incoming peer
// and innfeed's "<peer> global" per outgoing peer, plus innfeed's "ME global" process total.
func (s *Service) finalLines() {
	if s.innlog == nil {
		return
	}
	now := s.now()
	var me Counters
	var meSecs int64
	for _, r := range s.takeWindows(s.total, now, true) {
		switch r.dir {
		case In:
			// innd/chan.c:321
			s.innlog.innd(now, inndStatsLine(inndChanName(r.label, r.n), "checkpoint", r.secs, r.c))
		case Out:
			// innfeed/host.c:2005 / :2472
			s.innlog.innfeed(now, innfeedGlobalLine(logLabel(r.label), r.secs, r.c))
			me.Add(r.c)
			if r.secs > meSecs {
				meSecs = r.secs
			}
		}
	}
	if !me.IsZero() {
		// innfeed/host.c:2021: the process total under the literal name "ME".
		s.innlog.innfeed(now, innfeedGlobalLine("ME", meSecs, me))
	}
}
