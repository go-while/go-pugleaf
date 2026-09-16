package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

// lo1DBUser inserts a user row in db and returns it with its ID.
func lo1DBUser(t *testing.T, db *Database) *models.User {
	t.Helper()
	name := w0Name("lo1db_user")
	if err := db.InsertUser(&models.User{Username: name, Email: name + "@test.invalid", PasswordHash: "x", DisplayName: name}); err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	u, err := db.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	return u
}

// lo1DBFreshMain returns an isolated Database with a freshly migrated main DB.
// Tests that touch every user row (CleanupExpiredSessions) must not use w0DB.
func lo1DBFreshMain(t *testing.T) *Database {
	t.Helper()
	shared := w0DB(t)
	mainDB, err := sql.Open(driverNameMain, filepath.Join(t.TempDir(), "lo1db-main.sq3"))
	if err != nil {
		t.Fatalf("open main DB: %v", err)
	}
	t.Cleanup(func() { mainDB.Close() })
	fresh := &Database{mainDB: mainDB, dbconfig: shared.dbconfig}
	if err := fresh.migrateMainDB(); err != nil {
		t.Fatalf("main migrations: %v", err)
	}
	return fresh
}

// lo1DBSessionID returns users.session_id as stored.
func lo1DBSessionID(t *testing.T, db *Database, userID int64) string {
	t.Helper()
	var v sql.NullString
	if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT session_id FROM users WHERE id = ?", []interface{}{userID}, &v); err != nil {
		t.Fatalf("read session_id: %v", err)
	}
	return v.String
}

// C1: users.session_id holds hex(sha256(token)), the cookie value stays raw.
func TestLo1DBSessionHashedAtRest(t *testing.T) {
	db := w0DB(t)
	u := lo1DBUser(t, db)

	raw, err := db.CreateUserSession(u.ID, "192.0.2.10")
	if err != nil {
		t.Fatalf("CreateUserSession: %v", err)
	}
	if raw == "" {
		t.Fatal("CreateUserSession returned an empty token")
	}
	stored := lo1DBSessionID(t, db, u.ID)
	if stored == raw {
		t.Fatal("session_id stores the raw token")
	}
	if want := HashSessionToken(raw); stored != want {
		t.Fatalf("session_id = %q, want %q", stored, want)
	}
	if len(stored) != 64 {
		t.Fatalf("session_id length = %d, want 64", len(stored))
	}

	user, err := db.ValidateUserSession(raw)
	if err != nil {
		t.Fatalf("ValidateUserSession(raw): %v", err)
	}
	if user.ID != u.ID {
		t.Fatalf("ValidateUserSession returned user %d, want %d", user.ID, u.ID)
	}
	if user.SessionID != stored {
		t.Fatalf("user.SessionID = %q, want the hash %q", user.SessionID, stored)
	}
	if _, err := db.ValidateUserSession(stored); err == nil {
		t.Fatal("ValidateUserSession(hash) succeeded, want an error")
	}
	if _, err := db.ValidateUserSession(""); err == nil {
		t.Fatal("ValidateUserSession(\"\") succeeded, want an error")
	}

	if err := db.InvalidateUserSessionBySessionID(raw); err != nil {
		t.Fatalf("InvalidateUserSessionBySessionID: %v", err)
	}
	if got := lo1DBSessionID(t, db, u.ID); got != "" {
		t.Fatalf("session_id = %q after invalidation, want empty", got)
	}
	if _, err := db.ValidateUserSession(raw); err == nil {
		t.Fatal("session still valid after InvalidateUserSessionBySessionID")
	}
}

