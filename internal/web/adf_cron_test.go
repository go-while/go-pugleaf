package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// adf_cron_test.go covers slice adf-web-cron of the audit-delta-fixes plan:
//   - WEB-1 (D7): without -edit-cronjobs an admin can neither create a cron job nor enable a
//     disabled one; disabling stays allowed, and the admin page hides the create form.
//   - A-web-20: a scheduler that ends removes its registration (forgetJob), so a re-enabled
//     job can be started again instead of answering ErrCronExists until a restart.
//   - A-web-17: the log view applies its line limit after the filter.

// adfCronSetEdit sets w0Srv.CronEdit for one test and restores it afterwards.
func adfCronSetEdit(t *testing.T, v bool) {
	t.Helper()
	old := w0Srv.CronEdit
	w0Srv.CronEdit = v
	t.Cleanup(func() { w0Srv.CronEdit = old })
}

// adfCronFind returns the cron_jobs row named name, or nil.
func adfCronFind(t *testing.T, name string) *models.CronJob {
	t.Helper()
	jobs, err := w0DB(t).GetAllCronJobs()
	if err != nil {
		t.Fatalf("GetAllCronJobs: %v", err)
	}
	for _, j := range jobs {
		if j.Name == name {
			return j
		}
	}
	return nil
}

// adfCronDeleteByName removes the row named name if it exists (test cleanup).
func adfCronDeleteByName(t *testing.T, name string) {
	t.Helper()
	if j := adfCronFind(t, name); j != nil {
		if err := w0DB(t).DeleteCronJob(j.ID); err != nil {
			t.Errorf("DeleteCronJob(%d): %v", j.ID, err)
		}
	}
}

// adfCronInsert inserts a cron job row (command "true") and deletes it in cleanup.
func adfCronInsert(t *testing.T, enabled bool, intervalMin int) *models.CronJob {
	t.Helper()
	name := w0Name("adfcron")
	err := w0DB(t).InsertCronJob(&models.CronJob{
		Name:            name,
		Command:         "true",
		IntervalMinutes: intervalMin,
		Enabled:         enabled,
	})
	if err != nil {
		t.Fatalf("InsertCronJob: %v", err)
	}
	t.Cleanup(func() { adfCronDeleteByName(t, name) })
	j := adfCronFind(t, name)
	if j == nil {
		t.Fatalf("cron job %s not found after insert", name)
	}
	return j
}

// adfCronSetEnabled writes the enabled flag of a row directly.
func adfCronSetEnabled(t *testing.T, job *models.CronJob, enabled bool) {
	t.Helper()
	row, err := w0DB(t).GetCronJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetCronJobByID(%d): %v", job.ID, err)
	}
	row.Enabled = enabled
	if err := w0DB(t).UpdateCronJob(row); err != nil {
		t.Fatalf("UpdateCronJob(%d): %v", job.ID, err)
	}
}

// adfCronEnabled reads the enabled flag of a row.
func adfCronEnabled(t *testing.T, id int64) bool {
	t.Helper()
	row, err := w0DB(t).GetCronJobByID(id)
	if err != nil {
		t.Fatalf("GetCronJobByID(%d): %v", id, err)
	}
	return row.Enabled
}

// adfCronFlashError returns (and clears) the pending error flash of the session cookie.
func adfCronFlashError(cookie *http.Cookie) string {
	_, msg := GetAndClearFlash(cookie.Value, "error")
	return msg
}

// adfCronRegistered returns the registration of id, or nil.
func adfCronRegistered(cm *CronJobManager, id int64) *CronJob {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	return cm.jobs[id]
}

