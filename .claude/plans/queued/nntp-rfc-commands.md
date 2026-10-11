# Plan: NNTP RFC commands (RFC-1: DATE, NEXT/LAST, NEWGROUPS, LIST wildmat, OVER/HDR, streaming, STARTTLS)

- **Slug:** `nntp-rfc-commands`
- **Integration branch:** `plan-nntp-rfc-commands` (from `testing-001`)
- **Depends on:** `nntp-server-hardening` (merged first). Wave 0 stops if `testing-001` doesn't contain it.
- **Run with:** `/run-plan .claude/plans/queued/nntp-rfc-commands.md`
- **Status:** written 2026-10-11 by wave 4 of `nntp-audit-tests` (slice `fixplan-nntp-rfc-commands`), anchored at
  `plan-nntp-audit-tests` @ 2fb7741. Nothing has run yet. Line numbers below are **at 2fb7741**; hardening moves some of
  them, so every slice locates code by function name first and uses the line only as a hint.
- **Parallelism:** wave 1 has 5 implementer slices, wave 2 has 1 implementer slice after an inline dispatch step.
  Within a wave no two slices own the same file, and no slice owns a file that Appendix B of `nntp-audit-tests` gives to
  `nntp-server-hardening`. The only edits inside hardening's files are the inline dispatch and CAPABILITIES lines
  (D4 coordination rule), done by the orchestrator in wave 2.
- **Source:** `docs/audit/AUDIT-2026-10.md` (§4 nntp-server table row RFC-1, §5 RFC command matrix, §10 known-bug
  list, §11 decisions, §12 corrections), `docs/audit/audit-nntp-rfc.md` (RFC-1 block, Command matrix, Response code
  table, CAPABILITIES), `docs/audit/audit-nntp-sec.md` (Unauthenticated attack surface, TLS and shutdown).

---

## Context

### Request
The audit (plan `nntp-audit-tests`) routed exactly one finding to this plan: **RFC-1** (medium, rfc, CONFIRMED). The
server advertises READER but answers the READER-mandatory DATE, NEXT, LAST and NEWGROUPS with 500. LIST ACTIVE and
LIST NEWSGROUPS ignore the wildmat. TAKETHIS is offered without MODE STREAM and CHECK. OVER, HDR, LIST
OVERVIEW.FMT/HEADERS, NEWNEWS and STARTTLS are missing. Every other server finding (SEC-1…7, BUG-1…9, RFC-2…6, FN-1,
A-nntp-*) belongs to `nntp-server-hardening`; this plan builds on its result and changes none of its decisions.

### Findings (RFC-1, split per command)
The canonical id stays **RFC-1** (K6 of `nntp-audit-tests`: seed ids are never renumbered). `RFC-1a`…`RFC-1n` are
plan-local labels for slices and E-checks only; tests keep using `knownBug(t, "RFC-1", …)` until the marker is removed.
"Today" is the behavior at 2fb7741 (repro: `audit-nntp-rfc.md` Appendix A). All paths are under `internal/nntp/`.

| Label | Command | RFC § | Today (file:line at 2fb7741) | Severity | knownBug marker(s) today | Slice |
|----|----|----|----|----|----|----|
| RFC-1a | `DATE` | 3977 §7.1 (READER) | `500 Command not recognized` from the dispatch `default:` (`nntp-server-cliconns.go:154-155`) | medium | `TestGrpDate` (`nntp-server-group_test.go:656`) | `rfc-reader` |
| RFC-1b | `NEXT` | 3977 §6.1.4 (READER) | 500, `nntp-server-cliconns.go:154-155` | medium | `TestGrpNextLast/{no-group,walk,empty-group}` (`:690`, `:710`, `:720`) | `rfc-reader` |
| RFC-1c | `LAST` | 3977 §6.1.3 (READER) | 500, `nntp-server-cliconns.go:154-155` | medium | same three subtests | `rfc-reader` |
| RFC-1d | `NEWGROUPS date time [GMT]` | 3977 §7.3 (READER) | 500, `nntp-server-cliconns.go:154-155`; `newsgroups.created_at` exists (`migrations/0001_main_schema.sql:39`) but nothing reads it | medium | `TestGrpNewGroups` (`:751`) | `rfc-lists` |
| RFC-1e | `LIST ACTIVE wildmat` | 3977 §7.6.3, §4 | wildmat ignored, every group listed: `handleList` passes only `args[0]` (`nntp-cmd-list.go:9-22`, `:15-19`) | medium | `TestGrpListWildmat/active` (`:596`) | `rfc-lists` |
| RFC-1f | `LIST NEWSGROUPS wildmat` | 3977 §7.6.6, §4 | same (`nntp-cmd-list.go:18-19`, `:46-62`) | medium | `TestGrpListWildmat/newsgroups` (`:596`) | `rfc-lists` |
| RFC-1g | `LIST OVERVIEW.FMT` | 3977 §8.4; 2980 §2.1.7 (SHOULD with XOVER) | `501 Unknown LIST type` (`nntp-cmd-list.go:20-21`) | low | `TestGrpListOverviewFmt` (`:637`) | `rfc-overview` |
| RFC-1h | `OVER [range\|message-id]` | 3977 §8.3 | 500, `nntp-server-cliconns.go:154-155` (not advertised: conformant, missing feature) | low | `TestOvrOverHdr/OVER` (`nntp-server-overview_test.go:688`) | `rfc-overview` |
| RFC-1i | `HDR field [range\|message-id]` | 3977 §8.5 | 500, same | low | `TestOvrOverHdr/HDR` (`:698`) | `rfc-overview` |
| RFC-1j | `LIST HEADERS [MSGID\|RANGE]` | 3977 §8.6 | 501, `nntp-cmd-list.go:20-21` | low | none | `rfc-overview` |
| RFC-1k | `MODE STREAM` | 4644 §2.3 ("Servers MUST accept") | `500 Unknown MODE` (`nntp-cmd-basic.go:24-25`) although TAKETHIS is dispatched (`nntp-server-cliconns.go:152-153`) | medium | `TestSessCapabilities/processor-streaming-complete` (`nntp-server-session_test.go:272`) | `rfc-stream` |
| RFC-1l | `CHECK message-id` | 4644 §2.4 | 500, `nntp-server-cliconns.go:154-155` | medium | (same subtest: "the rest of streaming is expected too") | `rfc-stream` |
| RFC-1m | `NEWNEWS wildmat date time [GMT]` | 3977 §7.4 (capability NEWNEWS, optional) | 500, not advertised: conformant | info | none (`TestGrpNewNews` passes either way) | decision D3; verified by `rfc-wire` |
| RFC-1n | `STARTTLS` | 4642 §2.2 | 500, not advertised; AUTHINFO crosses plaintext (SEC-5) | medium (it is the remedy for plaintext AUTHINFO) | none | `rfc-starttls` |

Supporting facts at 2fb7741:
- Dispatch switch: `nntp-server-cliconns.go:119-156`; `getServerCapabilities` `:199-217` (static list; `"LIST ACTIVE
  NEWSGROUPS"` at `:205`, processor-only labels at `:212-214`).
- Range parsing is duplicated in `handleXOver` (`nntp-cmd-xover.go:9-100`) and `handleXHdr` (`nntp-cmd-xhdr.go:10-108`);
  hardening replaces it with `nntp-cmd-range.go` (K-H1).
- IHAVE/TAKETHIS history checks: `nntp-cmd-posting.go:72-84` (IHAVE), `:155-165` (TAKETHIS), via
  `ArticleProcessor.CheckMessageID` (`nntp-server.go:18-26`).
- TLS: `Start()` builds a local `tls.Config{Certificates}` (`nntp-server.go:119-135`, no `MinVersion`: A-nntp-sec-5,
  fixed by hardening). `cmd/nntp-server/main.go:58-67` copies `-nntpcertfile/-nntpkeyfile` into
  `Server.NNTP.TLSCert/TLSKey` **only** when `-nntptlsport > 0`; `config.ServerConfig.NNTP` has `TLSCert`, `TLSKey`
  (`internal/config/config.go:124-131`).
- `newsgroups.created_at DATETIME DEFAULT CURRENT_TIMESTAMP` (UTC `YYYY-MM-DD HH:MM:SS`); nothing inserts it explicitly
  (`query_InsertNewsgroup` `database/queries.go:120`, batch upsert `database/db_batch.go:553`). No index on it.
- `articles.imported_at DATETIME DEFAULT CURRENT_TIMESTAMP` in every group DB, **no index** (`migrations/0001_single_db_schema.sql:26`).
- `GetActiveNewsgroups` (`database/queries.go:1028-1045`), `GetOverviewByMessageID` (`:1847`), `GetOverviewsRange`
  (`:1810`), `GetHeaderFieldRange` (`:1872`), `MainDBGetNewsgroup` (`:213`).

### Decisions (behavior changes; report them to the user at the end)
1. **D1 OVER/HDR are thin wrappers over the hardened XOVER/XHDR machinery.** They share the range parser (K-H1), the
   chunked DB readers, the overview formatter and sanitizer, and the data-block writer that hardening introduces; the
   only code of their own is the RFC 3977 reply-code selection (224/225, 423, 420, 430) and the message-id forms.
   `OVER MSGID` and `HDR` are advertised. The message-id form uses the same lookup path as `ARTICLE <message-id>`
   (current group first, then the processor/history, `local430`), with the same per-command throttle and error delay.
