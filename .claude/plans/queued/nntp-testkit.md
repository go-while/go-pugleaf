# Plan: nntp-testkit — extract go-pugleaf's NNTP test helpers into a shared module

## Context
Brief from the user (2026-10-17, written by another agent for this one): several projects (NewsCache in C++,
nntp-server, nntp-proxy, go-pugleaf) need the same NNTP test tools. go-pugleaf has good fakes in `_test.go` files
of `internal/nntp` (`nntp-client-fake_test.go`, 920 lines; the `hClient` part of `nntp-server-harness_test.go`),
which other modules cannot import, and importing go-pugleaf would drag databases and web into every test module.
Move them into a standalone module `github.com/go-while/nntp-testkit` in `../nntp-testkit` (next to go-pugleaf),
standard library only, `go 1.22`, GPLv2 (copied from go-pugleaf's LICENSE), `git init` on branch `main`.
Local only: no GitHub repo, no push; the user decides.

Baseline (recorded before any change, `go test -race -count=1 -v ./internal/nntp/`): see
`### Baseline` under Progress. The go-pugleaf migration must give the same results.

## Decisions
- The kit's root package `testkit` holds the deadline default: `testkit.Deadline()` reads `NNTP_TEST_DEADLINE`
  (Go duration, default 60s). `fakeserver.Options.Deadline` and `client.Conn.Deadline` override it per use;
  go-pugleaf passes its `hDeadline` (`PUGLEAF_TEST_DEADLINE`) in. A passing test never waits on the hang guard.
- Everything that takes a `*testing.T` in go-pugleaf takes `testing.TB` in the kit.
- `Backend()` and `cliDial` stay in go-pugleaf (they need `BackendConfig`); the kit exports `Addr()`, `Host()`,
  `Port()`.
- The DB-backed harness stays in go-pugleaf: `newTestServer`, `hArticleSQL`, `hUser`, `hFakeProcessor`,
  `knownBug`, `runIsolated`.
- The migration branch of go-pugleaf uses a `replace github.com/go-while/nntp-testkit => ../nntp-testkit`
  directive and is NOT merged into `testing-001` until the kit is on GitHub and tagged `v0.1.0` (brief).
- Kit worktrees: the WorktreeCreate hook branches go-pugleaf; kit implementers create their own kit worktree
  with `git -C /tank0/efs/GO/src/github.com/go-while/nntp-testkit worktree add -b worktree-<slice>
  /tank0/claude/trees/nntp-testkit/<slice> main` and work there. The orchestrator merges `--no-ff` into the
  kit's `main`.

## Design — exported API

### `testkit` (root)
- `func Deadline() time.Duration` — `NNTP_TEST_DEADLINE` or 60s; `const DefaultDeadline = 60 * time.Second`.

### `client` — from `hClient` + the text helpers of the fake file
- `type Conn struct { Conn net.Conn; R *bufio.Reader; Greeting string; Deadline time.Duration; Strict bool }`
- `func Dial(tb testing.TB, addr string) *Conn` — connects (dial timeout = Deadline), reads the greeting,
  `tb.Cleanup(Close)`; `func TryDial(addr string, deadline time.Duration) (*Conn, error)`;
  `func New(tb testing.TB, c net.Conn) *Conn` (wraps, does not read a greeting);
  `func (c *Conn) ReadGreeting() (string, error)`.
- Methods (exported forms of `send`, `readLine`, `readStatus`, `cmd`, `tryCmd`, `readDotLines`, `rawUntilDot`,
  `close`): `Send(raw string) error`, `ReadLine() (string, error)`, `ReadStatus() (int, string, error)`,
  `Cmd(format string, args ...any) (int, string)` (fails tb on I/O error), `TryCmd(format, args...) (int, string, error)`,
  `ReadDotLines() []string` (fails tb), `TryReadDotLines() ([]string, error)`, `RawUntilDot() []byte`, `Close()`.
- `func ParseCode(line string) (int, error)`, `func ExpectCode(code int, line string, want ...int) error`,
  `func ExpectLines(got, want []string) error`, `func Unstuff(wire []string) []string`.
- Strict mode (`Strict: true`, default on for `Dial`): `ReadLine` reads raw bytes and returns a `*StrictError`
  (`Kind` one of `BareLF`, `BareCR`, `NoCRLF`, `BadDotStuffing`, `NoTerminator`, `Pending`) when a line lacks CRLF,
  contains a bare CR, a dot-block line starts with "." but not ".." (and is not the terminator), or a multi-line
  reply ends in EOF/timeout without `.\r\n`. `Cmd`/`ReadDotLines` fail tb with the StrictError text.
  `func (c *Conn) ExpectNoPending(d time.Duration) error` reads for `d` and returns `*StrictError{Kind: Pending,
  Bytes: ...}` on any byte (e.g. a NUL after `400 too many users\r\n`); nil on timeout. Non-strict mode keeps
  go-pugleaf's lenient behaviour (LF accepted).

### `fakeserver` — from the fake file
- `type Options struct { Addr, Greeting string; NoGreeting, RawStall bool; TLS *tls.Config; Script []Step;
  Handler func(*Conn); Store *Store; Deadline time.Duration }` — same semantics as `cliFakeOpts`; precedence
  RawStall > greeting > Handler > Store > Script.
- `type Step struct { Expect string; ReadDot bool; Delay time.Duration; Reply []string; Hang, CloseAfter bool }`.
- `type Server struct { ... }`; `func New(tb testing.TB, o Options) *Server` (registers `tb.Cleanup(Close)`);
  `func Start(o Options) (*Server, error)` (no testing dependency, for a CLI); methods `Addr() string`,
  `Host() string`, `Port() int`, `Lines() []string`, `Articles() [][]string`, `Mismatches() []string`,
  `Accepted() int`, `Open() int`, `WaitOpen(tb, n int, d time.Duration)`, `WaitLines(tb, want []string)`,
  `Close()`, `Count(cmd string) int` (upper-cased first word of every client line, all modes), `Counts() map[string]int`.
- `type Conn struct { ... }` with `ReadLine() (string, error)`, `WriteLines(lines ...string) error`,
  `ReadDotBlock() ([]string, error)`, `Raw() net.Conn`, `Server() *Server`.
- `func TLSConfig(tb testing.TB) (server *tls.Config, roots *x509.CertPool)`.
- `type PeerOptions` (= `cliPeerOpts`), `func PeerHandler(o *PeerOptions) func(*Conn)`.
- Store mode: `type Store struct` (mutex inside) with `AddGroup(name, description string)`,
  `AddArticle(group string, a Article) int64` (number assigned or `a.Number` when set), `Article(group string,
  n int64) (Article, bool)`, `ByMessageID(id string) (Article, []Location, bool)`, `Groups() []Group`,
  `Posted() []Article` (articles accepted by POST), `Overview(group string, from, to int64) []Article`;
  `type Article struct { Number int64; MessageID string; Headers []string; Body []string; Date time.Time }`,
  `type Group struct { Name, Description string; Low, High, Count int64; Posting bool }`.
  `StoreHandler(st *Store, o StoreOptions) func(*Conn)` serves `CAPABILITIES`, `MODE READER`, `LIST`
  (`ACTIVE`, `NEWSGROUPS`, `OVERVIEW.FMT`), `GROUP`, `LISTGROUP`, `ARTICLE`/`HEAD`/`BODY`/`STAT` (number, message-id,
  current), `XOVER`/`OVER`, `NEXT`/`LAST`, `NEWGROUPS`, `DATE`, `POST`, `AUTHINFO USER/PASS`, `QUIT`; RFC 3977
  codes; `StoreOptions{ User, Pass string; ReaderOnly bool }`. `Options.Store` runs it when Handler is nil.
- Fault hooks (`Options.Faults`, `type Faults struct`): `Delay map[string]time.Duration` (before the reply to a
  command), `CloseAfter int` (close the connection after N commands), `Override map[string][]string` (reply
  lines for a command, instead of the store/script answer), `DropInMultiline map[string]int` (close after N
  lines of the multi-line reply to a command). Apply to store mode and to Handler-less script mode.
- `TestCliFake*` tests move in as `TestScript`, `TestOpenCount`, `TestPeerStream` (using `client`).

### `proc` — external server binaries
- `type Options struct { Path string; Args []string; Env []string; Dir string (default tb.TempDir());
  StopTimeout time.Duration; Logs []string (files to collect) }`.
- `func StartStdio(tb testing.TB, o Options) *Process` — stdin/stdout as the client connection; `Process.Conn()
  net.Conn` (a `net.Pipe`-style adapter over the pipes with deadlines).
- `func StartTCP(tb testing.TB, o Options, addr string) *Process` — waits for readiness by dialling `addr` until a
  greeting line arrives (within the deadline); `Process.Addr() string`.
- `func (p *Process) Signal(sig os.Signal) error`, `Stop()` (SIGTERM, then SIGKILL after StopTimeout, default
  5s), `Stderr() string`, `Wait() error`; on tb failure, stderr and the `Logs` files are written to the test output
  via `tb.Log` (in a `tb.Cleanup`).
- Tests use a tiny Go program built in the test (`go build` into tb.TempDir() of a `testdata/echo-server`
  main that greets `200 proc-fake ready` and echoes commands as `500 echo <line>`), both modes.

### `conformance`
- `doc.go` stub only: black-box RFC 3977, 4643 and 4644 checks against any address; plan outline in the doc.

### README
- API overview per package and one short example: a fake upstream (store mode) plus a strict client.

## Waves

### Wave 0 (inline, orchestrator)
- `git init -b main ../nntp-testkit`; `go.mod` (`module github.com/go-while/nntp-testkit`, `go 1.22`); LICENSE
  copied; `testkit.go` (`Deadline`, `DefaultDeadline`) + test; `doc.go` of each package with the API contract
  above; `conformance/doc.go` stub; README skeleton; `.gitignore`. Commit on `main`.
- Record the go-pugleaf baseline in this plan.

### Wave 1 (kit; parallel)
- slice `client`: `client/client.go`, `client/strict.go`, `client/client_test.go` (tests run a local
  `net.Listen` fixture inside the package; no dependency on fakeserver). Checks: `gofmt -l client`, `go vet
  ./...`, `go test -race -count=1 ./client/`.
- slice `proc`: `proc/proc.go`, `proc/stdio.go`, `proc/proc_test.go`, `proc/testdata/echo-server/main.go`.
  Checks as above for `./proc/`.
- slice `store`: `fakeserver/store.go`, `fakeserver/store_test.go` — the data model only (Store, Article, Group,
  Location, Overview ordering, numbering, Posted); no server code. Checks for `./fakeserver/` (package compiles
  alone with `doc.go`).

### Wave 2 (kit)
- slice `fakeserver-core`: `fakeserver/server.go`, `fakeserver/conn.go`, `fakeserver/tls.go`, `fakeserver/peer.go`,
  `fakeserver/server_test.go` — the moved scripted server, Conn, TLSConfig, PeerHandler, WaitLines, Count/Counts,
  `Start`/`New`, the moved `TestCliFake*` tests using `client`. Owns no store files.
- slice `fakeserver-store-handler`: `fakeserver/storehandler.go`, `fakeserver/faults.go`,
  `fakeserver/storehandler_test.go` — StoreHandler, StoreOptions, Faults; tests use `client` and `New` from core.
  (Depends on core's `Conn`/`Server` API as fixed in `doc.go`; if core is not merged yet when it starts, it codes
  against the contract and the orchestrator merges core first.) Run as wave 2b if the contract is unclear.

### Wave 3
- slice `kit-readme`: `README.md` with the API list and the example; `doc.go` touch-ups (kit).
- slice `pugleaf-migration` (go-pugleaf, worktree from the hook, branch `worktree-pugleaf-migration`, integration
  branch `plan-nntp-testkit`): `go.mod`/`go.sum` (`require github.com/go-while/nntp-testkit v0.0.0` + `replace`
  to `../nntp-testkit`), `internal/nntp/nntp-client-fake_test.go` (thin wrappers: `cliFakeOpts = fakeserver.Options`
  type aliases or direct use; keep `cliDial`, `Backend()` as helpers on `Addr()`; `cliWait = hDeadline` passed in),
  `internal/nntp/nntp-server-harness_test.go` (hClient → `client.Conn`), every `_test.go` in `internal/nntp` that
  used the moved identifiers (type substitution only). Results must match the baseline exactly.

## Checks (kit)
```bash
cd /tank0/efs/GO/src/github.com/go-while/nntp-testkit && gofmt -l . && go vet ./... && go test -race -count=1 ./...
```
## Checks (go-pugleaf migration branch)
CLAUDE.md checks; `go test -race -count=1 -v ./internal/nntp/` PASS/FAIL/SKIP list identical to the baseline;
`PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` unchanged (only T-nntp-article-1).

## End-to-end
- Kit: the README example compiled as `Example` tests; `go test -race ./...` green.
- go-pugleaf: baseline comparison as above; `PUGLEAF_TEST_DEADLINE=5s` still honoured (a hang guard test).

## Report back (brief)
Exported API list, test results for both repos, what could not move cleanly, every commit hash. No push.

## Progress

### Baseline (2026-10-17, before any change)
`go test -race -count=1 -v ./internal/nntp/` on testing-001 c1a10ca (+ this plan file): 210 top-level results,
all `--- PASS`, 0 FAIL, 0 SKIP; package ok in 164.8 s (under race, machine loaded). Full sorted list kept at
`/tank0/efs/claude/tmp/claude-1000/-tank0-efs-GO-src-github-com-go-while-go-pugleaf/698bff14-0ada-4301-9aa4-caddf76f2a5b/scratchpad/nntp-baseline-keep.txt` for the migration comparison (also reproducible with the same command).

