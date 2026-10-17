# Plan: drop-gin (replace gin with net/http ServeMux + an in-package adapter)

Slug for run-plan: `drop-gin`. Base: `testing-001`.

## Context

gin (`github.com/gin-gonic/gin v1.11.0`) and `github.com/gin-contrib/secure v1.1.2` are imported only by
`internal/web` (41 of 45 non-test files, 7 of 27 test files; `cmd/web/main.go` uses only
`web.NewWebServer/Start/Shutdown/StartSessionCleanup/CronManager`). 33 of the 58 modules in the build graph and
36 third-party packages compiled into the webserver exist only because of gin (sonic, validator, ugorji, goccy,
json-iterator, quic-go, protobuf, ...). The web server is not performance-bound by routing; everything the code
uses from gin is thin over net/http. User decision 2026-10-17: drop gin; use Go 1.22+ `ServeMux` patterns and a
small adapter that keeps gin's method names so the 41 handler files convert by text substitution. User decision:
a known path hit with the wrong method answers **405 with an `Allow` header** (gin answered 404).

Verified facts (repo paths; gin/secure sources in the module cache; toolchain go1.27.2):
- Engine: `webserver_core_routes.go:248-257` (`gin.Default()` = Logger+Recovery, `UseRawPath=true`,
  `UnescapePathValues=false`, `unescapeRawPathParams` :575), `WebServer.Router *gin.Engine` (:29). Chain order:
  CrossOriginProtection (`webserver.go:64`, outside the router, unchanged) → Logger → Recovery →
  unescapeRawPathParams → `trackInFlight` (:590, defer after Next) → `secure.New` (:305-322) →
  `ReverseProxyMiddleware` (:766: logs untrusted forwarders once, sets URL.Scheme https on TLS or trusted
  X-Forwarded-Proto, `c.Set("is_https")`) → `BotDetectionMiddleware` (:599: blocked CIDRs / UA substrings →
  `c.String(403,"403")`+Abort). Route-level: `requireAPIEnabled` (`web_apiHandlers.go:80`, 503 JSON),
  `APIAuthRequired` (`web_apitokens.go:16`, header `X-API`, 401 JSON), `sectionValidationMiddleware` (:856, 303 to
  a YouTube URL) on the `Group("/")` holding the six `/:section` routes (:558-568). Trusted proxies:
  `configureTrustedProxies[WithHeader]` (:647/:661) → `SetTrustedProxies`, `ForwardedByClientIP`,
  `RemoteIPHeaders` = exactly one header (X-Real-IP if configured, else X-Forwarded-For); returns `[]*net.IPNet`
  kept in `s.trustedProxyNets` for `isTrustedPeer` (:715). Dead: `ApacheLogFormat` (:785).
- secure v1.1.2 sets, before Next (so gin 404s carry them): `X-Frame-Options: DENY`, `X-Content-Type-Options:
  nosniff`, `X-Xss-Protection: 1; mode=block`, `Content-Security-Policy: <contentSecurityPolicy const :234-243>`,
  `Referrer-Policy: strict-origin-when-cross-origin`; with `Config.SSL`: `Strict-Transport-Security:
  max-age=31536000; includeSubdomains` and SSLRedirect (plain HTTP, no https X-Forwarded-Proto → 301
  `https://host+RequestURI` + Abort).
- gin semantics to copy (gin 1.11): `ClientIP` (`context.go:925`): peer = `net.ParseIP(RemoteIP())`, nil → `""`;
  if the peer is trusted read the single header with `Header.Get` (first line only), split on ",", walk
  right→left with TrimSpace, an unparseable entry → break and fall back to the peer; return the first untrusted
  entry, else the left-most, as the trimmed string; otherwise `peer.String()`. `RemoteIP` = host of
  `net.SplitHostPort(TrimSpace(RemoteAddr))` or `""`. `Cookie(name)` = `Request.Cookie` + `url.QueryUnescape`
  (error ignored). `String`: `text/plain; charset=utf-8` unless Content-Type already set; `Fprintf` only when
  values given, else raw write. `JSON`: `application/json; charset=utf-8` + `json.Marshal` (HTML-escaped, sorted
  keys, no newline). `Status(code)` = `WriteHeader`. Forms: `ParseMultipartForm(32<<20)` once (ErrNotMultipart
  ignored), `PostForm*` read `Request.PostForm` only (query never merged). `Redirect` = `http.Redirect`.
  `ShouldBindJSON` = `json.NewDecoder(Body).Decode`. 404 body `404 page not found`; RedirectTrailingSlash: 301 for
  GET, 307 otherwise, middleware not run; no 405.