2. **D2 HDR field policy follows hardening's RFC-6 decision** (AUDIT §11 item 3). If hardened XHDR serves any header
   from `headers_json`, HDR does too and `LIST HEADERS` prints `:` plus `:bytes` and `:lines`. If hardened XHDR
   restricts to an indexed set, HDR answers `503` for any other field (RFC 3977 §8.5.2: "MUST respond with the generic
   503 response … rather than return erroneous results") and `LIST HEADERS` lists exactly that set. Metadata: only
   `:bytes` and `:lines` (other `:x` → 503). HDR `Bytes`/`Lines` (header names, no colon) are served the way hardened
   XHDR serves `bytes`/`lines` (documented deviation if those come from the metadata columns).
3. **D3 NEWNEWS is not implemented: it keeps answering 500 and is never advertised.** Reasons: `articles.imported_at`
   has no index in any group DB, so each call would full-scan every group DB the wildmat matches; `NEWNEWS * …` from an
   unauthenticated client would open thousands of group DBs per command (the same class of resource as SEC-2/A-nntp-sec-4
   on the attack-surface table); RFC 3977 §7.4 makes the capability optional, and modern readers use GROUP + OVER.
   `TestGrpNewNews` already asserts "500 and not advertised, or 230 and advertised" and keeps passing. No
   `nntp-cmd-newnews.go` is created; `handleNewNews` stays a reserved name. **Alternative (not chosen, a later plan):**
   an opt-in `-nntp-newnews` flag, authenticated users only, a migration adding `idx_articles_imported_at` to group DBs,
   a cap on matching groups with `503` above it, and `230` streaming per group.
4. **D4 NEWGROUPS** reads `newsgroups.created_at` of active groups, compares in UTC (`datetime(created_at) >=
   datetime(?)`), and answers in exactly the LIST ACTIVE line format of hardening (an equality test enforces it). A date
   without `GMT` is in the server's local time zone (`time.Local`, §7.3.2). 6-digit dates use the century rule of §7.3.2
   ("current century if yy is smaller than or equal to the current year, and the previous century otherwise"). A
   malformed date or time → 501. A future date → `231` with an empty list. **No truncating result cap:** RFC 3977 has no
   partial-list response and a truncated list makes clients miss groups permanently. The result is a subset of LIST
   ACTIVE (same table, same memory bound, no extra unauthenticated surface); rows are read into a slice before any
   network write, so no DB cursor is held while writing to a slow client.
5. **D5 Streaming.** `MODE STREAM` → `203` when `streamingAllowed()` (processor present and the connection is not an
   authenticated user without posting rights), `501` when there is no processor (§2.3.3 example: "501 Unknown MODE
   variant") or when an argument follows `STREAM`, `502` for an authenticated user without posting rights. MODE STREAM
   changes no state (§2.3.2). `STREAMING` is advertised iff `streamingAllowed()`, so "STREAMING listed iff MODE STREAM
   gives 203" (`TestSessCapabilities/processor-streaming-consistent`) holds in every state. `CHECK <id>` maps the same
   `CheckMessageID` results as IHAVE: `CasePass` → `238 <id>`, `CaseDupes` → `438 <id>`, `CaseRetry` → `431 <id>`,
   `CaseError` (temporary history trouble) → `431 <id>`; no processor → `500`; wrong argument count or a syntactically
   invalid message-id → `501`; not authenticated → `480`; authenticated without posting rights → `502` (the same gates and
   error delays as IHAVE at `nntp-cmd-posting.go:61-68`).
6. **D6 STARTTLS** (RFC 4642). Available iff `Server.NNTP.TLSCert` and `TLSKey` are set, the connection is not TLS and
   not authenticated (`starttlsAvailable()`); only then is `STARTTLS` advertised. `STARTTLS` → `382`, then the TLS
   handshake on the raw connection (30 s deadline, `MinVersion: tls.VersionTLS12`, same as the client
   `nntp-client.go:183`). `502` when TLS is already active, after authentication, or when no certificate is configured;
   `580` when the configured certificate can't be loaded; `501` with arguments. Never 480/483 (§2.2.1 NOTE). Bytes the
   client pipelined after `STARTTLS` are discarded unexecuted (§2.2.1 "MUST NOT be pipelined"; this is the defense
   against plaintext command injection into the TLS session). After a successful handshake the session is reset (§2.2.2:
   no greeting, not authenticated, no current group/article) and the connection counts as TLS for every later decision
   (CAPABILITIES, hardening's AUTHINFO-on-plaintext rule). A failed handshake closes the connection. The certificate is
   loaded once per (cert, key) path pair and cached for the process lifetime (rotation needs a restart, as for the TLS
   port today).
7. **D7 `cmd/nntp-server` uses the certificate for STARTTLS too:** `-nntpcertfile/-nntpkeyfile` are copied into the
   config whether or not `-nntptlsport` is set; giving only one of the two, or a certificate that doesn't load, is a
   startup error (`log.Fatalf`). Before, both were silently dropped without a TLS port.
8. **D8 Wildmat** follows RFC 3977 §4 exactly (`*`, `?`, comma-separated patterns, `!` negation, rightmost matching
   pattern decides, anchored, UTF-8 characters). A leading `!` on the first pattern is accepted as a §4.3 extension (it
   can only exclude). `\`, `[`, `]`, an empty pattern (`a,,b`, `a,`, `!`) or invalid UTF-8 make the wildmat invalid →
   `501`.
9. **D9 READER stays advertised.** With DATE/NEXT/LAST/NEWGROUPS implemented it becomes honest (the deferred question
   at the end of AUDIT §11).
10. **D10 Limits** (attack-surface parity with `audit-nntp-sec.md` §Unauthenticated attack surface): NEXT/LAST and the
    message-id forms of OVER/HDR use the same per-article-command throttle as ARTICLE/STAT; OVER/HDR ranges use the
    hardened chunked readers (no full-range load, clamped to the group's high mark, never a walk over unused numbers);
    LIST wildmat and NEWGROUPS cost at most one LIST ACTIVE; CHECK costs one history lookup like IHAVE; STARTTLS costs
    one handshake per connection (a second STARTTLS is 502). Every error reply goes through the hardened error-delay
    helper (`rateLimitOnError` at 2fb7741).
11. **D11** No new config keys, flags or migrations (D7 changes the behavior of existing flags only).

### Out of scope (list them in the Outcome)
- NEWNEWS (D3 alternative), LIST ACTIVE.TIMES / DISTRIB.PATS, the RFC 6048 LIST variants, AUTHINFO SASL, XPAT/XGTITLE.
- Turning hardening's opt-in `483` for plaintext AUTHINFO (AUDIT §11 item 7) into the default now that STARTTLS
  exists: a policy change for the operator, a later plan.
- HELP text (`handleHelp`, `nntp-cmd-basic.go`, a hardening file): not edited; the Outcome lists the missing lines.
  `internal/nntp/README.md` gets the new commands only if hardening left a command table there (wave 2 step A.4).
- An index on `newsgroups.created_at` (the table is small; NEWGROUPS scans it once).
- IMPLEMENTATION capability, MODE READER semantics, mode switching.

---

## Design

### What this plan needs from nntp-server-hardening (K-H; wave 0 verifies each and records the result)
Hardening is written in parallel. The task contract fixes K-H1, K-H2 and K-H4; the rest are behaviors hardening must
have for its own findings. Wave 0 records the exact names in `## Progress` ("contract record"); wave-1 slices read that
record and use the recorded names. If a **required** item is missing, the orchestrator stops and asks the user.

| Id | Item | Required? | Used by |
|----|----|----|----|
| K-H1 | `internal/nntp/nntp-cmd-range.go`: `type articleRange struct { First, Last int64; Open bool }` and `func (c *ClientConnection) parseRangeArg(arg string) (articleRange, error)`. Syntax error → 501 (record whether `parseRangeArg` sends the 501 itself or the caller does; record how `Open` is resolved: caller fills `Last` from the group's high mark, or the parser does) | required | `rfc-overview` |
| K-H2 | `func (c *ClientConnection) requireGroup() bool`: sends `412` (with the error delay) and returns false when no group is selected | required | `rfc-reader`, `rfc-overview` |
| K-H3 | Current-article semantics: GROUP and LISTGROUP-with-group select the group and set `c.currentArticle` to the low mark (0 for an empty group); ARTICLE/HEAD/BODY/STAT `<n>` set it (BUG-1); bare STAT uses it (RFC-3); `currentArticle == 0` means "invalid" (420) | required | `rfc-reader` |
| K-H4 | State-aware `getServerCapabilities` that builds a slice (record the variable name, READER's condition, and the line that appends `"LIST ACTIVE NEWSGROUPS"`) | required | wave 2 step A |
| K-H5 | Hardened overview pieces: (a) the overview line formatter (at 2fb7741 `formatOverviewLine(*models.Overview) string`, BUG-5 sanitizing) and whether it emits an `Xref` 9th field; (b) the XHDR field policy (RFC-6, §11 item 3) and the function that yields a sanitized value per article; (c) the chunked range readers for overview rows and header values (SEC-1/BUG-2), names and signatures; (d) the data-block writer hardened XOVER/XHDR use (dot-stuffing, one flush per response, A-nntp-rfc-2/10); (e) the source of the open-range end (`n-`); (f) whether `handleXOver`/`handleXHdr` delegate to a core that takes the reply codes as parameters | (a)–(e) required, (f) optional | `rfc-overview` |
| K-H6 | The per-article-command throttle in hardened `retrieveArticleCommon` (at 2fb7741 `if c.server.ArticleCmdDelay > 0 { time.Sleep(...) }`, `nntp-article-common.go:46-48`; BUG-3 may turn it into a token bucket) | required | `rfc-reader`, `rfc-overview` |
| K-H7 | The message-id lookup used by ARTICLE (at 2fb7741 `getArticleData(args, retrievalType) (*models.Article, int64)`, `nntp-article-common.go:91`): signature, whether it sends the 430 itself, and any message-id syntax validator in package `nntp` (hardening or intake) | required (validator optional) | `rfc-overview`, `rfc-stream` |
| K-H8 | How hardened handlers open the selected group's DB (an existence-checking helper, or `GetGroupDB(c.currentGroup)` after GROUP validated it) and the internal-fault code convention (403, A-nntp-rfc-4) | required | `rfc-reader`, `rfc-overview` |
| K-H9 | LIST ACTIVE/NEWSGROUPS: (a) the DB function hardened `handleListActive`/`handleListNewsgroups` read (at 2fb7741 `GetActiveNewsgroups`); (b) the per-group line expressions (high, low, status: BUG-4) or a formatter function; (c) whether those handlers already accept a name predicate | required ((c) optional) | `rfc-lists` |
| K-H10 | The article visibility predicate of hardened read queries (A-nntp-sec-2, §11 item 2: `hide`/`spam`), as an SQL fragment or constant in `internal/database/nntp_reads.go` | required if hardening filters | `rfc-reader` |
| K-H11 | Connection plumbing: how hardened `NewClientConnection` builds `textConn`, `writer` and any byte-limited reader (SEC-3); that `Handle` reads `c.textConn` (and any reader field) **on every loop iteration**, not from a local captured before the loop; the way a handler ends the connection without a reply (e.g. an `errQuit`-style sentinel from A-nntp-sec-1); whether another goroutine touches `c.conn` (e.g. `Stop()` closing live clients, BUG-7) and under which mutex; every per-session field hardening added to `ClientConnection`; any TLS-config builder (A-nntp-sec-5) | required | `rfc-starttls` |
| K-H12 | The greeting function name (at 2fb7741 `sendWelcome`), `UpdateDeadlines` (or its successor), and whether `handleCommand` keeps a known-command set (SEC-4) or an argument-count table (A-nntp-rfc-5) that must learn the new commands | required | wave-1 tests, wave 2 step A |

### Contracts
- **K1: stable names this plan adds (reserved; all free at 2fb7741, wave 0 re-checks at the merged base):**
  | Slice | Production names |
  |----|----|
  | `rfc-reader` | `handleDate`, `handleNext`, `handleLast`, `moveCurrentArticle`; DB `NNTPNextArticle`, `NNTPPrevArticle`, `query_NNTPNextArticle`, `query_NNTPPrevArticle` |
  | `rfc-lists` | `wildmatMatch`, `wildmatValid`, `wildmatSplit`, `wildmatGlob`, `handleListWildmat`, `activeLine`, `newsgroupsLine`, `handleNewGroups`, `parseNNTPDateTime`; DB `NNTPNewGroupsSince`, `query_NNTPNewGroupsSince` |
  | `rfc-overview` | `handleOver`, `handleHdr`, `handleListOverviewFmt`, `handleListHeaders`, `overviewFmtLines`, `listHeadersLines`, `hdrFieldValue`, `hdrMetadata`, `isMessageIDArg` |
  | `rfc-stream` | `handleModeStream`, `handleCheck`, `streamingAllowed`, `checkStreamMessageID` |
  | `rfc-starttls` | `handleStartTLS`, `starttlsAvailable`, `starttlsConfigFor`, `starttlsCache`, `starttlsEntry`, `starttlsHandshakeTimeout`, `resetSessionState` |
  | reserved, not created | `handleNewNews`, `parseNewNewsDate` (D3) |

  Signatures (the wave-2 dispatch calls exactly these):
  ```go
  func (c *ClientConnection) handleDate(args []string) error
  func (c *ClientConnection) handleNext(args []string) error
  func (c *ClientConnection) handleLast(args []string) error
  func (c *ClientConnection) handleNewGroups(args []string) error
  func (c *ClientConnection) handleListWildmat(keyword string, args []string) error // keyword "ACTIVE" | "NEWSGROUPS", args = the words after it
  func (c *ClientConnection) handleOver(args []string) error
  func (c *ClientConnection) handleHdr(args []string) error
  func (c *ClientConnection) handleListOverviewFmt(args []string) error           // args after "OVERVIEW.FMT"
  func (c *ClientConnection) handleListHeaders(args []string) error               // args after "HEADERS"
  func (c *ClientConnection) handleModeStream(args []string) error                // args after "STREAM"
  func (c *ClientConnection) handleCheck(args []string) error
  func (c *ClientConnection) handleStartTLS(args []string) error
  func (c *ClientConnection) streamingAllowed() bool
  func (c *ClientConnection) starttlsAvailable() bool
  func wildmatMatch(pattern, name string) bool // caller has checked wildmatValid(pattern)
  func wildmatValid(pattern string) bool
  func (db *Database) NNTPNextArticle(groupDB *GroupDB, cur int64) (num int64, messageID string, err error) // sql.ErrNoRows when none
  func (db *Database) NNTPPrevArticle(groupDB *GroupDB, cur int64) (num int64, messageID string, err error) // sql.ErrNoRows when none
  func (db *Database) NNTPNewGroupsSince(since time.Time) ([]*models.Newsgroup, error)
  ```
- **K2: no struct changes.** No slice adds fields to `ClientConnection` or `NNTPServer` (both live in hardening's files).
  State that needs to survive is either a package-level cache (`starttlsCache`) or existing fields (`isTLS`, `conn`,
  `textConn`, `writer`, `authenticated`, `user`, `authUsername`, `currentGroup`, `currentFirst`, `currentLast`,
  `currentArticle`).
- **K3: tests.**
  - Prefixes (free at 2fb7741; wave 0 re-checks): `rdr`/`TestRdr` (`rfc-reader`), `lst`/`TestLst`/`FuzzLst`
    (`rfc-lists`), `ovh`/`TestOvh` (`rfc-overview`), `stm`/`TestStm` (`rfc-stream`), `stls`/`TestStls`
    (`rfc-starttls`), `rfc`/`TestRfc` (`rfc-wire`, wave 2). The same prefixes are used in `internal/database` test
    files of the slice. In `cmd/nntp-server` the wave-2 e2e file uses `e2eRfc`/`TestE2EBinaryRFC`.
  - Groups are named `rc<N>.<test>` through `hGroup` (`N`: 1 `rfc-reader`, 2 `rfc-lists`, 3 `rfc-overview`, 4
    `rfc-stream`, 5 `rfc-starttls`, 6 `rfc-wire`), message-ids via `hMessageID`, users via `hUser`.
  - The harness (`nntp-server-harness_test.go`) is read-only. No `t.Parallel()`, no package global mutated after
    `TestMain`, no sleep over 100 ms (poll with a deadline), as in `nntp-audit-tests` K3.
  - **Wave-1 slices can't use the real dispatch** (it is edited only in wave 2), so each wave-1 test file defines a
    test-local session that serves one connection of a harness server and routes the slice's commands to the new
    handlers and everything else to the hardened `handleCommand`. Copy it with the slice prefix (`rdrSession`,
    `lstSession`, …; K5 forbids sharing it across slices); replace `sendWelcome`/`UpdateDeadlines` by the names in the
    K-H12 record:
    ```go
    // <p>Session serves one connection of hs.S: commands in extra go to the new
    // handlers, everything else to the hardened dispatch (handleCommand).
    func <p>Session(t *testing.T, hs *hServer, extra map[string]func(cc *ClientConnection, args []string) error) *hClient {
    	t.Helper()
    	l, err := net.Listen("tcp", "127.0.0.1:0")
    	if err != nil {
    		t.Fatal(err)
    	}
    	done := make(chan struct{})
    	go func() {
    		defer close(done)
    		conn, err := l.Accept()
    		_ = l.Close()
    		if err != nil {
    			return
    		}
    		defer conn.Close()
    		cc := NewClientConnection(conn, hs.S, false)
    		cc.UpdateDeadlines()
    		if cc.sendWelcome() != nil {
    			return
    		}
    		for {
    			line, err := cc.textConn.ReadLine() // re-read the field: STARTTLS replaces it
    			if err != nil {
    				return
    			}
    			cc.UpdateDeadlines()
    			if f := strings.Fields(line); len(f) > 0 {
    				if h, ok := extra[strings.ToUpper(f[0])]; ok {
    					if h(cc, f[1:]) != nil {
    						return
    					}
    					continue
    				}
    			}
    			if cc.handleCommand(line) != nil {
    				return
    			}
    		}
    	}()
    	conn, err := net.DialTimeout("tcp", l.Addr().String(), 5*time.Second)
    	if err != nil {
    		t.Fatal(err)
    	}
    	c := &hClient{Conn: conn, R: bufio.NewReaderSize(conn, 1<<20), Deadline: 5 * time.Second}
    	t.Cleanup(func() { _ = conn.Close(); <-done })
    	if c.Greeting, err = c.readLine(); err != nil {
    		t.Fatalf("greeting: %v", err)
    	}
    	return c
    }
    ```
  - Wave 2 removes every `knownBug(t, "RFC-1", …)` and keeps the assertion (`if err != nil { t.Fatal(err) }`).
- **K4:** nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt`, migrations, `internal/config`, the
  harness, or files outside their ownership list. `gofmt` runs only on owned files (the 2-file baseline
  `internal/database/embedded_migrations.go`, `internal/web/web_admin_provider.go` stays, plus whatever hardening's
  Outcome lists).
- **K5:** no slice calls a helper another slice of the same wave adds (e.g. `rfc-overview` doesn't call `wildmatValid`;
  `rfc-starttls` doesn't call `streamingAllowed`).
- **K6: ids.** RFC-1 stays canonical. Bugs a slice finds that aren't RFC-1 get `T-rfc-commands-<n>` and are listed in
  its report (not fixed unless they are in the slice's files and the fix is trivial).

### Component design (exact behavior per command)
Common rules for every new handler:
- Status lines never echo client input longer than 64 bytes (A-nntp-rfc-6): use fixed texts.
- Internal/DB faults → `403` (A-nntp-rfc-4 convention, K-H8). A DB error **after** a multi-line status line was sent
  ends the connection (return the error); never write a second status line inside a data block.
- Multi-line blocks go through the hardened data-block writer (K-H5d), so dot-stuffing and the terminator are shared.
- 4xx/5xx replies call the hardened error-delay helper (`rateLimitOnError` at 2fb7741) **except** `CHECK` replies
  `431`/`438` (normal streaming answers, never delayed).
- Surplus or missing arguments → `501` (A-nntp-rfc-5) with the fixed texts below.

| Command | Arguments | Replies (in check order) | Data block |
|----|----|----|----|
| `DATE` | none (else `501 DATE takes no arguments`) | `111 <time.Now().UTC().Format("20060102150405")>` | — |
| `NEXT` | none (else 501) | `412` via `requireGroup`; `420 Current article number is invalid` when `currentArticle == 0`; throttle (K-H6); open group DB (K-H8, fault → 403); `NNTPNextArticle(cur)`: `sql.ErrNoRows` → `421 No next article in this group`; other error → 403; else set `currentArticle = n` and reply `223 n <message-id> Article found` | — |
| `LAST` | none (else 501) | as NEXT with `NNTPPrevArticle`; none → `422 No previous article in this group`. On 412/420/421/422/403 the current group and article are unchanged (§6.1.3.2) | — |
| `NEWGROUPS` | `date time [GMT]` (2 or 3 words; 3rd must be `GMT`, case-insensitive; else 501) | `parseNNTPDateTime` error → `501 Invalid date or time`; `NNTPNewGroupsSince` error → 403; else `231 List of new newsgroups follows` | one `activeLine(g)` per group, ordered by name |
| `LIST ACTIVE <wildmat>` / `LIST NEWSGROUPS <wildmat>` | exactly one wildmat (else 501) | `!wildmatValid` → `501 Invalid wildmat`; list source (K-H9a) error → 403; else `215 List of newsgroups follows` (same text as hardened handleList) | `activeLine(g)` / `newsgroupsLine(g)` for every listed group with `wildmatMatch(p, g.Name)`, same order and same active filter as hardened LIST |
| `LIST OVERVIEW.FMT` | none (else 501) | `215 Order of fields in overview database` | `Subject:`, `From:`, `Date:`, `Message-ID:`, `References:`, `:bytes`, `:lines`, then `Xref:full` iff the K-H5a formatter emits Xref |
| `LIST HEADERS [MSGID\|RANGE]` | 0 or 1 word; the word must be `MSGID` or `RANGE` (case-insensitive) else 501; the forms are treated alike (§8.6.2: "MUST ignore any argument") | `215 Headers and metadata items supported` | D2: `:`, `:bytes`, `:lines` (any-header policy) or the indexed header names without colon plus `:bytes`, `:lines` |
| `OVER <message-id>` | `<…>` | throttle (K-H6); lookup (K-H7, it sends `430` on a miss); `224 Overview information follows` | one formatter line built from the article with number = the K-H7 reply number (0 outside the current group, §8.3.2) |
| `OVER [range]` | 0 or 1 word (else 501) | `412` via `requireGroup`; no arg: `420` when `currentArticle == 0`; range: `parseRangeArg` error → 501, reversed → `423 No articles in that range`; open group DB (fault → 403); rows through the chunked reader (K-H5c) with a **lazy status line**: `224` is written before the first row; no row at all → `423` (range) or `420` (no-arg form, the current article no longer exists) | formatter lines, ascending |
| `HDR field <message-id>` | field + `<…>` | field check (below) → 503; throttle; lookup (K-H7, 430 on a miss); `225 Headers follow` | one `"<n> <value>"` line, n = K-H7 reply number |
| `HDR field [range]` | field + 0/1 word (else 501; no field → 501) | field check → 503; then as OVER with `225` and the header reader (K-H5b/c) | `"<n> <value>"` per existing article (empty value keeps `"<n> "`, §8.5.2) |
| `MODE STREAM` | nothing after STREAM (else 501) | no processor → `501 MODE STREAM not supported`; `!streamingAllowed()` → `502 Streaming not permitted`; else `203 Streaming permitted` | — |
| `CHECK <id>` | exactly 1 (else 501) | no processor → `500 Command not recognized: CHECK`; `!checkStreamMessageID(id)` → `501 Invalid message-id`; not authenticated → `480 Transfer permission denied (authentication required)`; user without posting → `502 Transfer not permitted for this user`; then `CheckMessageID`: Pass `238 <id>`, Dupes `438 <id>`, Retry `431 <id>`, Error `431 <id>` (log `[NNTP] CHECK history error for <id>`) | — |
| `STARTTLS` | none (else 501) | TLS active → `502 STARTTLS not allowed with active TLS layer`; authenticated → `502 STARTTLS not allowed after authentication`; no cert → `502 STARTTLS not available`; cert load error → `580 Can not initiate TLS negotiation` (log); discard buffered input; `382 Continue with TLS negotiation`; handshake | — |

HDR field check (D2): `f := strings.ToLower(field)`. `:bytes` → value source "bytes", `:lines` → "lines"; any other
`:x` → `503 Metadata item not supported`. A header name: under the restricted policy, not in the K-H5b set → `503
Header not supported`; otherwise pass the name to the hardened reader exactly as XHDR would.

### Hand-over to the orchestrator (wave 2 step A applies these inline in `nntp-server-cliconns.go`)
**Dispatch** (`handleCommand` switch, `:119-156` at 2fb7741). Replace the `MODE` and `LIST` cases and add the rest
before `default:`; `strings` is already imported:
```go
	case "MODE":
		if len(args) > 0 && strings.EqualFold(args[0], "STREAM") {
			return c.handleModeStream(args[1:])
		}
		return c.handleMode(args)
	case "LIST":
		if len(args) > 0 {
			switch kw := strings.ToUpper(args[0]); kw {
			case "OVERVIEW.FMT":
				return c.handleListOverviewFmt(args[1:])
			case "HEADERS":
				return c.handleListHeaders(args[1:])
			case "ACTIVE", "NEWSGROUPS":
				if len(args) > 1 {
					return c.handleListWildmat(kw, args[1:])
				}
			}
		}
		return c.handleList(args)
	case "DATE":
		return c.handleDate(args)
	case "NEXT":
		return c.handleNext(args)
	case "LAST":
		return c.handleLast(args)
	case "NEWGROUPS":
		return c.handleNewGroups(args)
	case "OVER":
		return c.handleOver(args)
	case "HDR":
		return c.handleHdr(args)
	case "CHECK":
		return c.handleCheck(args)
	case "STARTTLS":
		return c.handleStartTLS(args)
	// NEWNEWS stays unknown (500): decision D3 of nntp-rfc-commands.
```
If hardening's `MODE`/`LIST` cases call different names, keep hardening's calls in the fall-through lines and only add
the routing above them. If `handleCommand` has a known-command set (SEC-4), add `DATE`, `NEXT`, `LAST`, `NEWGROUPS`,
`OVER`, `HDR`, `CHECK`, `STARTTLS` to it. If it has an argument-count table (A-nntp-rfc-5), add rows (min, max):
`DATE 0 0`, `NEXT 0 0`, `LAST 0 0`, `NEWGROUPS 2 3`, `OVER 0 1`, `HDR 1 2`, `CHECK 1 1`, `STARTTLS 0 0` (the
handlers also check, so the table is optional).

**CAPABILITIES** (hardened `getServerCapabilities`, K-H4; `caps` = the recorded slice variable). Replace hardening's
`"LIST ACTIVE NEWSGROUPS"` entry and append the rest after it; emit the first two under READER's condition (every state
unless hardening gates reading):
```go
	caps = append(caps, "LIST ACTIVE NEWSGROUPS OVERVIEW.FMT HEADERS") // replaces "LIST ACTIVE NEWSGROUPS"
	caps = append(caps, "OVER MSGID", "HDR")
	if c.streamingAllowed() { // processor present, not an authenticated user without posting (RFC 4644 §2.2)
		caps = append(caps, "STREAMING")
	}
	if c.starttlsAvailable() { // cert configured, plaintext, before AUTHINFO (RFC 4642 §2.2.2)
		caps = append(caps, "STARTTLS")
	}
	// DATE, NEXT, LAST and NEWGROUPS are part of READER. NEWNEWS is not advertised (D3).
```
Expected lists afterwards (VERSION 2 first; hardening decides everything not shown):
| State | Added by this plan | Never |
|----|----|----|
| plaintext, before auth, processor, cert set | LIST … OVERVIEW.FMT HEADERS, OVER MSGID, HDR, STREAMING, STARTTLS | NEWNEWS, TAKETHIS |
| plaintext, before auth, no cert | LIST … OVERVIEW.FMT HEADERS, OVER MSGID, HDR, STREAMING (with processor) | STARTTLS |
| after AUTHINFO, posting user | LIST …, OVER MSGID, HDR, STREAMING | STARTTLS |
| after AUTHINFO, user without posting | LIST …, OVER MSGID, HDR | STREAMING, STARTTLS |
| no processor | LIST …, OVER MSGID, HDR | STREAMING |
| TLS port, or after STARTTLS | LIST …, OVER MSGID, HDR, STREAMING (as above) | STARTTLS |

---

## Waves

### Wave 0 (inline, orchestrator on `plan-nntp-rfc-commands`)
Owned files: this plan file (the `## Progress` contract record) only.

1. **Preflight** as in the skill. `git mv .claude/plans/queued/nntp-rfc-commands.md .claude/plans/wip/`.
2. **P0 dependency:** `.claude/plans/done/nntp-server-hardening.md` exists, `git log --oneline testing-001 | grep -i
   hardening` shows its merge, and `internal/nntp/nntp-cmd-range.go` exists. Otherwise stop: this plan must not run
   first.
3. **Contract record.** For each K-H1…K-H12 item, read the hardened code and write one line into `## Progress`
   ("Wave 0 contract record") with the exact function/variable names, signatures, and the yes/no answers asked in the
   K-H table. Commands that help:
   ```bash
   sed -n '1,80p' internal/nntp/nntp-cmd-range.go
   grep -n 'func (c \*ClientConnection) \(requireGroup\|getServerCapabilities\|sendWelcome\|UpdateDeadlines\|handleCommand\|handleList\|handleListActive\|handleListNewsgroups\|handleXOver\|handleXHdr\|formatOverviewLine\|getArticleData\|retrieveArticleCommon\)' internal/nntp/*.go
   grep -n 'GetOverviewsRange\|GetHeaderFieldRange\|GetActiveNewsgroups\|hide' internal/database/nntp_*.go internal/database/queries.go | head -40
   grep -n 'c\.conn\b' internal/nntp/nntp-server.go internal/nntp/nntp-server-cliconns.go
   grep -n 'tls.Config' internal/nntp/nntp-server.go
   go test -count=1 -run 'TestGrpGroupSeeded|TestArtNoArgument|TestArtCurrentArticle' ./internal/nntp/   # K-H3: must pass with no knownBug skips
   ```
   Also record the TAKETHIS reply when unauthenticated (unchanged by this plan) and whether hardened `handleMode`
   answers `MODE STREAM` with 501 (expected; the dispatch intercepts STREAM in wave 2).
4. **Names free:** `for n in <every K1 name>; do grep -rnw "$n" --include=*.go internal cmd; done` prints nothing;
   `grep -rnoE '\b(rdr|lst|ovh|stm|stls|rfc|e2eRfc)[A-Z]' --include=*_test.go internal cmd` prints nothing. If hardening
   took a name, rename ours with a `rfc` infix and record the rename.
5. **Baseline** `## Checks` on the untouched branch; record PASS/FAIL and the `PUGLEAF_KNOWN_BUGS=1` id list (RFC-1 must
   be in it; anything else is hardening's leftovers).
6. Commit on `plan-nntp-rfc-commands`: `plan(nntp-rfc-commands): wave-0 contract record`. Record the base SHA.

### Wave 1 (5 parallel `pugleaf-implementer` slices)

Common rules for every wave-1 slice:
- Read the Context rows for your labels, the Design (component table and K-H record in `## Progress`), and your owned
  files' neighbors before editing. Follow K1–K6.
- Don't touch `nntp-server-cliconns.go`: the dispatch comes in wave 2. Test through `<p>Session` (K3).
- Checks (in your worktree):
  ```bash
  gofmt -l internal/nntp internal/database cmd/nntp-server   # none of your owned files listed
  go vet ./...
  go build ./...
  go test -race -count=3 -timeout 600s -run '<YourPrefix>' ./internal/nntp/ ./internal/database/
  go test -race -count=1 -timeout 900s ./internal/nntp/... ./internal/database/... ./cmd/nntp-server/...
  git status --short                                         # clean
  ```
- **Reviewer task:** each reply code against the component table and the RFC section; no write after a status line on a
  DB error; error delays; `defer rows.Close()`/`groupDB.Return()`; no data race (`-race -count=3`); no edit outside the
  ownership list.

---

#### Slice `rfc-reader`: DATE, NEXT, LAST
Labels: RFC-1a, RFC-1b, RFC-1c. E-tag `rfc-reader`.

Owned files (all new):
- `internal/nntp/nntp-cmd-date.go`
- `internal/nntp/nntp-cmd-nextlast.go`
- `internal/database/nntp_nextlast.go`
- `internal/nntp/nntp-rfc-reader_test.go`
- `internal/database/nntp_nextlast_test.go`

Changes:
1. **`nntp-cmd-date.go`:** `handleDate(args)`: `len(args) != 0` → error delay + `501 DATE takes no arguments`; else
   `c.sendResponse(111, time.Now().UTC().Format("20060102150405"))`. The doc comment cites RFC 3977 §7.1.2 ("MUST
   return a timestamp from the same clock as is used for determining article arrival and group creation times"): the
   DB defaults (`CURRENT_TIMESTAMP`) are UTC from the same host clock.
2. **`internal/database/nntp_nextlast.go`** (package `database`):
   ```go
   const query_NNTPNextArticle = `SELECT article_num, message_id FROM articles WHERE article_num > ?` + <K-H10 predicate, if any> + ` ORDER BY article_num ASC LIMIT 1`
   const query_NNTPPrevArticle = `SELECT article_num, message_id FROM articles WHERE article_num < ?` + <K-H10 predicate, if any> + ` ORDER BY article_num DESC LIMIT 1`
   ```
   Both use `RetryableQueryRowScan(groupDB.DB, q, []interface{}{cur}, &num, &msgID)` and return its error unchanged
   (`sql.ErrNoRows` when none). `article_num` is the rowid primary key, so each call is one index seek. Reuse the
   K-H10 constant by name when hardening defined one (same package); otherwise copy its WHERE term literally and cite
   the hardened function in a comment. No logging per call.
3. **`nntp-cmd-nextlast.go`:** `handleNext` and `handleLast` both call
   `moveCurrentArticle(args []string, next bool) error`:
   1. `len(args) != 0` → error delay + `501 NEXT/LAST take no arguments`.
   2. `if !c.requireGroup() { return nil }` (K-H2, 412).
   3. `c.currentArticle == 0` → error delay + `420 Current article number is invalid`.
   4. The K-H6 throttle (the same call as hardened `retrieveArticleCommon`).
   5. Open the selected group's DB as recorded in K-H8; on error → error delay + `403 Internal fault`; `defer
      groupDB.Return()`.
   6. `NNTPNextArticle`/`NNTPPrevArticle(groupDB, c.currentArticle)`. `errors.Is(err, sql.ErrNoRows)` → error delay +
      `421 No next article in this group` / `422 No previous article in this group`; other error → log `[NNTP]
      NEXT/LAST in <group>: <err>` + error delay + `403 Internal fault`. Leave `currentArticle` unchanged on every
      error path (§6.1.3.2 "MUST NOT be altered").
   7. Success: `c.currentArticle = num`; `c.sendResponse(223, fmt.Sprintf("%d %s Article found", num, msgID))`.
4. **Tests** `internal/nntp/nntp-rfc-reader_test.go` (prefix `rdr`; `rdrSession` with extra `DATE`, `NEXT`, `LAST`):
   - `TestRdrDate`: `111` + 14 digits, parses with `time.Parse("20060102150405", …)`, within 5 min of
     `time.Now().UTC()`; `DATE x` → 501.
   - `TestRdrNextLast` on `hGroup(t, "rc1.nextlast", 6, …)` with 1, 2, 5 skipped (articles 3, 4, 6):
     - no group: NEXT → 412, LAST → 412;
     - after GROUP (current = 3): LAST → 422, NEXT → `223 4 <id4>`, NEXT → `223 6 <id6>`, NEXT → 421, LAST →
       `223 4 <id4>`, bare STAT → `223 4` (K-H3), `NEXT x` → 501;
     - after the failing NEXT (421) at 6, STAT still answers `223 6` (unchanged current article);
     - empty group (`hGroup(t, "rc1.empty", 0, nil)`): NEXT → 420, LAST → 420;
     - `ARTICLE <id of 6>` (message-id form) after `STAT 3` leaves the current article at 3: NEXT → `223 4`.
   - `TestRdrNextLastHidden` (only when K-H10 filters): mark article 4 hidden with raw SQL on `GetGroupDB(g).DB`
     (`UPDATE articles SET hide = 1 WHERE article_num = 4`); from 3 NEXT → `223 6`, from 6 LAST → `223 3`.
   - `internal/database/nntp_nextlast_test.go` (prefix `rdr`, uses `w0DB`, `w0Name`): a group DB from `GetGroupDB(w0Name("rdr.next"))`
     with articles 10, 20, 30 inserted by raw SQL (all NOT NULL columns as in `nntp-audit-tests` D2): next of 10 → 20,
     next of 30 → `sql.ErrNoRows`, prev of 10 → `sql.ErrNoRows`, prev of 25 → 20, next of 0 → 10.

Acceptance: E01, E02 (through `rdrSession`; wave 2 re-checks through the dispatch).

---

#### Slice `rfc-lists`: wildmat, LIST ACTIVE/NEWSGROUPS wildmat, NEWGROUPS
Labels: RFC-1d, RFC-1e, RFC-1f. E-tag `rfc-lists`.

Owned files (all new):
- `internal/nntp/nntp-wildmat.go`
- `internal/nntp/nntp-wildmat_test.go`
- `internal/nntp/nntp-cmd-list-wildmat.go`
- `internal/nntp/nntp-cmd-newgroups.go`
- `internal/database/nntp_newgroups.go`
- `internal/nntp/nntp-rfc-lists_test.go`
- `internal/database/nntp_newgroups_test.go`

Changes:
1. **`nntp-wildmat.go`** (RFC 3977 §4; grammar in the doc comment:
   `wildmat = wildmat-pattern *("," ["!"] wildmat-pattern)`, `wildmat-wild = "*" / "?"`, exact characters exclude
   `! * , ? [ \ ]`):
   - `wildmatSplit(pattern string) (pats []string, neg []bool, ok bool)`: split on `,`; an element may start with one
     `!` (the first element too, D8), which sets `neg`; after that optional `!` the element must be non-empty and must
     not contain `!`, `\`, `[` or `]`; `ok=false` on any violation or when the pattern is not valid UTF-8
     (`utf8.ValidString`).
   - `wildmatValid(pattern string) bool`: `pattern != ""` and `wildmatSplit` ok.
   - `wildmatGlob(pat, name string) bool`: anchored match over **runes** (`[]rune` of both), `?` = exactly one rune,
     `*` = zero or more runes, everything else literal. Use the iterative star-backtracking algorithm (remember the
     last `*` position and the name position it matched; on mismatch retry from there), which is O(len(pat)·len(name))
     and never recursive, so a hostile pattern can't blow the stack or go exponential.
   - `wildmatMatch(pattern, name string) bool`: walk the elements **from the right**; the first (rightmost) element
     whose `wildmatGlob` matches decides: matched and not negated → true, negated → false; none matches → false
     (§4.2). Invalid patterns return false (callers check `wildmatValid` first).
2. **`nntp-wildmat_test.go`** (prefix `lst`):
   - `TestLstWildmatRFCExamples`: the §4.2/§4.4 table: `a*,!*b,*c*` with `aaa`→true, `abb`→false, `ccb`→true,
     `xxx`→false; `abc` matches only `abc`; `abc,def` matches `abc` and `def`; `"£"` matches `"£"` (2 octets, one
     character) and `?` matches `"£"`; `a*` / `a*b` / `a*,*b` / `a*,!*b` / `a*,!*b,c*` per §4.4; `*` matches `""`;
     `t3.*` matches `t3.x.y` but not `t3`.
   - `TestLstWildmatInvalid`: `""`, `a,,b`, `a,`, `,a`, `!`, `a!b`, `a\b`, `[ab]`, `a]`, `"\xff"` → `wildmatValid`
     false; `!a*` → valid, matches nothing.
   - `FuzzLstWildmat` (seed corpus only during `go test`): for valid patterns, `wildmatMatch` equals a reference built
     per element with `regexp.MustCompile("(?s)^" + <QuoteMeta with \* → .* and \? → .> + "$")` over the UTF-8 name, using
     the rightmost-match rule; never panics; finishes within 1 s for 512-byte inputs.
3. **`internal/database/nntp_newgroups.go`:**
   ```go
   const query_NNTPNewGroupsSince = `SELECT name, COALESCE(description,''), COALESCE(last_article,0), COALESCE(message_count,0),
   	COALESCE(high_water,0), COALESCE(low_water,1), COALESCE(status,'y'), active
   	FROM newsgroups
   	WHERE active = 1 AND created_at IS NOT NULL AND datetime(created_at) >= datetime(?)
   	ORDER BY name`
   ```
   `NNTPNewGroupsSince(since time.Time)` passes `since.UTC().Format("2006-01-02 15:04:05")`, uses
   `RetryableQuery(db.GetMainDB(), …)` (or the main-DB handle the other `queries.go` readers use), `defer rows.Close()`,
   scans into `models.Newsgroup{Name, Description, LastArticle, MessageCount, HighWater, LowWater, Status, Active}`,
   checks `rows.Err()`. If the K-H9b line format needs a column not selected here, add it (COALESCE'd). `datetime()`
   normalizes both the `CURRENT_TIMESTAMP` form and Go's `2006-01-02 15:04:05.999999999-07:00` form, so a row written
   by another tool with a Go `time.Time` still compares in UTC.
4. **`nntp-cmd-list-wildmat.go`:**
   - `activeLine(g *models.Newsgroup) string` and `newsgroupsLine(g *models.Newsgroup) string`: if K-H9b recorded a
     hardened per-group formatter, return its result; otherwise reproduce the hardened expressions **verbatim** (high,
     low, status for ACTIVE; `name + " " + description` or the hardened variant for NEWSGROUPS) with a comment naming the
     hardened function they mirror. `TestLstMatchesList` (below) fails if they drift.
   - `handleListWildmat(keyword string, args []string) error`: `len(args) != 1` → error delay + `501 Syntax: LIST
     ACTIVE|NEWSGROUPS [wildmat]`; `!wildmatValid(args[0])` → error delay + `501 Invalid wildmat`. If K-H9c says the
     hardened handlers take a name predicate, call them with `func(n string) bool { return wildmatMatch(p, n) }` and
     return. Otherwise read the K-H9a source (DB error → error delay + 403), send the hardened 215 status text, write
     `activeLine`/`newsgroupsLine` for every group the hardened handler would list (same active filter, same order) that
     matches, through the data-block writer (K-H5d), and terminate the block.
5. **`nntp-cmd-newgroups.go`:**
   - `parseNNTPDateTime(date, clock string, gmt bool, now time.Time) (time.Time, error)`:
     - `date` is 6 or 8 ASCII digits, `clock` exactly 6; anything else → error.
     - 8 digits: year `yyyy` in 1900..9999 (§7.3.2 "xx … (19-99)"). 6 digits: `yy`; `cy := now.UTC().Year()`; year =
       `cy - cy%100 + yy` if `yy <= cy%100`, else that minus 100.
     - month 01-12, day 01-31, hour 00-23, minute 00-59, second 00-60 (60 → 59, leap second).
     - `loc := time.UTC` if gmt else `time.Local`; `t := time.Date(y, m, d, hh, mi, ss, 0, loc)`; reject when
       `t.Day() != d` or `t.Month() != m` (e.g. 20260231).
   - `handleNewGroups(args []string) error`: `len(args)` must be 2 or 3 and `args[2]` (if present) `GMT`
     (`strings.EqualFold`), else error delay + `501 Syntax: NEWGROUPS date time [GMT]`; parse with `now = time.Now()`
     (error → error delay + `501 Invalid date or time`); `NNTPNewGroupsSince(t)` (error → log `[NNTP]` + error delay +
     403); `231 List of new newsgroups follows`; one `activeLine(g)` per row through the data-block writer; terminator.
6. **Tests** `internal/nntp/nntp-rfc-lists_test.go` (prefix `lst`; `lstSession` with extra `NEWGROUPS` and
   `LIST` → a wrapper that routes `ACTIVE|NEWSGROUPS <wildmat>` to `handleListWildmat` and everything else to
   `cc.handleList(args)`):
   - `TestLstListWildmat`: groups `rc2.lw.<tok>.a`, `.b`, `rc2.other.<tok>`; `LIST ACTIVE rc2.lw.<tok>.*` lists a and b
     only; `LIST NEWSGROUPS rc2.lw.<tok>.a` lists only a with description `harness group <a>`;
     `LIST ACTIVE rc2.lw.<tok>.*,!*.b` lists only a; `LIST ACTIVE a,,b` → 501; `LIST ACTIVE x y` → 501.
   - `TestLstMatchesList`: for a seeded group g, the line of g in `LIST ACTIVE <g>` equals its line in plain `LIST
     ACTIVE`; the same for NEWSGROUPS; `LIST ACTIVE *` returns exactly the lines of `LIST ACTIVE` (same order).
   - `TestLstNewGroups`: g = `hGroup(t, "rc2.ng", 2, nil)`; `NEWGROUPS <now-48h as yyyymmdd hhmmss> GMT` → 231 and lists
     g with fields `g 2 1 <status>`; that line equals g's `LIST ACTIVE` line; `NEWGROUPS 20991231 235959 GMT` → 231
     empty; 6-digit form `NEWGROUPS <yymmdd of now-48h> <hhmmss> GMT` lists g; `NEWGROUPS 20261301 000000 GMT` → 501;
     `NEWGROUPS 20260101 000000 UTC` → 501; `NEWGROUPS 20260101` → 501.
   - `TestLstParseNNTPDateTime`: table incl. the century rule with `now` = 2026-10-11 (`261011` → 2026, `270101` →
     1927, `991231` → 1999, `000101` → 2000), `20260230` → error, `235960` → second 59, `gmt=false` uses `time.Local`.
   - `internal/database/nntp_newgroups_test.go` (prefix `lst`): insert two groups with
     `InsertNewsgroup`, set one's `created_at` to `'2001-02-03 04:05:06'` and the other's to the Go format
     `'2001-02-03 05:05:06.123456789+01:00'` by raw SQL on the main DB, then a third inactive one; `NNTPNewGroupsSince(2001-02-03 04:05:00 UTC)`
     returns the first and the second (04:05:06 UTC) but not the inactive; since 04:05:07 UTC returns neither of them;
     NULL `last_article` scans as 0.

Acceptance: E03, E04 (through `lstSession`).

---

#### Slice `rfc-overview`: OVER, HDR, LIST OVERVIEW.FMT, LIST HEADERS
Labels: RFC-1g, RFC-1h, RFC-1i, RFC-1j. E-tag `rfc-overview`.

Owned files (all new):
- `internal/nntp/nntp-cmd-over-hdr.go`
- `internal/nntp/nntp-rfc-overview_test.go`

Changes (`nntp-cmd-over-hdr.go`):
1. `isMessageIDArg(s string) bool`: `len(s) >= 3 && s[0] == '<' && s[len(s)-1] == '>'` (the same test
   `getArticleData` uses at `nntp-article-common.go:101`).
2. **Variant choice** from the K-H5f record: if hardened `handleXOver`/`handleXHdr` delegate to a core that takes the
   reply codes (OK, empty-range, no-current-article), `handleOver`/`handleHdr` call that core with `224`/`225`, `423`,
   `420` after the argument handling of steps 3–4 and skip step 5. Otherwise implement step 5.
3. `handleOver(args)`:
   1. `len(args) > 1` → error delay + `501 Syntax: OVER [range|message-id]`.
   2. Message-id form: throttle (K-H6); `a, num := <K-H7 lookup>([]string{args[0]}, RetrievalHead)`; nil → return
      nil (the lookup sent 430); build `&models.Overview{ArticleNum: num, Subject: a.Subject, FromHeader: a.FromHeader,
      DateString: a.DateString, MessageID: a.MessageID, References: a.References, Bytes: a.Bytes, Lines: a.Lines}`
      (plus whatever the K-H5a formatter reads for Xref); send `224 Overview information follows`, the formatted line,
      the terminator.
   3. Otherwise `if !c.requireGroup() { return nil }`; no argument: `c.currentArticle == 0` → error delay + `420
      Current article number is invalid`, else `r = articleRange{First: cur, Last: cur}`, `emptyCode = 420`; with an
      argument: `r, err := c.parseRangeArg(args[0])` (error → 501 per K-H1), resolve `Open` per K-H1/K-H5e, `r.First >
      r.Last` → error delay + `423 No articles in that range`; `emptyCode = 423`.
   4. Step 5 with the overview reader and formatter, status `224 Overview information follows`.
4. `handleHdr(args)`:
   1. `len(args) == 0 || len(args) > 2` → error delay + `501 Syntax: HDR field [range|message-id]`.
   2. Field check (Design, D2) → `503`.
   3. Message-id form (`len(args) == 2 && isMessageIDArg(args[1])`): throttle; lookup; `225 Headers follow`; one line
      `fmt.Sprintf("%d %s", num, <K-H5b sanitizer>(hdrFieldValue(a, field)))`; terminator.
   4. Range/current forms as OVER step 3 (argument `args[1]`), then step 5 with the header reader and `225 Headers
      follow`.
5. **Shared streaming loop** (only when there is no hardened core): open the group DB (K-H8; fault → error delay +
   403); `defer groupDB.Return()`; `sent := false`; read rows with the K-H5c chunked reader from `r.First` to `r.Last`
   in ascending order; before writing the first row send the status line and set `sent = true`; write each line through
   the K-H5d writer; a reader error with `!sent` → log + error delay + `403 Internal fault`; with `sent` → return the
   error (ends the connection). After the reader: `!sent` → error delay + `emptyCode` reply (`423 No articles in that
   range` / `420 Current article number is invalid`); else terminate the block.
6. `hdrFieldValue(a *models.Article, field string) string`: `subject`, `from`, `date`, `message-id`, `references` → the
   struct fields (`Subject`, `FromHeader`, `DateString`, `MessageID`, `References`); `:bytes`/`:lines` (and, per D2,
   `bytes`/`lines`) → `strconv.Itoa(a.Bytes|a.Lines)`; any other header (only reachable under the any-header policy) →
   the first `Name:` line in `a.HeadersJSON` matched case-insensitively, with following continuation lines (leading SP
   or TAB) unfolded, the name, colon and one following space removed (§8.5.2); missing → `""`.
7. `overviewFmtLines() []string` (Design table) and `handleListOverviewFmt(args)`: `len(args) != 0` → 501; else `215
   Order of fields in overview database` + the lines + terminator.
8. `listHeadersLines() []string` and `handleListHeaders(args)`: more than one word, or a word other than `MSGID`/`RANGE`
   → 501; else `215 Headers and metadata items supported` + lines + terminator.
9. **Tests** `internal/nntp/nntp-rfc-overview_test.go` (prefix `ovh`; `ovhSession` with extra `OVER`, `HDR` and `LIST`
   → wrapper routing `OVERVIEW.FMT`/`HEADERS` to the new handlers and everything else to `cc.handleList`):
   - `TestOvhOver` on `hGroup(t, "rc3.over", 5, …)` with 2 skipped: no group → 412; after GROUP: `OVER` (current =
     low) → 224 with one line numbered with the low mark; `OVER 1-5` → numbers 1, 3, 4, 5 (no 2); `OVER 3-` → 3, 4, 5;
     `OVER 2` → 423; `OVER 5-3` → 423; `OVER 50-60` → 423; `OVER x` → 501; `OVER 1 2` → 501; every line has at least 8
     TAB-separated fields with field 1 the number.
   - `TestOvhOverMsgID`: `OVER <id of 3>` with the group selected → first field `3`; with no group selected and an
     `hFakeProcessor` whose `findFn` returns the article → first field `0`; unknown id with `findErr` wrapping
     `ErrArticleNotFound` → 430.
   - `TestOvhHdr`: `HDR Subject 1-5` → 225 and `n Test article n` for 1, 3, 4, 5; `HDR :lines 3` → `3 <lines>`; `HDR
     :foo 3` → 503; `HDR Subject 50-60` → 423; `HDR Subject` without a current article in an empty group → 420; `HDR
     Subject <id of 4>` → `4 Test article 4` (group selected) or `0 …` (no group, via `findFn`); `HDR` → 501.
   - `TestOvhHdrPolicy`: an article whose `headers_json` holds `X-Ovh-Test: v1`: under the any-header policy `HDR
     X-Ovh-Test 1` → `1 v1` and an article without it → `n ` (empty value); under the restricted policy → 503. Pick the
     branch from the K-H5b record (one `if` on a test-local constant documented with the record line).
   - `TestOvhOverviewFmt`: `LIST OVERVIEW.FMT` → 215 and the 7 fixed lines (case-insensitive) in order; the number of
     TAB fields in an `OVER` line (with trailing empty fields kept) is at most `1 + len(lines)`; `Xref:full` present
     iff the formatter emits a 9th field. `LIST OVERVIEW.FMT x` → 501.
   - `TestOvhListHeaders`: 215; contains `:bytes` and `:lines`; contains `:` (any-header) or `Subject` (restricted);
     `LIST HEADERS MSGID` and `LIST HEADERS RANGE` give the same set; `LIST HEADERS FOO` → 501.
   - `TestOvhLargeRange` (BUG-2 parity): `ovrBulkGroup`-style 1500 articles inserted in one transaction (copy the
     approach of `nntp-server-overview_test.go:143`, prefix `ovh`), `OVER 1-1500` → 1500 lines and `HDR Subject 1-` →
     1500 lines, each within 10 s.

Acceptance: E05, E06 (through `ovhSession`).

---

#### Slice `rfc-stream`: MODE STREAM, CHECK
Labels: RFC-1k, RFC-1l. E-tag `rfc-stream`.

Owned files (all new):
- `internal/nntp/nntp-cmd-stream.go`
- `internal/nntp/nntp-rfc-stream_test.go`

Changes (`nntp-cmd-stream.go`):
1. `streamingAllowed() bool`: `c.server.Processor != nil && !(c.authenticated && c.user != nil && !c.user.Posting)`.
   Doc comment: RFC 4644 §2.2 and decision D5; it is the single condition for the STREAMING capability and the 203.
2. `handleModeStream(args)`: `len(args) != 0` → error delay + `501 MODE STREAM takes no arguments`; `Processor == nil`
   → error delay + `501 MODE STREAM not supported`; `!streamingAllowed()` → error delay + `502 Streaming not permitted`;
   else `203 Streaming permitted`. No state change (§2.3.2).
3. `checkStreamMessageID(id string) bool`: if K-H7 recorded a message-id validator in package `nntp`, return its result;
   otherwise `len(id) >= 3 && len(id) <= 250 && id[0] == '<' && id[len(id)-1] == '>' && strings.Count(id, "@") >= 1`
   and every byte in 0x21..0x7e except that `<`/`>` occur only at the ends (RFC 5536 §3.1.3, RFC 3977 §3.6).
4. `handleCheck(args)` in this order (Design table): processor nil → `500 Command not recognized: CHECK`; `len(args) !=
   1` → error delay + `501 CHECK requires exactly one message-id`; invalid id → error delay + `501 Invalid message-id`;
   `!c.authenticated` → error delay + `480 Transfer permission denied (authentication required)`; user without posting →
   error delay + `502 Transfer not permitted for this user`; then `switch c.server.Processor.CheckMessageID(id)`:
   `history.CasePass` → `238 <id> Send article`, `history.CaseDupes` → `438 <id> Not wanted`, `history.CaseRetry` →
   `431 <id> Retry later`, default → log `[NNTP] CHECK history error for <id>` and `431 <id> Retry later`. Never read
   from the connection (CHECK has no data). These gates match IHAVE (`nntp-cmd-posting.go:50-84` at 2fb7741); if the
   intake plan changed IHAVE's gates before this plan runs, mirror the current IHAVE gates and record the difference.
5. **Tests** `internal/nntp/nntp-rfc-stream_test.go` (prefix `stm`; `stmSession` with extra `MODE` → wrapper: `STREAM`
   → `cc.handleModeStream(args[1:])`, else `cc.handleMode(args)`, and `CHECK`):
   - `TestStmModeStream`: with `hFakeProcessor` → 203; `MODE STREAM x` → 501; no processor → 501; authenticated
     non-posting user (`hUser(t, false, true)`) → 502; authenticated posting user → 203; `MODE READER` still answered by
     hardened `handleMode`.
   - `TestStmCheck`: posting user authenticated; `checkFn` returns per id: Pass → `238 <id>`, Dupes → `438 <id>`, Retry
     → `431 <id>`, `history.CaseError` → `431 <id>`; each reply's second word equals the id; unauthenticated → 480; non-posting →
     502; no processor → 500; `CHECK` → 501; `CHECK a b` → 501; `CHECK not-an-id` → 501; the fake recorded exactly the
     ids of the four successful CHECKs (`checked`).
   - `TestStmPipelined`: 50 CHECKs in one write (`c.send`) → 50 replies in order, each carrying its id (RFC 4644
     §2.1 pipelining); on a server with `hServerOpts{ErrorDelay: 200 * time.Millisecond}` and a `checkFn` that returns
     `history.CaseDupes`, 50 pipelined CHECKs get their 50 `438` replies within 1 s (no delay on 431/438).
   - `TestStmCheckThenTakethis`: CHECK `<new>` → 238, then TAKETHIS of the same id with a valid article (the fake's
     `processResult = history.CasePass`) → `239 <id>`: CHECK leaves no state that breaks TAKETHIS.

Acceptance: E07 (through `stmSession`).

---

#### Slice `rfc-starttls`: STARTTLS and the cmd/nntp-server certificate flags
Label: RFC-1n. E-tag `rfc-starttls`.

Owned files:
- `internal/nntp/nntp-cmd-starttls.go` (new)
- `internal/nntp/nntp-rfc-starttls_test.go` (new)
- `cmd/nntp-server/main.go` (only the TLS flag help texts at `:40-41` and the TLS block at `:58-67` at 2fb7741; if
  hardening reworked `main.go`, locate them by the `nntpcertFile`/`nntpkeyFile` variables)

Changes:
1. **`nntp-cmd-starttls.go`:**
   ```go
   const starttlsHandshakeTimeout = 30 * time.Second

   type starttlsEntry struct {
   	once sync.Once
   	cfg  *tls.Config
   	err  error
   }

   // starttlsCache holds one entry per (cert, key) path pair for the process lifetime.
   var starttlsCache sync.Map // string -> *starttlsEntry

   func starttlsConfigFor(certFile, keyFile string) (*tls.Config, error) {
   	v, _ := starttlsCache.LoadOrStore(certFile+"\x00"+keyFile, &starttlsEntry{})
   	e := v.(*starttlsEntry)
   	e.once.Do(func() {
   		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
   		if err != nil {
   			e.err = err
   			return
   		}
   		e.cfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
   	})
   	return e.cfg, e.err
   }
   ```
   If K-H11 recorded a hardened TLS-config builder, build `e.cfg` with it instead (it carries hardening's settings).
   - `starttlsAvailable() bool`: `!c.isTLS && !c.authenticated && c.server.Config.NNTP.TLSCert != "" &&
     c.server.Config.NNTP.TLSKey != ""`.
   - `resetSessionState()`: `authenticated = false`, `user = nil`, `authUsername = ""`, `currentGroup = ""`,
     `currentFirst = 0`, `currentLast = 0`, `currentArticle = 0`, plus every per-session field K-H11 lists (RFC 4642
     §2.2.2 "MUST discard any knowledge obtained from the client").
   - `handleStartTLS(args)` in this order: `len(args) != 0` → error delay + `501 STARTTLS takes no arguments`; `c.isTLS`
     → `502 STARTTLS not allowed with active TLS layer`; `c.authenticated` → `502 STARTTLS not allowed after
     authentication`; no cert → `502 STARTTLS not available`; `cfg, err := starttlsConfigFor(...)`; err → log `[NNTP]
     STARTTLS certificate: <err>` and `580 Can not initiate TLS negotiation`; `if n := c.textConn.R.Buffered(); n > 0 {
     log "[NNTP] STARTTLS from <addr>: discarding <n> pipelined bytes"; _, _ = c.textConn.R.Discard(n) }` (and the same for
     any byte-limited reader K-H11 lists); send `382 Continue with TLS negotiation`; `raw := c.conn`;
     `_ = raw.SetDeadline(time.Now().Add(starttlsHandshakeTimeout))`; `tc := tls.Server(raw, cfg)`; `tc.Handshake()`
     error → log `[NNTP] STARTTLS handshake with <addr> failed: <err>`, `_ = raw.Close()`, return the K-H11
     close-without-reply sentinel if hardening has one, else `fmt.Errorf("starttls handshake: %w", err)`. Success:
     replace the connection fields exactly the way hardened `NewClientConnection` builds them from a `net.Conn` (at
     2fb7741: `c.conn = tc`, `c.textConn = textproto.NewConn(tc)`, `c.writer = bufio.NewWriter(tc)`), under the mutex
     K-H11 names if another goroutine reads `c.conn`; `c.isTLS = true`; `c.resetSessionState()`; refresh the deadlines
     (K-H12 `UpdateDeadlines`); return nil **without** a greeting (§2.2.2).
   - If K-H11 shows that `Handle` captures `c.textConn` in a local before its loop, stop and report: the orchestrator
     must change that line inline in wave 2 step A (it is in hardening's file); the handler itself stays as above.
2. **`cmd/nntp-server/main.go`:**
   - Help texts: `-nntpcertfile` → `"NNTP TLS certificate file for the TLS port and STARTTLS (/path/to/fullchain.pem)"`,
     `-nntpkeyfile` → `"NNTP TLS key file for the TLS port and STARTTLS (/path/to/privkey.pem)"`.
   - Replace the TLS block with:
     ```go
     	if nntptlsport > 0 {
     		mainConfig.Server.NNTP.TLSPort = nntptlsport
     	} else {
     		mainConfig.Server.NNTP.TLSPort = 0
     		log.Printf("[NNTP]: No NNTP TLS port flag provided")
     	}
     	// The certificate serves the TLS port and STARTTLS on the plain port (RFC 4642).
     	if (nntpcertFile == "") != (nntpkeyFile == "") {
     		log.Fatalf("[NNTP]: -nntpcertfile and -nntpkeyfile must be given together")
     	}
     	mainConfig.Server.NNTP.TLSCert = nntpcertFile
     	mainConfig.Server.NNTP.TLSKey = nntpkeyFile
     	if nntpcertFile != "" {
     		if _, err := tls.LoadX509KeyPair(nntpcertFile, nntpkeyFile); err != nil {
     			log.Fatalf("[NNTP]: cannot load TLS certificate: %v", err)
     		}
     		log.Printf("[NNTP]: STARTTLS enabled (certificate %s)", nntpcertFile)
     	}
     ```
     and add `"crypto/tls"` to the imports. `Start()` still opens the TLS listener only when `TLSPort > 0` (unchanged).
3. **Tests** `internal/nntp/nntp-rfc-starttls_test.go` (prefix `stls`):
   - `stlsWriteCert(t) (certFile, keyFile string, der []byte)`: ECDSA P-256 self-signed for `127.0.0.1`/`localhost`
     (as `sessSelfSignedTLS`, `nntp-server-session_test.go:131`), PEM-encoded into `t.TempDir()`.
   - `stlsNewServer(t, certFile, keyFile string, proc ArticleProcessor) *hServer`: a copy of `newTestServer`
     (`nntp-server-harness_test.go:169-203`) whose `cfg.NNTP.TLSCert/TLSKey` are set **before** `NewNNTPServer`, so no
     test writes the config while server goroutines run.
   - `stlsSession` with extra `STARTTLS`; `stlsUpgrade(t, c *hClient)`: `tc := tls.Client(c.Conn,
     &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})` (`//nolint:gosec // test client`),
     `tc.Handshake()`, then `c.Conn = tc; c.R = bufio.NewReaderSize(tc, 1<<20)`.
   - `TestStlsStartTLS`: GROUP g → 211; `STARTTLS` → 382; upgrade; the peer certificate equals `der`; `NEXT` → 412
     (state reset: no current group); `STARTTLS` → 502; `AUTHINFO USER/PASS` over TLS → 281; `QUIT` → 205.
   - `TestStlsRefusals`: server without cert → `STARTTLS` → 502; with cert after `AUTHINFO` (posting user) → 502;
     `STARTTLS x` → 501; with a cert path that doesn't exist → 580, and the session continues in plaintext (`GROUP g` →
     211 on the same connection).
   - `TestStlsPipelinedDiscarded`: send `"STARTTLS\r\nGROUP <g>\r\n"` in one write; read `382`; upgrade; then
     `CAPABILITIES` over TLS returns `101` as the **first** reply (no stray `211`), proving the pipelined plaintext was
     discarded.
   - `TestStlsHandshakeFailure`: after `382` the client writes `"GARBAGE\r\n"` instead of a ClientHello; the server
     closes the connection within 2 s (read returns EOF/reset), and a new connection to the same server still gets its
     greeting.
   - `TestStlsAvailable`: table over `isTLS`, `authenticated`, cert set/unset: `a, b := net.Pipe()` (both closed in
     `t.Cleanup`), `cc := NewClientConnection(a, hs.S, isTLS)` on a server from `stlsNewServer` (cert set) or
     `newTestServer` (unset), set `cc.authenticated`, then compare `cc.starttlsAvailable()` with the expected value.

Acceptance: E09 (through `stlsSession`).

---

### Wave 2

#### Step A (inline, orchestrator on `plan-nntp-rfc-commands`, after merging wave 1)
Owned: `internal/nntp/nntp-server-cliconns.go` (dispatch switch and `getServerCapabilities` only, the D4 exception),
optionally `internal/nntp/README.md` (A.4 only; not an Appendix B file).

1. Merge the wave-1 branches in slice order (`rfc-reader`, `rfc-lists`, `rfc-overview`, `rfc-stream`, `rfc-starttls`),
   running `## Checks` after each merge (the dispatch is unchanged, so the RFC-1 markers still skip).
2. Apply the **Dispatch** hand-over exactly as written in Design (plus the known-command set and argument table rows if
   hardening has them).
3. Apply the **CAPABILITIES** hand-over exactly as written, using the K-H4 variable name.
4. Only if hardening's FN-1 work left a command list in `internal/nntp/README.md`: add one line each for `DATE`,
   `NEXT`, `LAST`, `NEWGROUPS date time [GMT]`, `LIST ACTIVE|NEWSGROUPS [wildmat]`, `LIST OVERVIEW.FMT`, `LIST HEADERS`,
   `OVER [range|<msgid>]`, `HDR field [range|<msgid>]`, `MODE STREAM`, `CHECK <msgid>`, `STARTTLS`, in the existing
   style. The HELP text (`handleHelp` in `nntp-cmd-basic.go`, an Appendix B hardening file) is not edited here: list
   the missing HELP lines in the Outcome as a follow-up.
5. Run `go build ./...` and `go test -race -count=1 ./internal/nntp/...`. The RFC-1 markers now **fail** with "known
   bug RFC-1 no longer reproduces": expected, step B removes them.
6. Commit: `nntp: dispatch and CAPABILITIES for the RFC 3977/4644/4642 commands (nntp-rfc-commands wave 2)`. Record
   the SHA as the base of step B.

#### Step B: slice `rfc-wire` (1 `pugleaf-implementer`)
Labels: RFC-1 (markers), RFC-1m (verification). E-tag `rfc-wire`.

Owned files:
- `internal/nntp/nntp-server-group_test.go` (only the RFC-1 markers and the header comment)
- `internal/nntp/nntp-server-overview_test.go` (only the two markers in `TestOvrOverHdr`)
- `internal/nntp/nntp-server-session_test.go` (only the marker in `processor-streaming-complete`)
- `internal/nntp/nntp-server-rfccmds_test.go` (new)
- `cmd/nntp-server/e2e_binary_rfc_test.go` (new, `//go:build e2e`)
- `scripts/test-nntp-server.sh`

Changes:
1. **Remove the RFC-1 markers, keep the assertions** (find each by test name; the lines are at 2fb7741):
   | File | Test | Line | Replacement |
   |----|----|----|----|
   | `nntp-server-session_test.go` | `TestSessCapabilities/processor-streaming-complete` | 272 | `if err != nil { t.Fatal(err) }` |
   | `nntp-server-group_test.go` | `TestGrpListWildmat/{active,newsgroups}` | 596 | `if err := errors.Join(errs...); err != nil { t.Fatal(err) }` |
   | same | `TestGrpListOverviewFmt` | 637 | `if err != nil { t.Fatal(err) }` |
   | same | `TestGrpDate` | 656 | `if err != nil { t.Fatal(err) }` |
   | same | `TestGrpNextLast/no-group` | 690 | `if err := errors.Join(expect(t, c, "NEXT", 412, 0), expect(t, c, "LAST", 412, 0)); err != nil { t.Fatal(err) }` |
   | same | `TestGrpNextLast/walk` | 710 | `if err != nil { t.Fatal(err) }` (keep the RFC-3 note wrapper at `:706-708` or drop it; it only adds text) |
   | same | `TestGrpNextLast/empty-group` | 720 | as `no-group` with 420 |
   | same | `TestGrpNewGroups` | 751 | `if err := errors.Join(errs...); err != nil { t.Fatal(err) }` |
   | `nntp-server-overview_test.go` | `TestOvrOverHdr/OVER` | 688 | `if err != nil { t.Fatal(err) }` |
   | same | `TestOvrOverHdr/HDR` | 698 | `if err != nil { t.Fatal(err) }` |

   In the group test header comment (`:4-10`), replace "the READER commands the server does not implement yet" by "the
   READER commands DATE …, NEXT/LAST …, NEWGROUPS … and NEWNEWS (not implemented, not advertised)" and drop `RFC-1`
   from the id list. If a hardening-owned marker in these tests (e.g. `RFC-5` on `TestSessMode/stream`) now reports
   "no longer reproduces", remove it the same way and note it in the report.
2. **`nntp-server-rfccmds_test.go`** (prefix `rfc`; real dispatch through `newTestServer`/`hs.dial`; a cert server
   via a local copy of the `stlsNewServer` idea named `rfcNewCertServer`):
   - `TestRfcDispatch`: one subtest per command proving it reaches its handler: `DATE` → 111; `NEXT`/`LAST` without a
     group → 412; `NEWGROUPS 20991231 235959 GMT` → 231 + empty block; `LIST ACTIVE rc6.none.*` → 215 + empty block;
     `LIST OVERVIEW.FMT` → 215; `LIST HEADERS` → 215; `OVER`/`HDR Subject` without a group → 412; `MODE STREAM` with a
     processor → 203; `CHECK <x@y>` unauthenticated with a processor → 480; `STARTTLS` without cert → 502; `NEWNEWS
     rc6.* 20000101 000000 GMT` → 500 (D3); `LIST ACTIVE` without a wildmat and `MODE READER` still answered by the
     hardened handlers.
   - `TestRfcCapabilities`: the "Expected lists" table of the Design, one subtest per row: present labels present,
     "Never" labels absent, `VERSION 2` first, `NEWNEWS` never listed.
   - `TestRfcStreamingConsistent`: in each of the states processor/unauthenticated, posting user, non-posting user, no
     processor: `STREAMING` listed iff `MODE STREAM` → 203.
   - `TestRfcStartTLSWire`: on `rfcNewCertServer`: `STARTTLS` listed before auth; `STARTTLS` → 382; upgrade (as
     `stlsUpgrade`); CAPABILITIES over TLS lacks `STARTTLS`; a second `STARTTLS` → 502; `AUTHINFO` → 281; on an
     implicit-TLS listener (`hServerOpts{TLS: cfg}` plus cert files in the config) `STARTTLS` is not listed and answers
     502.
   - `TestRfcReaderHonest`: `READER` is listed and DATE, NEXT, LAST, NEWGROUPS all answer with a non-500 code (RFC 3977
     §3.3.3 "MUST implement the extension").
3. **`cmd/nntp-server/e2e_binary_rfc_test.go`** (`//go:build e2e`, package main, prefix `e2eRfc`): `TestE2EBinaryRFC`
   skips without `PUGLEAF_E2E_ADDR`; reuses `e2eDialAddr`, `e2eReport`, `e2eEnv`, `e2eParseCode`, `e2eClient.tryCmd`,
   `tryDotLines`, `tryAuth` from the existing e2e files; prints `PASS|FAIL|INFO N4x [rfc] <text>`:
   - **N40** `DATE` → `111` + 14 digits within 5 min of UTC now.
   - **N41** GROUP `$PUGLEAF_E2E_GROUP` → parse `211 count low high`; `LAST` → 422; `NEXT` repeated until 421 gives
     `count-1` replies `223`, strictly increasing numbers between low and high; then `LAST` → 223.
   - **N42** `LIST OVERVIEW.FMT` → 215, the 7 fixed lines.
   - **N43** `OVER 1-` → 224 and ≥ 1 line starting with a number; `OVER 999999999-` → 423.
   - **N44** `HDR Subject 1` → 225 and one line starting `1 `; `LIST HEADERS` → 215 with `:bytes`.
   - **N45** `MODE STREAM` → 203; `AUTHINFO` → 281; `CHECK <fresh id>` → `238 <fresh id>`; `HDR Message-ID 1` gives the
     stored id; `CHECK <that id>` → `438 <that id>`.
   - **N46** `NEWGROUPS <now-2 days yyyymmdd hhmmss> GMT` → 231 listing the group; `LIST ACTIVE <group>` → exactly one
     line equal to the group's NEWGROUPS line; `LIST ACTIVE e2e.*,!*.b` lists `e2e.test.a` but not `e2e.test.b`.
   - **N47** with `PUGLEAF_E2E_STARTTLS=1`: a new connection; CAPABILITIES lists `STARTTLS`; `STARTTLS` → 382; TLS
     handshake (`InsecureSkipVerify`, `MinVersion` TLS 1.2); CAPABILITIES over TLS lacks `STARTTLS`; `STARTTLS` → 502.
     Without it: CAPABILITIES lacks `STARTTLS` and `STARTTLS` → 502, reported as `INFO N47 [rfc] no certificate
     configured` plus PASS for the 502.
4. **`scripts/test-nntp-server.sh`** (additive; keep hardening's checks intact):
   - Header comment: add "N40-N47 (nntp-rfc-commands) come from `go test -tags e2e -run TestE2EBinaryRFC`".
   - After `mkdir -p "$DATA/run"` and before the first `start_server`: if `command -v openssl`, generate
     `"$DATA_ABS/run/tls.key"`/`tls.crt` with `openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1
     -nodes -days 2 -subj /CN=localhost -keyout … -out … >/dev/null 2>&1` and set `TLSFLAGS=(-nntpcertfile
     "$DATA_ABS/run/tls.crt" -nntpkeyfile "$DATA_ABS/run/tls.key")` and `E2E_STARTTLS=1`; else `TLSFLAGS=()` and
     `E2E_STARTTLS=0` with `info N47 rfc "openssl missing: STARTTLS checked without a certificate"`.
   - `start_server`: append `${TLSFLAGS[@]+"${TLSFLAGS[@]}"}` to the server command line (safe under `set -u` with an
     empty array).
   - After the N01-N11 block: run `PUGLEAF_E2E_ADDR=… PUGLEAF_E2E_USER=… PUGLEAF_E2E_PASS=… PUGLEAF_E2E_GROUP=e2e.test.a
     PUGLEAF_E2E_STARTTLS=$E2E_STARTTLS timeout 300 go test -tags e2e -count=1 -v -run '^TestE2EBinaryRFC$'
     ./cmd/nntp-server/ >"$DATA/e2e-rfc-go-test.log" 2>&1`, count `PASS|FAIL|INFO N4[0-7]` lines into the totals as the
     N01-N11 block does, and `fail N00 rfc "go test reported $n of 8 rfc steps"` unless exactly 8 `PASS|FAIL N4[0-7]`
     lines were printed. `alive || setup_fail "server died during the rfc steps"`.
   - `bash -n scripts/test-nntp-server.sh` must pass.
5. Checks: the common wave-1 list plus `PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/...
   ./cmd/nntp-server/... 2>&1 | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u` (no `RFC-1`), `grep -rn
   '"RFC-1"' internal/nntp cmd/nntp-server` (no output), and `go vet -tags e2e ./cmd/nntp-server/`.

Acceptance: E01–E12.

### Wave 3 (inline): verify and finish
Per the skill: `pugleaf-verifier` runs `## Checks` and `## End-to-end` on the main checkout (`plan-nntp-rfc-commands`).
Outcome lists: the decisions D1–D11, the K-H contract record, the out-of-scope items, any `T-rfc-commands-*` ids,
whether README was updated (A.4), and the HELP lines still missing.

---

## Acceptance (E-checks)
| Id | Check | Tag |
|----|----|----|
| E01 | `TestGrpDate` passes without a knownBug skip; `TestRdrDate` passes | rfc-reader / rfc-wire |
| E02 | `TestGrpNextLast` (3 subtests) and `TestRdrNextLast` pass | rfc-reader / rfc-wire |
| E03 | `TestGrpNewGroups`, `TestLstNewGroups`, `TestLstParseNNTPDateTime` pass; the NEWGROUPS line equals the LIST ACTIVE line | rfc-lists / rfc-wire |
| E04 | `TestGrpListWildmat`, `TestLstListWildmat`, `TestLstMatchesList`, `TestLstWildmat*`, `FuzzLstWildmat` (seed corpus) pass | rfc-lists / rfc-wire |
| E05 | `TestGrpListOverviewFmt`, `TestOvhOverviewFmt`, `TestOvhListHeaders` pass | rfc-overview / rfc-wire |
| E06 | `TestOvrOverHdr`, `TestOvhOver`, `TestOvhOverMsgID`, `TestOvhHdr`, `TestOvhHdrPolicy`, `TestOvhLargeRange` pass | rfc-overview / rfc-wire |
| E07 | `TestSessCapabilities/processor-streaming-{complete,consistent}`, `TestRfcStreamingConsistent`, `TestStm*` pass | rfc-stream / rfc-wire |
| E08 | `TestGrpNewNews` passes and `TestRfcDispatch/NEWNEWS` gets 500; NEWNEWS never in CAPABILITIES | rfc-wire |
| E09 | `TestStls*` and `TestRfcStartTLSWire` pass | rfc-starttls / rfc-wire |
| E10 | `TestRfcCapabilities` and `TestRfcReaderHonest` pass | rfc-wire |
| E11 | `PUGLEAF_KNOWN_BUGS=1` id list no longer contains `RFC-1`; `grep -rn '"RFC-1"' internal/nntp cmd/nntp-server` is empty | rfc-wire |
| E12 | End-to-end: N40–N47 PASS (N47 PASS with a certificate; with openssl missing, PASS for the 502 plus INFO) and no FAIL among the checks hardening left | verifier |

## Checks
On every merged tree (after wave 0, after each merge in waves 1 and 2):
```bash
gofmt -l ./cmd ./internal            # only the baseline files (2 at 2fb7741 plus hardening's Outcome list)
go vet ./...
go vet -tags e2e ./cmd/nntp-server/
go build ./...
go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
  ./internal/nntp/... ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/...
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u
    # after wave 2: RFC-1 absent; the rest must equal the list hardening's Outcome left (wave 0 records it)
grep -rn '"RFC-1"' internal/nntp cmd/nntp-server   # after wave 2: no output
bash -n scripts/test-nntp-server.sh
git status --short                   # no data-test-*, zz_*, active.out or analysis_*.txt leftovers
```
Merge order in wave 1: `rfc-reader`, `rfc-lists`, `rfc-overview`, `rfc-stream`, `rfc-starttls`. The file sets are
disjoint and every file is new except `cmd/nntp-server/main.go`, so textual conflicts aren't expected. Semantic interplay
to watch: the NEWGROUPS/LIST wildmat lines vs hardening's LIST ACTIVE (equality tests), and STARTTLS vs hardening's
connection plumbing (K-H11).

## End-to-end
Runner: `pugleaf-verifier` on the main checkout (`plan-nntp-rfc-commands`), after wave 2.
```bash
./build_nntp-server.sh && ./build_nntpmgr.sh
PORT=21129 DATA=./data-test-nntp-rfc-commands scripts/test-nntp-server.sh
```
- Guards and setup are the script's (DATA must match `./data-test-*`, DNS for `-nntphostname example.org`, `sqlite3`,
  `timeout`, `nc`, `go`; `openssl` optional for N47).
- The server runs with `-data <abs path of ./data-test-nntp-rfc-commands> -nntptcpport 21129 -nntphostname example.org`
  plus `-nntpcertfile/-nntpkeyfile` when openssl generated a certificate (STARTTLS on the plain port; no TLS port).
- **Expected:** N01–N11 PASS; N40–N47 PASS (N47: STARTTLS 382 + handshake with a certificate; INFO + the 502 PASS
  without openssl); N20–N22/N30 as hardening's Outcome states (PASS after hardening); `SUMMARY … fail=0`.
- Manual spot check (optional, documented in the verifier report):
  ```bash
  printf 'DATE\r\nLIST OVERVIEW.FMT\r\nMODE STREAM\r\nQUIT\r\n' | timeout 5 nc 127.0.0.1 21129
  ```
  gives `111 …`, `215` + 7 lines + `.`, `203 Streaming permitted`, `205`.

---

## Progress
(empty until wave 0 runs; wave 0 writes the "Wave 0 contract record" here: one line per K-H1…K-H12 item)