// C1/C2: migration 0028 adds login_attempt_at and clears the stored raw sessions.
func TestLo1DBMigration0028(t *testing.T) {
	const fileName = "0028_main_session_hash_login_attempt_at.sql"

	shared := w0DB(t)
	var applied int
	if err := RetryableQueryRowScan(shared.GetMainDB(),
		"SELECT count(*) FROM schema_migrations WHERE filename = ?", []interface{}{fileName}, &applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("schema_migrations rows for %s = %d, want 1", fileName, applied)
	}
	var col int
	if err := RetryableQueryRowScan(shared.GetMainDB(),
		"SELECT count(*) FROM pragma_table_info('users') WHERE name = 'login_attempt_at'", nil, &col); err != nil {
		t.Fatal(err)
	}
	if col != 1 {
		t.Fatalf("users.login_attempt_at columns = %d, want 1", col)
	}

	// A fresh DB migrated up to 0027, seeded like a pre-0028 install, then upgraded.
	mainDB, err := sql.Open(driverNameMain, filepath.Join(t.TempDir(), "lo1db-0028.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	pre := &Database{mainDB: mainDB, dbconfig: shared.dbconfig}
	if err := ensureMigrationsTable(mainDB, "main"); err != nil {
		t.Fatal(err)
	}
	migrations, err := getMigrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Type != MigrationTypeMain || m.Version >= 28 {
			continue
		}
		if err := applyMigration(mainDB, m, "main"); err != nil {
			t.Fatalf("applyMigration %s: %v", m.FileName, err)
		}
	}
	if _, err := RetryableExec(mainDB, `INSERT INTO users
		(username, email, password_hash, display_name, session_id, session_expires_at, login_attempts, updated_at)
		VALUES ('lo1dbold', 'lo1dbold@test.invalid', 'x', 'old', 'rawtoken', ?, 3, ?)`,
		time.Now().UTC().Add(time.Hour), "2020-01-02 03:04:05"); err != nil {
		t.Fatalf("seed pre-0028 user: %v", err)
	}

	if err := pre.migrateMainDB(); err != nil {
		t.Fatalf("migrateMainDB: %v", err)
	}

	var sid string
	var expires sql.NullTime
	var attemptAt sql.NullString
	if err := RetryableQueryRowScan(mainDB,
		"SELECT session_id, session_expires_at, CAST(login_attempt_at AS TEXT) FROM users WHERE username = 'lo1dbold'",
		nil, &sid, &expires, &attemptAt); err != nil {
		t.Fatal(err)
	}
	if sid != "" {
		t.Fatalf("session_id = %q after 0028, want empty", sid)
	}
	if expires.Valid {
		t.Fatalf("session_expires_at = %v after 0028, want NULL", expires.Time)
	}
	if !attemptAt.Valid || !strings.HasPrefix(attemptAt.String, "2020-01-02") {
		t.Fatalf("login_attempt_at = %v after 0028, want the old updated_at", attemptAt)
	}
}

// C2: logout and the session cleanup no longer restart a running lockout window.
func TestLo1DBLockoutIgnoresSessionWrites(t *testing.T) {
	db := lo1DBFreshMain(t)
	u := lo1DBUser(t, db)

	for i := 0; i < MaxLoginAttempts; i++ {
		ok, err := db.ReserveLoginAttemptByID(u.ID)
		if err != nil || !ok {
			t.Fatalf("reservation %d = %v, %v; want true", i, ok, err)
		}
	}
	if ok, err := db.ReserveLoginAttemptByID(u.ID); err != nil || ok {
		t.Fatalf("reservation past the limit = %v, %v; want false", ok, err)
	}

	// A successful login resets the counter on purpose, so give the user a session
	// (for CleanupExpiredSessions to find) and redo the failed attempts afterwards.
	if _, err := db.CreateUserSession(u.ID, "192.0.2.11"); err != nil {
		t.Fatalf("CreateUserSession: %v", err)
	}
	if _, err := RetryableExec(db.GetMainDB(),
		"UPDATE users SET session_expires_at = ? WHERE id = ?",
		time.Now().UTC().Add(-time.Hour), u.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxLoginAttempts; i++ {
		if ok, err := db.ReserveLoginAttemptByID(u.ID); err != nil || !ok {
			t.Fatalf("reservation %d after login = %v, %v; want true", i, ok, err)
		}
	}

	// Session writes must not move the lockout clock.
	if err := db.InvalidateUserSession(u.ID); err != nil {
		t.Fatalf("InvalidateUserSession: %v", err)
	}
	if err := db.CleanupExpiredSessions(); err != nil {
		t.Fatalf("CleanupExpiredSessions: %v", err)
	}
	if ok, err := db.ReserveLoginAttemptByID(u.ID); err != nil || ok {
		t.Fatalf("reservation after the session writes = %v, %v; want false (still locked out)", ok, err)
	}
	if locked, err := db.IsUserLockedOutByID(u.ID); err != nil || !locked {
		t.Fatalf("IsUserLockedOutByID after the session writes = %v, %v; want true", locked, err)
	}

	// Window passed: the next attempt is allowed again.
	old := time.Now().UTC().Add(-2 * LoginLockoutTime).Format("2006-01-02 15:04:05")
	if _, err := RetryableExec(db.GetMainDB(), "UPDATE users SET login_attempt_at = ? WHERE id = ?", old, u.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ReserveLoginAttemptByID(u.ID); err != nil || !ok {
		t.Fatalf("reservation after the window = %v, %v; want true", ok, err)
	}
}

// C3: the retry cap can be changed while Retryable* helpers are retrying.
// TestLo1DBRetryWaitRace lowers the process-global retry cap to 20-100ms for about
// 200ms. That is safe only because no test in this package calls t.Parallel and the
// only contention in that window is on this test's own temp database: the cap is read
// from retryBackoff, i.e. only after a BUSY/LOCKED error. If this package ever gains
// t.Parallel, or OpenDatabase gains a periodic writer that contends, a 20ms cap can
// make that writer give up early and log "[DATABASE] ... giving up".
func TestLo1DBRetryWaitRace(t *testing.T) {
	t.Cleanup(func() { SetSQLiteMaxRetryWait(0) })
	if got := GetSQLiteMaxRetryWait(); got != defaultSQLiteMaxRetryWait {
		t.Fatalf("GetSQLiteMaxRetryWait() = %v, want %v", got, defaultSQLiteMaxRetryWait)
	}
	SetSQLiteMaxRetryWait(-1)
	if got := GetSQLiteMaxRetryWait(); got != defaultSQLiteMaxRetryWait {
		t.Fatalf("SetSQLiteMaxRetryWait(-1): cap = %v, want the default %v", got, defaultSQLiteMaxRetryWait)
	}
	SetSQLiteMaxRetryWait(100 * time.Millisecond)

	// A busy database: one connection holds a write lock for the whole test.
	tdb, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "retrywait.sq3"))
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	if _, err := tdb.Exec("CREATE TABLE lo1db_retry (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := tdb.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO lo1db_retry (n) VALUES (1)"); err != nil {
		conn.Close()
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Busy: it retries until the cap and then returns the error.
				if _, err := RetryableExec(tdb, "INSERT INTO lo1db_retry (n) VALUES (2)"); err == nil {
					return
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				select {
				case <-stop:
					return
				default:
				}
				SetSQLiteMaxRetryWait(time.Duration(20+10*i) * time.Millisecond)
				_ = GetSQLiteMaxRetryWait()
			}
		}(i)
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close conn: %v", err)
	}
	SetSQLiteMaxRetryWait(0)
	if got := GetSQLiteMaxRetryWait(); got != defaultSQLiteMaxRetryWait {
		t.Fatalf("SetSQLiteMaxRetryWait(0): cap = %v, want the default %v", got, defaultSQLiteMaxRetryWait)
	}
	// The lock is gone, so a write succeeds again.
	if _, err := RetryableExec(tdb, "INSERT INTO lo1db_retry (n) VALUES (3)"); err != nil {
		t.Fatalf("RetryableExec after the lock was released: %v", err)
	}
}

