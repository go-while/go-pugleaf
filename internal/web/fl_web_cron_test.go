package web

// Tests for slice fl-web of plan followup-leftovers, cron items:
//   - an ended job keeps its log until it starts again (bounded: maxEndedJobLogs entries);
//   - calculateNextRun does not divide by zero for an interval below one minute (the job runs
//     back to back, as it already did without a start time);
//   - enabling/disabling is a conditional UPDATE (SetCronJobEnabled): of concurrent toggles
//     exactly one changes the row, the others report "already changed".

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

func TestFlWebCronNextRunZeroInterval(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, interval := range []int{0, -5} {
		// A start time already passed today: run now (before the guard: integer divide by zero).
		for _, start := range []string{"", "00:00", "12:00"} {
			job := &models.CronJob{ID: 7, IntervalMinutes: interval, StartHourMinute: start}
			next, err := calculateNextRun(job, now)
			if err != nil || next.After(now) {
				t.Errorf("interval %d start %q: next run %v err %v, want now or earlier", interval, start, next, err)
			}
		}
		// A start time still ahead today waits for it.
		next, err := calculateNextRun(&models.CronJob{IntervalMinutes: interval, StartHourMinute: "23:59"}, now)
		if err != nil || !next.Equal(time.Date(2026, 10, 15, 23, 59, 0, 0, time.UTC)) {
			t.Errorf("interval %d start 23:59: next %v err %v, want 23:59", interval, next, err)
		}
	}
	// A valid interval still schedules as before.
	next, err := calculateNextRun(&models.CronJob{IntervalMinutes: 60, StartHourMinute: "00:00"}, now)
	if err != nil || !next.Equal(time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("interval 60 from 00:00 at 12:00: next %v err %v, want 13:00", next, err)
	}
}

// TestFlWebCronEndedJobKeepsLog: after the scheduler ends (stop signal) the job's log is still
// served; the next start replaces it with the new run's log.
func TestFlWebCronEndedJobKeepsLog(t *testing.T) {
	cm := NewCronJobManager(w0DB(t)) // not started: no reload loop
	row := adfCronInsert(t, true, 1440)
	t.Cleanup(func() { adfCronStopManager(t, cm) }) // runs before adfCronInsert's row delete

	if err := cm.startJob(row); err != nil {
		t.Fatalf("startJob = %v", err)
	}
	job := adfCronRegistered(cm, row.ID)
	if job == nil || !adfCronWait(func() bool { return adfCronHasLog(job, "Waiting until") }) {
		t.Fatalf("job %d did not reach its wait", row.ID)
	}
	// StopJob only signals a scheduler parked in its wait: repeat until it ended.
	if !adfCronWait(func() bool { _ = cm.StopJob(row.ID); return adfCronRegistered(cm, row.ID) == nil }) {
		t.Fatalf("job %d still registered after the stop signal", row.ID)
	}
	out := strings.Join(cm.GetJobOutput(row.ID), "\n")
	if !strings.Contains(out, "got stop signal") || !strings.Contains(out, "Waiting until") {
		t.Fatalf("output of the ended job = %q, want its log", out)
	}

	if err := cm.startJob(row); err != nil {
		t.Fatalf("second startJob = %v", err)
	}
	if out := strings.Join(cm.GetJobOutput(row.ID), "\n"); strings.Contains(out, "got stop signal") {
		t.Fatalf("after the next start the old log is still served: %q", out)
	}
	cm.mutex.RLock()
	_, kept := cm.ended[row.ID]
	cm.mutex.RUnlock()
	if kept {
		t.Fatalf("the ended log of job %d was not dropped by the next start", row.ID)
	}
}