- ServeMux (go1.27.2): wildcard segments are `url.PathUnescape`d (keeps `+`, `%2F` stays inside its segment):
  replaces `UseRawPath`/`unescapeRawPathParams` natively (A-web-18). `ServeMux.Handler(r)` returns pattern `""`
  for 404 and for 405; non-empty for a match and for the mux's own 307 redirects (subtree `/x/` and unclean
  paths). Pattern conflicts panic at registration: literal-vs-wildcard in one position plus wildcard-vs-multi in
  another = overlap. Hence `/hierarchy/{h}/{l1}/{l2}` (and `/static/{filepath...}`) conflict with
  `/{section}/{group}/articles/{n}`: the six section routes cannot share a mux with any literal-first pattern
  that has a wildcard second segment → they go to a second ("fallback") mux consulted only on a main miss.
- Rendering: no `c.HTML`; templates from `web/templates/` via html/template, written with
  `c.Data(status, "text/html; charset=utf-8", buf)` (`web_templates.go:139`); `c.File` ×3, `c.Status` ×1,
  `c.AbortWithStatus` ×1 (`embedded_static.go:36`), `c.Redirect` ×334 (303 ×323, 302 ×6, 301 ×5), `c.JSON` ×70
  live, `gin.H` ×70 live incl. as template data and as a type (`apiStatsCache.stats`, `[]gin.H`), `c.String` ×29,
  `c.Header` ×7, `c.GetHeader` ×12, `c.Cookie("session_id")` (hex value), cookies written with
  `http.SetCookie(c.Writer, …)`. Input: `c.PostForm` ×180, `PostFormArray` ×2, `GetPostForm`/`GetPostFormArray`
  (`web_admin_nntp:220-221`), `c.Query` ×39, `DefaultQuery` ×4, `c.Param` ×49, `ShouldBindJSON` ×1 live
  (`web_aichatPage:288`, behind `MaxBytesReader` → `*http.MaxBytesError` → 413), `c.FullPath` ×1
  (`web_aichatPage:509`, `/aichat/clear/all` vs `/{model}`), `c.Request.Context()` ×2.
- Context keys: `is_https` (core:780 → web_auth:334/:359), `pugleaf.webSessionChecked|webSession|isAdmin`
  (web_auth:137-214 memo), `api_token` (set, never read), `user` (read web_login:28, never set).
- Routes: 138 registrations (74 GET, 63 POST, 1 HEAD favicon), 12 param names, one catch-all `*filepath`
  (`EmbeddedStaticHandler`, embedded FS, 404 for `/static` and `/static/`, `Cache-Control: public, max-age=3600`);
  24 registrations end in `/`; groups `/api/v1` (8 routes) and `/` (6 section routes); `GET /api`, `/api/`,
  `/api/v1`, `/api/v1/` → 301 `/help` (pre-existing: no `/help` route); `HEAD /favicon.ico` explicit; the
  `.Static("/static","./web/static")` fallback (core:364) is dead.
- Tests: harness `testmain_test.go` (`w0Do` → `w0Srv.Router.ServeHTTP(rec, req)` :195, ~101 call sites,
  bypasses rootHandler; hang guard `PUGLEAF_TEST_DEADLINE`). gin sites: `gin.CreateTestContext`
  (`w2_templates_test:79-92` incl. `c.Writer.Written()`; `lo1_handlers_test:468`; `w1_auth_test:316`),
  `gin.New()` engines (`w1_server_test:46/84/145`, `lo2_server_test:70`, `w1_auth_test:430`,
  `adf_opt_admin_test:428` TestAdfOptRawPathParams which compares against gin defaults), late `srv.Router.GET`
  (`lo1_core_test:101/:322`). No test asserts a 404 body, TSR code, 405, or any secure header except the CSP
  (`adf_xss_test:20/:150`, exact). ClientIP specs: `w1_server_test:160-172` (12 cases), `lo2_server_test:93-140`.
- Dead code to delete: `adminCleanupExpiredTokens` (`web_admin_apitokens.go:159`), `SectionsHandler`
  (`web_admin_sections.go:16`), `requireAdminAuthJSON` (`web_utils.go:132`), `ApacheLogFormat`, the `/* */` block
  `web_apitokens.go:146-325`, the `c.Get("user")` check (`web_login.go:28`), `c.Set("api_token", …)`.
- No identifier in package `web` is named `Context`, `H`, `HandlerFunc`, `Router`, `RouteGroup`,
  `ResponseWriter`, `NewTestContext`, `NewRouter`.