// adfCronWait polls cond every 10 ms for at most testWait (a hang guard: every caller waits for
// something that does happen in a passing run).
func adfCronWait(cond func() bool) bool {
	deadline := time.Now().Add(testWait)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// adfCronHasLog reports whether a line of the job's output contains s.
func adfCronHasLog(job *CronJob, s string) bool {
	job.mutex.RLock()
	defer job.mutex.RUnlock()
	for _, l := range job.Output {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// adfCronStopManager stops a manager the test created (never started with
// StartCronManager): it takes the MainWG slot StopCronManager releases, then keeps signalling
// the remaining schedulers until every one has ended and forgotten its job.
func adfCronStopManager(t *testing.T, cm *CronJobManager) {
	t.Helper()
	cm.db.WG.Add(1)
	cm.StopCronManager()
	ok := adfCronWait(func() bool {
		for _, id := range fuCronJobIDs(cm) {
			_ = cm.StopJob(id) // a scheduler that was not parked yet gets the next signal
		}
		return len(fuCronJobIDs(cm)) == 0
	})
	if !ok {
		t.Errorf("schedulers still registered after stop: %v", fuCronJobIDs(cm))
	}
}

func TestAdfCronCreateRefusedWithoutEdit(t *testing.T) {
	adfCronSetEdit(t, false)
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfcron")
	t.Cleanup(func() { adfCronDeleteByName(t, name) })

	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/crons/create", Cookies: []*http.Cookie{cookie},
		Form: url.Values{"name": {name}, "command": {"true"}, "interval_minutes": {"60"}, "enabled": {"on"}}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create without -edit-cronjobs = %d, want 303", rec.Code)
	}
	if msg := adfCronFlashError(cookie); !strings.Contains(msg, "-edit-cronjobs") {
		t.Errorf("flash error = %q, want the cronEditRefused message", msg)
	}
	if j := adfCronFind(t, name); j != nil {
		t.Fatalf("cron job %q (id %d, enabled %v) was created without -edit-cronjobs", name, j.ID, j.Enabled)
	}
}

func TestAdfCronCreateAllowedWithEdit(t *testing.T) {
	adfCronSetEdit(t, true)
	_, cookie := w0NewUser(t, true)
	name := w0Name("adfcron")
	t.Cleanup(func() { adfCronDeleteByName(t, name) })

	// enabled off: the shared server's cron manager must not pick the row up and run it.
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/crons/create", Cookies: []*http.Cookie{cookie},
		Form: url.Values{"name": {name}, "command": {"true"}, "interval_minutes": {"60"}}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create with -edit-cronjobs = %d, want 303", rec.Code)
	}
	if msg := adfCronFlashError(cookie); msg != "" {
		t.Errorf("flash error = %q, want none", msg)
	}
	if adfCronFind(t, name) == nil {
		t.Fatalf("cron job %q was not created with -edit-cronjobs", name)
	}
}

func TestAdfCronToggleEnableRefused(t *testing.T) {
	adfCronSetEdit(t, false)
	_, cookie := w0NewUser(t, true)

	disabled := adfCronInsert(t, false, 1440)
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/crons/toggle", Cookies: []*http.Cookie{cookie},
		Form: url.Values{"cron_id": {fmt.Sprint(disabled.ID)}}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle = %d, want 303", rec.Code)
	}
	if msg := adfCronFlashError(cookie); !strings.Contains(msg, "-edit-cronjobs") {
		t.Errorf("flash error = %q, want the cronEditRefused message", msg)
	}
	if adfCronEnabled(t, disabled.ID) {
		t.Fatalf("disabled cron job %d was enabled without -edit-cronjobs", disabled.ID)
	}

	// Disabling stays allowed (D7).
	enabled := adfCronInsert(t, true, 1440)
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/crons/toggle", Cookies: []*http.Cookie{cookie},
		Form: url.Values{"cron_id": {fmt.Sprint(enabled.ID)}}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle = %d, want 303", rec.Code)
	}
	if msg := adfCronFlashError(cookie); msg != "" {
		t.Errorf("flash error on disable = %q, want none", msg)
	}
	if adfCronEnabled(t, enabled.ID) {
		t.Fatalf("enabled cron job %d is still enabled: disabling must stay allowed", enabled.ID)
	}

	// An unknown id is reported, nothing is toggled.
	rec = w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/crons/toggle", Cookies: []*http.Cookie{cookie},
		Form: url.Values{"cron_id": {"987654321"}}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle of an unknown id = %d, want 303", rec.Code)
	}
	if msg := adfCronFlashError(cookie); msg != "Cron job not found" {
		t.Errorf("flash error for an unknown id = %q, want %q", msg, "Cron job not found")
	}
}

func TestAdfCronToggleEnableAllowedWithEdit(t *testing.T) {
	adfCronSetEdit(t, true)
	_, cookie := w0NewUser(t, true)
	job := adfCronInsert(t, false, 1440)
	t.Cleanup(func() { adfCronSetEnabled(t, job, false) })

	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/crons/toggle", Cookies: []*http.Cookie{cookie},
		Form: url.Values{"cron_id": {fmt.Sprint(job.ID)}}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle = %d, want 303", rec.Code)
	}
	if !adfCronEnabled(t, job.ID) {
		t.Fatalf("cron job %d was not enabled with -edit-cronjobs (flash %q)", job.ID, adfCronFlashError(cookie))
	}
}