func TestFlWebCronEndedLogBounded(t *testing.T) {
	cm := &CronJobManager{jobs: make(map[int64]*CronJob)}
	for i := int64(1); i <= maxEndedJobLogs+20; i++ {
		job := &CronJob{ID: i, Output: []string{fmt.Sprintf("line %d", i)}}
		cm.jobs[i] = job
		cm.forgetJob(job)
	}
	if n := len(cm.ended); n != maxEndedJobLogs {
		t.Fatalf("%d ended logs kept, want the bound %d", n, maxEndedJobLogs)
	}
	// The kept log is a copy: later writes to the old job do not reach it.
	job := &CronJob{ID: 1, Output: []string{"kept"}}
	cm.jobs[1] = job
	cm.forgetJob(job)
	job.addLogLine("late")
	if got := cm.GetJobOutput(1); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("GetJobOutput(1) = %q, want [kept]", got)
	}
	// A stale *CronJob (not the registered one) keeps nothing.
	cm.forgetJob(&CronJob{ID: 999999, Output: []string{"stale"}})
	if _, ok := cm.ended[999999]; ok {
		t.Fatal("forgetJob of an unregistered job kept its log")
	}
	cm.forgetEndedOutput(1)
	if got := cm.GetJobOutput(1); len(got) != 1 || !strings.Contains(got[0], "No output") {
		t.Fatalf("after forgetEndedOutput: %q", got)
	}
	var nilCM *CronJobManager
	nilCM.forgetEndedOutput(1) // -no-cronjobs: no panic
}

// TestFlWebCronSetEnabledConditional: the conditional UPDATE lets exactly one of several
// concurrent writers flip the row.
func TestFlWebCronSetEnabledConditional(t *testing.T) {
	db := w0DB(t)
	row := adfCronInsert(t, true, 1440)
	const writers = 8
	var wg sync.WaitGroup
	results := make(chan bool, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := db.SetCronJobEnabled(row.ID, false, true)
			if err != nil {
				t.Errorf("SetCronJobEnabled: %v", err)
			}
			results <- changed
		}()
	}
	wg.Wait()
	close(results)
	n := 0
	for changed := range results {
		if changed {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d of %d concurrent disables changed the row, want exactly 1", n, writers)
	}
	if adfCronEnabled(t, row.ID) {
		t.Fatal("job still enabled")
	}
	// From the wrong state nothing changes; ToggleCronJob still flips.
	if changed, err := db.SetCronJobEnabled(row.ID, false, true); err != nil || changed {
		t.Fatalf("disable of a disabled job: changed=%v err=%v, want false/nil", changed, err)
	}
	if changed, err := db.SetCronJobEnabled(987654321, true, false); err != nil || changed {
		t.Fatalf("unknown id: changed=%v err=%v, want false/nil", changed, err)
	}
	if err := db.ToggleCronJob(row.ID); err != nil || !adfCronEnabled(t, row.ID) {
		t.Fatalf("ToggleCronJob = %v, enabled=%v, want nil/true", err, adfCronEnabled(t, row.ID))
	}
	if err := db.ToggleCronJob(row.ID); err != nil || adfCronEnabled(t, row.ID) {
		t.Fatalf("ToggleCronJob back = %v", err)
	}
	if database.ErrCronJobChanged == nil {
		t.Fatal("ErrCronJobChanged is nil")
	}
}

// TestFlWebCronToggleConcurrentAdmins: concurrent toggle requests of one enabled job (without
// -edit-cronjobs, so nothing may enable it again): exactly one request reports success, the
// others report the change or the -edit-cronjobs refusal, and the job ends disabled.
func TestFlWebCronToggleConcurrentAdmins(t *testing.T) {
	adfCronSetEdit(t, false)
	row := adfCronInsert(t, true, 1440)
	const admins = 6
	cookies := make([]*http.Cookie, admins)
	for i := range cookies {
		_, cookies[i] = w0NewUser(t, true)
	}
	var wg sync.WaitGroup
	for _, ck := range cookies {
		wg.Add(1)
		go func(ck *http.Cookie) {
			defer wg.Done()
			rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/crons/toggle", Cookies: []*http.Cookie{ck},
				Form: url.Values{"cron_id": {fmt.Sprint(row.ID)}}})
			if rec.Code != http.StatusSeeOther {
				t.Errorf("toggle = %d, want 303", rec.Code)
			}
		}(ck)
	}
	wg.Wait()
	ok := 0
	for _, ck := range cookies {
		success, _ := GetAndClearFlash(ck.Value, "success")
		_, failure := GetAndClearFlash(ck.Value, "error")
		switch {
		case success != "":
			ok++
		case strings.Contains(failure, "already changed"), strings.Contains(failure, "-edit-cronjobs"):
		default:
			t.Errorf("unexpected flash: success %q error %q", success, failure)
		}
	}
	if ok != 1 {
		t.Errorf("%d toggles reported success, want 1", ok)
	}
	if adfCronEnabled(t, row.ID) {
		t.Fatal("job enabled again after concurrent disables without -edit-cronjobs")
	}
}