- Docs/scripts naming gin: `.claude/CLAUDE.md:5`, `internal/web/README.md:3`,
  `.github/copilot-instructions.md:11/13/143/166`, `docs/audit/audit-web.md:175/565`, `docs/audit/audit-db.md:609`,
  `scripts/test-web-leftovers.sh:179/182` (greps `gin.H{…}`). Generated files (`docs/audit/tools/golangci-lint.txt`,
  `FuncStructList.txt`) stay. Sample data for the end-to-end: `/home/fed/WORKSPACES/workspace.go-pugleaf/go-pugleaf/etc`
  + spool `/home/fed/WORKSPACES/workspace.go-pugleaf/legacy-sqlite3` (25 groups) via `./build_rslight_importer.sh`.

## Design

### Adapter (`internal/web/httpctx.go`, `httprouter.go`, `httpclientip.go`)
```go
type H map[string]any
type HandlerFunc func(*Context)
type ResponseWriter struct{ http.ResponseWriter /* status, size, written */ }
func (w *ResponseWriter) WriteHeader(int); Write([]byte) (int, error); Status() int; Size() int; Written() bool; Unwrap() http.ResponseWriter
type Context struct { Writer *ResponseWriter; Request *http.Request /* keys, handlers, index, router, form cache */ }
func NewTestContext(w http.ResponseWriter, r *http.Request) *Context   // router nil: ClientIP == RemoteIP; FullPath from r.Pattern
func (c *Context) Param(name string) string                             // c.Request.PathValue
func (c *Context) Query(key string) string; DefaultQuery(key, def string) string   // present key wins, like gin
func (c *Context) PostForm(key string) string; PostFormArray(key string) []string
func (c *Context) GetPostForm(key string) (string, bool); GetPostFormArray(key string) ([]string, bool)   // ok iff len > 0
func (c *Context) ShouldBindJSON(obj any) error                         // *http.MaxBytesError passes through unwrapped
func (c *Context) JSON(code int, obj any); String(code int, format string, values ...any); Data(code int, contentType string, data []byte)
func (c *Context) File(path string); Status(code int); Redirect(code int, location string)
func (c *Context) Header(key, value string); GetHeader(key string) string   // Header("", ) deletes, like gin
func (c *Context) Cookie(name string) (string, error)
func (c *Context) Set(key string, v any); Get(key string) (any, bool); MustGet(key string) any
func (c *Context) ClientIP() string; RemoteIP() string
func (c *Context) FullPath() string                                     // Request.Pattern without "METHOD "; "" when unmatched
func (c *Context) Next(); Abort(); AbortWithStatus(code int); IsAborted() bool

type Router struct { /* main, fallback *http.ServeMux; middleware []HandlerFunc; proxies []*net.IPNet; ipHeader string; RedirectTrailingSlash bool = true */ }
func NewRouter() *Router
func (r *Router) Use(mw ...HandlerFunc)                                  // global chain, read at ServeHTTP time
func (r *Router) GET/POST/HEAD(path string, h ...HandlerFunc); Handle(method, path string, h ...HandlerFunc)
func (r *Router) Group(prefix string, mw ...HandlerFunc) *RouteGroup    // main mux; pattern = METHOD + " " + prefix + path
func (r *Router) Fallback(mw ...HandlerFunc) *RouteGroup                // second mux, consulted only when the main one has no pattern
func (r *Router) SetTrustedProxies(nets []*net.IPNet, header string); RemoteIPHeader() string
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request)
func Recovery() HandlerFunc; AccessLog() HandlerFunc; SecureHeaders(cfg SecureConfig) HandlerFunc   // SecureConfig{SSLRedirect, STS bool}
```
Dispatch: `ServeHTTP` wraps `w`, builds the Context, stores it in the request context (one request pointer for
the whole request), runs `middleware + dispatch` via `Next`. `dispatch`: for main, then fallback: `if _, pat :=
m.Handler(c.Request); pat != "" { m.ServeHTTP(c.Writer, c.Request); return }` (the mux stamps `r.Pattern` and
path values and runs the registered `http.HandlerFunc`, which fetches the Context from the request context and
runs the route chain = group middleware + handlers with saved/restored handlers/index, so Abort works in nested
chains). On a miss, in this order: (1) `RedirectTrailingSlash`: the escaped path is not `/` and is clean →
probe both muxes with a shallow copy whose path has the slash toggled (same method): a non-empty pattern → 301
for GET, 307 otherwise, to the toggled path + `?RawQuery`; (2) **405**: probe both muxes with the same path for
each other method of {GET, HEAD, POST} (shallow copies): any non-empty pattern → `Allow: <methods>` (GET implies
HEAD) and `405 Method Not Allowed`; (3) `http.NotFound` (`404 page not found\n`). All three run inside the global
chain, so secure headers, bot block and the access line apply. Middleware: `Recovery` (recover, re-panic
`http.ErrAbortHandler`, log `[WEB]: panic serving METHOD PATH: v` + stack, 500 if nothing written); `AccessLog`
(one line after the chain: `[WEB]: <ClientIP> "<METHOD> <RequestURI>" <status> <size>B <latency>`; RequestURI
captured before handlers since EmbeddedStaticHandler rewrites `URL.Path`); `SecureHeaders` writes the five
headers with `Header().Set` before Next, identical values, HSTS spelled `includeSubDomains` (RFC 6797),
SSLRedirect only when `cfg.SSLRedirect && r.TLS == nil && !EqualFold(URL.Scheme,"https") &&
!EqualFold(X-Forwarded-Proto,"https")` → 301 + Abort. `ClientIP` implements the gin algorithm against
`router.proxies`/`router.ipHeader`; `configureTrustedProxies[WithHeader](router *Router, …) []*net.IPNet` keep
their signatures and log lines and call `SetTrustedProxies(nets, "X-Real-IP"|"X-Forwarded-For")`.

