package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/processor"
	"golang.org/x/crypto/bcrypt"
)

// w0Password is the password of every user created by w0NewUser.
const w0Password = "w0-password-0123456789"

// Shared by every test of this package: OpenDatabase may run only once per process and it
// starts background goroutines, so tests must not mutate package globals those goroutines
// read (see K3 in the web-sqlite-hardening plan).
var (
	w0Srv      *WebServer         // server built like cmd/web (no NNTP server, cron manager on)
	w0TestDB   *database.Database // database of w0Srv, data root w0DataDir
	w0DataDir  string             // temp data root (group DBs live in <w0DataDir>/db)
	w0RepoRoot string             // checkout root; <w0RepoRoot>/web is linked into the cwd
	w0Seq      atomic.Int64
)

// w0Req describes one request served by w0Do.
type w0Req struct {
	Method     string            // default GET
	Path       string            // may include a query string
	Form       url.Values        // GET/HEAD: appended to the query; else an urlencoded body
	Header     map[string]string // set after Content-Type, so it can override it
	Cookies    []*http.Cookie
	RemoteAddr string // default 192.0.2.10:1234
}

func TestMain(m *testing.M) {
	os.Exit(w0Main(m))
}

func w0Main(m *testing.M) int {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if _, err := os.Stat(filepath.Join(root, "web", "templates", "base.html")); err != nil {
		fmt.Fprintln(os.Stderr, "templates not found:", err)
		return 2
	}
	w0RepoRoot = root

	tmp, err := os.MkdirTemp("", "pugleaf-webtest-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// Handlers load web/templates relative to the working directory. Run from a temp dir that
	// only links web/, so cwd-relative paths (./data, cron job commands) never reach the checkout.
	cwd := filepath.Join(tmp, "cwd")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := os.Symlink(filepath.Join(root, "web"), filepath.Join(cwd, "web")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := os.Chdir(cwd); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// Globals cmd/web sets before NewWebServer.
	config.AppVersion = "test" // config.NewDefaultConfig log.Fatalf's while it is "-unset-" (config.go:542)
	processor.LocalNNTPHostname = "test.invalid"
	database.GlobalDateParser = processor.ParseNNTPDate
	models.DisableSanitizedCache = false
	models.InitSanitizedCache(10000, 30*time.Minute)
	models.InitNewsgroupCache(4096, 5*time.Minute)

	w0DataDir = filepath.Join(tmp, "data")
	cfg := database.DefaultDBConfig()
	cfg.DataDir = w0DataDir
	cfg.BackupDir = filepath.Join(tmp, "backups")
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "OpenDatabase:", err)
		return 2
	}
	w0TestDB = db

	// isAdmin treats user ID 1 as admin: give that ID to a plain user before any test runs.
	if _, err := w0CreateUser("w0root", false); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	w0Srv = NewWebServer(db, config.NewDefaultConfig().Server.WEB, nil, false, false)

	code := m.Run()
	if code == 0 {
		_ = os.RemoveAll(tmp)
	} else {
		fmt.Fprintln(os.Stderr, "test data kept in", tmp)
	}
	return code
}

// w0DB returns the shared test database.
func w0DB(t *testing.T) *database.Database {
	t.Helper()
	if w0TestDB == nil {
		t.Fatal("test DB not open")
	}
	return w0TestDB
}

// w0Name returns a unique name like "<prefix>.t42" (valid as a newsgroup name).
func w0Name(prefix string) string {
	return fmt.Sprintf("%s.t%d", prefix, w0Seq.Add(1))
}

// w0Do serves one request through w0Srv.Router and returns the recorded response.
func w0Do(t *testing.T, r w0Req) *httptest.ResponseRecorder {
	t.Helper()
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	target := r.Path
	var body io.Reader
	if r.Form != nil {
		if method == http.MethodGet || method == http.MethodHead {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + r.Form.Encode()
		} else {
			body = strings.NewReader(r.Form.Encode())
		}
	}
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.RemoteAddr = "192.0.2.10:1234"
	if r.RemoteAddr != "" {
		req.RemoteAddr = r.RemoteAddr
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	for _, ck := range r.Cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	w0Srv.Router.ServeHTTP(rec, req)
	return rec
}

// w0CreateUser inserts a user with password w0Password (plus the admin permission when admin).
func w0CreateUser(name string, admin bool) (*models.User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(w0Password), bcrypt.MinCost)
	if err != nil {
		return nil, err
	}
	err = w0TestDB.InsertUser(&models.User{
		Username:     name,
		Email:        name + "@test.invalid",
		PasswordHash: string(hash),
		DisplayName:  name,
	})
	if err != nil {
		return nil, fmt.Errorf("InsertUser %s: %w", name, err)
	}
	u, err := w0TestDB.GetUserByUsername(name)
	if err != nil {
		return nil, fmt.Errorf("GetUserByUsername %s: %w", name, err)
	}
	if admin {
		perm := &models.UserPermission{UserID: u.ID, Permission: "admin", GrantedAt: time.Now()}
		if err := w0TestDB.InsertUserPermission(perm); err != nil {
			return nil, fmt.Errorf("InsertUserPermission %s: %w", name, err)
		}
	}
	return u, nil
}

// w0NewUser creates a user named w0user_<n> (email <name>@test.invalid, password w0Password)
// with a fresh web session and returns it with its session_id cookie.
func w0NewUser(t *testing.T, admin bool) (*models.User, *http.Cookie) {
	t.Helper()
	u, err := w0CreateUser(fmt.Sprintf("w0user_%d", w0Seq.Add(1)), admin)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := w0TestDB.CreateUserSession(u.ID, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	return u, &http.Cookie{Name: "session_id", Value: sid}
}

// w0NewGroup inserts a newsgroup row named w0grp.t<n> (no group DB file is created).
func w0NewGroup(t *testing.T, active bool) string {
	t.Helper()
	name := w0Name("w0grp")
	_, err := database.RetryableExec(w0TestDB.GetMainDB(),
		"INSERT INTO newsgroups(name, description, last_article, message_count, active, hierarchy) VALUES (?, '', 0, 0, ?, ?)",
		name, active, database.ExtractHierarchyFromGroupName(name))
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestW0Harness(t *testing.T) {
	if rec := w0Do(t, w0Req{Path: "/ping"}); rec.Code != http.StatusOK {
		t.Fatalf("GET /ping = %d, want 200", rec.Code)
	}
	_, cookie := w0NewUser(t, false)
	if rec := w0Do(t, w0Req{Path: "/profile", Cookies: []*http.Cookie{cookie}}); rec.Code != http.StatusOK {
		t.Fatalf("GET /profile with a w0NewUser cookie = %d, want 200", rec.Code)
	}
	group := w0NewGroup(t, true)
	if _, err := w0DB(t).GetActiveNewsgroupByName(group); err != nil {
		t.Fatalf("GetActiveNewsgroupByName(%s): %v", group, err)
	}
}
