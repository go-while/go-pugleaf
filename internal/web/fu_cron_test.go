package web

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// fu_cron_test.go covers C2 of the web-db-followups plan: loadAndStartJobs did not re-check
// stopChannel between jobs, so a job started while StopCronManager was taking its jobIDs
// snapshot was never stopped and db.WG.Done() ran while that job's goroutine was still
// executing.
//
// TestLo2ServerCronLoopSurvivesLoadError covers the loop around loadAndStartJobs; these tests
// only drive loadAndStartJobs and startJob directly, so none of them takes a db.WG slot.

// fuCronManager returns a cron manager that is not started: no reload loop, no db.WG slot, and
// loadJobs returns n enabled jobs with IDs no cron_jobs row has, so a job that does get started
// exits in runJobScheduler (GetCronJobByID fails) without ever executing a command.
func fuCronManager(t *testing.T, n int) *CronJobManager {
	t.Helper()
	jobs := make([]*models.CronJob, 0, n)
	for i := 0; i < n; i++ {
		jobs = append(jobs, &models.CronJob{
			ID:              9000001 + int64(i),
			Name:            "fuCron-job",
			Command:         "true",
			IntervalMinutes: 1440,
			Enabled:         true,
		})
	}
	cm := &CronJobManager{
		db:          w0DB(t),
		jobs:        make(map[int64]*CronJob),
		stopChannel: make(chan struct{}),
		reloadEvery: time.Hour,
		loadJobs:    func() ([]*models.CronJob, error) { return jobs, nil },
	}
	return cm
}

// fuCronJobIDs returns the IDs the manager currently holds.
func fuCronJobIDs(cm *CronJobManager) []int64 {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	ids := make([]int64, 0, len(cm.jobs))
	for id := range cm.jobs {
		ids = append(ids, id)
	}
	return ids
}

// TestFuCronLoadAndStartJobsStopsOnStopChannel: once StopCronManager has closed stopChannel,
// loadAndStartJobs must not start any further job. Before C2 it started every job of the list
// it had already loaded, and a job registered after StopCronManager's jobIDs snapshot is never
// stopped while db.WG.Done() runs (C2).
func TestFuCronLoadAndStartJobsStopsOnStopChannel(t *testing.T) {
	logs := lo2ServerCaptureLog(t)
	cm := fuCronManager(t, 3)

	// A job StopCronManager's snapshot already holds, so the assertion below is about the
	// jobs loadAndStartJobs would add, not about an empty map.
	cm.mutex.Lock()
	cm.jobs[42] = &CronJob{ID: 42, Name: "fuCron-snapshotted", stopChan: make(chan struct{})}
	cm.mutex.Unlock()

	close(cm.stopChannel)
	if err := cm.loadAndStartJobs(); err != nil {
		t.Fatalf("loadAndStartJobs after the stop = %v, want nil", err)
	}

	if ids := fuCronJobIDs(cm); len(ids) != 1 || ids[0] != 42 {
		t.Fatalf("jobs after the stop = %v, want only the snapshotted 42: loadAndStartJobs started %d job(s) that StopCronManager will never stop",
			ids, len(ids)-1)
	}
	if !strings.Contains(logs.String(), "[CRON] stop requested") {
		t.Errorf("the skipped jobs were not logged:\n%s", logs.String())
	}
}

// TestFuCronStartJobRefusesAfterStop: the same gate one level down, where it is serialized
// with StopCronManager's snapshot by cm.mutex.
func TestFuCronStartJobRefusesAfterStop(t *testing.T) {
	cm := fuCronManager(t, 1)
	close(cm.stopChannel)

	err := cm.startJob(&models.CronJob{ID: 9000001, Name: "fuCron-job", Command: "true", IntervalMinutes: 1440, Enabled: true})
	if !errors.Is(err, errCronStopping) {
		t.Fatalf("startJob after the stop = %v, want errCronStopping", err)
	}
	if ids := fuCronJobIDs(cm); len(ids) != 0 {
		t.Fatalf("startJob registered %v after the stop", ids)
	}
}

// TestFuCronStartJobLosesRaceToStopSnapshot drives the window C2 is about: loadAndStartJobs
// passes its stop check while the manager is still running and reaches startJob, which then
// has to wait for cm.mutex - the lock StopCronManager takes for its jobIDs snapshot after it
// closed stopChannel. Holding that lock here reproduces the interleaving deterministically:
// whatever startJob does once it gets the lock, the job must not end up registered.
func TestFuCronStartJobLosesRaceToStopSnapshot(t *testing.T) {
	cm := fuCronManager(t, 1)

	loaded := make(chan struct{})
	jobs, _ := cm.loadJobs()
	cm.loadJobs = func() ([]*models.CronJob, error) {
		close(loaded)
		return jobs, nil
	}

	cm.mutex.Lock() // StopCronManager is about to take this for its snapshot
	done := make(chan error, 1)
	go func() { done <- cm.loadAndStartJobs() }()
	<-loaded
	time.Sleep(50 * time.Millisecond) // let the goroutine reach startJob and park on the lock
	close(cm.stopChannel)             // StopCronManager closes stopChannel before it locks
	cm.mutex.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("loadAndStartJobs = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("loadAndStartJobs did not return")
	}
	if ids := fuCronJobIDs(cm); len(ids) != 0 {
		t.Fatalf("job %v was registered after stopChannel was closed: StopCronManager's snapshot cannot stop it", ids)
	}
}

// TestFuCronLoadAndStartJobsStartsWhileRunning is the control: while the manager runs, the new
// stop checks must not keep enabled jobs from starting (the -no-cronjobs e2e cases E19/E23 and
// every real start go through here).
func TestFuCronLoadAndStartJobsStartsWhileRunning(t *testing.T) {
	cm := fuCronManager(t, 3)
	t.Cleanup(func() { close(cm.stopChannel) })

	if err := cm.loadAndStartJobs(); err != nil {
		t.Fatalf("loadAndStartJobs = %v, want nil", err)
	}
	if ids := fuCronJobIDs(cm); len(ids) != 3 {
		t.Fatalf("jobs = %v, want all 3 started", ids)
	}
	// A second round must not start them twice (ErrCronExists) and must not fail.
	if err := cm.loadAndStartJobs(); err != nil {
		t.Fatalf("second loadAndStartJobs = %v, want nil", err)
	}
	if ids := fuCronJobIDs(cm); len(ids) != 3 {
		t.Fatalf("jobs after the second round = %v, want the same 3", ids)
	}
}