### Route table conversion (web-port)
`GET/POST/HEAD(path)` calls stay; `:name` → `{name}`; `*filepath` → `{filepath...}`; a path ending in `/` →
`…/{$}` (24 cases); `"/"` → `/{$}`; `Group("/api/v1", requireAPIEnabled, APIAuthRequired)` → `Router.Group`;
the `Group("/")` with `sectionValidationMiddleware` → `Router.Fallback(s.sectionValidationMiddleware())` holding
exactly `GET /{section}`, `GET /{section}/{$}`, `GET /{section}/{group}/{$}`,
`GET /{section}/{group}/articles/{articleNum}`, `GET /{section}/{group}/message/{messageId}`,
`GET /{section}/{group}/tree/{threadRoot}`. Everything else goes to the main mux unchanged in shape: the nine
`/hierarchy/{hierarchy}[/{level1}[/{level2}[/{level3}]]][/{$}]` registrations, `/hierarchy-groups/{hierarchy}`,
`GET /static/{filepath...}` (`EmbeddedStaticHandler` unchanged apart from the type), `GET`+`HEAD /favicon.ico`,
`POST /aichat/clear/all` + `POST /aichat/clear/{model}` (literal wins; `FullPath()` strips the method so
`web_aichatPage.go:509` keeps comparing `"/aichat/clear/all"`), `GET /api`, `/api/{$}`, `/api/v1`, `/api/v1/{$}`.
The dead on-disk static fallback is deleted.

### Mechanical conversion (web-port)
`*gin.Context` → `*Context`, `gin.H` → `H` (also as a type), `gin.HandlerFunc` → `HandlerFunc`; drop the
gin/secure imports; `NewWebServer`: `router := NewRouter(); router.Use(Recovery(), AccessLog())`, delete
`SetMode/UseRawPath/UnescapePathValues/unescapeRawPathParams`, `router.Use(server.trackInFlight())`,
`router.Use(SecureHeaders(SecureConfig{SSLRedirect: webconfig.SSL, STS: webconfig.SSL}))`,
`router.Use(server.ReverseProxyMiddleware())`; `WebServer.Router *Router`; comments naming gin reworded.
Dead code deleted (list in Context). Tests: `gin.CreateTestContext(rec)` + `c.Request = …` →
`c := NewTestContext(rec, httptest.NewRequest(…))`; `gin.New()` → `NewRouter()` with `{name}` paths;
`TestAdfOptRawPathParams` rewritten against `NewRouter()` only (`GET /g/{group}/m/{id}`, `GET /s/{filepath...}`)
with literal expectations (`comp.lang.c++|<ab+c@x>`, `a.b|<a b%c?d#e@x>`, `js/thread-tree.js`, `a b.css`; the
catch-all value has no leading slash); `lo1_core_test` late `GET` unchanged apart from the type. go.mod: remove
the two requires, `go mod tidy` (offline; expect ~28 requires gone).

## Waves

### Wave 0 (inline, orchestrator, branch `plan-drop-gin` from `testing-001`)
Move this plan to wip; preflight (`go build ./... && go vet ./...`). No stub needed: the adapter compiles next to gin.

