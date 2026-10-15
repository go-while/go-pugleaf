package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/go-while/go-pugleaf/internal/database"
)

// flPqRow inserts a post_queue row for a new active group and returns its message-id and id;
// the row is removed when the test ends.
func flPqRow(t *testing.T, attempts int, lastError string, posted, inProcessing bool) (msgID string, id int64) {
	t.Helper()
	db := w0DB(t)
	group := w0NewGroup(t, true)
	var gid int64
	if err := database.RetryableQueryRowScan(db.GetMainDB(), "SELECT id FROM newsgroups WHERE name = ?", []interface{}{group}, &gid); err != nil {
		t.Fatalf("id of %s: %v", group, err)
	}
	msgID = fmt.Sprintf("<flpq.%d@test.invalid>", w0Seq.Add(1))
	res, err := database.RetryableExec(db.GetMainDB(),
		`INSERT INTO post_queue (newsgroup_id, message_id, posted_to_remote, in_processing, attempts, last_error) VALUES (?, ?, ?, ?, ?, ?)`,
		gid, msgID, posted, inProcessing, attempts, lastError)
	if err != nil {
		t.Fatalf("insert post_queue row: %v", err)
	}
	if id, err = res.LastInsertId(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := database.RetryableExec(db.GetMainDB(), "DELETE FROM post_queue WHERE newsgroup_id = ?", gid); err != nil {
			t.Errorf("cleanup post_queue of %s: %v", group, err)
		}
	})
	return msgID, id
}

// flPqState returns attempts, last_error ("" for NULL) and posted_to_remote of row id, and
// whether the row exists.
func flPqState(t *testing.T, id int64) (attempts int, lastError string, posted, exists bool) {
	t.Helper()
	var n int
	if err := database.RetryableQueryRowScan(w0DB(t).GetMainDB(), "SELECT COUNT(*) FROM post_queue WHERE id = ?", []interface{}{id}, &n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return 0, "", false, false
	}
	if err := database.RetryableQueryRowScan(w0DB(t).GetMainDB(),
		"SELECT attempts, COALESCE(last_error, ''), posted_to_remote FROM post_queue WHERE id = ?",
		[]interface{}{id}, &attempts, &lastError, &posted); err != nil {
		t.Fatalf("state of post_queue row %d: %v", id, err)
	}
	return attempts, lastError, posted, true
}

// TestFlPqAdminPageParked: the post queue tab lists a parked row with its attempts, the escaped
// last error, a parked badge and a Retry form; the parked filter and stat exist.
func TestFlPqAdminPageParked(t *testing.T) {
	_, cookie := w0NewUser(t, true)
	msgID, id := flPqRow(t, 5, "441 flpq rejected <b>bold</b>", false, false)

	rec := w0Do(t, w0Req{Path: "/admin", Form: url.Values{"tab": {"postqueue"}, "status_filter": {"parked"}, "search": {msgID}},
		Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin?tab=postqueue = %d, want 200", rec.Code)
	}
	page := rec.Body.String()
	for _, want := range []string{
		"<th>Attempts</th>", "<th>Last error</th>",
		`<option value="parked" selected>Parked</option>`,
		">Parked</span>",
		"441 flpq rejected &lt;b&gt;bold&lt;/b&gt;",
		`action="/admin/postqueue/retry"`,
		`name="id" value="` + strconv.FormatInt(id, 10) + `"`,
		"<small>5</small>",
		"<small class=\"text-muted\">Parked</small>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("post queue tab does not contain %q", want)
		}
	}
	if strings.Contains(page, "<b>bold</b>") {
		t.Errorf("last error rendered unescaped")
	}

	// a pending row without failures has no Retry button
	pendingID, pid := flPqRow(t, 0, "", false, false)
	rec = w0Do(t, w0Req{Path: "/admin", Form: url.Values{"tab": {"postqueue"}, "search": {pendingID}}, Cookies: []*http.Cookie{cookie}})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin?tab=postqueue (pending) = %d, want 200", rec.Code)
	}
	page = rec.Body.String()
	if !strings.Contains(page, `name="id" value="`+strconv.FormatInt(pid, 10)+`"`) {
		t.Fatalf("pending row %d not listed", pid)
	}
	if strings.Contains(page, `action="/admin/postqueue/retry"`) {
		t.Errorf("pending row without attempts shows a Retry button")
	}
}

