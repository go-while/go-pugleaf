# Plan: e2e gap tests from INN, NewsCache and diablo-nntp; kit bump and helper cleanup

## Context

User request (2026-11-10): "check ../inn ../NewsCache ../diablo-nntp for e2e tests we don't have", then
"yes coordinate with nntp-testkit-1" (the nntp-testkit session, room `nntp-testkit-`).

Surveys (2026-11-10, read-only agents, go-pugleaf testing-001 at 504de9b):
- **INN** (`../inn/tests/`): no wire-level tests; C/TAP unit tests pin header/article rules
  (`innd/artparse-t.c`, `lib/headers-t.c`, `lib/wire-t.c`, `lib/date-t.c`, `lib/messageid-t.c`,
  `lib/network/addr-ipv*-t.c`, `lib/hissqlite-t.c`, `data/overview/bogus`). We turn those rules into tests.
- **NewsCache** (`../NewsCache/tests/e2e/`, 22 Go files): strongest on the client/fetcher side (upstream faults,
  provider quirks, TLS, AUTHINFO order).
- **diablo-nntp** (`../diablo-nntp/tests/e2e/`, 78 Go files, README table): strongest on the outgoing feed.
- **nntp-testkit-1's helper survey of go-pugleaf**: `/tank0/claude/nntp-testkit-consolidation/survey-go-pugleaf.md`
  (sections 2-4: local copies of kit helpers, candidates for the kit, cleanup table with minimum kit version).