### Wave 1 (2 parallel slices)
- slice `web-adapter`: owned new `internal/web/httpctx.go`, `internal/web/httprouter.go`,
  `internal/web/httpclientip.go`, new `internal/web/httpx_test.go`. Not: any existing file, go.mod.
  Goal: the adapter exactly as designed, compiling alongside gin (no gin import, no name clash;
  `configureTrustedProxies*` stay in core until wave 2). Tests (no DB; run under the package TestMain): every
  Context method incl. String raw-vs-format and Content-Type precedence, JSON bytes (`<` escaped, sorted keys,
  charset header), Data/Status/File (favicon-style Content-Type set before File), Redirect codes, Cookie
  QueryUnescape, PostForm on urlencoded and multipart bodies (query never merged; GetPostForm ok semantics),
  ShouldBindJSON + MaxBytesReader → `*http.MaxBytesError`, Set/Get/MustGet; router: chain order, Abort in global
  and route chains, Group prefix+middleware, Fallback consulted only on main miss, late registration after a
  request, HEAD served by a GET route, `{$}` vs TSR (GET 301, POST 307, root never redirected, TSR before 405),
  **405 + Allow** for a known path with the wrong method (GET route hit with POST → `Allow: GET, HEAD`; POST route
  hit with GET → `Allow: POST`), 404 through the global chain (secure headers present on a 404 and on a 405),
  FullPath for a `/x/all` vs `/x/{model}` pair, `%2F`/`+`/`%2B` path values (the TestAdfOptRawPathParams table),
  Recovery 500 + log line, AccessLog one line; ClientIP: the 12 cases of `w1_server_test:160-172` and the cases
  of `lo2_server_test:93-140` copied verbatim (peer "" / RemoteAddr without port → ""); SecureHeaders exact values
  with and without SSL (HSTS `includeSubDomains`; 301 to `https://host/uri` on plain HTTP only); a conflict test
  registering the full converted production route table (main-mux list + the six fallback patterns, kept as a
  `[]string` in the test) with no-op handlers → no panic. Checks: `gofmt -l internal/web`; `go vet ./...`;
  `go build ./...`; `go test -race -run 'Httpx' ./internal/web/`.
- slice `web-docs`: owned `.claude/CLAUDE.md` (line 5 "Gin web gateway" → "net/http web gateway"),
  `internal/web/README.md` (line 3 + one line naming the three adapter files), `.github/copilot-instructions.md`
  (:11, :13, :143, :166), `docs/audit/audit-web.md` (:175 "gin writes" → "encoding/json writes"; :565 "gin's
  `:messageId`" → "the `{messageId}` segment"), `docs/audit/audit-db.md` (:609 "gin Recovery" → "the router's
  Recovery middleware"), `scripts/test-web-leftovers.sh` (:179/:182 greps → `(gin\.)?H\{` so they work before and
  after). Not: any `*.go`, go.mod, generated files. Checks: `bash -n scripts/test-web-leftovers.sh`;
  `grep -rn -i "\bgin\b" <the five docs>` → nothing.

### Wave 2 (1 slice, after wave 1 merged)
- slice `web-port`: owned every other file in `internal/web` (the 41 gin files, the 7 gin test files,
  `testmain_test.go` if needed), `go.mod`, `go.sum`. Not: the three adapter files and `httpx_test.go` (report
  needed adapter changes; a blocking one may be fixed in a clearly separate commit), docs/scripts.
  Goal: the mechanical conversion, core rewrite (router construction, chain, trusted proxies on `*Router`, secure
  config, route table in ServeMux syntax, `Fallback` for the section routes), `embedded_static.go` type change,
  dead code removal, test edits, gin+secure out of go.mod, `go mod tidy`. Checks: `gofmt -l internal/web cmd/web`;
  `go vet ./...`; `go build ./...`; `grep -rn "gin-gonic\|gin-contrib\|gin\." --include=*.go .` → nothing;
  `grep -n gin go.mod go.sum` → nothing; `go test -race ./internal/web/... ./cmd/web/...`.

### Wave 3 (verifier)
Full Checks, `./build_webserver.sh`, `./build_rslight_importer.sh`, the End-to-end below.

## Checks (merged tree)
```bash
gofmt -l internal/web cmd/web                                    # nothing
go vet ./... && go build ./...
grep -rn "gin-gonic\|gin-contrib" --include=*.go --include=go.mod --include=go.sum .   # nothing
go list -deps ./cmd/web | grep -c "^github.com/\|^golang.org/\|^google.golang.org/"  # ~10 (was 36 gin-tree packages more)
go test -race ./internal/web/... ./cmd/web/...
# then the full CLAUDE.md race list
PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/   # only T-nntp-article-1
./build_webserver.sh && ls -la build/webserver
```