// TestFlPqAdminRetry: POST /admin/postqueue/retry resets attempts and last_error of a parked
// row; posted rows, rows in processing, non-admins and cross-site requests change nothing.
func TestFlPqAdminRetry(t *testing.T) {
	_, admin := w0NewUser(t, true)
	_, user := w0NewUser(t, false)
	_, parked := flPqRow(t, 5, "441 flpq", false, false)
	_, posted := flPqRow(t, 2, "441 earlier", true, false)
	_, busy := flPqRow(t, 1, "441 busy", false, true)

	form := func(id int64) url.Values { return url.Values{"id": {strconv.FormatInt(id, 10)}} }

	// non-admin: refused, unchanged
	rec := w0Do(t, w0Req{Method: http.MethodPost, Path: "/admin/postqueue/retry", Form: form(parked), Cookies: []*http.Cookie{user}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/profile" {
		t.Errorf("non-admin retry = %d Location %q, want 303 to /profile", rec.Code, rec.Header().Get("Location"))
	}
	if attempts, _, _, _ := flPqState(t, parked); attempts != 5 {
		t.Errorf("non-admin retry changed attempts to %d", attempts)
	}

	// cross-site POST: rejected by the CSRF layer before the handler
	req := httptest.NewRequest(http.MethodPost, "/admin/postqueue/retry", strings.NewReader(form(parked).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(admin)
	crec := httptest.NewRecorder()
	w0Srv.rootHandler().ServeHTTP(crec, req)
	if crec.Code != http.StatusForbidden {
		t.Errorf("cross-site retry = %d, want 403", crec.Code)
	}
	if attempts, _, _, _ := flPqState(t, parked); attempts != 5 {
		t.Errorf("cross-site retry changed attempts to %d", attempts)
	}

	// admin: reset
	resp, success, failure := adfOptPost(t, admin, "/admin/postqueue/retry", form(parked))
	if resp.StatusCode != http.StatusSeeOther || success == "" || failure != "" {
		t.Errorf("retry parked: %d success=%q error=%q", resp.StatusCode, success, failure)
	}
	if attempts, lastErr, _, _ := flPqState(t, parked); attempts != 0 || lastErr != "" {
		t.Errorf("after retry: attempts=%d last_error=%q, want 0, \"\"", attempts, lastErr)
	}

	// posted and in-processing rows: error, unchanged
	for _, c := range []struct {
		name     string
		id       int64
		attempts int
	}{{"posted", posted, 2}, {"in processing", busy, 1}} {
		resp, success, failure := adfOptPost(t, admin, "/admin/postqueue/retry", form(c.id))
		if resp.StatusCode != http.StatusSeeOther || success != "" || failure == "" {
			t.Errorf("retry %s: %d success=%q error=%q, want an error", c.name, resp.StatusCode, success, failure)
		}
		if attempts, _, _, _ := flPqState(t, c.id); attempts != c.attempts {
			t.Errorf("retry %s changed attempts to %d", c.name, attempts)
		}
	}
	if _, _, isPosted, _ := flPqState(t, posted); !isPosted {
		t.Errorf("retry reset posted_to_remote of a posted row")
	}

	// invalid ids
	for _, v := range []string{"", "abc"} {
		resp, success, failure := adfOptPost(t, admin, "/admin/postqueue/retry", url.Values{"id": {v}})
		if resp.StatusCode != http.StatusSeeOther || success != "" || failure == "" {
			t.Errorf("retry id=%q: %d success=%q error=%q, want an error", v, resp.StatusCode, success, failure)
		}
	}
}

// TestFlPqAdminDelete: delete removes a pending row (before, only posted rows could be deleted)
// and refuses a row in processing with an error message.
func TestFlPqAdminDelete(t *testing.T) {
	_, admin := w0NewUser(t, true)
	_, pending := flPqRow(t, 3, "441 flpq", false, false)
	_, busy := flPqRow(t, 0, "", false, true)
	form := func(id int64) url.Values { return url.Values{"id": {strconv.FormatInt(id, 10)}} }

	resp, success, failure := adfOptPost(t, admin, "/admin/postqueue/delete", form(pending))
	if resp.StatusCode != http.StatusSeeOther || success == "" || failure != "" {
		t.Errorf("delete pending: %d success=%q error=%q", resp.StatusCode, success, failure)
	}
	if _, _, _, exists := flPqState(t, pending); exists {
		t.Errorf("pending row %d still there after delete", pending)
	}

	resp, success, failure = adfOptPost(t, admin, "/admin/postqueue/delete", form(busy))
	if resp.StatusCode != http.StatusSeeOther || success != "" || failure == "" {
		t.Errorf("delete in processing: %d success=%q error=%q, want an error", resp.StatusCode, success, failure)
	}
	if _, _, _, exists := flPqState(t, busy); !exists {
		t.Errorf("row %d in processing was deleted", busy)
	}
}
