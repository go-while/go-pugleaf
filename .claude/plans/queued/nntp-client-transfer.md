# Plan: NNTP client, pool, streaming transfer and nntp-transfer tool (from audit 2026-10)

- **Slug:** `nntp-client-transfer`
- **Integration branch:** `plan-nntp-client-transfer` (from `testing-001`)
- **Run with:** `/run-plan .claude/plans/queued/nntp-client-transfer.md`
- **Base:** written against `plan-nntp-audit-tests` @ 03d5ef7 (all `file:line` anchors below were checked there with
  `grep -n`/`sed -n`). The run starts from `testing-001` after `plan-nntp-audit-tests` is merged into it; the preflight
  re-checks the anchors listed in K6 and records any drift in `## Progress`.
- **Depends on:** `nntp-server-hardening`, `nntp-intake-injection` (both merged first). Final run order:
  nntp-server-hardening → nntp-intake-injection → **nntp-client-transfer** → nntp-rfc-commands → audit-delta-fixes.
  From the intake plan this plan uses `common.ValidMessageID` (waves 0, 2, 3), see K1. Note from the intake run
  (2026-10-11): `ValidMessageID` is RFC 5536-strict on the right side (dot-atom-text or a `[...]` literal with `\[`,
  `\]`, `\\` escapes) but lax on the left: any printable except `<`, `>`, `@` (so `<a..b@x>` passes and a quoted
  `<"a@b"@c>` fails); ≤ 250 octets. `CheckHeaderBlock(lines, HeaderBlockFetch)` lets DEL (0x7F) pass and drops
  WSP-only continuation lines.
  Neither earlier plan owns a file of this plan (see Coordination); the K6 anchor check absorbs line drift.
- **Parallelism:** wave 1 has 2 slices, wave 2 has 2, wave 3 has 1. Within a wave, no two slices own the
  same file. A file may be owned by different slices in different waves (the later wave builds on the merged earlier one).
- **Source:** `docs/audit/AUDIT-2026-10.md` (§3 Top 25, §4 "client" table and every row whose fix-plan column names
  `nntp-client-transfer`, §11 decisions, §12 corrections), `docs/audit/audit-nntp-client.md` (all findings, the client
  command table, "Not covered"), `docs/audit/audit-intake.md` (CLI-1 `headers.go` part, A-intake-6),
  `docs/audit/audit-tools.md` (A-tools-3), `docs/audit/audit-ops.md` (OPS-3 routing). Format follows
  `.claude/plans/done/web-sqlite-hardening.md` (WSH).

---

## Context

The outbound side of go-pugleaf is one package (`internal/nntp`: `BackendConn`, `Pool`, the CHECK/TAKETHIS streaming
engine and its `ResponseDemuxer`), one header serializer (`common.ReconstructHeaders`) and one tool
(`cmd/nntp-transfer`). It is used by `cmd/nntp-fetcher` and `internal/processor` (download), `internal/postmgr`
(POST upstream) and `cmd/nntp-transfer` (CHECK/TAKETHIS feed). The audit found five high findings here: every TAKETHIS
carries two Path headers and mangles folded headers and the last body line (CLI-1), a 411 reply panics the fetcher
(CLI-2), a silent upstream wedges connections forever (CLI-3), authenticated streaming transfers stall at the first
CHECK (CLI-4), and the transfer UI hands out the NNTP and Redis passwords through pprof (OPS-3).

There are **no client tests today** and this area has **no `knownBug` markers to remove** (the wave-2 test slices of
`nntp-audit-tests` covered the server only). This plan adds a scripted fake NNTP server (`cliFakeServer`, 127.0.0.1:0)
and one test per fix; every new test asserts the RFC-correct behavior directly (no markers).

### Findings

Severity is the one AUDIT §4 adopted. "Slice" names the one slice that closes the id. Plan-local ids `NC-1…3` are the
audit's "Not covered" candidates (audit-nntp-client.md `## Not covered`), taken in scope here.

