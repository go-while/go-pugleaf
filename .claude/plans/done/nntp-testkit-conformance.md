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

## Progress / Outcome (2026-10-25)
Run in the kit repo (`/tank0/efs/GO/src/github.com/go-while/nntp-testkit`), orchestrated from the go-pugleaf session
(general-purpose implementers in kit worktrees; the go-pugleaf worktree hook does not apply there).
- Integration branch `conformance` (worktree `/tank0/claude/trees/nntp-testkit/conformance`), base main dd1ec87 (v0.7.1).
  Wave 0 inline 8a8b7db (doc.go contract, types.go, session.go). Wave 1: `worktree-conf-reader` (7f50ab3 + review fixes
  edd0e11) merged 4955aec; `worktree-conf-auth-stream` (4814eee + 6897f43) merged f0a292e. Wave 2: `worktree-conf-run-cli`
  (29cd8f8 + a5f6275) merged 56e5eb1. Inline on the branch: 0682b01 Store HDR/XHDR + LIST HEADERS + NoHdr/NoXhdr +
  HdrLines (the go-pugleaf fetcher leftover), dcd71bb, 92475dc + e699630 (peer: no READER capability, MODE READER 502,
  AUTHINFO 482/502 and restart after a failed PASS: found by the checks).
- Merged into kit `main` as **540bcd5** (`--no-ff`, worktree `/tank0/claude/trees/nntp-testkit/main`), 22 files, +4217.
  Not pushed, not tagged (user: v0.8.0 would fit, XHDR and the conformance package are new API).
- Result: 41 checks (25 RFC 3977 reader, 6 RFC 4643, 10 RFC 4644/IHAVE), `Run`/`Check`/`Names`, `Options.Workers`,
  posting checks run after all others, `cmd/nntp-conformance` (-addr -user -pass -group -tls -tls-insecure -deadline
  -workers -skip -json; exit 0/1/2). Self-test: the Store server passes every applicable check; deliberate mistakes
  fail exactly their check. Whole kit green under -race.
- End-to-end against go-pugleaf's nntp-server (build of plan-fetcher-compress-deflate, scratch `-data
  ./data-test-conformance`, user via nntpmgr, group conf.test): first run 32 pass / 0 fail / 9 skip (empty group, in-flight
  431/436); second run with the posted articles 38 pass / 0 fail / 3 skip. "AUTHINFO not advertised" Passes are the
  deliberate `plaintextauth=accept` behaviour.
- Review decisions pinned: legal negative replies (431/438/439/435/436/437) are Skip with the code in Detail, never Fail;
  "any password accepted" is Pass-with-Detail; OVER lines may omit trailing empty fields; CAPABILITIES re-reads compare
  as sets; LIST ACTIVE status may be a private token; auth checks run without an advertised AUTHINFO.
- Leftovers: a shared cached LIST ACTIVE (11 downloads per run today); `-pass` visible in `ps` (a `-pass-env` would
  help); `asNewID` package var is a test hook; a 502 greeting's "connection closed" and "no capability twice" edge
  checks exist only partly; kit worktrees under `/tank0/claude/trees/nntp-testkit/` (conformance, conf-reader,
  conf-auth-stream, conf-run-cli) are the user's to clean.