func TestAdfCronAdminPageWithoutEdit(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	job := adfCronInsert(t, false, 1440)
	const createForm = `action="/admin/crons/create"`
	enableBtn := regexp.MustCompile(`name="cron_id" value="` + fmt.Sprint(job.ID) + `">\s*<button[^>]*title="Enable"`)

	adfCronSetEdit(t, false)
	rec := w0Do(t, w0Req{Path: "/admin?tab=crons", Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin?tab=crons = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, job.Name) {
		t.Fatalf("crons tab does not list job %q", job.Name)
	}
	if strings.Contains(body, createForm) {
		t.Errorf("crons tab without -edit-cronjobs contains the create form")
	}
	if enableBtn.MatchString(body) {
		t.Errorf("crons tab without -edit-cronjobs shows the enable button of job %d", job.ID)
	}
	// The card header always mentions the flag; the {{else}} hint is the warning alert.
	if !strings.Contains(body, `alert alert-warning mb-0`) {
		t.Errorf("crons tab without -edit-cronjobs does not show the hint alert")
	}

	// Control: with the flag both are shown, so the assertions above test the gate.
	w0Srv.CronEdit = true
	rec = w0Do(t, w0Req{Path: "/admin?tab=crons", Cookies: []*http.Cookie{cookie}})
	body = rec.Body.String()
	if !strings.Contains(body, createForm) {
		t.Errorf("crons tab with -edit-cronjobs lacks the create form")
	}
	if !enableBtn.MatchString(body) {
		t.Errorf("crons tab with -edit-cronjobs lacks the enable button of job %d", job.ID)
	}
}

func TestAdfCronForgetJob(t *testing.T) {
	cm := NewCronJobManager(w0DB(t)) // not started: no reload loop
	t.Cleanup(func() { adfCronStopManager(t, cm) })
	row := adfCronInsert(t, false, 1440)

	// A disabled job's scheduler ends at once; its registration must go with it.
	if err := cm.startJob(row); err != nil {
		t.Fatalf("startJob(disabled job) = %v", err)
	}
	if !adfCronWait(func() bool { return adfCronRegistered(cm, row.ID) == nil }) {
		t.Fatalf("job %d is still registered after its scheduler ended (A-web-20)", row.ID)
	}

	// Re-enabled, the job starts again instead of answering ErrCronExists.
	adfCronSetEnabled(t, row, true)
	row.Enabled = true
	t.Cleanup(func() { adfCronSetEnabled(t, row, false) })
	if err := cm.startJob(row); err != nil {
		t.Fatalf("startJob after re-enable = %v, want nil (ErrCronExists means the dead registration stayed)", err)
	}
	job := adfCronRegistered(cm, row.ID)
	if job == nil {
		t.Fatalf("job %d not registered after the second startJob", row.ID)
	}
	if !adfCronWait(func() bool { return adfCronHasLog(job, "Waiting until") }) {
		t.Fatalf("the re-enabled job's scheduler did not reach its wait")
	}
	if err := cm.startJob(row); !errors.Is(err, ErrCronExists) {
		t.Fatalf("third startJob while the scheduler runs = %v, want ErrCronExists", err)
	}

	// forgetJob deletes only its own registration (pointer identity).
	stale := &CronJob{ID: row.ID}
	cm.forgetJob(stale)
	if adfCronRegistered(cm, row.ID) != job {
		t.Fatalf("forgetJob of a stale *CronJob removed the live registration")
	}
}

// adfCronSchedulerFrames counts the goroutines with a runJobScheduler frame whose arguments
// include ptr (the job pointer, as the traceback prints the argument words).
func adfCronSchedulerFrames(ptr string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		for _, line := range strings.Split(g, "\n") {
			if strings.Contains(line, ").runJobScheduler(") && strings.Contains(strings.ReplaceAll(line, "?", ""), ptr) {
				count++
				break
			}
		}
	}
	return count
}