go-pugleaf pins `github.com/go-while/nntp-testkit v0.8.0`. Latest tag v0.11.2. Kit main (untagged, PR #2 + #3)
has what the gap tests need: package `article` (builder, MsgID per test, Parse, Compare/ExpectSame, corpus),
package `tlscert` (CA, leafs incl. expired / not-yet-valid / wrong-name, PEM files), client
`Post/Ihave/Check/Takethis/ModeStream/Login` (+`Try*`), `Pipeline`, `DialFrom`, `SendDrip`, `ExpectClosed`,
`StartTLS`, fakeserver STARTTLS, `PeerOptions` (`HoldGreeting`, `GreetingDelay`, `CapabilitiesReply`,
`ModeStreamReply`, `DropAfterChecks`, `SilentAfterModeStream`), `Faults` applied to `PeerHandler`, 7 more
conformance reader checks (incl. NEWNEWS), canlock, mailrec + `cmd/fakesendmail`.
Kit main 750ef8b (PR #5, untagged) adds: conformance starttls / starttls-twice / starttls-pipelined
(CVE-2011-0411) / tls-capabilities / compress*, `Options.Allow`, `Report.Require/RequireNoFail`; `proc.Run/TryRun`
(timeout, capped output, exit code), process-group kill, `Options.Daemonizes`, `GoBuild`;
`testkit.ShortTempDir/MaxSocketPath`; client `Try{Post,Ihave,Takethis}` return (code, line, sent, err),
`Expect{Post,Ihave,Takethis}`, `SendBytes`; fakeserver `NewPeer/StartPeer`, `ArticlesRaw`, `OnArticleRaw`;
article `PrependPath`, `PathEntries`, `NewArticle`. Behaviour change: a `proc` server exiting 0 before listening
fails unless `Options.Daemonizes`.
Kit behaviour changes to absorb on the bump: fakeserver Store CAPABILITIES lists `LIST ... ACTIVE.TIMES HEADERS`;
`LIST ACTIVE.TIMES` answers 215 (was 501); implicit-TLS fakeserver handshakes before the greeting.

Rules that apply: no wall-clock assertions (hang guards via `PUGLEAF_TEST_DEADLINE` only); NEWNEWS stays 500
(skip that conformance check); the 430 cache-delay known bug stays; control messages stay refused; before writing
any generic NNTP/test helper, ask nntp-testkit-1 in room `nntp-testkit-` (agreed 2026-11-10); never a `replace`
of the kit in go.mod.

## Decisions (go-while) — needed before wave 2

Answers 2026-11-11: **D2 reject everywhere** and **D3 allow `<` only inside `[..]`** are decided.
D1 and D4: go-while asked to mirror INN after checking with inn-1 (room `inn-`). INN source reading
(innd/art.c ARTparseheader/ARTcheckheader, nnrpd/post.c, lib/headers.c IsValidHeaderField):
- innd (IHAVE/TAKETHIS): no header line length limit (artparse-t.c: "Not an error for a relaying agent");
  bare CR / bare LF accepted and only logged ("Article accepted but includes CR without LF(n)", art.c:2656);
  NUL in header or body → 437; colon must be followed by SP or HTAB (`X:` + CRLF → 437 "No colon-space",
  `X: ` empty passes the parse); whitespace inside a non-system field name → 437 "Space before colon".
- nnrpd (POST): header line > 998 → "Header line too long"; `IsValidHeaderField` needs exactly `": "` and a
  valid non-empty body.
So INN = strict POST, relay-lax peers, but colon + whitespace required for peers too.
inn-1 confirmed 2026-11-11 (innd/art.c @ 6843efceb) and added: required fields (Date, From, Message-ID,
Newsgroups, Path, Subject; missing or duplicate → 437); Message-ID valid and ≤ 250 octets; Date must parse
(parsedate_rfc5322_lax), Injection-Date too if present; age/future limits are optional config only (artcutoff,
DATE_FUZZ); do NOT reject 8-bit bytes or control characters other than NUL on the relay path.
**Decided 2026-11-11 (go-while: "could be a per peer option? -lax / -strict"):** D1/D4 become a per-peer
setting `header_check` = `relay` | `strict` (new column in `peers`, migration after the last one, admin
/admin/peers field, validation like `in_max_size`). Default `relay` (innd behaviour, so peers do not lose
articles); `strict` = the POST rules below. POST is always strict.
Date limits: keep 72 h past / 24 h future as defaults, make them admin CFG settings (0 = off), like innd's
artcutoff / DATE_FUZZ; they apply to peer intake (both modes).
Rules of the two modes (was: proposed D1/D4): peer intake drops the 998 limit, accepts bare CR and bare
LF inside header lines (logged with a counter, stored verbatim), accepts colon + SP or HTAB and an empty value,
keeps NUL / missing or duplicate required fields / Message-ID / Date-parse rejects (`relay`). POST and `strict`
mirror nnrpd: ≤ 998, exactly `": "`, non-empty value.

| # | Question | Today | Recommendation |
|---|---|---|---|
| D1 | Peer intake (IHAVE/TAKETHIS) strictness | `validateIntakeHeaders` uses `HeaderBlockStrict` for POST and peers (`internal/processor/intake.go:177`): header line > 998 octets or a bare CR in a header → 437, and the reject is remembered, so peers lose those articles for good. INN accepts both as a relay. | Peers get a relay mode: accept > 998 and bare CR in header values (stored verbatim), keep NUL/LF and duplicate core headers as rejects. POST stays strict. |
| D2 | NUL byte in the body | not checked anywhere in intake | Reject in POST, IHAVE and TAKETHIS (437/439/441), like INN. |
| D3 | `<` inside a message-id | rejected anywhere | Accept only inside a `no-fold-literal` id-right (`<x@[a<b]>`, RFC 5536 mdtext); still reject elsewhere. |
| D4 | Colon-space rule | strict mode accepts `X:value` and `X:` | Strict (POST) requires `": "` and a non-empty value (RFC 5536 §2.2); peer relay mode per D1 accepts them. |

## Design

### Fix F1: a 400 reply ends the feed connection
`internal/feed/sender_conn.go:271` (CHECK) and `:324` (TAKETHIS) treat 400 like 431. RFC 3977/4644: 400 means the
peer is going away. Change: on 400 mark this article and every later one of the batch deferred and return an
error, so the sender drops the connection and redials. Test: diablo `TestDnewslinkDeferredKeptOnExit` (D-66):
CHECK a1, a2 → 431, a3 → `400 going away` + close; all three stay queued exactly once; also the case where 400
is the last answer of the batch (today: Send returns nil and the dead connection is reused once).

### Gap tests (each names its source)
Feed (diablo):
- G1 relay through the pipeline: peer A IHAVE → `feed-queue.sq3` row → TAKETHIS to peer B, never offered back to
  A (Path); a 438 at B gets no TAKETHIS. Source `dnewslink_test.go` TestDnewslinkStreaming/PeerHas. Fold in
  outgoing size/crosspost/hop limits end to end (`feedlimits_test.go`).
- G2 peer drops after mixed outcomes, sender reconnects: queue rows exact (done vs deferred, no duplicates).
  Source `dnewslink_flags_test.go` (D-73), `refile_test.go` (D-63). Uses `DropAfterChecks`.
- G3 stalled peer with the real dialer: no greeting; MODE STREAM ok then silence (`SilentAfterModeStream`).
  Sender Stop must return (hang guard only), articles stay queued. Source `dnewslinkterm_test.go`.
- G4 small window: Batch 1 and 2 with 6 queued, all offered, queue empty. Source `dnewslink_smallmax_test.go`.
Intake / server (diablo, INN, NewsCache):
- G5 bare `TAKETHIS` and bare `IHAVE` → 501, article drained, next pipelined command answered (239/335).
  Source `d91_takethis_test.go`, `replycodes_test.go`.
- G6 too many header lines over IHAVE/TAKETHIS → 437/439, next article 235/239. Source `maxheadersize_test.go`.
- G7 size limits at exactly N and N+1 wire bytes for `MaxArtSize`, peer `in_max_size`, group `max_art_size`,
  counting CRLF and dot-stuffing as `TestIntakeCommonWireArticleSize` defines. Source INN `artparse-t.c` (#6).
- G8 byte-at-a-time intake: IHAVE and pipelined CHECK/TAKETHIS written with `SendDrip`, terminator and a `..`
  header split across writes. Source INN artparse "slow"/"shift" modes.
- G9 framing rejects over POST/IHAVE/TAKETHIS: no blank line after headers, blank first line, empty article
  (and NUL in body after D2); nothing stored, session in sync. Source INN `artparse-t.c`, `wire-t.c`.
- G10 unusual valid message-ids round trip through POST/IHAVE → history → `ARTICLE <id>`/`STAT <id>`: quoted
  local part, domain literal `<x@[127.0.0.1]>`, exactly 250 octets. Source NewsCache TestHistoryUnusualMessageIDs.
- G11 AUTHINFO order: PASS after a failed PASS → 482; fresh USER+PASS → 281; rejected replacement USER ends the
  pending one. Already implemented (`nntp-cmd-auth.go` clears the user), only untested. Source NewsCache auth_test.
- G12 bogus group names (`..foo..`, `../..`, `/`): GROUP/LISTGROUP/XHDR → 411 and no group DB file created;
  `Newsgroups:` with them → 437. Source INN `data/overview/bogus`.
- G13 re-offer of a hidden/deleted article (and one whose group is gone) by IHAVE/CHECK → 435/438, not accepted
  again. Source INN `hissqlite-t.c`.
Fetcher / client (NewsCache):
- G14 upstream drops mid ARTICLE / XOVER / LIST ACTIVE (`Faults.DropInMultiline`, already in v0.8.0): client
  returns an error, nothing partial stored, fetch progress not advanced, active list not trusted.
  Source `upstream_test.go` TestUpstreamCutOff*, ArticleErrorNot423, Fault403.
- G15 provider caps OVER ranges: DownloadArticles asks again after the last record, skips nothing.
  Source TestUpstreamOverRangeCap.
- G16 low water 0 and emptied group (low = high+1 and 0 0 0): no chunk skipped. Source TestUpstreamLowZero,
  TestUpstreamEmptiedGroup.
- G17 MODE READER refused (503/500): no reconnect storm, clean error. Source TestUpstreamConnectRefusals.
- G18 upstream TLS wrong name, expired cert, close right after the handshake (`tlscert`). Source `tls_test.go`.
- G19 NUL / bare CR in a fetched article's headers on the bulk path (`ParseLegacyArticleLines`): pin the
  behaviour (skip or normalise; bare CR → space is today's fetch-mode rule). Source TestUpstreamArticleNULAndCR.
Units (INN):
- G20 peer address matching: IPv6 masks /0, /112, /113, invalid /129 and -1; `::ffff:a.b.c.d` equals the IPv4
  address. Source `lib/network/addr-ipv6-t.c`, `addr-ipv4-t.c`. Today only exact IPv6 and IPv4 /24 are tested.
- G21 Date forms in `ParseNNTPDateStrict`: 2/3-digit years, UT/GMT/EDT/`CdT`, military zones, comments,
  lowercase, no weekday, `6:4:6`; lax forms rejected in strict. Source `lib/date-t.c`. Matters because a
  misread year trips the 72h-stale / 24h-future check and 437s a valid peer article.

### Not taken
Per-peer incoming connection limit (diablo `maxconnect`): a feature, not a test; propose separately if wanted.
AUTHINFO failure limit (NewsCache TestAuthMaxFailures): SEC-5 decided on the delayed 481 only. Control-character
parity (DEL) with INN: `strict-del-passes` is deliberate.

## Waves

### Wave 0 (inline, on plan-e2e-gap-tests)
- Bump `github.com/go-while/nntp-testkit` to the kit tag that contains PR #2 + #3 (expected v0.12.0; ask
  nntp-testkit-1 / go-while for the name). `go get`, `go mod tidy`.
- Fix expectations broken by the bump: CAPABILITIES LIST line of the fake Store (`ACTIVE.TIMES HEADERS`),
  `LIST ACTIVE.TIMES` 215, implicit-TLS fakeserver greeting order.
- `PUGLEAF_TEST_DEADLINE` vs kit `NNTP_TEST_DEADLINE`: per nntp-testkit-1's answer (kit accepts extra env names,
  or go-pugleaf maps it in TestMain).
- Checks: full CLAUDE.md list green.

### Wave 1 (parallel, disjoint files)
- slice `feed-400-and-feed-e2e`: F1 + G2, G3, G4. Owns `internal/feed/sender_conn.go`, new
  `internal/feed/sender_e2e_test.go`. Checks: `go test -race ./internal/feed/...`.
- slice `relay-e2e`: G1. Owns new `cmd/nntp-server/e2e_relay_test.go` (real nntp-server binary or the in-process
  server with the feed wired as in `cmd/nntp-server/main.go`; two fake peers). Checks: `./cmd/nntp-server/...`.
- slice `intake-wire`: G5-G8, G10-G13. Owns new `internal/nntp/nntp-intake-wire_test.go`,
  `internal/nntp/nntp-auth-order_test.go`, `internal/nntp/nntp-group-names_test.go`. Pins today's behaviour where
  D1-D4 will change it (marked `// D1` etc. so wave 2 updates them). Checks: `./internal/nntp/...`.
- slice `fetcher-faults`: G14-G19. Owns new `internal/nntp/nntp-client-upstream-faults_test.go`,
  `internal/processor/proc_DLArt_upstream_test.go`. If G14/G15/G16 find bugs, fix them in this slice and list the
  touched production files in the report (likely `internal/nntp/nntp-client-commands.go`,
  `internal/processor/proc_DLArt.go`). Checks: `./internal/nntp/... ./internal/processor/...`.
- slice `units`: G20, G21. Owns `internal/nntp/nntp-peers-addr_test.go` (new), `internal/common/date_forms_test.go`
  (new); fixes in `internal/nntp/nntp-peers.go` match logic / `internal/common` date parser only if a case fails.
  Checks: `./internal/nntp/... ./internal/common/...`.

### Wave 2 (after D1-D4 are answered)
- slice `intake-policy`: implement D1-D4 in `internal/common/intake.go` (new `HeaderBlockRelay` next to
  Strict/Fetch), `internal/processor/intake.go` (mode per path: POST strict, IHAVE/TAKETHIS per peer
  `header_check`), migration `peers.header_check TEXT NOT NULL DEFAULT 'relay'` + `models.Peer.HeaderCheck` +
  peers.go insert/update/validation + `admin_peers.html` field, CFG keys for the peer Date limits (72 h / 24 h,
  0 = off) in `db_config.go` + `admin_settings.html`; message-id syntax in
  `internal/common` (D3); update the wave-1 pins marked `// D1..D4` and the INN-derived G9 NUL-in-body case.
  Checks: full list.

### Wave 3 (after wave 1+2 merged; touches many test files, so alone)
- slice `helper-cleanup`: survey section 4 rows — `e2eClient`, `sessDialRaw`, `hNewClient` → kit client;
  `xrefEnv.start/stop` → `proc.StartTCP`; `flPqFake` ×2 → fakeserver Script; `adfTorSilentProxy` → RawStall;
  FreePort/WaitFor copies → `testkit`; 5 Expect copies → `client.ExpectCmd`; 4 caps parsers →
  `client.Capabilities`; `e2eWire` stuffing → `client.Stuff`; cert/StartTLS/Ihave/Post/Login copies →
  `tlscert`/client; `e2e_binary_rfc` N50-N56 and the STARTTLS checks (N57, `nntp-rfc-starttls_test.go:277`) → `conformance.Run`
  (skip NEWNEWS); the 3 `GoBuild` copies and `xrefEnv.tool` → `proc.GoBuild` / `proc.Run`. Survey section 3 items
  (runIsolated, knownBug, RecordingConn, ParseGroup211/ExpectID, PEM-to-TempDir, fake SOCKS5) only after
  nntp-testkit-1 has moved them into a tagged kit; until then keep our copies.
  Checks: full list; test count must not drop (compare `go test -list . ./...` before/after).

## Checks
CLAUDE.md "green" list, plus `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` lists only the
known 430 bug.

## End-to-end
Wave 1 `relay-e2e` is the end-to-end scenario: build `./build_nntp-server.sh` into the worktree's `build/`, run
with `-data ./data-test-e2e-gap`, two kit fake peers on free ports; expected: article offered by peer A appears
once at peer B, never at A, queue empty afterwards, peer stats show accepted 1 in and out.
