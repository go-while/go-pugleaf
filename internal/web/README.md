# internal/web

Gin web gateway for go-pugleaf: the public site, the JSON API under `/api/v1`, and the admin UI.
Started from `cmd/web`, which owns the process lifecycle; this package owns the HTTP surface.

For signatures and details use `go doc ./internal/web` — this file is a map, not an API reference.
Deployment and operational behaviour (reverse proxy, sessions, CSRF, `-data`) live in
[`docs/web-deployment.md`](../../docs/web-deployment.md).

## Server and routing

| File | Responsibility |
|---|---|
| `webserver.go` | `WebServer` lifecycle: `Start`, `serveOn`, `Shutdown` (drains in-flight requests, then cancels their contexts) |
| `webserver_core_routes.go` | `NewWebServer`, route table, middleware order, trusted proxies and the client-IP header, background goroutine starts |
| `web_templates.go` | template parsing and the process-wide template cache; `renderPage`, `renderTemplateSet`, `publicErrorDetail` |
| `web_helpers.go` | `checkGroupAccess` / `checkGroupAccessAPI` — the group existence and active checks every group route funnels through |
| `web_utils.go` | `renderError`, `getBaseTemplateData` and other shared render helpers |
| `embedded_static.go` | embedded `static/*` FS and `staticContentType` (`/static/*` itself is served by `http.FileServer`) |
| `cronjobs.go` | `CronJobManager`: loads jobs from the DB on an interval and starts/stops them |
| `web_session_cleanup.go` | periodic expiry of stale sessions |

## Auth, sessions and users

| File | Responsibility |
|---|---|
| `web_auth.go` | session cookie and lookup, `isAdminRequest`, display-name validation |
| `web_login.go` | login and logout, lockout handling |
| `web_registerPage.go` | registration |
| `web_profile.go` | profile form: email, password and display name |

Session tokens are stored hashed (`database.HashSessionToken`); the raw token only ever lives in
the cookie. The login lockout uses `users.login_attempt_at`, not `updated_at`.

## Public pages

| File | Responsibility |
|---|---|
| `web_homePage.go`, `web_newsPage.go`, `web_statsPage.go`, `web_helpPage.go`, `web_ircPage.go` | site pages |
| `web_groupsPage.go`, `webgroupPage.go` | group index and one group's article list |
| `web_groupThreadsPage.go`, `web_threadPage.go`, `web_threadTreePage.go` | thread list, single thread, tree view |
| `web_articlePage.go` | single article and article preview |
| `web_hierarchiesPage.go`, `web_sectionsPage.go` | hierarchy and section browsing |
| `web_searchPage.go` | search |
| `web_sitePostPage.go` | web posting: validation, reservation and the post queue |
| `web_aichatPage.go` | AI chat proxy and its per-user/model history |

## API and admin

| File | Responsibility |
|---|---|
| `web_apiHandlers.go` | `/api/v1` handlers (groups, overview, threads, stats, article preview) |
| `web_apitokens.go` | API token auth and buffered usage accounting |
| `web_admin.go`, `web_adminPage.go` | admin page data and dashboard |
| `web_admin_settings_unified.go` | the settings form: one entry per setting, with validator and success message |
| `web_admin_*.go` | per-area admin handlers (users, sections, newsgroups, providers, spam, cron, post queue, NNTP, Ollama, cache, API tokens, site news); `isAdmin` lives in `web_admin_userfuncs.go` |

The settings form (`web_admin_settings_unified.go`) deliberately shows real error text to the admin;
most other admin handlers use a generic message. Visitor-facing pages show `publicErrorDetail` and log
the detail with a `[WEB]` prefix.

## Tests

`testmain_test.go` owns the shared fixtures (`w0Srv`, `w0DB`, `w0Do`, `w0NewUser`, `w0NewGroup`,
`w0Name`, ...) and must not be edited by feature work. Tests never shut down `w0Srv`, never call
`t.Parallel` while mutating a global, and restore anything they change with `t.Cleanup`.
End-to-end coverage lives in `scripts/test-web-hardening.sh` and `scripts/test-web-leftovers.sh`.
