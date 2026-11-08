package web

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
)

// Global maintenance jobs of the CFG tab, run "hot" in the web server: FNA (newsgroup last
// activity from the newest visible article) and FTA (thread last activity from the newest
// article of each thread) over every newsgroup with articles. The per-group buttons of the
// groups tab and the offline webserver flag -update-newsgroups-activity stay as they are.
// One job runs at a time per web process, a few groups in parallel; it stops on the Stop
// button and on Shutdown.

const (
	maintKindFNA = "FNA"
	maintKindFTA = "FTA"

	// maintWorkers is how many newsgroups a job processes at the same time.
	maintWorkers = 4
)

const query_maintGroups = "SELECT id, name FROM newsgroups WHERE COALESCE(message_count, 0) > 0 ORDER BY name"
const query_maintFNALatest = "SELECT MAX(date_sent) FROM articles WHERE hide = 0"
const query_maintFNAUpdate = "UPDATE newsgroups SET updated_at = ? WHERE id = ? AND (updated_at IS NULL OR updated_at != ?)"

// maintenanceJob is the state of the global maintenance job of one web server.
type maintenanceJob struct {
	mu       sync.Mutex
	running  bool
	kind     string
	total    int
	started  time.Time
	finished time.Time
	stopped  bool
	stop     chan struct{} // closed by the Stop button
	done     chan struct{} // closed when the job goroutine has returned

	processed atomic.Int64
	updated   atomic.Int64
	failed    atomic.Int64
}

// MaintenanceStatus is what the CFG tab shows about the last or running job.
type MaintenanceStatus struct {
	Kind      string
	Running   bool
	Stopped   bool
	Total     int
	Processed int64
	Updated   int64
	Failed    int64
	Started   time.Time
	Finished  time.Time
}

// maintenanceStatus returns the current job state; Kind is "" when no job ran yet.
func (s *WebServer) maintenanceStatus() MaintenanceStatus {
	j := &s.maint
	j.mu.Lock()
	defer j.mu.Unlock()
	return MaintenanceStatus{
		Kind: j.kind, Running: j.running, Stopped: j.stopped, Total: j.total,
		Processed: j.processed.Load(), Updated: j.updated.Load(), Failed: j.failed.Load(),
		Started: j.started, Finished: j.finished,
	}
}

type maintGroup struct {
	id   int64
	name string
}

// startMaintenance starts a job of kind unless one is running; it returns an error message
// for the admin otherwise.
func (s *WebServer) startMaintenance(kind string) error {
	j := &s.maint
	j.mu.Lock()
	if j.running {
		running := j.kind
		j.mu.Unlock()
		return fmt.Errorf("%s is still running: stop it or wait until it has finished", running)
	}
	groups, err := s.maintGroups()
	if err != nil {
		j.mu.Unlock()
		return err
	}
	j.running, j.kind, j.total, j.stopped = true, kind, len(groups), false
	j.started, j.finished = time.Now(), time.Time{}
	j.stop, j.done = make(chan struct{}), make(chan struct{})
	j.processed.Store(0)
	j.updated.Store(0)
	j.failed.Store(0)
	stop, done := j.stop, j.done
	j.mu.Unlock()

	log.Printf("[WEB-MAINT] %s started over %d newsgroups", kind, len(groups))
	go s.runMaintenance(kind, groups, stop, done)
	return nil
}

// stopMaintenance asks the running job to stop after the groups in progress; false when none runs.
func (s *WebServer) stopMaintenance() bool {
	j := &s.maint
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running || j.stopped {
		return false
	}
	j.stopped = true
	close(j.stop)
	return true
}

// waitMaintenance waits at most d for a running job to return (Shutdown).
func (s *WebServer) waitMaintenance(d time.Duration) {
	j := &s.maint
	j.mu.Lock()
	done := j.done
	running := j.running
	j.mu.Unlock()
	if !running || done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(d):
		log.Printf("[WEB-MAINT] the maintenance job did not stop within %v", d)
	}
}