## End-to-end (verifier; scratch root `-data ./data-test-dropgin`; never `./data`, `data_ram`, `data_local`; non-default port)
```bash
cd /tank0/efs/GO/src/github.com/go-while/go-pugleaf && ./build_webserver.sh && ./build_rslight_importer.sh
HOST=i2pn2.pugleaf.net   # must resolve
./build/rslight-importer -data ./data-test-dropgin -nntphostname $HOST -etc /home/fed/WORKSPACES/workspace.go-pugleaf/go-pugleaf/etc \
  -spool /home/fed/WORKSPACES/workspace.go-pugleaf/legacy-sqlite3; echo exit=$?    # 0; ~25 groups
./build/webserver -data ./data-test-dropgin -webport 18991 -nntphostname $HOST >./data-test-dropgin/web.log 2>&1 & PID=$!; sleep 3
B=http://127.0.0.1:18991; J=./data-test-dropgin/jar
curl -si $B/ping                        # 200; X-Frame-Options DENY, X-Content-Type-Options nosniff, X-Xss-Protection, Referrer-Policy, CSP; no HSTS
curl -si $B/a/b/c/d/e                   # 404 "404 page not found" WITH the CSP header
curl -si $B/nonexistent                 # 303 to the YouTube URL (section middleware, as before)
curl -si $B/groups/ ; curl -si $B/stats ; curl -si $B/hierarchies ; curl -si $B/hierarchy/rocksolid ; curl -si $B/hierarchy/rocksolid/nodes/   # 200 each
curl -si "$B/rocksolid/rocksolid.nodes.help"            # 301 Location: /rocksolid/rocksolid.nodes.help/ (TSR) → then 200
curl -si -X POST "$B/login/"                            # 307 /login (TSR for non-GET)
curl -si -X POST $B/groups                              # 405, Allow: GET, HEAD, with the CSP header
curl -si "$B/groups/rocksolid.nodes.help/message/%3Ca%2Fb+c@x%3E"   # the handler's own not-found page, not the router's 404 (%2F stayed in the segment)
curl -sI $B/static/js/thread-tree.js    # 200, Cache-Control public, max-age=3600; curl -si $B/static/ → 404
curl -sI $B/favicon.ico                 # 200 image/x-icon (HEAD)
curl -s -c $J -b $J -X POST --data-urlencode username=e2eadmin --data-urlencode email=e2e@e2e.invalid --data-urlencode password1=E2e-pass-123 --data-urlencode password2=E2e-pass-123 $B/register -o /dev/null -w '%{http_code}\n'   # 303, first user = admin (check the form field names in web/templates/register.html)
curl -s -c $J -b $J -X POST --data-urlencode username=e2eadmin --data-urlencode password=E2e-pass-123 $B/login -o /dev/null -w '%{http_code}\n'   # 303, Set-Cookie session_id
curl -s -b $J $B/profile -o /dev/null -w '%{http_code}\n'; curl -s -b $J $B/admin -o /dev/null -w '%{http_code}\n'   # 200 200; then browse /groups/rocksolid.nodes.help, an article page, a thread tree, /SitePost
# API: enable it (admin settings form or the config key behind config.CFG_KEY_API_ENABLED), create a token via the admin page with the jar, then:
curl -si $B/api/v1/groups                            # 401 JSON (application/json; charset=utf-8)
curl -si -H "X-API: $TOKEN" $B/api/v1/groups         # 200 JSON; curl -si -H "X-API: $TOKEN" $B/api/v1/stats → 200
curl -si -b $J -X POST $B/aichat/clear/all           # JSON answer for model "all" (FullPath path)
grep -c '\[WEB\]: ' ./data-test-dropgin/web.log      # one access line per request; grep -c panic → 0
kill -INT $PID; wait $PID; echo exit=$?               # 0
# optional header diff: build the base commit's webserver in a throwaway worktree under /tank0/claude/trees, run it on 18992 against a copy of the scratch root, curl -sI the same URLs, diff (expected differences: HSTS spelling only with SSL, 405 vs 404, TSR now carries the secure headers).
# optional: PORT=18993 ./scripts/test-web-leftovers.sh with its own scratch root must stay green.
```
Leave `./data-test-dropgin` for the user to delete.