// lo1DBChildQuery builds the OLD spelling of the thread-reply child query with n
// placeholders, for comparison only. The live query comes from threadChildrenQuery in
// thread_cache.go - do not add a second copy of it here, or a revert of the "+hide"
// plan fix would pass against the copy.
func lo1DBChildQuery(hideTerm string, n int) string {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", n), ",")
	return fmt.Sprintf(`
		SELECT article_num, subject, from_header, date_sent, date_string,
			   message_id, "references", bytes, lines, reply_count, downloaded
		FROM articles
		WHERE article_num IN (%s) AND %s = 0
		ORDER BY date_sent ASC
	`, placeholders, hideTerm)
}

// lo1DBArticleNums runs q and returns the article numbers it yields, in order.
func lo1DBArticleNums(t *testing.T, gdb *sql.DB, q string, args ...interface{}) []int64 {
	t.Helper()
	rows, err := RetryableQuery(gdb, q, args...)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// C4: the child page query looks articles up by rowid instead of walking idx_articles_hide_date.
func TestLo1DBChildQueryPlan(t *testing.T) {
	db := w0DB(t)
	group := w0Name("lo1db.child")
	if _, err := RetryableExec(db.GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 0, 0, 1, ?)",
		group, ExtractHierarchyFromGroupName(group)); err != nil {
		t.Fatalf("insert newsgroup: %v", err)
	}
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB: %v", err)
	}
	defer gdb.Return()

	base := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := int64(1); i <= 5; i++ {
		// Descending dates, so date_sent ordering differs from article_num ordering.
		if _, err := db.InsertOverview(gdb, &models.Overview{
			ArticleNum: i, Subject: fmt.Sprintf("s%d", i), FromHeader: "a <a@b>",
			DateSent: base.Add(time.Duration(-i) * time.Minute), DateString: "x",
			MessageID: fmt.Sprintf("<lo1db-child-%d@test.invalid>", i), Downloaded: 1,
		}); err != nil {
			t.Fatalf("InsertOverview %d: %v", i, err)
		}
	}
	// Hide two of the replies.
	if _, err := RetryableExec(gdb.DB, "UPDATE articles SET hide = 1 WHERE article_num IN (3, 5)"); err != nil {
		t.Fatalf("hide articles: %v", err)
	}

	args := []interface{}{2, 3, 4, 5}
	newQ := threadChildrenQuery(strings.TrimSuffix(strings.Repeat("?,", len(args)), ","))
	oldQ := lo1DBChildQuery("hide", len(args))

	plan := w2DBPerfPlan(t, gdb.DB, newQ, args...)
	t.Logf("child query plan: %s", plan)
	if !strings.Contains(plan, "INTEGER PRIMARY KEY") {
		t.Errorf("child query plan does not use INTEGER PRIMARY KEY: %s", plan)
	}
	if strings.Contains(plan, "idx_articles_hide_date") {
		t.Errorf("child query plan still uses idx_articles_hide_date: %s", plan)
	}

	numQ := func(q string) string {
		return strings.Replace(q,
			`SELECT article_num, subject, from_header, date_sent, date_string,
			   message_id, "references", bytes, lines, reply_count, downloaded`,
			"SELECT article_num", 1)
	}
	got := lo1DBArticleNums(t, gdb.DB, numQ(newQ), args...)
	want := lo1DBArticleNums(t, gdb.DB, numQ(oldQ), args...)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("child rows = %v, want %v (same as the old query)", got, want)
	}
	if fmt.Sprint(got) != fmt.Sprint([]int64{4, 2}) {
		t.Fatalf("child rows = %v, want [4 2] (hidden 3 and 5 filtered, date_sent ascending)", got)
	}

	// The live path returns the same rows.
	if _, err := RetryableExec(gdb.DB, `INSERT INTO thread_cache
		(thread_root, root_date, message_count, child_articles, last_child_number, last_activity)
		VALUES (1, ?, 5, '2,3,4,5', 5, ?)`, base, base); err != nil {
		t.Fatalf("seed thread_cache: %v", err)
	}
	replies, total, err := db.GetCachedThreadReplies(gdb, 1, 1, 25)
	if err != nil {
		t.Fatalf("GetCachedThreadReplies: %v", err)
	}
	if total != 4 {
		t.Errorf("total replies = %d, want 4", total)
	}
	var nums []int64
	for _, r := range replies {
		nums = append(nums, r.ArticleNum)
	}
	if fmt.Sprint(nums) != fmt.Sprint([]int64{4, 2}) {
		t.Fatalf("GetCachedThreadReplies rows = %v, want [4 2]", nums)
	}
}
