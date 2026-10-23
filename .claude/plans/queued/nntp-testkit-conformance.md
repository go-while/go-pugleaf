# Plan: nntp-testkit-conformance — black-box RFC checks in the kit

## Context
The brief for nntp-testkit (2026-10-17) listed a `conformance` package as "later, not in this task"; the kit
holds its `doc.go` stub (planned checks for RFC 3977, 4643, 4644). First consumers: NewsCache (C++ caching
proxy), go-pugleaf's nntp-server, nntp-server and nntp-proxy. Repo: /tank0/efs/GO/src/github.com/go-while/nntp-testkit
(kit worktrees under /tank0/claude/trees/nntp-testkit/, branch per slice, merged --no-ff into main).

## Design
- `conformance.Options{Addr string; User, Pass string; PostGroup string; TLS *tls.Config; Deadline time.Duration;
  Skip []string}`; `type Result struct{ Name string; Status (Pass|Fail|Skip); Detail string; RFC string }`;
  `type Report struct{ Results []Result; Capabilities []string }` with `Failed() []Result` and `String()`.
- `func Run(tb testing.TB, o Options) Report` (fails tb when any check fails unless `o.Report` only) and
  `func Check(o Options) (Report, error)` without testing.
- Checks, each a `func(*session) Result` speaking through `client.Conn` in strict mode: greeting code and CRLF;
  CAPABILITIES format (101, VERSION line, "."), every advertised capability consistent with behaviour (READER →
  GROUP works; OVER [MSGID]; POST/IHAVE/STREAMING per RFC 4644); MODE READER; GROUP/LISTGROUP codes and ranges;
  ARTICLE/HEAD/BODY/STAT by number/id/current, 412/420/423/430, dot-stuffing and terminator (strict client);
  NEXT/LAST 421/422; LIST variants and OVERVIEW.FMT; OVER field count; DATE format; HELP; QUIT 205 then EOF
  with ExpectNoPending; unknown command 500, bad args 501; line ends CRLF everywhere; nothing pending after any
  reply; AUTHINFO (4643) sequences when User set; MODE STREAM/CHECK/TAKETHIS/IHAVE (4644) when STREAMING is
  advertised and PostGroup set (articles posted carry a unique Message-ID under @conformance.invalid).
- Checks skip (not fail) when the capability is not advertised; every check runs on its own connection so one
  failure does not poison the next.
- `cmd/nntp-conformance`: flags `-addr -user -pass -group -tls -deadline -json`; prints the report; exit 1 on
  any Fail.
- Self-test: the kit's own `fakeserver` Store server must pass every check (this doubles as the handler's
  conformance test); a scripted fake with deliberate mistakes (bare LF, missing terminator, pending NUL after
  400) must produce the matching Fail results.

## Waves
### Wave 0 (inline): conformance/doc.go rewritten to the contract above; `conformance/types.go` (Options, Result,
  Report, Status) so slices compile against it.
### Wave 1 (parallel)
- slice `conf-reader`: conformance/reader.go (+_test): greeting, CAPABILITIES, MODE, GROUP/LISTGROUP, ARTICLE
  family, NEXT/LAST, LIST*, OVER, DATE, HELP, QUIT, unknown/501, wire checks. Tests against fakeserver Store.
- slice `conf-auth-stream`: conformance/auth.go, conformance/stream.go (+_tests): RFC 4643 and 4644 checks;
  tests against fakeserver StoreHandler (auth) and PeerHandler (streaming).
### Wave 2
- slice `conf-run-cli`: conformance/run.go (Run/Check, per-check connections, skip logic, Report.String),
  cmd/nntp-conformance/main.go, README section, negative tests with scripted mistakes.
## Checks
`cd ../nntp-testkit && gofmt -l . && go vet ./... && go test -race -count=1 ./...`
## End-to-end
`go run ./cmd/nntp-conformance -addr 127.0.0.1:<port>` against a fakeserver Store server started by a tiny
helper (or against go-pugleaf's nntp-server on a scratch data root `-data ./data-test-conformance`): report with
0 Fail; the same against a scripted fake with a bare-LF greeting: 1 Fail naming BareLF.