func (s *WebServer) maintGroups() ([]maintGroup, error) {
	rows, err := database.RetryableQuery(s.DB.GetMainDB(), query_maintGroups)
	if err != nil {
		return nil, fmt.Errorf("failed to list the newsgroups: %w", err)
	}
	defer rows.Close()
	var groups []maintGroup
	for rows.Next() {
		var g maintGroup
		if err := rows.Scan(&g.id, &g.name); err != nil {
			return nil, fmt.Errorf("failed to read the newsgroups: %w", err)
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func (s *WebServer) runMaintenance(kind string, groups []maintGroup, stop <-chan struct{}, done chan struct{}) {
	j := &s.maint
	defer close(done)
	work := make(chan maintGroup)
	var wg sync.WaitGroup
	for range maintWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for g := range work {
				changed, err := s.maintOneGroup(kind, g)
				if err != nil {
					j.failed.Add(1)
					log.Printf("[WEB-MAINT] %s %s: %v", kind, g.name, err)
				} else if changed > 0 {
					j.updated.Add(1)
				}
				if n := j.processed.Add(1); n%1000 == 0 {
					log.Printf("[WEB-MAINT] %s: %d/%d newsgroups, %d updated", kind, n, len(groups), j.updated.Load())
				}
			}
		}()
	}
feed:
	for _, g := range groups {
		select {
		case <-stop:
			break feed
		case <-s.stopCh:
			break feed
		case work <- g:
		}
	}
	close(work)
	wg.Wait()

	if kind == maintKindFNA && j.updated.Load() > 0 {
		// The group lists show cached activity dates that only ever move forward: rebuild them.
		if err := s.DB.UpdateHierarchyCounts(); err != nil {
			log.Printf("[WEB-MAINT] FNA: updating the hierarchy counts: %v", err)
		}
		if s.DB.HierarchyCache != nil {
			s.DB.HierarchyCache.InvalidateAll()
			go s.DB.HierarchyCache.WarmCache(s.DB)
		}
	}

	j.mu.Lock()
	j.running = false
	j.finished = time.Now()
	stopped := j.stopped
	j.mu.Unlock()
	state := "finished"
	if stopped {
		state = "stopped"
	}
	log.Printf("[WEB-MAINT] %s %s after %v: %d/%d newsgroups, %d updated, %d failed",
		kind, state, time.Since(j.started).Round(time.Second), j.processed.Load(), len(groups), j.updated.Load(), j.failed.Load())
}

// maintOneGroup runs kind on one newsgroup and returns how much it changed (rows).
func (s *WebServer) maintOneGroup(kind string, g maintGroup) (int, error) {
	groupDB, err := s.DB.GetGroupDB(g.name)
	if err != nil {
		return 0, fmt.Errorf("opening the group database: %w", err)
	}
	// Close it again when nothing else uses it: a job walks every group, and keeping them
	// all open would pile up file handles.
	defer func() {
		if err := s.DB.ForceCloseGroupDB(groupDB); err != nil {
			log.Printf("[WEB-MAINT] closing %s: %v", g.name, err)
		}
	}()
	switch kind {
	case maintKindFNA:
		var latest sql.NullString
		if err := database.RetryableQueryRowScan(groupDB.DB, query_maintFNALatest, nil, &latest); err != nil {
			return 0, err
		}
		if !latest.Valid {
			return 0, nil
		}
		res, err := database.RetryableExec(s.DB.GetMainDB(), query_maintFNAUpdate, latest.String, g.id, latest.String)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		return int(n), err
	case maintKindFTA:
		return s.fixThreadActivityInDB(groupDB, g.name)
	}
	return 0, fmt.Errorf("unknown maintenance job %q", kind)
}

// adminMaintenanceStart starts the global FNA or FTA job (form field "job").
func (s *WebServer) adminMaintenanceStart(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)
	kind := c.PostForm("job")
	if kind != maintKindFNA && kind != maintKindFTA {
		session.SetError("Unknown maintenance job")
		c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
		return
	}
	if err := s.startMaintenance(kind); err != nil {
		session.SetError(err.Error())
	} else {
		session.SetSuccess(kind + " started for all newsgroups; the progress is shown in the maintenance section")
	}
	c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
}

// adminMaintenanceStop stops the running global job.
func (s *WebServer) adminMaintenanceStop(c *Context) {
	if !s.requireAdminAuth(c) {
		return
	}
	session := s.getWebSession(c)
	if s.stopMaintenance() {
		session.SetSuccess("Maintenance job stops after the newsgroups in progress")
	} else {
		session.SetError("No maintenance job is running")
	}
	c.Redirect(http.StatusSeeOther, "/admin?tab=settings")
}