func TestAdfCronSingleScheduler(t *testing.T) {
	// (a) Source: exactly one place starts a scheduler, and it is startJob.
	src, err := os.ReadFile("cronjobs.go")
	if err != nil {
		// The w0 harness runs the tests from a temp dir; read the file from the checkout.
		src, err = os.ReadFile(w0RepoRoot + "/internal/web/cronjobs.go")
	}
	if err != nil {
		t.Fatalf("read cronjobs.go: %v", err)
	}
	text := string(src)
	if n := strings.Count(text, "go cm.runJobScheduler("); n != 1 {
		t.Fatalf("cronjobs.go has %d `go cm.runJobScheduler(` sites, want exactly 1 (in startJob)", n)
	}
	site := strings.Index(text, "go cm.runJobScheduler(")
	startJob := strings.Index(text, "func (cm *CronJobManager) startJob(")
	if startJob < 0 || site < startJob {
		t.Fatalf("the `go cm.runJobScheduler(` site is not inside startJob")
	}
	if next := strings.Index(text[startJob+1:], "\nfunc "); next >= 0 && site > startJob+1+next {
		t.Fatalf("the `go cm.runJobScheduler(` site is not inside startJob")
	}

	// (b) Runtime: a job that runs back to back (interval 0) keeps one scheduler goroutine.
	cm := NewCronJobManager(w0DB(t))
	t.Cleanup(func() { adfCronStopManager(t, cm) })
	row := adfCronInsert(t, true, 0)
	t.Cleanup(func() { adfCronSetEnabled(t, row, false) })
	if err := cm.startJob(row); err != nil {
		t.Fatalf("startJob = %v", err)
	}
	job := adfCronRegistered(cm, row.ID)
	if job == nil {
		t.Fatalf("job %d not registered", row.ID)
	}
	ptr := fmt.Sprintf("%p", job)

	countRuns := func() int {
		job.mutex.RLock()
		defer job.mutex.RUnlock()
		runs := 0
		for _, l := range job.Output {
			if strings.Contains(l, "Job completed") {
				runs++
			}
		}
		return runs
	}
	// Sample for a 2 s observation window (the sampling window, not a bound) and in any case
	// until the job has run at least twice, so the samples span several scheduler cycles even
	// on a loaded machine. testWait only stops a scheduler that never cycles.
	maxSeen, samples, runs := 0, 0, 0
	window := time.Now().Add(2 * time.Second)
	deadline := time.Now().Add(testWait)
	for {
		n := adfCronSchedulerFrames(ptr)
		samples++
		if n > maxSeen {
			maxSeen = n
		}
		runs = countRuns()
		now := time.Now()
		if (runs >= 2 && now.After(window)) || now.After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if maxSeen > 1 {
		t.Fatalf("saw %d runJobScheduler goroutines for job %d at once, want at most 1", maxSeen, row.ID)
	}
	if maxSeen == 0 {
		t.Logf("no runJobScheduler frame showed the job pointer in %d samples (traceback arguments not readable)", samples)
	}
	if runs < 2 {
		t.Fatalf("the job ran %d times within %v, want several cycles of the scheduler loop", runs, testWait)
	}

	// Disabled, the scheduler ends at the top of its next cycle and forgets the job.
	adfCronSetEnabled(t, row, false)
	if !adfCronWait(func() bool { return adfCronRegistered(cm, row.ID) == nil }) {
		t.Fatalf("job %d still registered after it was disabled", row.ID)
	}
	if n := adfCronSchedulerFrames(ptr); n > 0 {
		// The deferred forgetJob runs as the scheduler returns; allow it a moment to unwind.
		if !adfCronWait(func() bool { return adfCronSchedulerFrames(ptr) == 0 }) {
			t.Fatalf("a runJobScheduler goroutine for job %d is still alive after it was forgotten", row.ID)
		}
	}
}

func TestAdfCronLogFilterLimit(t *testing.T) {
	var logLines []string
	for i := 0; i < 50; i++ {
		tag := "noise"
		if i%10 == 0 {
			tag = "MATCH"
		}
		logLines = append(logLines, fmt.Sprintf("line %02d %s", i, tag))
	}

	// Filter on a log longer than the limit: the result is the filtered lines, not the tail.
	got, avail := filterCronLog(logLines, "MATCH", 10)
	want := []string{
		"Filtered log output (filter: 'MATCH'):",
		"line 00 MATCH", "line 10 MATCH", "line 20 MATCH", "line 30 MATCH", "line 40 MATCH",
		"--- End of filtered output lines: 5 ---",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") || avail != len(want) {
		t.Fatalf("filterCronLog(MATCH, 10) = %q (available %d), want %q (available %d)", got, avail, want, len(want))
	}

	// The limit applies to the filtered result.
	got, avail = filterCronLog(logLines, "MATCH", 3)
	if strings.Join(got, "|") != strings.Join(want[len(want)-3:], "|") || avail != len(want) {
		t.Fatalf("filterCronLog(MATCH, 3) = %q (available %d), want %q", got, avail, want[len(want)-3:])
	}

	// Without a filter: the last limit lines of the whole log.
	got, avail = filterCronLog(logLines, "", 5)
	if strings.Join(got, "|") != strings.Join(logLines[45:], "|") || avail != 50 {
		t.Fatalf("filterCronLog(\"\", 5) = %q (available %d), want the last 5 of 50", got, avail)
	}
	got, avail = filterCronLog(logLines, "", 1000)
	if len(got) != 50 || avail != 50 {
		t.Fatalf("filterCronLog(\"\", 1000) returned %d lines (available %d), want all 50", len(got), avail)
	}
}