## Decisions taken
- 405 + `Allow` for a known path with the wrong method (user, 2026-10-17); TSR redirect is tried first.
- Per-request access line kept (`[WEB]: …` through `log`, stderr, project prefix convention; nothing parses gin's `[GIN]` lines).
- `X-Xss-Protection` kept for parity although deprecated.
- The dead on-disk static fallback (`./web/static`) is deleted; static files stay embedded.

## Risks / behaviour changes (name them in the merge commit)
- 405 where gin answered 404; TSR redirects now run inside the global chain (secure headers, bot block, access
  line); `/static` without slash is no longer special-cased beyond the mux; unclean paths (`//x`, `/a/../b`) get the
  mux's 307 to the cleaned path (were 404).
- Request bodies are capped (user, 2026-10-17, after the 32 MiB multipart finding): `Router.SetMaxBodyBytes`,
  1 MiB for every route; only `POST /SitePostSubmit` gets `SetRouteMaxBodyBytes` =
  `max(1 MiB, 6 × WebPostMaxArticleSize + 64 KiB)` (CRLF arrives as 6 bytes), set at startup and from the admin
  settings page (review of 7bc21b1: a global raise would have reopened big anonymous bodies on /login);
  a urlencoded form past the cap is 413 and any multipart body 415 before the route runs (no upload feature;
  nothing is spooled to disk any more, the multipart cleanup of wave 1 is gone); the 10 MiB urlencoded cap of
  net/http no longer applies (MaxBytesReader replaces it); JSON past the cap fails in `ShouldBindJSON`.
- HSTS spelled `includeSubDomains`; 404 body gains a trailing newline; the catch-all value has no leading slash
  (only the rewritten test sees it); Recovery logs a stack also for broken-pipe panics.
- The Context travels in the request context; route handlers must keep using `c.Request` (the pointer the mux
  stamped) for `PathValue`. Late `Router.GET` after serving is allowed; duplicate patterns panic like gin.
- Pattern conflicts: the conflict test in wave 1 guards the converted table; any new route with a literal first
  segment and a wildcard second one must stay out of the fallback mux's shape.

### Progress
- Wave 0 (461b5b2 on `plan-drop-gin`, branched from testing-001 at the wip commit): preflight build + vet green; no code change.
- Wave 1 (merged, tree deefc83):
  - `web-docs` (worktree-agent-a6ee302c268b2956c, ecc461b, merge a3c0df4; + inline 2192cd0 README route syntax):
    CLAUDE.md, internal/web/README.md, copilot-instructions, audit-web/audit-db wording, leftovers script greps
    `(gin\.)?H\{`. Review MERGE.
  - `web-adapter` (worktree-agent-a53b76b27657aba0b, 780ffcf + 6229e06, merge deefc83): httpctx.go (408),
    httprouter.go (418), httpclientip.go (70), httpx_test.go (1194; 30 TestHttpx*). Deviations accepted: status
    line written late like gin (WriteHeaderNow), Redirect writes the header at once, the mux's own clean-path
    slash redirect counts as a miss so the router's TSR step answers 301/307 and a fallback exact match wins,
    SecureHeaders redirect uses URL.RequestURI(), RouteGroup.Use added, 137 live routes (the plan's 138 counted
    a commented-out line). Review MERGE AFTER FIXES: MAJOR (security) multipart temp files were never deleted
    because the request copy carried the parsed form → RemoveAll in a defer + test; minors: Hijack on the writer
    wrapper, AccessLog line in a defer so panics get their line. Notes for wave 2: register
    `router.Use(AccessLog(), Recovery())`; vet now checks `c.String` like Printf (three calls need "%s");
    a bare NewRouter trusts no proxy; GET on a two-segment POST-only path 301s to the slash form (as gin did).
- Wave 2 (merged, tree 6570e9d): `web-port` (worktree-agent-ac34f24f6834e5ae1, 4e724dc + 2775814 + 05d9aba +
  a482b90, merge 9963851): 50 files, +484/−918; all 41 handler files converted by type substitution (the reviewer
  diffed each against a mechanically converted base copy: no semantic change beyond the listed dead-code removals,
  three "%s" vet fixes and comment rewording); route table in ServeMux syntax identical to the adapter's test
  table (131 main + 6 fallback); chain AccessLog → Recovery → trackInFlight → SecureHeaders → ReverseProxy →
  Bot; TestAdfOptRawPathParams deleted (TestHttpxPathValues covers the same 12 targets); go.mod requires 38 → 11,
  go.sum 105 → 26 lines; cmd/web third-party deps now only go-sqlite3 and x/*. Review MERGE AFTER FIXES: MAJOR
  (`GET /login/` and 14 other slashed main-mux paths landed in the section fallback instead of the 301 to the
  slashless route gin gave) fixed in the adapter (a482b90: slash-removal redirect probes the main mux before the
  fallback; tests in httpx_test.go and w1_server_test.go, failing without the fix). Inline after the merge: stale
  comment in internal/models/models.go (6570e9d). Unlisted behaviour differences named in the merge message:
  unknown one-segment paths answer 405 to non-GET methods (the fallback `/{section}` matches them); 404
  Content-Type now carries charset=utf-8. scripts/test-web-leftovers.sh ran green (16/0) in the worktree with
  its own scratch root.

### Verification (0588d9d)
- Verifier PASS. Full checks green on 6570e9d (31 packages under race, 0 gin references, go.mod 11 requires);
  web + cmd/web race tests 61.5 s / 3.5 s; cmd/web third-party deps only go-sqlite3, x/crypto, x/net, x/text;
  build/webserver 33.1 MB (gin base: 48.9 MB).
- End-to-end on ./data-test-dropgin (rslight sample, 536 groups / 24 with messages) on 127.0.0.1:18991 and, for
  comparison, a build of the pre-port base dcdee8f on 18992 (worktree /tank0/claude/trees/go-pugleaf/verify-dropgin-base):
  37 anonymous URLs compared on status, Location and security headers; the only differences are the intended
  ones (405+Allow for POST /groups and POST /nonexistent; HEAD on GET routes 200; redirects carry the security
  headers; 404 body newline + charset; `//groups` and `/a/../groups` 307 to the cleaned path instead of gin's
  protocol-relative 301 / 404). HTML page lengths identical; API JSON bodies byte-identical. Register (first user =
  admin), login (2.1 s loginDelay), logout Sec-Fetch-Site rules, admin pages, group/article/thread-tree/section
  pages, API 401/200/405, /aichat/clear/all (FullPath) all as expected; CrossOriginProtection still 403s a
  cross-site POST. Access log: one `[WEB]:` line per routed request, 0 panics; gin had not logged its slash
  redirects. SIGINT → exit 0 with "Graceful shutdown completed" on both servers. scripts/test-web-leftovers.sh on
  18993: 16/0, 0 races. No leftover processes.
- Pre-existing, identical on the gin base, not part of this plan: `GET /static` answers 301 to itself (the
  reserved-name redirect in sectionPage, web_sectionsPage.go:60-67, loops); `GET /admin?tab=apitokens` returns
  500 once a token exists (`admin_apitokens.html:96` uses `.TokenHash`, which `database.APIToken` lacks; since
  ab3a497). The default BadBots list blocks the curl User-Agent (expected).

### Body cap (after the verification, user request 2026-10-17)
- 7bc21b1 caps request bodies (1 MiB, ParseForm instead of ParseMultipartForm, 413/415 before the route runs,
  multipart temp cleanup gone); review (opus): no blocker, 1 major-ish (the raise applied to every route) + 3 minor
  → c453c36 (per-route cap `SetRouteMaxBodyBytes` for POST /SitePostSubmit only, factor 6, every multipart/*,
  GET test) and 9086a5b (duplicate pattern across muxes panics, rawBody comment); re-review: MERGE. Full checks
  green on c453c36 (31 packages under race, known bugs only T-nntp-article-1); web + cmd/web rerun on 9086a5b.
  Live on 127.0.0.1:18994 against ./data-test-dropgin: startup line `Request body cap: 1048576 bytes
  (/SitePostSubmit: 1048576 bytes)`, multipart → 415, 2 MiB form → 413 on /login and /SitePostSubmit, normal
  forms unchanged, 0 temp files, SIGINT exit 0.

### Pre-existing web bugs (user request 2026-10-17, "fix these")
- b788b2c: sectionPage answers 404 for a reserved first segment (knownNonSectionPaths) instead of a 301 to itself
  (GET /static looped); the four /api base paths redirect to /SiteHelp (/help never had a route);
  admin_apitokens.html reads `.APIToken` (the stored hash) instead of the missing `.TokenHash`, so the tokens tab
  renders again. Tests TestW1ServerReservedSectionNames, TestW1ServerAdminAPITokensTab. Review (opus): MERGE, 3 minor
  → 4d299a2: create/update section refuse reserved names, stale /help mentions. Full checks green on b788b2c
  (31 packages, known bugs only T-nntp-article-1); web + cmd/web rerun on 4d299a2. Live: /static 404, /api* 301
  /SiteHelp, /SiteHelp 200.


## Outcome
- Integration branch `plan-drop-gin` (23 commits), merged into `testing-001` as **8b9dfcb** on 2026-10-17 (unpushed).
- Worktrees under /tank0/claude/trees/go-pugleaf/ (left for the user): `agent-a6ee302c268b2956c`
  (worktree-agent-a6ee302c268b2956c, web-docs), `agent-a53b76b27657aba0b` (worktree-agent-a53b76b27657aba0b,
  web-adapter), `agent-ac34f24f6834e5ae1` (worktree-agent-ac34f24f6834e5ae1, web-port), `verify-dropgin-base`
  (detached at the pre-port base dcdee8f, used for the header diff).
- Verification: PASS (see the Verification note above); after it, the body cap (7bc21b1, c453c36, 9086a5b) and the
  two pre-existing web bugs (b788b2c, 4d299a2) were added on request, each reviewed and re-checked; full checks
  green on b788b2c, web + cmd/web rerun on 4d299a2.
- Behaviour changes are listed in the merge commit message and under "Risks / behaviour changes".
- Leftovers: none from this plan. Scratch roots to delete when convenient: `./data-test-dropgin`,
  `./data-test-dropgin-base`. Reviewer minors deferred: a global middleware that reads the body other than through
  the form helpers would make a route cap restart counting (documented at Context.rawBody); adminUpdateSettings
  panics (→ Recovery 500) on a hand-built WebServer without the /SitePostSubmit route.