**High**
| ID | Where (at 03d5ef7) | Defect | Slice |
|----|----|----|----|
| CLI-1 | `common/headers.go:267-297` (Path), `:321`,`:396-411`,`:487` (Newsgroups), `nntp/nntp-client-commands.go:1149-1154`,`:1271-1276` (body writers) | Part 1: `pathline` already holds `"Path: <host>!.TX!<path>"`, the `strings.Fields(pathline)` loop appends every token again (`:276-289`), then a second Path line is appended (`:293-297`): two Path lines, three copies. Part 2: the `getLines` loop does `i++` (`:403`) on the `range` variable of `:321`, so folded Newsgroups continuation lines are emitted again (`:487`). Part 3: both writers `break` on an empty last element, so a body whose last line is empty loses it. | cli-commands |
| CLI-2 | `nntp/nntp-client-commands.go:636-642` | `SelectGroup` returns `(nil, 411, err)`; `XHdrStreamedBatch` tolerates 411 and reads `groupInfo.Last`: nil deref in the pool goroutine (`nntp-backend-pool.go:128-136`), no `recover`, `nntp-fetcher` dies. | cli-commands |
| CLI-3 | `nntp/nntp-client.go:206-232` (leak: returns at `:214`,`:219`,`:232` after `c.conn = conn`), `:346-362` (dead `xSetReadDeadline`/`xSetWriteDeadline`), `:365-371` (commented deadlines), `nntp/proxy.go:59-60` (`Handshake()` without deadline), `nntp/nntp-client-commands.go:76-87` (commented deadline) | No read/write deadline anywhere on client sockets, no TLS handshake timeout, greeting read unbounded, and `Connect` leaks the socket on its error paths (`createConnection` returns nil, so `Get`'s close at `nntp-backend-pool.go:270-272` never runs). | cli-conn |
| CLI-4 | `nntp/nntp-transfer-demuxer.go:19`,`:185-191` | `LastID` starts at 0 and the demuxer only advances on `LastID+1 == CmdID`; with `-username`, AUTHINFO USER/PASS and MODE STREAM consume textproto ids 0–2, the first CHECK is id 3 and waits in the backlog forever. | cli-conn |
| OPS-3 | `cmd/nntp-transfer/main.go:12` (`_ "net/http/pprof"`), `:149`,`:165` (`-password`, `-redis-pass`), `:539-541`, `:2935-2944` (`http.ListenAndServe(":%d", nil)`), `:544-552`; `cmd/history-rebuild/main.go:12`,`:75`,`:97-101` | `-web-port` serves the default mux on all interfaces with pprof registered: `GET /debug/pprof/cmdline` returns argv with both passwords; no server timeouts (gosec G108/G114). history-rebuild `-pprof` accepts any address. | cli-transfer |
| INT-5 (`bytes` half; routed here by the coordinator) | `nntp/nntp-client-commands.go:876-878` | `article.Bytes = len(article.BodyText)`: the LF-joined body length. RFC 3977 §8.1.1 / §8.3.2: `:bytes` is the size of the whole article (headers, separator, body) with CRLF line ends. `ParseLegacyArticleLines` is the shared parser of the fetcher, POST/IHAVE intake (`nntp-cmd-posting.go:274`), `proc_reuse.go:57` and `rslight.go:334`. The article-number-reuse half of INT-5 belongs to `audit-delta-fixes` (cross-reference only). | cli-commands |

**Medium**
| ID | Where | Defect | Slice |
|----|----|----|----|
| CLI-5 | `nntp/nntp-client.go:211-219` (greeting), `:251-259` (AUTHINFO USER), `nntp/nntp-backend-pool.go:189-201` (480), `nntp/nntp-client-commands.go:51-53`,`:150-152`,`:206-208` (430/451 unreachable), `:1231-1232` (401) | `ReadCodeLine(200)` rejects a 201 greeting; 281 after AUTHINFO USER counts as failure; 480 is reported as `ErrNewsgroupNotFound`; `StatArticle`/`GetHead`/`GetBody` return on `ReadCodeLine`'s mismatch error before their 430/451 arms; the 401 retry compares `strings.ToLower(line) == "mode reader"` but RFC 3977 §3.2.1 sends `401 MODE-READER`. `GetArticle` (`:96-97`) is already correct. | cli-commands |
| A-nntp-client-1 | `nntp/nntp-client-commands.go:780-847` (`readMultilineResponse`), `:681-716` (`XHdrStreamedBatch` loop, no line limit), `nntp/proxy.go:81-116` (SOCKS) | No line-length cap and no octet budget on responses (256 Ki lines of 1 MiB are one "article"); the XHDR stream has no line cap; SOCKS negotiation runs with no deadline. | cli-commands |
| A-nntp-client-3 (PLAUSIBLE) | `nntp/nntp-client-commands.go:43,88,142,198,246,293,374,450,452,515,517,654,656,736,738,1066,1126,1213,1238,1337,1372`, `nntp/nntp-client.go:245,263` | Every command is `TextConn.Cmd(format, remoteData)`; message-ids and group names from remote XOVER/XHDR (bare CR survives `ReadLine`, spaces split arguments) reach the wire unvalidated. | cli-hygiene |
| A-nntp-client-6 | `nntp/nntp-transfer.go:27,90,209,241,288`, `nntp/nntp-transfer-demuxer.go:33-35`, `cmd/nntp-transfer/main.go:2185-2186` | Five package-level `make(chan *T, 1024*1024)` pools (~40 MiB in every binary importing `internal/nntp`), ~16.5 MiB per demuxer and 16 MiB per CHTT worker. | cli-transfer |
| A-intake-6 (sender half) | `common/headers.go:165-537`, `nntp/nntp-client-commands.go:1114` | TAKETHIS sends `ReconstructHeaders(...)`: reorders, drops, renames headers, replaces Date, adds `.TX` and `X-pugleaf-debug`. RFC 5537 §3.6: relaying agents MUST NOT alter, delete or rearrange any part of an article except Path and Xref. The intake half is `nntp-intake-injection`. | cli-commands |
| A-ops-3 (sub-items d, f only; handed over by `audit-delta-fixes`) | `cmd/history-rebuild/main.go:161`, `cmd/nntp-transfer/main.go:347` | `signal.Notify(sigChan, os.Interrupt)` only: SIGTERM (systemd stop, `kill`) ends both tools without their graceful shutdown (history-rebuild's flush, nntp-transfer's pool/DB shutdown). The other A-ops-3 sub-items stay in `audit-delta-fixes`. | cli-transfer |
| A-tools-3 (client guard only) | `nntp/nntp-client.go:167`, `nntp/nntp-backend-pool.go:235,277,289,309,565` | Log/error paths dereference `Backend.Provider.Name`/`.Host`; a `BackendConfig` without `Provider` (fetcher list update, `cmd/nntp-fetcher/main.go:838-845`) panics on any connect failure or `.onion` host. The fetcher half (build the list-update backend with proxy fields) stays in `audit-delta-fixes`. | cli-conn |

**Low**
| ID | Where | Defect | Slice |
|----|----|----|----|
| CLI-6 | `nntp/nntp-peering.go:47-564` (`PeeringManager`; stub `LoadConfiguration` `:196-205`, DNS limiter self-deadlock `:344`+`:416`, slice aliasing `:262-263`, loop-copy return `:299`,`:305`), `nntp/nntp-peering-struct.go.bak`, `cmd/test-nntp/main.go:18-25`,`:49`,`:79-125` | Unused and buggy peering manager plus a dead `.bak`; `test-nntp` is hardcoded to an external server and calls removed APIs in a comment block. | cli-tools |
| A-nntp-client-2 | `nntp/transfer-progress.go:50-59` | Progress DB opened without `busy_timeout` (the `PRAGMA journal_mode` Exec at `:56` reaches one pooled connection only). The two direct `QueryRow` readers are A-9's to delete. | cli-tools |
| A-nntp-client-4 | `nntp/nntp-backend-pool.go:330` (30 s), `nntp/nntp-client.go:43` (`DefaultConnExpire` 25 s), `nntp/nntp-backend-pool.go:270-290`,`:389-393` (accounting in three places) | Pool wait and idle expiry hardcoded, `Get` takes no context, duplicated counter bookkeeping. | cli-conn |
| A-nntp-client-7 | `nntp/nntp-client-commands.go:995-999` (`parseOverviewLine` drops parse errors), `:702-715` (no `ArticleNum <= 0` filter) | Malformed overview/XHDR lines become article number 0 and are forwarded. | cli-commands |
| A-nntp-client-9 | `nntp/transfer-progress-utils.go:10,45,96` (+`truncateString` `:146`), `nntp/transfer-progress.go:220`, `nntp/nntp-client-commands.go:283-361` (`ListGroupsLimited`), `cmd/nntp-transfer/main.go:36,235` (`dbBatchSize`), `:967` (`IgnoreNewsgroupProgress`), `:1086` (`matchesAnyPattern`) | Dead or unused helpers. `MultiLineHeaderToMergedString` (`nntp-client-commands.go:894`) is **kept**: `ParseHeaders` uses it (sub-item rejected). | cli-hygiene |
| A-nntp-client-10 | `cmd/nntp-transfer/main.go:2946-3414` (`handleIndex` holds `ResultsMutex.RLock` across `tmpl.Execute(w, …)` `:3411`), `:3417-3431` | UI shows operational data to anyone on the network (bind: OPS-3) and holds the results lock while writing to a possibly stalled client. | cli-transfer |
| A-nntp-client-11 | `nntp/nntp-client.go:313-322`, `nntp/nntp-client-commands.go:684`, `nntp/nntp-backend-pool.go:132`,`:378-396`,`:425` | `ForceCloseConn` starts a goroutine per call; two calls on one connection close it twice and `closeConn` decrements `activeConns` twice, so `Get` can exceed `maxConns`. `CHTTWorker` calls it 4+ times per connection (`main.go:2212,2227,2390,2570,2688`). | cli-conn |
| NC-1 | `common/headers.go:243-245` | `GetHeaderFirst` joins repeated Subject/From with LF (`headers.go:580-601`); `ReconstructHeaders` emits `"Subject: "+article.Subject` unchanged, so a bare LF (and a `.` line) reaches the outbound stream. | cli-commands |
| NC-2 | `cmd/nntp-transfer/main.go:2513-2522` (431 counted, not requeued), `:1453-1465` (progress row written anyway), `nntp/transfer-progress.go:358-363` (`SELECT DISTINCT newsgroup`), `main.go:867,899,933,951` (skip) | A group whose CHECKs got 431 (or TX/conn errors) is recorded as done; the next run with the same date range skips it, so those articles are never sent. | cli-tools |
| NC-3 | `cmd/nntp-transfer/main.go:1629`,`:1733` (EXISTS), `:2508`,`:2648`,`:2657` (SET) | Redis keys are bare message-ids: transfers to two peers sharing one Redis DB skip each other's articles. | cli-transfer |

Retired, not in this plan: A-nntp-client-5 (rejected in review), A-nntp-client-8 (= CLI-1 part 3).

### Decisions (behaviour changes, to report to the user at the end)
1. **Outbound Path (CLI-1, A-intake-6).** Exactly one `Path:` line. Its value is
   `common.EnsureLocalPathPrefix(stored path, localHostname)`: the stored `path` column already starts with the local
   identity (intake prefixes it), so it is sent unchanged; otherwise `<host>!` is prepended once. The `!.TX!`
   diagnostic is **dropped**: RFC 5537 §3.2.1 defines `MISMATCH`, `SEEN` and `POSTED` only, and `.TX` repeated the
   identity. The same rule builds POST's `X-Path` (no `.TX`). The line is never folded.
2. **TAKETHIS relays the stored header block where it is complete (A-intake-6, RFC 5537 §3.6) — default, to be
   confirmed by the user (see "Question for the user" below).** `common.RelayHeaders` returns one of three outcomes:
   - **relay:** the stored header lines are sent in stored order, unchanged except: the Path header (with
     continuations) is replaced by the line of decision 1; Xref (with continuations) is dropped; `X-Goo*` fields are
     dropped when `common.IgnoreGoogleHeaders` is set (`-ignore-google-headers`, as today); a Date value that is not an
     RFC date is replaced by the value `ReconstructHeaders` computes today (`DateSent`, then `Date-Received`), so the
     date repair behind `-rewrite-dates` keeps applying.
   - **incomplete → fallback:** when Date, From, Message-ID, Newsgroups or Subject is missing, or a field line is
     malformed (no colon, empty or non-printable name, a continuation as first line), the article is serialized with
     `ReconstructHeaders` as today (it fills the fields from the row's columns). This keeps web posts sendable: their
     `headers_json` has no Date/Message-ID/Subject/Path (INT-7). Rows without `headers_json` take this path too.
   - **unsafe → skip:** a CR, LF or NUL inside a line, a mandatory field (the five above or Path) present more than
     once, or a Message-ID value that differs from `article.MessageID` or fails `common.ValidMessageID`: the article is
     skipped (counted `skipped`, logged with the reason), never repaired.
   POST upstream (`postmgr`) keeps `ReconstructHeaders`: there the tool acts as a posting agent, not a relay.
3. **Header and body lines are dot-stuffed on the wire (CLI-1).** Both writers stuff every line starting with `.`,
   header lines included, and send the body byte-exact: `strings.Split(BodyText, "\n")` reverses the parser's join
   exactly, so a final empty line is sent; an empty `BodyText` sends no body lines.
4. **Client timeouts (CLI-3).** `BackendConfig.ConnectTimeout` bounds dial, SOCKS negotiation, TLS handshake, the
   greeting and AUTHINFO. After that `ReadTimeout`/`WriteTimeout` (new fields, default 2 min) are **idle** timeouts:
   each `Read`/`Write` call on the socket must make progress within them. 0 means the default, a negative value
   disables the timeout.
5. **Client limits (A-1).** One line may be at most `MaxResponseLineBytes` (default 1 MiB); an ARTICLE/HEAD/BODY response at
   most `MaxArticleResponseBytes` (default 64 MiB); LIST/XOVER/XHDR/LISTGROUP at most `MaxListResponseBytes` (default
   512 MiB). (Client-side names; `MaxArticleBytes`/`DefaultMaxArticleBytes`/`ErrArticleTooLarge` belong to the server's
   `readArticleBlock` in `nntp-server-hardening`.) An XHDR
   stream may not return more lines than articles requested. Exceeding a limit returns an error and force-closes the
   connection. `Provider.MaxArtSize` is not used as a read limit (it is a fetch policy; a read abort would churn
   connections).
6. **Response codes (CLI-5).** A 201 greeting is accepted. 281 directly after AUTHINFO USER is success (no PASS sent).
   480 on GROUP returns the new `ErrAuthRequired` (connection force-closed), not `ErrNewsgroupNotFound`; the fetcher
   logs it in its default branch. 430/451 to STAT/HEAD/BODY map to `false`/`ErrArticleNotFound`/`ErrArticleRemoved`.
   `401 MODE-READER` (case-insensitive prefix) triggers the MODE READER retry.
7. **411 inside a streamed XHDR (CLI-2)** returns `ErrNewsgroupNotFound`; `XOver`/`XHdr` do the same instead of
   sending XOVER/XHDR with no group selected. `XHdrStreamed` closes its channel by `defer` and turns a panic into an
   error.
8. **Demuxer start (CLI-4).** `NewResponseDemuxer` synchronizes with the connection's textproto pipeline by reserving
   one id as a no-op request/response pair; the first command registered afterwards is `LastID+1` whatever ran before
   (AUTHINFO, MODE STREAM, anything else).
9. **Connection close (A-11).** `ForceCloseConn` stays asynchronous (callers may hold other locks) but is idempotent:
   one goroutine per connection lifetime. Pool counters change only when a connection was really closed.
10. **Auth failure back-off (CLI-3).** The 15 s sleep after a failed AUTHINFO stays the default
    (`BackendConfig.AuthFailureBackoff`, negative disables); the socket is closed before sleeping.
11. **Pool (A-4).** `BackendConfig.IdleExpire` (default 25 s) and `PoolWaitTimeout` (default 30 s); new
    `Pool.GetContext(ctx, mode)`; `Get(mode)` keeps its signature and calls it with `context.Background()`.
12. **Invalid command arguments (A-3) are rejected, not sanitized.** Message-ids must pass `common.ValidMessageID`
    (owned by `nntp-intake-injection`, `common/intake.go`: full RFC 5536 msg-id grammar, at most 250 octets, exactly
    one `@`; legacy ids without `@` are therefore skipped by the transfer and rejected by `ReconstructHeaders`/
    `RelayHeaders`); group names are 1–497 octets without CTL, WSP or `,`; any
    command line containing CR, LF or NUL, or longer than 510 octets, is refused with `ErrInvalidCommandArg` before it
    is written. nntp-transfer drops invalid message-ids when it builds a batch (counted `skipped`).
13. **pprof and the transfer UI (OPS-3, A-10).** pprof is never on the UI server. It is served only on `-pprof-port`
    (localhost), registered explicitly **without** `/debug/pprof/cmdline` (that path returns 404). The UI binds
    `-web-bind` (new flag, default `127.0.0.1`) on a private mux inside an `http.Server` with timeouts; both servers are
    shut down at exit. history-rebuild `-pprof` accepts loopback addresses only (`:N` means `127.0.0.1:N`; a
    non-loopback host exits with an error). The UI renders into a buffer and releases `ResultsMutex` before writing.
14. **Progress after 431/errors (NC-2).** No in-run requeue. A group counts as done for a date range only when its
    **latest** progress row for that remote and range has `retry = 0 AND tx_errors = 0 AND conn_errors = 0`; otherwise
    the next run offers it again (CHECK answers 438 for what the peer already has).
15. **Redis keys (NC-3)** are `pugleaf:tx:<host>:<port>:<message-id>`. Existing bare keys are ignored and expire by TTL
    (one round of extra CHECKs after the upgrade).
16. **Recycle pools (A-6)** become `sync.Pool`s. Demuxer and CHTT-worker queues are sized
    `StreamQueueDepth(batchCheck, maxQueuedJobs) = clamp(4*maxQueuedJobs*batchCheck + 1024, 4096, 1<<20)` (an upper bound
    of the commands one connection can have outstanding, so no new blocking).
17. **INT-5 `bytes`.** `Bytes = Σ(len(header line)+2) + 2 + Σ(len(body line)+2)`, over the parsed (un-stuffed) lines,
    without the terminator; set also when the body is empty. Only newly parsed articles change; existing rows keep
    their value (no migration).
18. **Fetched header blocks are not changed here.** The intake plan's S3 runs
    `common.CheckHeaderBlock(…, HeaderBlockFetch)` on every bulk/fetched article in `processArticle` and stores the
    normalized `HeadersJSON`; calling it again in `ParseHeaders` would be redundant and would also hit the server
    intake path (`ParseLegacyArticleLines` is called from `nntp-cmd-posting.go:274`). The fetcher's `..`→`.`
    un-stuffing in `readMultilineResponse` is correct (RFC 3977 §3.1.1) and stays. A-intake-2 and the fetch side of
    A-nntp-rfc-2 are cross-referenced to `nntp-intake-injection`.
19. **Removed (CLI-6, A-9):** `PeeringManager` and its types (the `Default*Patterns` vars and `nntp-peering-pattern.go`
    stay), `nntp-peering-struct.go.bak`, `cmd/test-nntp/` (no build script, hardcoded external server, only user of
    `ListGroupsLimited`), the unused progress readers and nntp-transfer helpers. `-batch-db` stays accepted (used by
    `scripts/nntp-transfer-loop.sh`) and its help text says it has no effect.

**Question for the user (decision 2):** relay verbatim where complete, fall back to `ReconstructHeaders` for
incomplete blocks, skip only unsafe ones (the default above); or relay strictly verbatim and skip every incomplete
block (RFC 5537 §3.6 to the letter, but every web post and many legacy imports stop being fed); or keep
`ReconstructHeaders` for everything (today's behaviour minus the CLI-1 bugs). The plan implements the default unless
the user decides otherwise before wave 2.

### Out of scope (list them in the Outcome)
- In-run requeue of CHECK 431 (decision 14 makes the next run pick them up).
- Passwords for nntp-transfer from environment or file instead of argv (local `ps`/`/proc` exposure remains).
- Authentication for the transfer UI (it binds loopback by default).
- INT-5 article-number reuse → `audit-delta-fixes`. A-tools-3: `audit-delta-fixes` keeps only the fetcher half (proxy
  fields and `Provider` in `cmd/nntp-fetcher/main.go:838-845`); the client guard is here (cli-conn). A-tools-14 (`rslight-importer` pprof) → `audit-delta-fixes`. OPS-4 (history-rebuild
  `-useshorthashlen`, `cmd/history-rebuild/main.go:74`) → `audit-delta-fixes`.
- Server-side dot-stuffing (BUG-6, A-nntp-rfc-2 served half) → `nntp-server-hardening`; intake header validation
  (A-intake-2, including the fetch side of A-nntp-rfc-2: `CheckHeaderBlock(…, HeaderBlockFetch)` in `processArticle`)
  and Xref handling on intake (INT-4, `ParseHeaders` `:936-939` is not changed here) → `nntp-intake-injection`.
- govulncheck findings (toolchain/`x/net`/`quic-go` versions): `go.mod` is frozen (K4).
- An opt-in `-rewrite-headers` for pushing legacy archives with invalid stored headers.
- Rewriting `bytes` of existing rows; folding long Path lines.
- `assignWorkerToNewsgroup` "least busy" loop (`main.go:1858`, function inventory only, not a finding).

---

## Design

### Contracts all slices rely on
- **K1: stable exported API** (callers outside `internal/nntp`: `cmd/nntp-fetcher`, `cmd/nntp-transfer`, `cmd/web`
  (`main_functions.go:579-693`), `internal/processor` (`proc_DLArt.go`, `proc_DLXHDR.go`, `proc_ImportOV.go`,
  `analyze.go`, `proc_reuse.go`, `rslight.go`), `internal/postmgr`, `internal/nntp/nntp-cmd-posting.go`). Signatures stay;
  only the listed semantic changes happen:
  - Types/fields: `BackendConfig` (existing fields unchanged; new fields are additive, see K2), `BackendConn` (exported
    fields `TextConn`, `Backend`, `Pool`, `ModeReader`, `ModeStream`), `Pool`, `PoolStats`, `GroupInfo`, `OverviewLine`,
    `HeaderLine`, `MODE_READER_MV`, `MODE_STREAM_MV`, `MaxReadLinesXover`, `ErrArticleNotFound`, `ErrArticleRemoved`,
    `ErrNewsgroupNotFound`, `ErrOutOfRange`.
  - `NewConn(*BackendConfig) *BackendConn`; `(*BackendConn).Connect() error`, `CloseFromPoolOnly() error`,
    `ForceCloseConn()`, `IsConnected() bool`, `Lock()`, `Unlock()`, `SwitchMode(int) error`,
    `StatArticle(string) (bool, error)`, `GetArticle(*string, bool) (*models.Article, error)`,
    `GetHead(string) (*models.Article, error)`, `GetBody(string) ([]byte, error)`, `ListGroups() ([]GroupInfo, error)`,
    `SelectGroup(string) (*GroupInfo, int, error)`, `XOver(string, int64, int64, bool) ([]OverviewLine, error)`,
    `XHdr(string, string, int64, int64) ([]HeaderLine, error)`,
    `XHdrStreamed(string, string, int64, int64, chan<- HeaderLine, <-chan struct{}) error`, `ListGroup(...)`,
    `PostArticle(*models.Article) (int, error)`, `SendCheckMultiple(...)`, `SendTakeThisArticleStreaming(...)`.
  - `NewPool(*BackendConfig) *Pool`; `(*Pool).Get(int)`, `Put`, `XOver`, `XHdr`, `XHdrStreamed`, `GetArticle`,
    `SelectGroup(string) (*GroupInfo, error)` (480 → `ErrAuthRequired`, decision 6), `ListNewsgroups`,
    `FileCachedListNewsgroups(cacheDir)`, `ClosePool`, `Stats`.
  - `ParseLegacyArticleLines(messageID string, lines []string, bulkmode bool) (*models.Article, error)` (`Bytes`
    changes, decision 17), `ParseHeaders`, `ParseGroupLine`,
    `MultiLineHeaderToMergedString`.
  - Transfer engine: `NewResponseDemuxer(*BackendConn, chan struct{}) *ResponseDemuxer` and its `Start`,
    `RegisterCommand`, `GetCheckResponseChan`, `GetTakeThisResponseChan`, `GetDemuxerStats`; `CHTTJob`, `ReadRequest`,
    `ResponseData`, `TTSetup`, `TTResponse`, `OffsetQueue`, `TakeThisMode`, `NewsgroupTransferProgress(Map)`,
    `IncrFLAG_*`, `ResultsMutex`, `JobIDCounter`, `NNTPTransferThreads`, `GetResponseData`, `RecycleResponseData`,
    `GetTTSetup`, `RecycleTTSetup`, `GetTTResponse`, `RecycleTTResponse`, `GetTTResponseChan`, `RecycleTTResponseChan`,
    `GetReadRequest`, `RecycleReadRequest`, `(*ReadRequest).ClearReadRequest`.
  - Progress DB: `OpenTransferProgressDB(dataDir, remoteName)`, `InsertResult(...)`,
    `GetAllProgressNewsgroups(start, end *time.Time) (map[string]bool, error)` (semantics: decision 14),
    `NewsgroupExists`, `GetRemoteID`, `GetRemoteName`, `Close`.
  - `common.ReconstructHeaders(article, withPath bool, nntphostname *string, newsgroup string) ([]string, error)`.
  - The server-side K1 of `nntp-audit-tests` (D3) is untouched: no file of this plan is a server file.
  - **Dependencies on `nntp-intake-injection` (merged first):**
    - `func ValidMessageID(id string) bool` in `internal/common` (`common/intake.go`): full RFC 5536 grammar, exactly
      one `@`. This plan calls it (waves 0, 2, 3) and does **not** define its own.
    - Optional: if the merged tree has `common.WireArticleSize` (intake plan, whole-article CRLF size), wave 2 uses it
      instead of defining `articleWireBytes`.
    - If a name or signature differs in the merged tree, the slice that uses it adapts its call sites and records it in
      `## Progress`.
- **K2: reserved names** (checked free at 03d5ef7 with `grep -rnw <name> --include=*.go internal cmd`):
  - wave 0: `cliFakeServer`, `cliFakeOpts`, `cliFakeStep`, `cliFakeConn`, `cliFakeTLSConfig`, `newCliFakeServer`,
    `cliDial`, `cliUnstuff`, `cliPeerOpts`, `cliPeerHandler` and the `TestCliFake*`/`TestCliE2E*`
    tests.
  - `cli-conn`: `deadlineConn`, `newDeadlineConn`, `(*deadlineConn).setTimeouts`, `DefaultClientReadTimeout`,
    `DefaultClientWriteTimeout`, `DefaultAuthFailureBackoff`, `DefaultPoolWaitTimeout`, `BackendConfig.ReadTimeout`,
    `BackendConfig.WriteTimeout`, `BackendConfig.AuthFailureBackoff`, `BackendConfig.IdleExpire`,
    `BackendConfig.PoolWaitTimeout`, `(*BackendConfig).providerLabel`, `(*BackendConfig).cacheHost`,
    `(*BackendConn).dconn`, `(*BackendConn).closing`, `(*BackendConn).abortConnect`, `(*BackendConn).closeFromPool`,
    `(*Pool).GetContext`, `(*Pool).failSlot`, `(*ResponseDemuxer).syncPipeline`.
  - `cli-tools`: `query_getAllProgressNewsgroupsDone`.
  - `cli-commands`: `ErrAuthRequired`, `ErrAuthRejected`, `ErrResponseTooLarge`, `ErrResponseLineTooLong`,
    `DefaultClientMaxLineBytes`, `DefaultClientMaxArticleBytes`, `DefaultClientMaxListBytes`,
    `BackendConfig.MaxResponseLineBytes`, `BackendConfig.MaxArticleResponseBytes`, `BackendConfig.MaxListResponseBytes`,
    `(*BackendConn).greetingCode`, `(*BackendConn).responseBudget`, `writeArticleLines`, `wireBodyLines`, `dotStuff`,
    `articleWireBytes` (only if `common.WireArticleSize` does not exist), `common.RelayHeaders`,
    `common.ErrRelayHeadersInvalid`, `common.ErrRelayHeadersIncomplete`, `common.EnsureLocalPathPrefix`,
    `common.firstHeaderLine`, `common.outboundDate`, `common.relayHeaders`.
  - **Not** reserved here (owned by the earlier plans; never define them): `DefaultMaxArticleBytes`, `MaxArticleBytes`,
    `ErrArticleTooLarge`, `readArticleBlock` (`nntp-server-hardening`); `common.ValidMessageID`,
    `common.CheckHeaderBlock`, `common.HeaderBlockMode`, `common.HeaderBlockFetch` (`nntp-intake-injection`).
  - `cli-transfer`: `DemuxerQueueDepth`, `StreamQueueDepth`, `responseDataPool`, `ttSetupPool`, `ttResponsePool`,
    `ttResponseChanPool`, `readRequestPool` (the exported chan vars `ResponseDataPool`, `TTSetupPool`, `TTResponsePool`,
    `TTResponseChans`, `ReadRequestsPool` are removed; grep shows no user outside `nntp-transfer.go`), main-package
    `newUIServer`, `newPprofMux`, `newPprofServer`, `renderIndex`, `redisKey`, `redisKeyPrefix`, `buildRedisKeyPrefix`,
    `webBind`; in
    `cmd/history-rebuild`: `loopbackPprofAddr`, `newPprofMux`.
  - `cli-hygiene`: `ErrInvalidCommandArg`, `(*BackendConn).sendCmd`, `validGroupArg`, `validTokenArg`,
    `validMessageIDArg`, `maxClientCommandLine` (distinct from any server-side command-line limit of
    `nntp-server-hardening`).
- **K3: tests.**
  - Package `nntp` already has `TestMain` (`nntp-server-harness_test.go:47`, opens a temp DB, discards logs unless
    `PUGLEAF_TEST_LOG=1`). Client tests need no DB and must not call `hTestDB`.
  - The fake server lives in `internal/nntp/nntp-client-fake_test.go`, written in wave 0 and **read-only afterwards**
    (a slice that needs more behavior writes a `Handler` in its own test file).
  - Test files and prefixes: `cli-conn` → `nntp-client-conn_test.go` (`cliConn`/`TestCliConn`); `cli-tools` →
    `nntp-client-progress_test.go` (`cliProg`/`TestCliProg`); `cli-commands` → `nntp-client-cmds_test.go`
    (`cliCmd`/`TestCliCmd`) and `internal/common/relay_headers_test.go` (`cliHdr`/`TestCliHdr`); `cli-transfer` →
    `nntp-client-transfer_test.go` (`cliTx`/`TestCliTx`), `cmd/nntp-transfer/debugserver_test.go` (`txDbg`/`TestTxDbg`),
    `cmd/history-rebuild/pprof_addr_test.go` (`TestHRPprof`); `cli-hygiene` → `nntp-client-validate_test.go`
    (`cliVal`/`TestCliVal`). Wave 0 uses
    `cliFake`/`cliPeer`/`cliE2E`/`cliDial`/`cliUnstuff`/`TestCliFake`/`TestCliE2E`. Taken in package `nntp`: `h*`, `knownBug`, `runIsolated`,
    `newTestServer`, `sess`, `auth`, `grp`, `art`, `ovr`, `e2e`, `Wiring*`, `NNTPHist*`, `Lo1Paths*`.
  - No `t.Parallel()`. No package global is mutated by tests: everything a test needs to vary is a `BackendConfig`
    field (timeouts, limits, back-off) or an argument. Polling uses deadlines; no sleep longer than 100 ms except the
    timeout under test.
  - Every listener is `127.0.0.1:0` (or the address the e2e script passes). No test or script contacts an external host.
- **K4:** nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt`, release scripts or files outside
  their ownership list. `gofmt` runs on owned files only (the 2-file baseline stays). `build_ALL.sh`/`Build_DEV.sh` are
  never run.
- **K5:** a slice never calls a helper another slice of the **same** wave adds. Wave 2 may use everything merged from
  wave 1, wave 3 everything from waves 1–2.
- **K6: preflight anchor check** (orchestrator, before wave 1): `grep -n` that these still match at the integration
  base, else update the slice text in `## Progress`: `ReadCodeLine(NNTPWelcomeCodeMin)` (nntp-client.go),
  `func (c \*BackendConn) xSetReadDeadline`, `if d.LastID+1 != cmdInfo.CmdID` (demuxer), `if end > groupInfo.Last`
  (commands), `article.Bytes = len(article.BodyText)`, `headers = append(headers, "Path: "+\*nntphostname+"!.TX!"+article.Path)`
  (headers.go), `_ "net/http/pprof"` (both cmds), `SELECT DISTINCT newsgroup FROM transfers`.

### Coordination with the other fix plans (Appendix B of nntp-audit-tests)
- This plan owns, in all of its waves, the client row of Appendix B: `internal/common/headers.go` (ReconstructHeaders),
  `internal/nntp/{nntp-client.go, nntp-client-commands.go, nntp-transfer-demuxer.go, nntp-backend-pool.go, proxy.go}`,
  `cmd/nntp-transfer/main.go`; plus `internal/nntp/{nntp-transfer.go, transfer-progress.go, transfer-progress-utils.go,
  nntp-peering.go, nntp-peering-struct.go.bak}`, `cmd/test-nntp/`, `cmd/history-rebuild/main.go` (pprof lines
  `:12`, `:75`, `:97-104` and the `signal.Notify` at `:161`) and the new files listed per slice. None of these is owned by `nntp-server-hardening`,
  `nntp-intake-injection` or `nntp-rfc-commands`.
- `nntp-client-commands.go` holds the shared parser `ParseLegacyArticleLines`/`ParseHeaders` (`:849-952`). The intake
  plan must not edit this file: INT-5 `bytes` (wave 2) is done here; INT-4's Xref
  handling in `ParseHeaders` (`:936-939`) is left as it is. If the intake plan needs another hunk there, it hands the
  exact change to this plan (or runs after it).
- `internal/nntp/nntp-history-parse_test.go` is touched only in `TestNNTPHistParseLegacyRoundTripNoBody`'s `Bytes`
  assertion (wave 2).
- No slice changes how articles are parsed or stored beyond `Bytes`, so no `knownBug` marker of another plan
  (`cmd/nntp-server/server_e2e_test.go`, `internal/nntp/nntp-server-*_test.go`) can flip; the known-bug comparison in
  every check confirms it.
- `audit-delta-fixes` (written later) must not own the files above; for `cmd/history-rebuild/main.go` it may own the
  `-useshorthashlen` lines (OPS-4) and merges after this plan.

### Component design
- **Socket wrapper (`nntp-client-conn.go`).** `deadlineConn` embeds `net.Conn` and holds `readTimeout`,
  `writeTimeout` (`atomic.Int64`, nanoseconds; `<= 0` = none). `Read` sets `SetReadDeadline(now+readTimeout)` before
  each call, `Write` the same with the write deadline. `Connect` wraps the dialed (TLS) conn with ConnectTimeout for both,
  builds `textproto.NewConn(dc)` on it, and after greeting + auth calls `dc.setTimeouts(read, write)`. `c.conn` (type
  `net.Conn`) is the wrapper, so `bufio.NewWriter(c.conn)` in the TAKETHIS/POST writers gets write deadlines too.
  Wave 2 adds the line cap: `Read` keeps `run` (bytes since the last `\n` seen in earlier reads) and walks **every**
  `\n` in `p[:n]`: the first segment's length is `run + index`, each following segment is measured between two
  newlines, and the tail after the last newline becomes the new `run` (no newline: `run += n`). Any segment over
  `maxLine` (set from `BackendConfig.MaxResponseLineBytes`) returns `ErrResponseLineTooLong`.
- **Demuxer sync (`syncPipeline`).** Under `conn.Lock()`: `id := conn.TextConn.Next(); StartRequest(id);
  EndRequest(id); StartResponse(id); EndResponse(id); d.LastID = id`. All earlier commands ended their responses
  (they are synchronous), so nothing blocks; the next `Cmd` gets `id+1`. **Precondition:** the connection never had a
  failed command. textproto consumes an id even when the write fails, and a command whose response was never read
  leaves the response sequencer behind, so `StartResponse` would block. Every error path in the client therefore
  force-closes the connection (today's pool wrappers do), and a connection handed to `NewResponseDemuxer` is a fresh
  one from `Pool.Get(MODE_STREAM_MV)`. Document this on `NewResponseDemuxer`.
- **Close bookkeeping.** `closeFromPool() bool` closes and returns true only on the connected→closed transition;
  `CloseFromPoolOnly()` wraps it. `Pool.closeConn` decrements `activeConns` and increments `totalClosed` only on true.
  `ForceCloseConn` does `if c.closing.Swap(true) { return }`, sets `forceClose` under `c.mux`, then
  `go c.Pool.Put(c)` (or `go c.closeFromPool()` when `Pool == nil`). `Pool.Put` and `isConnectionValid` treat
  `c.closing.Load()` like `forceClose`, so a connection being force-closed is never handed out again, even before the
  goroutine runs.
- **Relay serializer (`common/relay_headers.go`).** `RelayHeaders(article *models.Article, localHost string)
  ([]string, error)` splits `HeadersJSON` on `\n` (one trailing `\r` per line is removed), groups lines into fields (a
  line starting with SP/HT continues the previous field) and applies decision 2:
  - errors wrapping `ErrRelayHeadersInvalid` (unsafe → the caller skips): a CR, LF or NUL inside a line; Date, From,
    Message-ID, Newsgroups, Subject or Path present more than once (case-insensitive); a Message-ID value (trimmed) that
    differs from `article.MessageID` or fails `ValidMessageID`;
  - errors wrapping `ErrRelayHeadersIncomplete` (→ the caller falls back to `ReconstructHeaders`): any of Date, From,
    Message-ID, Newsgroups, Subject missing; a first line that is a continuation; a field line without `:`, or whose
    name is empty or contains bytes `<= 0x20` or `0x7f`;
  - otherwise the lines unchanged except: Path replaced in place (or inserted first when absent; `article.Path` empty
    and no Path header → `EnsureLocalPathPrefix("", host)`), Xref dropped, `X-Goo*` dropped when `IgnoreGoogleHeaders`,
    and a non-RFC Date value replaced by `outboundDate(article)`. Unsafe checks run before completeness checks.
  - The work is done by the unexported `relayHeaders(article, localHost string, ignoreGoogle bool)`; `RelayHeaders`
    calls it with the package flag `IgnoreGoogleHeaders`, so tests never mutate that global.
  - `outboundDate(article *models.Article) (string, error)` is the Date selection of `ReconstructHeaders`
    (`headers.go:182-241`) moved into a function unchanged (including its `DateSent`/`DateString` updates);
    `ReconstructHeaders` calls it.
- **Wire writer (`writeArticleLines`).** `writeArticleLines(w *bufio.Writer, headers []string, body string) (int, error)`
  writes each header line through `dotStuff` + CRLF, an empty line, each of `wireBodyLines(body)` through `dotStuff` +
  CRLF, then `.` CRLF, and returns the bytes written. `wireBodyLines("") = nil`, otherwise
  `strings.Split(body, "\n")` with one trailing `\r` trimmed per line. Both TAKETHIS and POST use it.
- **Fake server (`cliFakeServer`, wave 0).** API in the wave-0 steps. It records every client line and every
  dot-terminated payload, can stall at any phase (before TLS, before greeting, after a command, mid-response), and
  counts open connections as seen from the server (a connection counts as closed when its server-side read returns
  EOF/error), which is how the socket-leak tests observe the client.

---

## Waves

### Wave 0 (inline, orchestrator on `plan-nntp-client-transfer`)

**Owned files (all new):**
- `internal/nntp/nntp-client-fake_test.go`
- `internal/nntp/nntp-client-e2e_test.go` (`//go:build e2e`)
- `scripts/test-nntp-client-transfer.sh`

**Steps:**
1. **Preflight** as in the skill (`git mv .claude/plans/queued/nntp-client-transfer.md .claude/plans/wip/`), the K6
   anchor check, then the baseline `## Checks` (record PASS/FAIL; gofmt may list only the 2 baseline files). Record the
   sorted `PUGLEAF_KNOWN_BUGS=1 … | grep -o 'known bug … still open'` list in `## Progress`: every later check compares
   against it (this plan removes and adds no marker).
2. **Write `nntp-client-fake_test.go`** (package `nntp`, imports only stdlib + `internal/config`):
   ```go
   type cliFakeOpts struct {
   	Addr       string                // listen address, default "127.0.0.1:0"
   	Greeting   string                // default "200 cli-fake ready"
   	NoGreeting bool                  // accept, never write (greeting stall); reads are still recorded
   	RawStall   bool                  // accept TCP, never read or write (TLS-handshake / SOCKS stall)
   	TLS        *tls.Config           // non-nil: tls.NewListener (self-signed ECDSA P-256 cert from cliFakeTLSConfig)
   	Script     []cliFakeStep         // run sequentially on every connection after the greeting
   	Handler    func(fc *cliFakeConn) // replaces Script when set
   }
   type cliFakeStep struct {
   	Expect     string        // regexp, anchored ^…$, matched against the client line; "" matches any line
   	ReadDot    bool          // after the match, read a dot-terminated block (POST/TAKETHIS payload)
   	Delay      time.Duration // before replying
   	Reply      []string      // lines written with CRLF; multi-line replies include their "." line
   	Hang       bool          // after the match (and ReadDot), never reply; held until Close
   	CloseAfter bool          // close the connection after the reply
   }
   // a step whose Expect does not match writes "500 cli-fake: unexpected <line>" and records the mismatch (Mismatches());
   // after the last step the connection keeps reading and recording lines (no replies) until EOF/error, so Open()
   // drops only when the client closes; Hang steps also keep reading
   func newCliFakeServer(t *testing.T, o cliFakeOpts) *cliFakeServer // listens, serves, t.Cleanup(s.Close)
   func cliFakeTLSConfig(t *testing.T) (server *tls.Config, clientRoots *x509.CertPool)
   func (s *cliFakeServer) Backend() *BackendConfig // Host, Port, MaxConns 2, ConnectTimeout 2s,
   	// Provider: &config.Provider{Name: "cli-fake", Host: s.Host, Port: s.Port}
   func (s *cliFakeServer) Lines() []string        // every client line, arrival order, all connections
   func (s *cliFakeServer) Articles() [][]string   // every dot block as raw wire lines (still dot-stuffed), arrival order
   func (s *cliFakeServer) Mismatches() []string
   func (s *cliFakeServer) Accepted() int
   func (s *cliFakeServer) Open() int              // connections whose server-side read has not seen EOF/error
   func (s *cliFakeServer) WaitOpen(t *testing.T, n int, d time.Duration) // polls every 10 ms, t.Fatalf on timeout
   func (s *cliFakeServer) Close()                 // closes listener + conns, waits ≤5s for handler goroutines
   type cliFakeConn struct{ /* conn, bufio reader, server */ }
   func (fc *cliFakeConn) ReadLine() (string, error)      // CRLF stripped, recorded in Lines()
   func (fc *cliFakeConn) WriteLines(lines ...string) error
   func (fc *cliFakeConn) ReadDotBlock() ([]string, error) // until ".", recorded in Articles()
   func (fc *cliFakeConn) Raw() net.Conn
   func cliDial(t *testing.T, s *cliFakeServer, mutate func(*BackendConfig)) (*BackendConn, error)
   	// NewConn(s.Backend() after mutate) + Connect; on success t.Cleanup(c.CloseFromPoolOnly)
   func cliUnstuff(wire []string) []string // ".." prefix -> "."
   type cliPeerOpts struct {
   	User, Pass      string          // non-empty: AUTHINFO required; CHECK/TAKETHIS before auth → 480
   	Have            map[string]bool // ids the peer holds; guarded by the peer's mutex; TAKETHIS 239 adds to it
   	Defer431Once    map[string]bool // CHECK of these ids → 431 the first time only
   	StallModeStream time.Duration   // sleep before the first connection's "203" reply
   	OnArticle       func(msgID string, wire []string)
   }
   func cliPeerHandler(o *cliPeerOpts) func(fc *cliFakeConn)
   ```
   Peer replies: `CAPABILITIES` → `101`, `VERSION 2`, `READER`, `STREAMING`, `.`; `MODE STREAM` → `203 streaming ok`;
   `MODE READER` → `200 reader`; `AUTHINFO USER u` → `381` (or `481` for a wrong user); `AUTHINFO PASS p` → `281`/`481`;
   `CHECK <id>` → `238 <id>` / `438 <id>` (in `Have`) / `431 <id>` (first time in `Defer431Once`);
   `TAKETHIS <id>` → `ReadDotBlock`, then `239 <id>` (added to `Have`, `OnArticle` called) or `439 <id>` when already
   held; `QUIT` → `205` and close; anything else → `500`.
   Self-tests: `TestCliFakeScript` (greeting, two scripted steps, `Lines()`), `TestCliFakeOpenCount` (client close
   observed by `WaitOpen(0)`), `TestCliFakePeerStream` (a raw `textproto` client: AUTHINFO, MODE STREAM, CHECK,
   TAKETHIS with a dot-stuffed body, CHECK again → 438).
3. **Write `nntp-client-e2e_test.go`** (`//go:build e2e`; each test `t.Skip`s unless its env is set). All `PASS|FAIL|INFO`
   lines are printed with `fmt.Println` to stdout: the package `TestMain` sends `log` to `io.Discard`, and `t.Log`
   output is indented, so neither matches the script's `^(PASS|FAIL|INFO) ` greps.
   - `TestCliE2EFakePeer`: `PUGLEAF_E2E_PEER_ADDR` (listen), `PUGLEAF_E2E_PEER_DIR`, `PUGLEAF_E2E_PEER_MODE`
     (`peer` | `silent`; silent = `RawStall`), `PUGLEAF_E2E_PEER_USER`/`PASS`, `PUGLEAF_E2E_PEER_431` (comma-separated
     ids), `PUGLEAF_E2E_PEER_STALL` (`time.ParseDuration`). Writes `<dir>/ready` once listening, appends every client
     line to `<dir>/commands.log` (written as `%q` per line so a CR shows), writes each TAKETHIS payload's raw wire
     lines to `<dir>/<hex(msgid)>.wire` (CRLF-joined); runs until `<dir>/STOP` exists (polled every 100 ms) or 10 min.
   - `TestCliE2ESeed`: `PUGLEAF_E2E_ADDR`, `PUGLEAF_E2E_USER`, `PUGLEAF_E2E_PASS`. Raw `textproto` client; AUTHINFO;
     POSTs three articles, printing `PASS|FAIL S0<n> <text>` per POST (240 expected):
     - `<ct-1@e2e.invalid>`: `Path: poster.e2e.invalid!not-for-mail`, From, `Newsgroups: e2e.ct.a,` + continuation line
       ` e2e.ct.b`, Subject `ct one`, Date (now, RFC 1123Z), Message-ID, `Organization: E2E`; body `line1`, `.dotline`,
       `` (final empty line).
     - `<ct-2@e2e.invalid>`: plain headers, `Newsgroups: e2e.ct.a`, body `two`.
     - `<ct-3@e2e.invalid>`: headers in the order Message-ID, Date, Subject, From, `X-Custom: keep`, Newsgroups, Path
       (no Xref: the intake plan answers 441 to a POST with Xref, A-intake-4; Xref dropping on relay is covered by
       `TestCliHdrRelayVerbatim`); body `three`.
   - `TestCliE2EVerify` (uses stdlib, `database/sql`, the mattn driver already linked into the package and
     `common.ValidMessageID`; no helper from later waves of this plan): `PUGLEAF_E2E_PEER_DIR`, `PUGLEAF_E2E_GROUPDB` (path of `e2e_ct_a.db`),
     `PUGLEAF_E2E_LOCALHOST`, `PUGLEAF_E2E_PHASE` (`1` or `2`). Opens the group DB read-only with
     `sql.Open("sqlite3", "file:"+path+"?mode=ro")` and prints `PASS|FAIL <E-id> [<tag>] <text>` lines:
     - phase 1: **E03**, **E04**, **E05**, **E15** (definitions in `## End-to-end`);
     - phase 2: **E07b** (`<ct-2@e2e.invalid>.wire` exists).
4. **Write `scripts/test-nntp-client-transfer.sh`** from the listing in `## End-to-end`, `chmod +x`, `bash -n`.
5. **Checks:** `gofmt -l internal/nntp scripts`; `go vet ./...`; `go vet -tags e2e ./internal/nntp/`; `go build ./...`;
   `go test -race -count=1 -run 'TestCliFake' ./internal/nntp/`.
6. **Baseline e2e:** `./build_nntp-server.sh && ./build_nntpmgr.sh && ./build_nntp-transfer.sh && ./build_history-rebuild.sh`,
   then `scripts/test-nntp-client-transfer.sh`. Record the PASS/FAIL list in `## Progress` (expected at baseline:
   E01 FAIL (CLI-4 stall, transfer killed by its timeout), E02 FAIL (hang), E03–E08 FAIL or not reached (E06 depends on
   E01), E10/E11 FAIL (cmdline leaks the password — only the fake peer's scratch password `peer-secret-0123` is ever
   exposed, on 127.0.0.1), E12 FAIL (bound on all interfaces), E13 FAIL, E16 FAIL (SIGTERM kills without the
   shutdown log line), E09/E14/E15 PASS).
7. **Commit** `test(nntp): wave-0 client fake server, e2e peer and script for nntp-client-transfer` together with the
   plan move. Record the base SHA for wave 1.

### Wave 1 (2 parallel `pugleaf-implementer` slices)

Common rules for every slice of every wave:
- Follow K1–K5. Read the findings rows of your ids and every owned file in full before editing.
- **Checks** (in your worktree):
  ```bash
  gofmt -l internal/nntp internal/common cmd/nntp-transfer cmd/history-rebuild   # none of your owned files listed
  go vet ./... && go vet -tags e2e ./internal/nntp/
  go build ./...
  go test -race -count=3 -timeout 600s -run 'TestCli|TestTxDbg|TestHRPprof|TestNNTPHist' \
    ./internal/nntp/ ./internal/common/... ./cmd/nntp-transfer/... ./cmd/history-rebuild/...
  go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
    ./internal/nntp/... ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/...
  PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
    | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u   # must equal the list recorded in wave 0 (no marker flips)
  ./build_nntp-transfer.sh && ./build_history-rebuild.sh && scripts/test-nntp-client-transfer.sh | grep -E "\[(<your tag>)\]|SUMMARY"
  git status --short   # clean (no data-test-*, no build output tracked)
  ```
  E-checks tagged for other slices may still FAIL in your worktree. Use your own ports for the script
  (`PORT`, `PEERPORT`, `WEBPORT`, `PPROFPORT`, `SILENTPORT`, `HRPORT` = 213<slice-index>0…5, e.g. cli-conn 21310–21315).
- **Report:** commits, the E-lines with your tag, tests added (name → id), and anything a later wave must know.
- **Reviewer task:** each change against the slice text and the RFC; locking (`c.mux`, `pool.mux`, demuxer),
  goroutine and socket lifetimes, error paths that leave a connection half-used; tests fail for the right reason
  without the fix (the reviewer reverts one fix locally to confirm at least two tests).

---

#### Slice `cli-conn`: deadlines, TLS handshake, socket leaks, demuxer sync, pool accounting
Ids: CLI-3, CLI-4, A-nntp-client-4, A-nntp-client-11, A-tools-3 (client guard). E-tag `cli-conn`.

Owned files:
- `internal/nntp/nntp-client.go`
- `internal/nntp/proxy.go`
- `internal/nntp/nntp-client-conn.go` (new)
- `internal/nntp/nntp-backend-pool.go`
- `internal/nntp/nntp-transfer-demuxer.go`
- `internal/nntp/nntp-client-conn_test.go` (new)

Changes:
1. **Config (nntp-client.go).** In `BackendConfig` replace the commented `ReadTimeout`/`WriteTimeout` lines (`:85-86`)
   with real fields and add the rest:
   ```go
   ReadTimeout        time.Duration // idle timeout per Read after connect; 0 = DefaultClientReadTimeout, <0 = none
   WriteTimeout       time.Duration // idle timeout per Write after connect; 0 = DefaultClientWriteTimeout, <0 = none
   AuthFailureBackoff time.Duration // sleep after a failed AUTHINFO; 0 = DefaultAuthFailureBackoff, <0 = none
   IdleExpire         time.Duration // pooled conn idle expiry; 0 = DefaultConnExpire
   PoolWaitTimeout    time.Duration // Pool.Get wait for a free slot; 0 = DefaultPoolWaitTimeout
   ```
   Add consts `DefaultClientReadTimeout = 2 * time.Minute`, `DefaultClientWriteTimeout = 2 * time.Minute`,
   `DefaultAuthFailureBackoff = 15 * time.Second`, `DefaultPoolWaitTimeout = 30 * time.Second`, and an unexported
   `func pickDuration(v, def time.Duration) time.Duration` (`v == 0` → def, `v < 0` → 0).
   Add `func (b *BackendConfig) providerLabel() string` (Provider.Name when `Provider != nil && Name != ""`, else
   `net.JoinHostPort(Host, strconv.Itoa(Port))`) and `cacheHost() string` (Provider.Host when set, else Host).
2. **`nntp-client-conn.go` (new):** `deadlineConn` as in Component design (read/write timeouts only; no line cap yet),
   `newDeadlineConn(c net.Conn, rd, wr time.Duration) *deadlineConn`, `setTimeouts(rd, wr time.Duration)`.
3. **`Connect` (nntp-client.go:150-240).**
   - `:167`: use `c.Backend.providerLabel()`.
   - After a successful dial: `dc := newDeadlineConn(conn, ConnectTimeout, ConnectTimeout)`; `c.dconn = dc`;
     `c.conn = dc`; `c.TextConn = textproto.NewConn(dc)`.
   - New `func (c *BackendConn) abortConnect()` (caller holds `c.mux`): `c.TextConn.Close()` (closes the socket), then
     `c.TextConn, c.conn, c.dconn = nil, nil, nil`. Call it before **every** error return after the dial: the welcome
     read error (`:212-214`), the bad welcome code (`:217-219`), the auth failure (`:229-232`).
   - Auth failure: `abortConnect()` first, then sleep `pickDuration(c.Backend.AuthFailureBackoff,
     DefaultAuthFailureBackoff)` (skip when 0), then return the error. Keep the log line, with the configured duration.
   - On success, before `c.isConnected <- struct{}{}`:
     `dc.setTimeouts(pickDuration(ReadTimeout, DefaultClientReadTimeout), pickDuration(WriteTimeout, DefaultClientWriteTimeout))`.
   - Leave the greeting's `ReadCodeLine(NNTPWelcomeCodeMin)` and `authenticate` as they are (CLI-5 is wave 2).
4. **Dead helpers:** delete `xSetReadDeadline`/`xSetWriteDeadline` (`:345-362`) and the two commented lines in
   `UpdateLastUsed` (`:369-370`).
5. **`proxy.go` `DialTLS`:** replace `tlsConn.Handshake()` with
   `ctx, cancel := context.WithTimeout(context.Background(), pd.config.ConnectTimeout)` (no timeout when
   `ConnectTimeout <= 0`: use `context.Background()`), `defer cancel()`, `tlsConn.HandshakeContext(ctx)`. Keep the error
   texts. Leave `dialSOCKS4`/`dialSOCKS5` (A-1, wave 2).
6. **Close bookkeeping (A-11).**
   - Add `closing atomic.Bool` to `BackendConn`.
   - `closeFromPool() bool` = today's `CloseFromPoolOnly` body returning `true` only when it closed (false when
     `!IsConnected()`); `CloseFromPoolOnly() error` calls it and returns nil.
   - `ForceCloseConn()`: `if c.closing.Swap(true) { return }`; then `go func() { c.mux.Lock(); c.forceClose = true;
     c.mux.Unlock(); if c.Pool != nil { _ = c.Pool.Put(c) } else { c.closeFromPool() } }()`.
   - `Pool.closeConn` (`nntp-backend-pool.go:378-396`): `closed := client.closeFromPool()`; update `totalClosed`/`activeConns`
     only when `lock && closed`.
   - `Pool.Put` (`:337-346`) and `isConnectionValid` (`:492`): `conn.forceClose || conn.closing.Load() ||
     !conn.IsConnected()` means force-close / invalid.
7. **Pool (A-4, A-tools-3, CLI-3 leftovers) in `nntp-backend-pool.go`.**
   - `NewPool`: `idleTimeout: pickDuration(cfg.IdleExpire, DefaultConnExpire)`.
   - `func (pool *Pool) GetContext(ctx context.Context, wantMode int) (*BackendConn, error)` takes today's `Get` body.
     At the top: `if err := ctx.Err(); err != nil { return nil, err }`, `wait := pickDuration(pool.Backend.PoolWaitTimeout,
     DefaultPoolWaitTimeout)`, `deadline := time.Now().Add(wait)` — computed **once**, so the `goto newConn` retries
     share it. The final wait becomes `t := time.NewTimer(time.Until(deadline)); select { case pconn, ok :=
     <-pool.connections: t.Stop(); if !ok { return nil, fmt.Errorf("connection pool is closed") } …; case <-t.C: return
     nil, fmt.Errorf("timeout waiting for connection from pool after %v", wait); case <-ctx.Done(): t.Stop(); return
     nil, ctx.Err() }` (when `time.Until(deadline) <= 0`, return the timeout error without waiting).
     `Get(wantMode)` = `return pool.GetContext(context.Background(), wantMode)`.
   - `func (pool *Pool) failSlot()`: under `pool.mux`: `activeConns--; failedConns++`. Use it for the createConnection
     error path (delete the dead `if pconn != nil && pconn.conn != nil { … }` block, `:270-272`). For the SwitchMode
     failure after a new connection (`:280-291`) replace the manual decrement and `pconn.conn.Close()` with
     `pool.closeConn(pconn, true)` followed by `pool.mux.Lock(); pool.failedConns++; pool.mux.Unlock()`.
   - Replace `pool.Backend.Provider.Name` at `:235`, `:277`, `:289`, `:309` with `pool.Backend.providerLabel()` and
     `pool.Backend.Provider.Host` at `:565` with `pool.Backend.cacheHost()`.
   - Leave `Pool.SelectGroup`, `Pool.GetArticle` and `Pool.XHdrStreamed` bodies unchanged (wave 2/3).
8. **Demuxer (CLI-4) in `nntp-transfer-demuxer.go`.** Add `syncPipeline()` (Component design) and call it at the end of
   `NewResponseDemuxer` when `conn != nil && conn.TextConn != nil`. Document on `LastID` that it is the id reserved by
   `syncPipeline`, and on `NewResponseDemuxer` the precondition from Component design (a connection with a failed
   command must be force-closed, never given to a demuxer). Leave the channel sizes (A-6, wave 2).
9. Tests in `nntp-client-conn_test.go` (all against `cliFakeServer`; each with a 5 s outer deadline):
   - `TestCliConnGreetingTimeout` (CLI-3): `NoGreeting`, `ConnectTimeout 300ms` → `Connect` errors within 2 s;
     `WaitOpen(0, 2s)` (the socket was closed).
   - `TestCliConnBadGreetingClosesSocket` (CLI-3): greeting `400 go away` → error; `WaitOpen(0)`.
   - `TestCliConnAuthFailureClosesSocket` (CLI-3): script `AUTHINFO USER .*`→`381`, `AUTHINFO PASS .*`→`481`;
     `AuthFailureBackoff: -1` → error within 1 s; `WaitOpen(0)`.
   - `TestCliConnTLSHandshakeTimeout` (CLI-3): `RawStall`, `SSL: true`, `ConnectTimeout 300ms` → error within 2 s.
   - `TestCliConnTLSUnknownCA`: `TLS: cliFakeTLSConfig` server (self-signed, not in the system roots), `SSL: true` →
     `Connect` returns an error containing "TLS handshake failed" within 2 s (no production hook for custom roots is
     added).
   - `TestCliConnReadIdleTimeout` (CLI-3): script `ARTICLE .*` → reply `220 0 <a@b>`, `Path: x`, then `Hang`;
     `ReadTimeout 300ms` → `GetArticle` errors within 2 s.
   - `TestCliConnDemuxerAfterAuth` (CLI-4): `Handler: cliPeerHandler(&cliPeerOpts{User:"u", Pass:"p"})`, pool with
     `Username/Password`; `pool.Get(MODE_STREAM_MV)`; `NewResponseDemuxer(conn, errChan)`; `Start()`; under
     `conn.Lock()` send `CHECK <cli4@test.invalid>` with `conn.TextConn.Cmd`, then `RegisterCommand(id, TYPE_CHECK)`;
     a `238` arrives on `GetCheckResponseChan()` within 2 s (at base: stall). Same test without auth as a subtest.
     Cleanup: `common.SignalErrChan(errChan)`, `conn.ForceCloseConn()`.
   - `TestCliConnForceCloseIdempotent` (A-11): `MaxConns 2` and `PoolWaitTimeout 200ms` set on the config **before**
     `NewPool`; get one conn; call `ForceCloseConn()` three times; poll
     until `pool.Stats().ActiveConnections == 0` and stays 0 for 200 ms (never negative); then `Get` twice succeeds and a
     third `Get` times out after about 200 ms.
   - `TestCliConnGetContextCancel` (A-4): `MaxConns 1`, hold one conn; `GetContext(ctx with 100ms timeout)` returns
     `context.DeadlineExceeded` within 1 s.
   - `TestCliConnIdleExpire` (A-4): `IdleExpire 100ms`; Get, Put, wait 250 ms, Get → `Stats().TotalCreated == 2`.
   - `TestCliConnNilProvider` (A-tools-3): `BackendConfig{Host: "127.0.0.1", Port: <closed port>, ConnectTimeout:
     300ms, MaxConns: 1}` (no Provider): `pool.Get` returns an error, no panic; `Host: "x.onion"` without proxy:
     `Connect` returns an error, no panic.

Acceptance: E01, E02 PASS (E02 needs only this slice: greeting bounded by `-timeout`).

---

#### Slice `cli-tools`: peering removal, test-nntp removal, progress DB
Ids: CLI-6, A-nntp-client-2, NC-2. E-tag `cli-tools`.

Owned files:
- `internal/nntp/nntp-peering.go`
- `internal/nntp/nntp-peering-struct.go.bak` (delete)
- `cmd/test-nntp/main.go` (delete the directory)
- `internal/nntp/transfer-progress.go`
- `internal/nntp/nntp-client-progress_test.go` (new)

Changes:
1. **CLI-6.** In `nntp-peering.go` delete `PeeringManager`, `PeeringConfig`, `Peer`, `PeeringStats` and every function
   and method from `:165` to the end (`NewPeeringManager` … `CreateDefaultPeer`, `RemoteAddr`, `StrIsIPv4`,
   `StrIsIPv6`), keeping only the package clause, the three `Default*Patterns` vars (`:17-45`, used by
   `nntp-peering-pattern_test.go`) and the imports they need. Before deleting, `grep -rnw` each name over `internal cmd`
   (expected: only `nntp-peering.go`). `git rm internal/nntp/nntp-peering-struct.go.bak` and `git rm -r cmd/test-nntp`
   (no build script references it; `grep -rn 'test-nntp\b' *.sh scripts .github` finds only `scripts/test-nntp-server.sh`,
   which is a different name). Do not touch `nntp-peering-pattern.go` or its test.
2. **A-2.** `OpenTransferProgressDB`: open `sql.Open("sqlite3", dbPath+"?_busy_timeout=30000&_journal_mode=WAL")` so
   every pooled connection gets both (mattn DSN parameters); keep the explicit `PRAGMA journal_mode=WAL` Exec as is
   (harmless) or drop it — drop it.
3. **NC-2.** `GetAllProgressNewsgroups`: replace the query (`:359-364`) with a const
   `query_getAllProgressNewsgroupsDone`:
   ```sql
   SELECT t.newsgroup FROM transfers t
   WHERE t.remote_id = ? AND t.start_date = ? AND t.end_date = ?
     AND t.id = (SELECT MAX(id) FROM transfers x
                 WHERE x.remote_id = t.remote_id AND x.newsgroup = t.newsgroup
                   AND x.start_date = t.start_date AND x.end_date = t.end_date)
     AND t.retry = 0 AND t.tx_errors = 0 AND t.conn_errors = 0
   ```
   Same arguments, same scan, same `rows.Err()`. Update the doc comment ("groups whose latest result for this remote
   and date range is complete"). Leave `GetRecentTransfers` (A-9, wave 3).
4. Tests in `nntp-client-progress_test.go`:
   - `TestCliProgBusyTimeout` (A-2): open in `t.TempDir()`; take 4 `tpdb.db.Conn(ctx)` at once; each reports
     `PRAGMA busy_timeout` = 30000 and `PRAGMA journal_mode` = `wal`.
   - `TestCliProgIncompleteNotDone` (NC-2): `InsertResult` for `g.clean` (all zero), `g.retry` (retry=1), `g.tx`
     (txErrors=1), `g.conn` (connErrors=1), with the same start/end; `GetAllProgressNewsgroups` returns only `g.clean`.
     Then `InsertResult(g.retry, …all zero)` → `g.retry` is included (latest row wins); a row with another date range
     does not count.

Acceptance: E06, E07, E08 PASS (E07/E08 also need cli-conn's E01; in this worktree they may FAIL until wave 1 merges).

---

### Wave 2 (2 parallel slices, based on the merged wave 1)

#### Slice `cli-commands`: outbound serialization, response codes, limits, parsing
Ids: CLI-1, CLI-2, CLI-5, A-nntp-client-1, A-nntp-client-7, A-intake-6 (sender), INT-5 (`bytes`), NC-1.
E-tag `cli-commands`.

Owned files:
- `internal/nntp/nntp-client-commands.go`
- `internal/nntp/nntp-client.go` (only: the `BackendConfig` limit fields and their defaults, the greeting block of
  `Connect`, `authenticate`, the `greetingCode` field, the `ErrAuthRequired`/`ErrAuthRejected` vars)
- `internal/nntp/nntp-backend-pool.go` (only `Pool.SelectGroup`)
- `internal/nntp/nntp-client-conn.go` (only the line cap)
- `internal/nntp/proxy.go` (only `dialSOCKS4`/`dialSOCKS5`)
- `internal/common/headers.go`
- `internal/common/relay_headers.go` (new)
- `internal/common/relay_headers_test.go` (new)
- `internal/nntp/nntp-client-cmds_test.go` (new)
- `internal/nntp/nntp-history-parse_test.go` (only the `Bytes` assertion of `TestNNTPHistParseLegacyRoundTripNoBody`)

Changes:
1. **`common/relay_headers.go` (new; A-intake-6, decision 1/2).**
   - `var ErrRelayHeadersInvalid = errors.New("stored header block not relayable")`.
   - Do **not** define a message-id validator here: call `ValidMessageID` (same package, `common/intake.go`, owned by
     `nntp-intake-injection`).
   - `func EnsureLocalPathPrefix(path, localHost string) string`: `localHost == ""` → path; `path == ""` →
     `localHost + "!not-for-mail"`; `path == localHost || strings.HasPrefix(path, localHost+"!")` → path; else
     `localHost + "!" + path`.
   - `var ErrRelayHeadersIncomplete = errors.New("stored header block incomplete")`.
   - `func RelayHeaders(article *models.Article, localHost string) ([]string, error)` as in Component design.
2. **`ReconstructHeaders` (common/headers.go; CLI-1 parts 1–2, NC-1, decision 1).**
   - Add `func firstHeaderLine(v string) string`: text before the first `\r` or `\n`, `strings.TrimSpace`d.
   - `:243-245`: `from := firstHeaderLine(article.FromHeader)`, `subject := firstHeaderLine(article.Subject)`; move the
     empty checks (`:175-180`) to these values; `if !ValidMessageID(article.MessageID) { return nil, fmt.Errorf("article
     has invalid Message-ID %q", …) }` next to the existing Message-ID checks.
   - Replace the whole `withPath` switch (`:267-311`) with: `host := ""; if nntphostname != nil { host = *nntphostname }`;
     `withPath`: `article.Path != ""` → `headers = append(headers, "Path: "+EnsureLocalPathPrefix(article.Path, host))`,
     else `pathHeader_inv1`; `!withPath`: `article.Path != ""` → `"X-Path: "+EnsureLocalPathPrefix(article.Path, host)`,
     else `pathHeader_inv2`. (No `.TX`, no folding, one line.)
   - Turn `for i, headerLine := range moreHeaders` (`:321`) into `for i := 0; i < len(moreHeaders); i++ { headerLine :=
     strings.TrimSuffix(moreHeaders[i], "\r") …`; the label `checkHeader` stays on the `for`. The `i++` in `getLines`
     (`:403`) now skips the merged continuation lines. Lines containing `\r` or `\x00` after the trim are dropped like
     malformed lines (`ignoreLine = true`).
   - Move the Date selection (`:182-241`) unchanged into `outboundDate(article) (string, error)` and call it; leave
     References, the Newsgroups filter and `X-pugleaf-debug` unchanged (fallback path, decision 2).
3. **Writers (nntp-client-commands.go; CLI-1 part 3, decision 3).**
   - Add `dotStuff(line string) string` (`"."+line` when it starts with `.`), `wireBodyLines(body string) []string`
     and `writeArticleLines(w *bufio.Writer, headers []string, body string) (int, error)` (Component design).
   - `SendTakeThisArticleStreaming` (`:1096-1195`): `host` is `*nntphostname` or ""; `headers, err :=
     common.RelayHeaders(article, host)` when `article.HeadersJSON != ""`; if `HeadersJSON == ""` or
     `errors.Is(err, common.ErrRelayHeadersIncomplete)`, use `common.ReconstructHeaders(article, true, nntphostname,
     newsgroup)` instead; any remaining error (including `ErrRelayHeadersInvalid`) returns `0, 0, err, true` (skipped,
     as today). Replace the header/separator/body/terminator loops
     (`:1131-1176`) with one `writeArticleLines(writer, headers, article.BodyText)` call (add its count to `txBytes`).
     Keep `TAKETHIS` Cmd, `Flush`, `RegisterCommand` and the ReadRequest order exactly as today.
   - `PostArticle` (`:1257-1294`): same `writeArticleLines` call with the `ReconstructHeaders(article, false, nil, "")`
     headers.
4. **CLI-2.**
   - `XHdrStreamedBatch` (`:636-642`): `if err != nil || groupInfo == nil { if code == 411 { return fmt.Errorf("XHDR
     group '%s': %w", groupName, ErrNewsgroupNotFound) }; return fmt.Errorf("failed to select group '%s': code=%d
     err=%w", groupName, code, err) }`. Replace `go c.ForceCloseConn()` (`:684`) and the unlocked
     `c.conn.Close(); c.forceClose = true` (`:709-710`) with a plain `return fmt.Errorf("shutdown requested")` (the
     pool wrapper force-closes on any error; `ForceCloseConn` is idempotent since wave 1).
   - `XHdrStreamedBatch` read loop (`:689-694`): a `ReadLine` error is **not** success any more (today it `break`s
     and returns nil, so a cut-off stream looks complete): `return fmt.Errorf("XHDR stream ng '%s': %w", groupName, err)`.
     The pool wrapper (`nntp-backend-pool.go:130-132`) then force-closes the connection; nothing goes back to the pool.
   - `XOver` (`:436-440`) and `XHdr` (`:501-505`): the same 411 → `ErrNewsgroupNotFound` return.
   - `XHdrStreamed` (`:572-625`): named result `(err error)`; first statements `defer close(xhdrChan)` and
     `defer func() { if r := recover(); r != nil { log.Printf("[NNTP-CLIENT] XHdrStreamed panic ng='%s': %v", groupName,
     r); err = fmt.Errorf("XHdrStreamed panic: %v", r) } }()`; delete the four explicit `close(xhdrChan)` calls.
5. **CLI-5.**
   - `Connect` greeting (nntp-client.go:211-220): `code, message, err := c.TextConn.ReadCodeLine(2)`;
     `if err != nil && code == 0 { abortConnect; return err }`; `if code != 200 && code != 201 { abortConnect; return
     fmt.Errorf("unexpected welcome code %d: %s", …) }`; `c.greetingCode = code`.
   - `authenticate` (`:243-281`): read USER's reply with `ReadCodeLine(0)`; `281` → return nil (no PASS); `381` → send
     PASS; anything else → `fmt.Errorf("%w: AUTHINFO USER: %d %s", ErrAuthRejected, code, message)`. PASS reply: `281`
     → nil, else `fmt.Errorf("%w: AUTHINFO PASS: %d %s", ErrAuthRejected, code, message)`. I/O errors (`code == 0`) are
     returned as is. Declare `ErrAuthRequired = errors.New("authentication required")` and
     `ErrAuthRejected = errors.New("authentication rejected")` in `nntp-client.go` (so the pool-file edit stays limited
     to `Pool.SelectGroup`).
   - `Pool.SelectGroup` (`nntp-backend-pool.go:174-204`): `411` → put back, `ErrNewsgroupNotFound`; `480` →
     `client.ForceCloseConn()`, return `nil, ErrAuthRequired`; other errors as today.
   - `StatArticle`, `GetHead`, `GetBody` (`:51-53`, `:150-152`, `:206-208`): `if err != nil && code == 0 { return …
     err }`, then the existing `switch`/`if code != …` handles 430/451/other.
   - `PostArticle` 401 (`:1231-1232`): `if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "MODE-READER")`.
6. **A-1.**
   - nntp-client.go: `BackendConfig` gets `MaxResponseLineBytes int`, `MaxArticleResponseBytes int64`,
     `MaxListResponseBytes int64` (0 = the defaults `DefaultClientMaxLineBytes = 1 << 20`,
     `DefaultClientMaxArticleBytes = 64 << 20`, `DefaultClientMaxListBytes = 512 << 20`;
     negative = unlimited). `Connect` passes the line cap to `newDeadlineConn`/`setTimeouts` (add a `maxLine int`
     parameter or a `setMaxLine` method; your choice, inside `nntp-client-conn.go`).
   - `nntp-client-conn.go`: line cap in `Read` (Component design), `var ErrResponseLineTooLong = errors.New("nntp: response line
     too long")`.
   - `readMultilineResponse` (`:780-847`): `budget := c.responseBudget(src)` (`article`/`headers`/`body` →
     MaxArticleResponseBytes, others → MaxListResponseBytes); `total += int64(len(line)) + 2` per line; over budget → `c.conn.Close();
     c.forceClose = true` (the existing limit pattern, caller holds `c.mux`) and
     `fmt.Errorf("%w: %s response over %d bytes", ErrResponseTooLarge, src, budget)`.
   - `XHdrStreamedBatch` loop: count lines; more than `end-start+1` (after the 1000 clamp) → return
     `fmt.Errorf("%w: XHDR returned more lines than requested", ErrResponseTooLarge)`.
   - `proxy.go` `dialSOCKS4`/`dialSOCKS5`: after `proxy.SOCKS5(...)`, `if cd, ok := dialer.(proxy.ContextDialer); ok {
     ctx, cancel := context.WithTimeout(context.Background(), pd.config.ConnectTimeout); defer cancel(); return
     cd.DialContext(ctx, network, address) }` (plain `Dial` otherwise; no timeout when `ConnectTimeout <= 0`).
7. **A-7.** `parseOverviewLine` (`:995`): `articleNum, err := strconv.ParseInt(parts[0], 10, 64); if err != nil ||
   articleNum <= 0 { return OverviewLine{}, fmt.Errorf("invalid article number in XOVER line: %q", parts[0]) }` (bytes and
   lines keep the lenient 0). `XHdrStreamedBatch`: skip `header.ArticleNum <= 0` like `XHdr` does (`:548`).
8. **INT-5 `bytes` (decision 17).** If the merged tree has `common.WireArticleSize` (intake plan), use it; otherwise add
   `articleWireBytes(head, body []string) int` (`Σ len+2` over both, `+2` for the separator). In
   `ParseLegacyArticleLines` set `article.Bytes = articleWireBytes(lines[:bodyStart-1],
   lines[bodyStart:])` outside the `if bodyStart < len(lines)` block (delete `:877`). Fix
   `TestNNTPHistParseLegacyRoundTripNoBody` (`nntp-history-parse_test.go:110-111`): keep the `BodyText`/`Lines` checks
   and expect `Bytes == Σ(len(h)+2) + 2` over `nntpHistTestHeadLines()`.
9. Tests:
   - `internal/common/relay_headers_test.go`:
     - `TestCliHdrReconstructSinglePath` (CLI-1 p1): `withPath` true, `Path "news.test.invalid!poster!not-for-mail"`,
       host `news.test.invalid` → exactly one element with prefix `Path:` equal to `Path: news.test.invalid!poster!not-for-mail`;
       unprefixed path `poster!not-for-mail` → `Path: news.test.invalid!poster!not-for-mail`; no element contains `.TX`.
     - `TestCliHdrReconstructFoldedNewsgroups` (CLI-1 p2): `HeadersJSON` with `Newsgroups: a.b,` + ` c.d` → the
       continuation ` c.d` appears exactly once.
     - `TestCliHdrReconstructNoLF` (NC-1): Subject `"first\n."`, From `"a <a@b>\nb <b@c>"` → `Subject: first`,
       `From: a <a@b>`, and no element contains `\r` or `\n`.
     - `TestCliHdrRelayVerbatim` (A-intake-6): order kept, `Xref` and its continuation dropped, Path replaced in place,
       `X-Custom` and folded continuations kept byte-exact.
     - `TestCliHdrRelayGoogleAndDate`: calls the unexported `relayHeaders(article, host, true)` (no package var is
       touched, K3): `X-Google-Foo` is dropped; with `false` it is kept; a stored `Date: Tuesday 7pm` with a valid
       `DateSent` is replaced by `outboundDate`'s value; an RFC date is kept verbatim.
     - `TestCliHdrRelayInvalid`: table → each wraps `ErrRelayHeadersInvalid`: two Subject lines, two Path lines,
       Message-ID ≠ `article.MessageID`, an invalid Message-ID, a line with a bare CR, a line with NUL.
     - `TestCliHdrRelayIncomplete`: table → each wraps `ErrRelayHeadersIncomplete`: a web-post style block (MIME-Version,
       Content-Type, From, Newsgroups only), missing Date, a colon-less line, a first line that is a continuation.
     - `TestCliHdrEnsureLocalPathPrefix`: table.
   - `internal/nntp/nntp-client-cmds_test.go` (fake server; `AuthFailureBackoff: -1` everywhere):
     - `TestCliCmdXHdrStreamed411` (CLI-2): script `GROUP .*` → `411 no such group`; `XHdrStreamed` returns an error
       with `errors.Is(err, ErrNewsgroupNotFound)`, no panic, the channel is closed.
     - `TestCliCmdGreeting201` (CLI-5): greeting `201 no posting` → `Connect` succeeds.
     - `TestCliCmdAuth281AfterUser` (CLI-5): `AUTHINFO USER` → `281` → `Connect` succeeds and `Lines()` has no
       `AUTHINFO PASS`.
     - `TestCliCmdSelectGroup480` (CLI-5): `Pool.SelectGroup` → `errors.Is(err, ErrAuthRequired)` and not
       `ErrNewsgroupNotFound`.
     - `TestCliCmdNotFoundCodes` (CLI-5): STAT 430 → `(false, nil)`; HEAD 430 → `ErrArticleNotFound`; BODY 451 →
       `ErrArticleRemoved`; the connection stays usable (a following STAT 223 works).
     - `TestCliCmdPost401ModeReader` (CLI-5): `POST` → `401 MODE-READER`, `MODE READER` → `200`, `POST` → `340`
       (`ReadDot`) → `240` → `PostArticle` returns 240 and `Lines()` contains `MODE READER`.
     - `TestCliCmdTakeThisWire` (CLI-1, A-intake-6): `cliPeerHandler`; connection from `pool.Get(MODE_STREAM_MV)`;
       `NewResponseDemuxer` (not started) and a `chan *ReadRequest` of cap 4; under `conn.Lock()` call
       `SendTakeThisArticleStreaming` with an article whose `HeadersJSON` has Path, folded Newsgroups, Xref, a header
       line `.X-Dot: y`, and `BodyText "a\n.b\n"`. `Articles()[0]` (raw wire) has exactly one `Path:` line, no `Xref`,
       the continuation once, `..X-Dot: y`, and body lines `a`, `..b`, `` before the end.
     - `TestCliCmdTakeThisInvalidSkipped` (A-intake-6): `HeadersJSON` with two Subject lines → the call returns an
       error wrapping `common.ErrRelayHeadersInvalid` with `doContinue == true`, and nothing reaches the wire
       (`RelayHeaders` runs before `Cmd`): the fake's `Lines()` has no `TAKETHIS`.
     - `TestCliCmdTakeThisIncompleteFallback` (A-intake-6, INT-7): a web-post style `HeadersJSON` (no Date/Message-ID/
       Subject/Path) with the row's columns set → the article is sent; the wire has the `ReconstructHeaders` block
       (one Path line, Date, Message-ID, Subject).
     - `TestCliCmdXHdrStreamedReadError` (CLI-2): through `pool.XHdrStreamed`: (a) script `GROUP .*` → `211 3 1 3 g`,
       `XHDR .*` → `221 ok`, `1 a`, then `Hang`; `ReadTimeout 300ms`; (b) the same with a 4 KiB line and
       `MaxResponseLineBytes 512`. Each: the result channel closes, the goroutine's error is visible as a closed
       connection (`WaitOpen(0, 2s)`), and `pool.Stats().IdleConnections == 0` (nothing returned to the pool).
     - `TestCliCmdPostWire` (CLI-1 p2/p3, NC-1): `PostArticle` with folded Newsgroups, Subject `"s\nx"` and
       `BodyText "x\n"` → wire has the continuation once, `Subject: s`, body `x`, `` then `.`.
     - `TestCliCmdArticleBudget` (A-1): `MaxArticleResponseBytes 1024`, ARTICLE reply of 4 KiB → `errors.Is(err,
       ErrResponseTooLarge)`.
     - `TestCliCmdLineCap` (A-1): `MaxResponseLineBytes 512`, XOVER reply with a 4 KiB line → `errors.Is(err,
       ErrResponseLineTooLong)`.
     - `TestCliCmdXHdrStreamedLineCap` (A-1): request 1-3, fake returns 10 lines → error wrapping
       `ErrResponseTooLarge`.
     - `TestCliCmdSOCKSTimeout` (A-1): a `RawStall` fake used as the SOCKS5 proxy (`ProxyEnabled`, `ProxyType
       "socks5"`, `ProxyHost/Port` = fake), `ConnectTimeout 300ms` → `Connect` errors within 2 s.
     - `TestCliCmdOverviewMalformed` (A-7): XOVER lines `abc\t…` and `0\t…` dropped, a valid line kept; XHDR stream with
       `0 x` dropped.
     - `TestCliCmdBytesCRLF` (INT-5): `ParseLegacyArticleLines` on known head/body → `Bytes` equals the CRLF wire size;
       an empty body gives header size + 2.

Acceptance: E03, E04, E05 PASS; E01 still PASS.

---

#### Slice `cli-transfer`: pprof and UI exposure, queue sizing, Redis keys
Ids: OPS-3, A-nntp-client-6, A-nntp-client-10, NC-3, A-ops-3 (d, f). E-tag `cli-transfer`.

Owned files:
- `internal/nntp/nntp-transfer.go`
- `internal/nntp/nntp-transfer-demuxer.go` (only the channel sizes in `NewResponseDemuxer`)
- `cmd/nntp-transfer/main.go`
- `cmd/nntp-transfer/debugserver.go` (new)
- `cmd/nntp-transfer/debugserver_test.go` (new)
- `cmd/history-rebuild/main.go` (only `:12`, `:75`, `:97-104` and the `signal.Notify` at `:161` plus its import)
- `cmd/history-rebuild/pprof_addr.go` (new)
- `cmd/history-rebuild/pprof_addr_test.go` (new)
- `internal/nntp/nntp-client-transfer_test.go` (new)

Changes:
1. **A-6 pools (nntp-transfer.go).** Replace the five chan pools (`:27`, `:90`, `:209`, `:241`, `:288`) with
   `sync.Pool`s `responseDataPool`, `ttSetupPool`, `ttResponsePool`, `ttResponseChanPool`, `readRequestPool`
   (`New` returns a fresh value; `ttResponseChanPool.New` returns `make(chan *TTResponse, 1)`). The `Get*`/`Recycle*`
   functions keep their signatures and reset logic; `RecycleTTResponseChan` keeps its capacity check and drain.
2. **A-6 queue sizing.** In `nntp-transfer.go` add `var DemuxerQueueDepth = 1 << 20` (set once at startup by tools,
   read by `NewResponseDemuxer`) and `func StreamQueueDepth(batchCheck int64, maxQueuedJobs int) int` (decision 16).
   `NewResponseDemuxer` (`nntp-transfer-demuxer.go:33-35`) uses `DemuxerQueueDepth` for all three channels (keep the
   `syncPipeline` call from wave 1). In `cmd/nntp-transfer/main.go`, right after `BatchCheck = *batchCheck` (`:229`):
   `nntp.DemuxerQueueDepth = nntp.StreamQueueDepth(BatchCheck, MaxQueuedJobs)`; `CHTTWorker` (`:2185-2186`) makes both
   ReadRequest channels with `nntp.DemuxerQueueDepth`.
3. **OPS-3 / A-10 in nntp-transfer.**
   - Delete the blank import (`main.go:12`).
   - `debugserver.go`: `newPprofMux() *http.ServeMux` registering `"/debug/pprof/"` → `pprof.Index`,
     `"/debug/pprof/profile"` → `pprof.Profile`, `"/debug/pprof/symbol"` → `pprof.Symbol`, `"/debug/pprof/trace"` →
     `pprof.Trace` (no `Cmdline`; `Index` answers 404 "Unknown profile" for `/debug/pprof/cmdline`);
     `newPprofServer(addr string) *http.Server` (`ReadHeaderTimeout 10s`, `IdleTimeout 120s`, no Write/Read timeout so
     CPU profiles work); `newUIServer(addr string) *http.Server` with its own mux (`"/{$}"` → `handleIndex` — the Go
     1.22+ exact-root pattern, so `/debug/pprof/…` and any other path get 404 instead of the index page; `/results` →
     `handleResults`), `ReadHeaderTimeout 10s`, `ReadTimeout 30s`, `WriteTimeout 60s`, `IdleTimeout 120s`,
     `MaxHeaderBytes 1 << 20`.
   - Note: importing `net/http/pprof` (needed for `pprof.Index` etc.) still registers its handlers on
     `http.DefaultServeMux` in the package `init`. That is harmless only while nothing serves the default mux: no
     `http.ListenAndServe(…, nil)`, `http.Handle`/`http.HandleFunc` or `Server{Handler: nil}` may remain in
     `cmd/nntp-transfer` or `cmd/history-rebuild` (acceptance grep below).
   - Flags: `webBind = flag.String("web-bind", "127.0.0.1", "Address the -web-port UI listens on (use 0.0.0.0 to expose it)")`;
     `-pprof-port` help text: "localhost only; /debug/pprof/cmdline is disabled".
   - Replace `startWebServer` (`:2934-2944`) by starting `uiSrv := newUIServer(net.JoinHostPort(*webBind,
     strconv.Itoa(*webPort)))` in a goroutine (log `[TRANSFER] web UI on http://<addr>/`; `http.ErrServerClosed` is not
     an error), and the pprof block (`:544-552`) by `pprofSrv := newPprofServer(fmt.Sprintf("localhost:%d", *pprofPort))`.
   - Shutdown: after `wgP.Wait()` (`:574`) and before `pool.ClosePool()`, shut down each non-nil server with a 5 s
     context; log errors with `[TRANSFER]`.
   - `handleIndex`: move the data collection and template execution into `renderIndex() ([]byte, error)`, which takes
     `nntp.ResultsMutex.RLock()` with `defer` and executes into a `bytes.Buffer`; `handleIndex` then writes the bytes
     (500 on error) with no lock held. `handleResults`: copy `results` under the lock, write after unlocking.
4. **OPS-3 in history-rebuild.**
   - `pprof_addr.go`: `func loopbackPprofAddr(addr string) (string, error)` (`net.SplitHostPort`; empty host →
     `127.0.0.1`; `localhost` allowed; otherwise `net.ParseIP(host)` must be non-nil and `IsLoopback()`; else
     `fmt.Errorf("-pprof %q: only loopback addresses are allowed (e.g. localhost:6060)", addr)`), and
     `newPprofMux()` (same registrations as above).
   - `main.go`: delete the blank import (`:12`); `-pprof` help: "pprof HTTP server address, loopback only (e.g.
     'localhost:6060'; ':6060' means 127.0.0.1:6060)"; the block at `:97-104` becomes: validate with
     `loopbackPprofAddr` (`log.Fatalf("[HISTORY] %v", err)` on error), then serve `&http.Server{Addr: addr, Handler:
     newPprofMux(), ReadHeaderTimeout: 10 * time.Second}` in a goroutine. No other line of `main.go` changes.
5. **NC-3 Redis keys.** `func buildRedisKeyPrefix(host string, port int) string { return "pugleaf:tx:" + host + ":" +
   strconv.Itoa(port) + ":" }` (pure); `var redisKeyPrefix string` set once right after flag parsing with
   `buildRedisKeyPrefix(*host, *port)`; `func redisKey(msgID string) string { return redisKeyPrefix + msgID }`. Use it at every live EXISTS/SET: `:1629`, `:1733`, `:2508`, `:2648`, `:2657` (the
   commented block at `:2516-2522` may stay commented; if you touch it, use `redisKey` too). Add a line to
   `showUsageExamples` / the `-redis-cache` help that keys are scoped per target, and extend the `-redis-flushdb` help:
   "… flushes ALL keys in the selected Redis DB, including other targets' transfer keys".
5a. **A-ops-3 (d, f): SIGTERM.** `cmd/history-rebuild/main.go:161` and `cmd/nntp-transfer/main.go:347`:
   `signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)` (add the `syscall` import to both files). The handlers after
   them stay as they are, so SIGTERM takes exactly the SIGINT shutdown path (history-rebuild: stop after the current
   range and flush; nntp-transfer: `common.ForceShutdown()`, pool and DB shutdown). Test note: the signal setup lives in
   `main`, so there is no unit test; acceptance is the grep below plus E16 (nntp-transfer only; history-rebuild is covered by the grep).
6. Tests:
   - `cmd/nntp-transfer/debugserver_test.go`:
     - `TestTxDbgPprofMuxNoCmdline`: `httptest.NewServer(newPprofMux())`; `/debug/pprof/` → 200; `/debug/pprof/cmdline`
       → 404 and the body does not contain `os.Args[0]`.
     - `TestTxDbgUIServerNoPprof`: `newUIServer("127.0.0.1:0").Handler` via `httptest`: `/debug/pprof/`,
       `/debug/pprof/cmdline` and `/nope` → 404; `/results` → 200; `/` → 200 or 500 (template data may be empty) but
       never 404.
     - `TestTxDbgUIServerTimeouts`: the five fields set as listed.
     - `TestTxDbgRedisKey`: `buildRedisKeyPrefix("peer.example", 119) + "<a@b>"` is
       `pugleaf:tx:peer.example:119:<a@b>`, and two hosts give different keys (pure function; no global is mutated, K3).
   - `cmd/history-rebuild/pprof_addr_test.go`: `TestHRPprofAddr` table: `:6060` → `127.0.0.1:6060`;
     `localhost:6060`, `127.0.0.1:1`, `[::1]:2` ok; `0.0.0.0:6060`, `192.0.2.1:6060`, `example.org:1`, `6060` → error.
   - `internal/nntp/nntp-client-transfer_test.go`:
     - `TestCliTxStreamQueueDepth`: table (defaults 100/8 → 4224; tiny → 4096; huge → 1<<20).
     - `TestCliTxPoolsRoundTrip`: for each pool type, `Get…` returns a value whose fields equal the arguments, and
       after `Recycle…` the **recycled value's** fields are zero (assert on the value just recycled; do not expect the
       next `Get` to return the same pointer: `sync.Pool` may drop Puts, always under `-race`); `RecycleTTResponseChan`
       rejects a cap-2 chan and drains a cap-1 chan.
     - `TestCliTxDemuxerDepth`: a demuxer built while `DemuxerQueueDepth` has its default has channels of that cap —
       read the package default, do not mutate it (K3).

Acceptance: E09, E10, E11, E12, E13, E16 PASS; `grep -n 'syscall.SIGTERM' cmd/history-rebuild/main.go
cmd/nntp-transfer/main.go` finds one `signal.Notify` line in each; and
`grep -rnE 'ListenAndServe(TLS)?\([^)]*, *nil\)|http\.Handle(Func)?\(' cmd/nntp-transfer cmd/history-rebuild` prints
nothing (no handler on, and no server for, `http.DefaultServeMux`).

---

### Wave 3 (1 slice, based on the merged wave 2)

#### Slice `cli-hygiene`: command-argument validation and dead code
Ids: A-nntp-client-3, A-nntp-client-9. E-tag `cli-hygiene`.

Owned files:
- `internal/nntp/nntp-client-validate.go` (new)
- `internal/nntp/nntp-client-commands.go`
- `internal/nntp/nntp-client.go` (only `authenticate`'s two `Cmd` calls)
- `internal/nntp/nntp-backend-pool.go` (only `Pool.GetArticle`)
- `internal/nntp/transfer-progress.go` (only `GetRecentTransfers`)
- `internal/nntp/transfer-progress-utils.go` (delete)
- `cmd/nntp-transfer/main.go` (only the A-9 deletions and the `processBatch` filter)
- `internal/nntp/nntp-client-validate_test.go` (new)

Changes:
1. **`nntp-client-validate.go` (new; A-3, decision 12).**
   ```go
   var ErrInvalidCommandArg = errors.New("nntp: invalid command argument")
   const maxClientCommandLine = 510 // RFC 3977 §3.1: 512 octets including CRLF
   func validMessageIDArg(id string) error // common.ValidMessageID or wrapped ErrInvalidCommandArg
   func validGroupArg(name string) error   // 1..497 octets, no byte <= 0x20, 0x7f or ','
   func validTokenArg(s string, allowSpace bool) error // non-empty, no CR/LF/NUL; no SP/HT unless allowSpace
   func (c *BackendConn) sendCmd(format string, args ...any) (uint, error)
   	// line := fmt.Sprintf(format, args...); CR/LF/NUL or len > maxClientCommandLine → ErrInvalidCommandArg (nothing written);
   	// else c.TextConn.Cmd("%s", line)
   ```
2. **Call sites.** Replace every `c.TextConn.Cmd(` in `nntp-client-commands.go` and `authenticate` with
   `c.sendCmd(` (same format and args). Validate before sending: message-id in `StatArticle`, `GetArticle`, `GetHead`,
   `GetBody`, `SendTakeThisArticleStreaming` (return `0, 0, err, true`: skipped) and `SendCheckMultiple` (an invalid id
   returns an error before anything is written; the transfer filters them earlier, step 4); group in `SelectGroup`,
   `XOver`, `XHdr`, `XHdrStreamedBatch`, `ListGroup`; XHDR field with `validTokenArg(field, false)`; AUTHINFO user and
   password with `validTokenArg(v, true)` (RFC 4643 allows no CR/LF; T-nntp-auth-1 is about spaces, so spaces pass).
3. **`Pool.GetArticle`:** an error with `errors.Is(err, ErrInvalidCommandArg)` puts the connection back (it is
   healthy) and returns the error, like 430/451.
4. **`processBatch` filter (main.go:1588-1610).** Before the job is built, drop `nil` and
   `!common.ValidMessageID(*id)` entries from `messageIDs` (log once per batch with the count, increment
   `ngtprogress.Increment(nntp.IncrFLAG_SKIPPED, n)`); if nothing is left, return `nil, nil` as for an empty batch.
5. **A-9 deletions.** `ListGroupsLimited` (`nntp-client-commands.go:282-361`); the commented deadline block in
   `GetArticle` (`:76-87`); `GetRecentTransfers` (`transfer-progress.go:219-291`); `git rm
   internal/nntp/transfer-progress-utils.go` (all three functions and `truncateString` are unused); in `main.go`:
   `IgnoreNewsgroupProgress` (`:967-979`), `matchesAnyPattern` (`:1084-1093`, with its comment), `var dbBatchSize` (`:36`) and its
   assignment (`:235`); keep `-batch-db` parsed, help text "No effect (accepted for compatibility)", and `_ = batchDB`
   after the clamp. `grep -rnw` each name first; expected callers: none (test-nntp was removed in wave 1).
6. Tests in `nntp-client-validate_test.go`:
   - `TestCliValArgs`: tables for the three validators (CR inside, space inside a message-id, `<a@b> x`, 251-octet id,
     group with `,`/space/tab, empty).
   - `TestCliValNothingWritten`: fake server; `StatArticle("<a\rb@c>")`, `SelectGroup("a b")`, `XHdr(g, "Sub ject", …)`
     each return `ErrInvalidCommandArg` and the fake's `Lines()` stays empty after the greeting; the connection is
     still usable (`StatArticle("<ok@x>")` → 223).
   - `TestCliValSendCmdLength`: a 600-octet command → `ErrInvalidCommandArg`.

Acceptance: E15 PASS; all earlier E-checks still PASS; `staticcheck`-style dead code from the audit is gone
(`grep -rn 'ListGroupsLimited\|PrintRecentTransfers\|IgnoreNewsgroupProgress\|matchesAnyPattern\|xSetReadDeadline' internal cmd`
prints nothing).

---

## Checks

On every merged tree (after wave 0 and after each merge in waves 1–3):
```bash
gofmt -l ./cmd ./internal            # may list only internal/database/embedded_migrations.go and internal/web/web_admin_provider.go
go vet ./...
go vet -tags e2e ./internal/nntp/ ./cmd/nntp-server/
go build ./...
go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
  ./internal/nntp/... ./internal/processor/... ./internal/common/... \
  ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/... ./cmd/nntp-transfer/...
go test -race -count=3 -timeout 600s -run 'TestCli|TestTxDbg|TestHRPprof' ./internal/nntp/ ./internal/common/... \
  ./cmd/nntp-transfer/... ./cmd/history-rebuild/...      # flake run of the new tests
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u    # unchanged from the wave-0 record: this plan removes no marker
./build_nntp-transfer.sh && ./build_history-rebuild.sh
git status --short                   # no data-test-*, build/ or scratch files
```
The new client tests run under `go test -race ./internal/nntp/` (package `nntp`, files `nntp-client-*_test.go`).
Merge order in wave 1: `cli-tools`, then `cli-conn`. Wave 2: `cli-transfer`, then `cli-commands`. The file sets of a
wave are disjoint, so no textual conflicts are expected; run the checks after each merge. Semantic interplay to watch:
`cli-commands`' `writeArticleLines` runs under the `conn.Lock()` that `sendArticlesBatchViaTakeThis` holds, now with
write deadlines from wave 1; `cli-transfer`'s smaller demuxer queues must not block a sender (E01 and the -race run).

## End-to-end

**Runner:** `pugleaf-verifier` on the main checkout (`plan-nntp-client-transfer`), after the last executed wave.
```bash
./build_nntp-server.sh && ./build_nntpmgr.sh && ./build_nntp-transfer.sh && ./build_history-rebuild.sh
PORT=21219 PEERPORT=21220 WEBPORT=21221 PPROFPORT=21222 SILENTPORT=21223 HRPORT=21224 \
  DATA=./data-test-nntp-client-transfer scripts/test-nntp-client-transfer.sh
```
Expected: `SUMMARY … fail=0`; E14 shows no `DATA RACE`/`panic:` (the transfer binary is a `-race` build).
Everything runs against 127.0.0.1: the nntp-server and the fake peer are local; no external server is contacted.

### E-checks
| E | Tag | Check |
|----|----|----|
| E01 | cli-conn | run 1 (`-username` set) exits 0 within 120 s; peer `commands.log` has `AUTHINFO USER`, `AUTHINFO PASS`, `MODE STREAM` and at least one `CHECK` and one `TAKETHIS` (CLI-4) |
| E02 | cli-conn | `nntp-transfer -test-conn -timeout 2` against the silent peer exits non-zero within 15 s (CLI-3) |
| E03 | cli-commands | every received article has exactly one `Path:` line, it starts with `Path: $NNTPHOST!`, `$NNTPHOST!` occurs once, no `!.TX!` (CLI-1 p1, decision 1) |
| E04 | cli-commands | each received header block (CRLF stripped, un-stuffed, Path line removed) equals the stored `headers_json` lines minus Path and Xref fields; ct-1's continuation ` e2e.ct.b` occurs exactly once (CLI-1 p2, A-intake-6) |
| E05 | cli-commands | ct-1's body on the wire is `line1`, `..dotline`, `` then `.` (CLI-1 p3) |
| E06 | cli-tools | progress DB: latest row of `e2e.ct.a` after run 1 has `retry = 1` (meaningful only when E01 passes) |
| E07 | cli-tools | run 2 log has no `Skipping newsgroup e2e.ct.a` and (E07b, verify phase 2) the peer now holds `<ct-2@e2e.invalid>` (NC-2) |
| E08 | cli-tools | run 3 log has `Skipping newsgroup e2e.ct.a - already has transfer results` |
| E09 | cli-transfer | during run 1, `GET http://127.0.0.1:$WEBPORT/` → 200 |
| E10 | cli-transfer | `GET http://127.0.0.1:$WEBPORT/debug/pprof/cmdline` → 404 and the body lacks the peer password |
| E11 | cli-transfer | `GET http://127.0.0.1:$PPROFPORT/debug/pprof/` → 200; `/debug/pprof/cmdline` → 404 without the password |
| E12 | cli-transfer | `ss -ltn` shows `$WEBPORT` bound to `127.0.0.1` only (INFO when `ss` is missing) |
| E13 | cli-transfer | `history-rebuild -pprof 0.0.0.0:$HRPORT -analyze-only` exits non-zero within 20 s and prints `loopback` |
| E14 | any | no `WARNING: DATA RACE` and no `panic:` in `transfer-*.log` |
| E16 | cli-transfer | a transfer to the silent peer (kept busy by retried connects) gets SIGTERM 2 s after `Found 1 newsgroups to transfer`: its log shows `Received shutdown signal` (the SIGINT shutdown path), i.e. SIGTERM is handled, not a bare kill (A-ops-3 f). If shutdown then takes over 30 s, INFO E16b (shared SIGINT path) |
| E15 | cli-hygiene | every `CHECK`/`TAKETHIS` argument in `commands.log` is a valid message-id and no logged line contains `\r` or `\x00` |

### `scripts/test-nntp-client-transfer.sh` (wave 0 writes this; later waves fix only real script bugs)
```bash
#!/usr/bin/env bash
# End-to-end checks for plan nntp-client-transfer. Scratch data and 127.0.0.1 only.
# Usage: scripts/test-nntp-client-transfer.sh [--build]
# Env: PORT (21219 nntp-server), PEERPORT (21220 fake peer), WEBPORT (21221), PPROFPORT (21222),
#      SILENTPORT (21223 silent peer), HRPORT (21224), DATA (./data-test-nntp-client-transfer),
#      NNTPHOST (example.org; nntp-server and nntp-transfer check it by DNS lookup)
set -u
cd "$(git rev-parse --show-toplevel)" || exit 2
PORT="${PORT:-21219}"; PEERPORT="${PEERPORT:-21220}"; WEBPORT="${WEBPORT:-21221}"; PPROFPORT="${PPROFPORT:-21222}"
SILENTPORT="${SILENTPORT:-21223}"; HRPORT="${HRPORT:-21224}"
DATA="${DATA:-./data-test-nntp-client-transfer}"; NNTPHOST="${NNTPHOST:-example.org}"
SRV=./build/pugleaf-nntp-server; MGR=./build/pugleaf-nntpmgr; TX=./build/nntp-transfer; HR=./build/history-rebuild
E2E_USER=ctuser00001; E2E_PASS=ctpass00001; PEER_USER=peeruser01; PEER_PASS=peer-secret-0123
case "$DATA" in *..*) echo "refusing DATA=$DATA (no .. allowed)"; exit 2 ;; esac
case "$DATA" in ./data-test-*) ;; *) echo "refusing DATA=$DATA (must start with ./data-test-)"; exit 2 ;; esac
for p in "$PORT" "$PEERPORT" "$WEBPORT" "$PPROFPORT" "$SILENTPORT" "$HRPORT"; do
  case "$p" in ''|*[!0-9]*) echo "refusing port '$p'"; exit 2 ;; esac
  nc -z -w 1 127.0.0.1 "$p" >/dev/null 2>&1 && { echo "refusing to run: 127.0.0.1:$p is in use"; exit 2; }
done
for t in sqlite3 timeout nc go curl; do command -v "$t" >/dev/null || { echo "missing tool: $t"; exit 2; }; done
[ -e ./.update ] && { echo "refusing to run: ./.update exists"; exit 2; }
if command -v getent >/dev/null; then getent hosts "$NNTPHOST" >/dev/null 2>&1 || { echo "SKIP dns: $NNTPHOST"; exit 2; }; fi
if [ "${1:-}" = "--build" ]; then
  ./build_nntp-server.sh && ./build_nntpmgr.sh && ./build_nntp-transfer.sh && ./build_history-rebuild.sh || exit 2
fi
for b in "$SRV" "$MGR" "$TX" "$HR"; do [ -x "$b" ] || { echo "missing $b (pass --build)"; exit 2; }; done
abs() { echo "$(cd "$(dirname "$1")" && pwd -P)/$(basename "$1")"; }
SRV_ABS=$(abs "$SRV"); MGR_ABS=$(abs "$MGR"); TX_ABS=$(abs "$TX"); HR_ABS=$(abs "$HR")

PASSES=0; FAILS=0; INFOS=0; SPID=""; PPID_PEER=""; PPID_SILENT=""; TXPID=""
pass() { PASSES=$((PASSES+1)); echo "PASS $1 [$2] $3"; }
fail() { FAILS=$((FAILS+1)); echo "FAIL $1 [$2] $3 :: ${4:-}"; }
info() { INFOS=$((INFOS+1)); echo "INFO $1 [$2] $3"; }
alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null; }
stop_pid() { [ -n "$1" ] || return 0; kill -INT "$1" 2>/dev/null
  for _ in $(seq 1 60); do alive "$1" || return 0; sleep 1; done; kill -KILL "$1" 2>/dev/null; }
cleanup() { [ -d "${PEERDIR:-/nonexistent}" ] && touch "$PEERDIR/STOP" "$SILENTDIR/STOP" 2>/dev/null
  stop_pid "$TXPID"; stop_pid "$SPID"; sleep 1; stop_pid "$PPID_PEER"; stop_pid "$PPID_SILENT"; }
trap cleanup EXIT

rm -rf "$DATA"; mkdir -p "$DATA/run" "$DATA/peer" "$DATA/silent" || exit 2
DATA_ABS=$(cd "$DATA" && pwd -P); RUN="$DATA_ABS/run"; PEERDIR="$DATA_ABS/peer"; SILENTDIR="$DATA_ABS/silent"
DB="$DATA/cfg/pugleaf.sq3"; q() { sqlite3 -cmd '.timeout 5000' "$DB" "$1"; }
setup_fail() { echo "$1"; tail -30 "$DATA/server.log" 2>/dev/null; exit 2; }
start_srv() {
  ( cd "$RUN" && exec "$SRV_ABS" -data "$DATA_ABS" -nntptcpport "$PORT" -nntphostname "$NNTPHOST" ) >>"$DATA/server.log" 2>&1 &
  SPID=$!
  for _ in $(seq 1 90); do nc -z -w 1 127.0.0.1 "$PORT" >/dev/null 2>&1 && return 0; alive "$SPID" || return 1; sleep 1; done; return 1
}

# ---- setup: schema, groups, user, three posted articles (nntp-server on scratch data)
start_srv || setup_fail "nntp-server failed to start"; stop_pid "$SPID"; SPID=""
q "INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status,low_water,high_water)
   VALUES('e2e.ct.a','',0,0,1,'e2e','y',1,0),('e2e.ct.b','',0,0,1,'e2e','y',1,0)" || setup_fail "seed groups"
[ "$(q "SELECT value FROM config WHERE key='local_nntp_hostname'")" = "$NNTPHOST" ] \
  || q "INSERT OR REPLACE INTO config(key,value) VALUES('local_nntp_hostname','$NNTPHOST')" || setup_fail "seed hostname"
( cd "$RUN" && exec "$MGR_ABS" -data "$DATA_ABS" -create -username "$E2E_USER" -password "$E2E_PASS" -posting -maxconns 4 ) \
  >"$DATA/nntpmgr.log" 2>&1 || setup_fail "nntpmgr -create failed"
start_srv || setup_fail "nntp-server failed to restart"
PUGLEAF_E2E_ADDR="127.0.0.1:$PORT" PUGLEAF_E2E_USER="$E2E_USER" PUGLEAF_E2E_PASS="$E2E_PASS" \
  timeout 120 go test -tags e2e -count=1 -v -run '^TestCliE2ESeed$' ./internal/nntp/ >"$DATA/seed.log" 2>&1
[ "$(grep -c '^PASS S0' "$DATA/seed.log")" = 3 ] || setup_fail "seeding POSTs failed: $(grep -E '^(PASS|FAIL) S0' "$DATA/seed.log" | tr '\n' ' ')"
for _ in $(seq 1 30); do [ "$(q "SELECT message_count FROM newsgroups WHERE name='e2e.ct.a'")" -ge 3 ] 2>/dev/null && break; sleep 1; done
stop_pid "$SPID"; SPID=""   # the transfer reads the DB directly; the server must not write concurrently
GDB=$(find "$DATA_ABS/db" -name 'e2e_ct_a.db' | head -1); [ -n "$GDB" ] || setup_fail "group DB e2e_ct_a.db not found"

# ---- fake peers (go test, build tag e2e)
start_peer() { # $1 mode, $2 port, $3 dir
  PUGLEAF_E2E_PEER_ADDR="127.0.0.1:$2" PUGLEAF_E2E_PEER_DIR="$3" PUGLEAF_E2E_PEER_MODE="$1" \
  PUGLEAF_E2E_PEER_USER="$PEER_USER" PUGLEAF_E2E_PEER_PASS="$PEER_PASS" \
  PUGLEAF_E2E_PEER_431='<ct-2@e2e.invalid>' PUGLEAF_E2E_PEER_STALL=15s \
    go test -tags e2e -count=1 -timeout 15m -run '^TestCliE2EFakePeer$' ./internal/nntp/ >"$3/peer.log" 2>&1 &
  echo $!
}
PPID_PEER=$(start_peer peer "$PEERPORT" "$PEERDIR"); PPID_SILENT=$(start_peer silent "$SILENTPORT" "$SILENTDIR")
for _ in $(seq 1 120); do [ -e "$PEERDIR/ready" ] && [ -e "$SILENTDIR/ready" ] && break; sleep 1; done
[ -e "$PEERDIR/ready" ] && [ -e "$SILENTDIR/ready" ] || setup_fail "fake peers did not start: $(tail -5 "$PEERDIR/peer.log")"

TXARGS="-data $DATA_ABS -host 127.0.0.1 -port $PEERPORT -username $PEER_USER -password $PEER_PASS -group e2e.ct.a -redis-cache=false -max-threads 1 -batch-check 10 -timeout 5"
run_tx() { ( cd "$RUN" && exec timeout 180 "$TX_ABS" $TXARGS "${@:2}" ) >"$DATA/transfer-$1.log" 2>&1; }

# ---- run 1 (background: web/pprof checks happen while the peer stalls MODE STREAM for 15 s; the poll below
#      gives up after 10 s, so all four checks run inside the stall window)
( cd "$RUN" && exec timeout 180 "$TX_ABS" $TXARGS -web-port "$WEBPORT" -pprof-port "$PPROFPORT" ) >"$DATA/transfer-1.log" 2>&1 &
TXPID=$!
for _ in $(seq 1 100); do curl -fs -o /dev/null "http://127.0.0.1:$WEBPORT/" && break; sleep 0.1; done
c=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEBPORT/")
[ "$c" = 200 ] && pass E09 cli-transfer "UI answers on loopback" || fail E09 cli-transfer "UI answers on loopback" "status=$c"
b=$(curl -s -w '|%{http_code}' "http://127.0.0.1:$WEBPORT/debug/pprof/cmdline")
case "$b" in *"$PEER_PASS"*) fail E10 cli-transfer "no pprof on the UI port" "password leaked" ;;
  *'|404') pass E10 cli-transfer "no pprof on the UI port" ;; *) fail E10 cli-transfer "no pprof on the UI port" "got ${b##*|}" ;; esac
c=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PPROFPORT/debug/pprof/")
b=$(curl -s -w '|%{http_code}' "http://127.0.0.1:$PPROFPORT/debug/pprof/cmdline")
case "$b" in *"$PEER_PASS"*) fail E11 cli-transfer "pprof without cmdline" "password leaked" ;;
  *'|404') [ "$c" = 200 ] && pass E11 cli-transfer "pprof without cmdline" || fail E11 cli-transfer "pprof without cmdline" "index=$c" ;;
  *) fail E11 cli-transfer "pprof without cmdline" "cmdline=${b##*|} index=$c" ;; esac
if command -v ss >/dev/null; then
  l=$(ss -ltnH "sport = :$WEBPORT" 2>/dev/null | awk '{print $4}' | tr '\n' ' ')
  case "$l" in 127.0.0.1:"$WEBPORT"" ") pass E12 cli-transfer "UI bound to 127.0.0.1" ;; *) fail E12 cli-transfer "UI bound to 127.0.0.1" "listen='$l'" ;; esac
else info E12 cli-transfer "ss not installed"; fi
wait "$TXPID"; rc=$?; TXPID=""
if [ "$rc" = 0 ] && grep -q '^"AUTHINFO PASS' "$PEERDIR/commands.log" && grep -q '^"MODE STREAM' "$PEERDIR/commands.log" \
   && grep -q '^"CHECK ' "$PEERDIR/commands.log" && grep -q '^"TAKETHIS ' "$PEERDIR/commands.log"; then
  pass E01 cli-conn "authenticated streaming transfer completes"
else fail E01 cli-conn "authenticated streaming transfer completes" "rc=$rc; $(tail -3 "$DATA/transfer-1.log" | tr '\n' ' ')"; fi
r=$(sqlite3 "$DATA/transfer-progress/transfer-progress.db" "SELECT retry FROM transfers WHERE newsgroup='e2e.ct.a' ORDER BY id DESC LIMIT 1" 2>/dev/null)
[ "$r" = 1 ] && pass E06 cli-tools "431 recorded as retry" || fail E06 cli-tools "431 recorded as retry" "retry='$r'"
PUGLEAF_E2E_PEER_DIR="$PEERDIR" PUGLEAF_E2E_GROUPDB="$GDB" PUGLEAF_E2E_LOCALHOST="$NNTPHOST" PUGLEAF_E2E_PHASE=1 \
  timeout 120 go test -tags e2e -count=1 -v -run '^TestCliE2EVerify$' ./internal/nntp/ >"$DATA/verify-1.log" 2>&1
lines=$(grep -E '^(PASS|FAIL|INFO) E[0-9]+b? ' "$DATA/verify-1.log"); [ -n "$lines" ] && printf '%s\n' "$lines"
PASSES=$((PASSES + $(printf '%s\n' "$lines" | grep -c '^PASS '))); FAILS=$((FAILS + $(printf '%s\n' "$lines" | grep -c '^FAIL ')))
[ "$(printf '%s\n' "$lines" | grep -cE '^(PASS|FAIL) E(03|04|05|15) ')" = 4 ] || fail E00 any "verify phase 1 incomplete" "$(tail -3 "$DATA/verify-1.log" | tr '\n' ' ')"

# ---- run 2 (re-offers the group) and run 3 (skips it)
run_tx 2
if ! grep -q 'Skipping newsgroup e2e.ct.a' "$DATA/transfer-2.log"; then pass E07 cli-tools "incomplete group re-offered"
else fail E07 cli-tools "incomplete group re-offered" "run 2 skipped it"; fi
PUGLEAF_E2E_PEER_DIR="$PEERDIR" PUGLEAF_E2E_GROUPDB="$GDB" PUGLEAF_E2E_LOCALHOST="$NNTPHOST" PUGLEAF_E2E_PHASE=2 \
  timeout 120 go test -tags e2e -count=1 -v -run '^TestCliE2EVerify$' ./internal/nntp/ >"$DATA/verify-2.log" 2>&1
lines=$(grep -E '^(PASS|FAIL) E07b ' "$DATA/verify-2.log"); [ -n "$lines" ] && printf '%s\n' "$lines"
PASSES=$((PASSES + $(printf '%s\n' "$lines" | grep -c '^PASS '))); FAILS=$((FAILS + $(printf '%s\n' "$lines" | grep -c '^FAIL ')))
[ -n "$lines" ] || fail E07b cli-tools "verify phase 2 did not report" "$(tail -3 "$DATA/verify-2.log" | tr '\n' ' ')"
run_tx 3
grep -q 'Skipping newsgroup e2e.ct.a - already has transfer results' "$DATA/transfer-3.log" \
  && pass E08 cli-tools "complete group skipped" || fail E08 cli-tools "complete group skipped" "$(tail -2 "$DATA/transfer-3.log" | tr '\n' ' ')"

# ---- E16: SIGTERM takes the graceful path. Target the silent peer so the process stays busy (connects time out
#      after -timeout 2 and are retried) until it is signalled.
( cd "$RUN" && exec timeout 120 "$TX_ABS" -data "$DATA_ABS" -host 127.0.0.1 -port "$SILENTPORT" -group e2e.ct.a \
    -redis-cache=false -use-progress-db=false -max-threads 1 -timeout 2 ) >"$DATA/transfer-term.log" 2>&1 &
TXPID=$!   # the pid of GNU timeout, which forwards SIGTERM to nntp-transfer
for _ in $(seq 1 100); do grep -q 'Found [0-9]* newsgroups to transfer' "$DATA/transfer-term.log" && break; alive "$TXPID" || break; sleep 0.1; done
sleep 2
kill -TERM "$TXPID" 2>/dev/null
t0=$(date +%s); exited=no
for _ in $(seq 1 30); do alive "$TXPID" || { exited=yes; break; }; sleep 1; done
[ "$exited" = yes ] || kill -KILL "$TXPID" 2>/dev/null
wait "$TXPID" 2>/dev/null; TXPID=""
if grep -q 'Received shutdown signal' "$DATA/transfer-term.log"; then
  pass E16 cli-transfer "SIGTERM handled like SIGINT (exited=$exited after $(( $(date +%s) - t0 ))s)"
  [ "$exited" = yes ] || info E16b cli-transfer "graceful shutdown did not finish within 30s (same path as SIGINT; not this plan's finding)"
else fail E16 cli-transfer "SIGTERM handled like SIGINT" "no 'Received shutdown signal' in log; $(tail -2 "$DATA/transfer-term.log" | tr '\n' ' ')"; fi

# ---- E02 silent peer, E13 history-rebuild pprof, E14 logs
t0=$(date +%s)
( cd "$RUN" && exec timeout 30 "$TX_ABS" -test-conn -host 127.0.0.1 -port "$SILENTPORT" -timeout 2 -group x -data "$DATA_ABS" ) >"$DATA/testconn.log" 2>&1
rc=$?; el=$(( $(date +%s) - t0 ))
[ "$rc" != 0 ] && [ "$rc" != 124 ] && [ "$el" -le 15 ] && pass E02 cli-conn "silent upstream fails fast (${el}s)" \
  || fail E02 cli-conn "silent upstream fails fast" "rc=$rc elapsed=${el}s"
( cd "$RUN" && exec timeout 20 "$HR_ABS" -data "$DATA_ABS" -analyze-only -pprof "0.0.0.0:$HRPORT" ) >"$DATA/hr-pprof.log" 2>&1
rc=$?
[ "$rc" != 0 ] && [ "$rc" != 124 ] && grep -qi loopback "$DATA/hr-pprof.log" && pass E13 cli-transfer "history-rebuild refuses non-loopback -pprof" \
  || fail E13 cli-transfer "history-rebuild refuses non-loopback -pprof" "rc=$rc $(tail -2 "$DATA/hr-pprof.log" | tr '\n' ' ')"
if grep -l 'WARNING: DATA RACE\|^panic:' "$DATA"/transfer-*.log >/dev/null 2>&1; then
  fail E14 any "no race or panic in transfer logs" "$(grep -l 'WARNING: DATA RACE\|^panic:' "$DATA"/transfer-*.log | tr '\n' ' ')"
else pass E14 any "no race or panic in transfer logs"; fi

echo "SUMMARY pass=$PASSES fail=$FAILS info=$INFOS data=$DATA"
[ "$FAILS" -gt 125 ] && FAILS=125
exit "$FAILS"
```
Script notes for wave 0: the 15 s MODE STREAM stall is far below the client's 2 min idle `ReadTimeout`, and run 1's
`timeout 180` covers it. The baseline run (before any fix) leaks only the fake peer's scratch password, on 127.0.0.1.
The peer writes `commands.log` lines with `%q`, so the greps above match a leading `"`;
`-analyze-only` on scratch data has nothing to open before the flag check fails. If a real script bug shows up in a
later wave, fix the script and note it in `## Progress`.

**TestCliE2EVerify definitions** (phase 1 unless noted; read the `*.wire` files and the group DB):
- **E03:** for each `.wire` file: exactly one line with prefix `Path:` (case-insensitive) in the header part; it starts
  with `Path: <LOCALHOST>!`; `<LOCALHOST>!` occurs once in it; it does not contain `!.TX!`.
- **E04:** for ct-1 and ct-3: header part (lines before the first empty line), un-stuffed, minus the Path line, equals
  `strings.Split(headers_json, "\n")` minus the Path and Xref fields (with their continuation lines); ct-1 has exactly one
  line ` e2e.ct.b`.
- **E05:** ct-1's wire lines after the first empty line are exactly `line1`, `..dotline`, `` (the file holds the block
  without the `.` terminator, which `ReadDotBlock` consumed).
- **E15:** every `CHECK`/`TAKETHIS` line in `commands.log` has an argument that passes `common.ValidMessageID`
  (present because `nntp-intake-injection` is merged first); no unquoted line contains `\r` or `\x00`.
- **E07b (phase 2):** `<hex("<ct-2@e2e.invalid>")>.wire` exists.

---

## Progress

(filled in by `/run-plan`)
