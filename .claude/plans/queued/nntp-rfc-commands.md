# Plan: NNTP RFC commands (RFC-1: DATE, NEXT/LAST, NEWGROUPS, LIST wildmat, OVER/HDR, streaming, STARTTLS)

- **Slug:** `nntp-rfc-commands`
- **Integration branch:** `plan-nntp-rfc-commands` (from `testing-001`)
- **Depends on:** `nntp-server-hardening`, `nntp-intake-injection` (both merged into `testing-001` first). Cross-plan
  run order (final): nntp-server-hardening → nntp-intake-injection → nntp-client-transfer → **nntp-rfc-commands** →
  audit-delta-fixes. Wave 0 stops if either dependency is missing.
- **Run with:** `/run-plan .claude/plans/queued/nntp-rfc-commands.md`
- **Status:** written 2026-10-11 by wave 4 of `nntp-audit-tests` (slice `fixplan-nntp-rfc-commands`), anchored at
  `plan-nntp-audit-tests` @ 2fb7741; review fixes applied the same day against the final hardening, intake and client
  plans (the K-H record in `## Progress` is pre-filled from them). Nothing has run yet. Line numbers are **at 2fb7741**
  unless they say otherwise; the dependencies move many of them, so every slice locates code by function name first.
- **Parallelism:** wave 1 has 5 implementer slices, wave 2 has an inline step and 1 implementer slice. Within a wave no
  two slices own the same file. Files the dependencies changed are touched only where this plan says so: the inline
  wave-2 step A edits the dispatch, `getServerCapabilities`, the `commandMaxArgs` literal, the 10 RFC-1 marker lines,
  `sessFuzzMultiCodes`, the HELP lines and `internal/nntp/README.md`; `rfc-starttls` owns the TLS flag block of
  `cmd/nntp-server/main.go` (hardening's file, merged first); `rfc-wire` owns `scripts/test-nntp-server.sh` (shared by
  all NNTP plans; this plan adds only its N50–N59 block and the certificate setup).
- **Source:** `docs/audit/AUDIT-2026-10.md` (§4 nntp-server row RFC-1, §5 RFC command matrix, §10 known-bug list, §11
  decisions, §12 corrections), `docs/audit/audit-nntp-rfc.md` (RFC-1 block, Command matrix, Response code table,
  CAPABILITIES), `docs/audit/audit-nntp-sec.md` (Unauthenticated attack surface, TLS and shutdown), and the queued plans
  `nntp-server-hardening.md` (decisions 2, 3, 7, 14, 15; D1–D6), `nntp-intake-injection.md` (K1 `common.ValidMessageID`,
  IHAVE/TAKETHIS gates, N40–N49), `nntp-client-transfer.md`.

---

## Context

### Request
The audit (plan `nntp-audit-tests`) routed exactly one finding to this plan: **RFC-1** (medium, rfc, CONFIRMED). The
server advertises READER but answers the READER-mandatory DATE, NEXT, LAST and NEWGROUPS with 500. LIST ACTIVE and
LIST NEWSGROUPS ignore the wildmat. TAKETHIS is offered without MODE STREAM and CHECK. OVER, HDR, LIST
OVERVIEW.FMT/HEADERS, NEWNEWS and STARTTLS are missing. Every other server finding belongs to `nntp-server-hardening`,
the intake findings to `nntp-intake-injection`; this plan builds on both and changes none of their decisions.

### Findings (RFC-1, split per command)
The canonical id stays **RFC-1** (K6 of `nntp-audit-tests`: seed ids are never renumbered). `RFC-1a`…`RFC-1n` are
plan-local labels for slices and E-checks only. "Today" is the behavior at 2fb7741 (repro: `audit-nntp-rfc.md`
Appendix A). After hardening the dispatch `default:` still answers 500 (`rateLimitOnError()` + `500 Command not
recognized`, hardening D4). Paths are under `internal/nntp/` unless stated.

| Label | Command | RFC § | Today (file:line at 2fb7741) | Severity | knownBug marker(s) | Slice |
|----|----|----|----|----|----|----|
| RFC-1a | `DATE` | 3977 §7.1 (READER) | `500 Command not recognized` from the dispatch `default:` (`nntp-server-cliconns.go:154-155`) | medium | `TestGrpDate` (`nntp-server-group_test.go:656`) | `rfc-reader` |
| RFC-1b | `NEXT` | 3977 §6.1.4 (READER) | 500, `nntp-server-cliconns.go:154-155` | medium | `TestGrpNextLast/{no-group,walk,empty-group}` (`:690`, `:710`, `:720`) | `rfc-reader` |
| RFC-1c | `LAST` | 3977 §6.1.3 (READER) | 500, same | medium | same three subtests | `rfc-reader` |
| RFC-1d | `NEWGROUPS date time [GMT]` | 3977 §7.3 (READER) | 500, same; `newsgroups.created_at` exists (`migrations/0001_main_schema.sql:39`) but nothing reads it | medium | `TestGrpNewGroups` (`:751`) | `rfc-lists` |
| RFC-1e | `LIST ACTIVE wildmat` | 3977 §7.6.3, §4 | wildmat ignored, every group listed: `handleList` reads only `args[0]` (`nntp-cmd-list.go:9-22`); hardening keeps it ignored | medium | `TestGrpListWildmat/active` (`:596`) | `rfc-lists` |
| RFC-1f | `LIST NEWSGROUPS wildmat` | 3977 §7.6.6, §4 | same (`nntp-cmd-list.go:18-19`, `:46-62`) | medium | `TestGrpListWildmat/newsgroups` (`:596`) | `rfc-lists` |
| RFC-1g | `LIST OVERVIEW.FMT` | 3977 §8.4; 2980 §2.1.7 (SHOULD with XOVER) | `501 Unknown LIST type` (`nntp-cmd-list.go:20-21`) | low | `TestGrpListOverviewFmt` (`:637`) | `rfc-overview` |
| RFC-1h | `OVER [range\|message-id]` | 3977 §8.3 | 500 (not advertised: conformant, missing feature) | low | `TestOvrOverHdr/OVER` (`nntp-server-overview_test.go:688`) | `rfc-overview` |
| RFC-1i | `HDR field [range\|message-id]` | 3977 §8.5 | 500, same | low | `TestOvrOverHdr/HDR` (`:698`) | `rfc-overview` |
| RFC-1j | `LIST HEADERS [MSGID\|RANGE]` | 3977 §8.6 | 501, `nntp-cmd-list.go:20-21` | low | none | `rfc-overview` |
| RFC-1k | `MODE STREAM` | 4644 §2.3 ("Servers MUST accept") | `500 Unknown MODE` (`nntp-cmd-basic.go:24-25`; 501 after hardening) although TAKETHIS is dispatched (`nntp-server-cliconns.go:152-153`) | medium | `TestSessCapabilities/processor-streaming-complete` (`nntp-server-session_test.go:272`) | `rfc-stream` |
| RFC-1l | `CHECK message-id` | 4644 §2.4 | 500 | medium | (same subtest) | `rfc-stream` |
| RFC-1m | `NEWNEWS wildmat date time [GMT]` | 3977 §7.4 (capability NEWNEWS, optional) | 500, not advertised: conformant | info | none (`TestGrpNewNews` passes either way) | decision D3; verified by `rfc-wire` |
| RFC-1n | `STARTTLS` | 4642 §2.2 | 500, not advertised; AUTHINFO crosses plaintext (SEC-5) | medium (remedy for plaintext AUTHINFO) | none | `rfc-starttls` |

Supporting facts at 2fb7741:
- Dispatch switch `nntp-server-cliconns.go:119-156`; `getServerCapabilities` `:199-217`. Hardening rewrites both in its
  wave 0 (its D4/D5); this plan's hand-over below is written against those rewritten forms.
- TLS: `Start()` builds the `tls.Config` inline (`nntp-server.go:119-135`; hardening keeps it inline and adds
  `MinVersion: tls.VersionTLS12`). `cmd/nntp-server/main.go:58-67` copies `-nntpcertfile/-nntpkeyfile` into
  `Server.NNTP.TLSCert/TLSKey` **only** when `-nntptlsport > 0`; `config.ServerConfig.NNTP` has `TLSCert`, `TLSKey`
  (`internal/config/config.go:124-131`).
- `newsgroups.created_at DATETIME DEFAULT CURRENT_TIMESTAMP` (UTC `YYYY-MM-DD HH:MM:SS`); nothing inserts it explicitly
  (`query_InsertNewsgroup` `database/queries.go:120`, batch upsert `database/db_batch.go:553`). No index on it.
- `articles.imported_at DATETIME DEFAULT CURRENT_TIMESTAMP` in every group DB, **no index**
  (`migrations/0001_single_db_schema.sql:26`).
- The IHAVE/TAKETHIS history checks use `ArticleProcessor.CheckMessageID` (`nntp-server.go:18-26`).

### Decisions (behavior changes; report them to the user at the end)
1. **D1 OVER/HDR are thin wrappers over the hardened XOVER/XHDR machinery**: `parseRangeArg`/`requireGroup`, the chunked
   readers `GetOverviewsRange`/`GetHeaderFieldRange(…, limit)`, `overviewLine`, `sanitizeOverviewField`,
   `HeaderValueFromJSON` and the data-block writer. Hardening has no shared core that takes reply codes, so each handler
   has a short chunk loop of its own (spelled out in `rfc-overview`). The only behavior of their own is the RFC 3977
   reply-code selection (224/225, 423, 420, 430) and the message-id forms. `OVER MSGID` and `HDR` are advertised. The
   message-id form uses the same lookup as `ARTICLE <message-id>` (`resolveArticleArg`: current group, `local430`,
   processor + `visibleInOtherGroup`), with the same `ArticleCmdDelay` sleep.
2. **D2 HDR serves any header** (hardening decision 3: the 7 indexed columns plus any other header from `headers_json`,
   first occurrence, unfolded, sanitized; an absent header gives the line `n`). `LIST HEADERS` therefore prints `:`,
   `:bytes`, `:lines`. Metadata: only `:bytes` and `:lines`; any other `:x` → `503 Metadata item not supported` (RFC 3977
   §8.5.2). HDR `Bytes`/`Lines` (header names, no colon) are served the way hardened XHDR serves `bytes`/`lines` (from
   the metadata columns; documented deviation). `LIST OVERVIEW.FMT` has the 7 fixed lines only (`overviewLine` has 8
   fields and no Xref, so no `Xref:full`).
3. **D3 NEWNEWS is not implemented: it keeps answering 500 and is never advertised.** `articles.imported_at` has no
   index in any group DB, so each call would full-scan every group DB the wildmat matches; `NEWNEWS * …` from an
   unauthenticated client would open thousands of group DBs per command (the resource class of SEC-2/A-nntp-sec-4).
   RFC 3977 §7.4 makes the capability optional; modern readers use GROUP + OVER. `TestGrpNewNews` asserts "500 and not
   advertised, or 230 and advertised" and keeps passing. No `nntp-cmd-newnews.go`; `handleNewNews` stays reserved.
   **Alternative (not chosen):** an opt-in `-nntp-newnews` flag, authenticated users only, a migration adding
   `idx_articles_imported_at`, a cap on matching groups with `503` above it.
4. **D4 NEWGROUPS** reads `newsgroups.created_at` of active groups, compared in UTC (`datetime(created_at) >=
   datetime(?)`), and answers in exactly hardening's LIST ACTIVE line format (`activeLine`, an equality test enforces it).
   A date without `GMT` is in the server's local time zone (`time.Local`, §7.3.2). 6-digit dates use the §7.3.2 century
   rule. A malformed date or time → 501. A future date → `231` with an empty list. **No truncating result cap:** RFC 3977
   has no partial-list response and truncation makes clients miss groups permanently; the result is a subset of LIST
   ACTIVE (same table, same memory bound, no extra unauthenticated surface); rows are read into a slice before any
   network write, so no DB cursor is held while writing to a slow client.
5. **D5 Streaming.** `streamingAllowed()` is exactly hardening's `c.postingAllowed()` (processor present and not an
   authenticated user without posting rights). `MODE STREAM` → `203` when allowed, `501` without a processor (§2.3.3
   example "501 Unknown MODE variant") or with an argument after `STREAM` (`commandMaxArgs["MODE"] = 1` answers that
   first), `502` for an authenticated user without posting rights. MODE STREAM changes no state (§2.3.2). `STREAMING` is
   advertised iff `streamingAllowed()`, so "STREAMING listed iff MODE STREAM gives 203" holds in every state.
   `CHECK` mirrors intake's IHAVE gate order: no processor → `500` → argument count (`501`; `CHECK a b` already gets the
   table's 501 before the handler) → not authenticated `480` → no posting `502` → invalid message-id
   (`common.ValidMessageID`) → **`438 <id>`** (a streaming peer matches replies by id; intake's TAKETHIS answers
   `439 <id>` for the same case) → `CheckMessageID`: Pass `238 <id>`, Dupes `438 <id>`, Retry `431 <id>`, Error
   (temporary history trouble) `431 <id>`. The 480/500/502 replies carry the id when exactly one argument was given (as
   intake's TAKETHIS replies).
6. **D6 STARTTLS** (RFC 4642). Available iff `Server.NNTP.TLSCert` and `TLSKey` are set, the connection is not TLS and
   not authenticated (`starttlsAvailable()`); only then is `STARTTLS` advertised. `STARTTLS` → `382`, then the TLS
   handshake on the raw connection (30 s deadline, `MinVersion: tls.VersionTLS12`, as hardening's TLS port and the
   client `nntp-client.go:183`). `502` when TLS is already active, after authentication, or when no certificate is
   configured; `580` when the configured certificate can't be loaded; `501` with arguments (the table answers first).
   Never 480/483 (§2.2.1 NOTE). Bytes the client pipelined after `STARTTLS` are discarded unexecuted (§2.2.1 "MUST NOT be
   pipelined"; defense against plaintext command injection into the TLS session). After a successful handshake the
   session is reset (§2.2.2: no greeting, not authenticated, no current group/article) and the connection counts as TLS
   for every later decision (CAPABILITIES, hardening's `authinfoAdvertised`/`PlaintextAuth` policy). A failed handshake
   closes the connection (`errQuit`, no reply). The certificate is loaded once per (cert, key) path pair and cached for
   the process lifetime (rotation needs a restart, as for the TLS port).
7. **D7 `cmd/nntp-server` uses the certificate for STARTTLS too:** `-nntpcertfile/-nntpkeyfile` are copied into the
   config whether or not `-nntptlsport` is set; giving only one of the two, or a certificate that doesn't load, is a
   startup error (`log.Fatalf`). Before, both were silently dropped without a TLS port.
8. **D8 Wildmat** follows RFC 3977 §4 (`*`, `?`, comma-separated patterns, `!` negation, rightmost matching pattern
   decides, anchored, UTF-8 characters). A leading `!` on the first pattern is accepted as a lenient extension (it can
   only exclude). `\`, `[`, `]`, a C0 control (0x00–0x1F) or DEL (0x7F), an empty pattern (`a,,b`, `a,`, `!`) or invalid
   UTF-8 make the wildmat invalid → `501`.
9. **D9 READER stays advertised** (unconditionally, hardening D5). With DATE/NEXT/LAST/NEWGROUPS implemented it becomes
   honest.
10. **D10 Error delays follow hardening decision 7 verbatim:** `rateLimitOnError()` before every 412, 420, 423, 430,
    481, 482, 500 and 501; never before 2xx/3xx, 205, 400, 403, 411, 440, 480, 483 or 502. For the codes this plan adds:
    **421 and 422 no delay** (normal end of a NEXT/LAST walk), **503 delay**, **580 no delay**, **238/431/438 no delay**
    (normal streaming answers).
11. **D11 Limits** (attack-surface parity with `audit-nntp-sec.md`): NEXT/LAST and the message-id forms of OVER/HDR use
    the `ArticleCmdDelay` sleep like ARTICLE/STAT; OVER/HDR ranges read `OverviewRangeChunk` (1000) rows per query and
    stop at the last row (no full-range load and never a walk over unused numbers, also for `n-` = MaxInt64); LIST
    wildmat and NEWGROUPS cost at most one LIST ACTIVE; CHECK costs one history lookup like IHAVE; STARTTLS costs one
    handshake per connection (a second STARTTLS is 502).
12. **D12** No new config keys, flags or migrations (D7 changes the behavior of existing flags only).

### Out of scope (list them in the Outcome)
- NEWNEWS (D3 alternative), LIST ACTIVE.TIMES / DISTRIB.PATS, the RFC 6048 LIST variants, AUTHINFO SASL, XPAT/XGTITLE.
- Turning hardening's `-plaintextauth refuse` (483) into the default now that STARTTLS exists: an operator policy
  change for a later plan.
- An index on `newsgroups.created_at` (the table is small; NEWGROUPS scans it once).
- IMPLEMENTATION capability, MODE READER semantics, mode switching.

---

## Design

### What this plan uses from the dependencies (K-H; final, pre-filled from the merged plans)
Wave 0 only **verifies** each line against the merged code (grep/read) and notes deviations in `## Progress`. A
required item that is missing or different stops the run (ask the user).

| Id | Contract (as merged by `nntp-server-hardening` unless stated) | Used by |
|----|----|----|
| K-H1 | `internal/nntp/nntp-cmd-range.go`: `type articleRange struct { First, Last int64; Open bool }`, `func (r articleRange) Empty() bool` (`Last < First`), `var errRangeSyntax`, `func (c *ClientConnection) parseRangeArg(arg string) (articleRange, error)`. It sends nothing: the caller answers `rateLimitOnError()` + `501 Syntax error in range`. `n-` gives `Last = math.MaxInt64`, `Open = true`; no clamping (the chunked reads stop at the last row). | `rfc-overview` |
| K-H2 | `func (c *ClientConnection) requireGroup() bool`: `rateLimitOnError()` + `412 No newsgroup selected`, returns false. | `rfc-reader`, `rfc-overview` |
| K-H3 | GROUP/LISTGROUP select via `selectableGroup`/`selectGroup`, `currentArticle = low` (0 for an empty group); the number form of ARTICLE/HEAD/BODY/STAT sets it (BUG-1); bare STAT uses it (RFC-3); `currentArticle == 0` = invalid (420). | `rfc-reader` |
| K-H4 | `getServerCapabilities` (hardening D5): slice variable **`capabilities`**; `"VERSION 2", "READER"` unconditional; then optional `"AUTHINFO USER"`; then **one** `capabilities = append(capabilities, "LIST ACTIVE NEWSGROUPS", "XOVER", "XHDR")`; then `POST`, `IHAVE` under `c.postingAllowed()`. | wave 2 step A |
| K-H5 | (a) `overviewLine(o *models.Overview) string`: 8 TAB fields, no trailing TAB, **no Xref**; (b) any-header policy (decision 3) with `database.HeaderValueFromJSON(headersJSON, field string) string` and `sanitizeOverviewField(s string) string`; (c) `(*Database).GetOverviewsRange(groupDB, startNum, endNum int64, limit int) ([]*models.Overview, error)` and `(*Database).GetHeaderFieldRange(groupDB, field string, startNum, endNum int64, limit int) ([]database.HeaderFieldValue, error)` (`HeaderFieldValue{ArticleNum int64; Value string}`), both `hide = 0`, ascending, `LIMIT`; `database.OverviewRangeChunk = 1000`; (d) data blocks: `c.newDataBlock()` with `.Line(s) error` / `.Close() error`, and `c.sendDataBlock(code int, msg string, lines []string) error` for short lists; (e) open-range end `math.MaxInt64`; (f) **no** shared core that takes reply codes → `rfc-overview`'s own loop is mandatory. | `rfc-overview`, `rfc-lists` |
| K-H6 | The per-article-command throttle is `if c.server.ArticleCmdDelay > 0 { time.Sleep(c.server.ArticleCmdDelay) }` (default 25 ms). | `rfc-reader`, `rfc-overview` |
| K-H7 | Message-id lookup: the message-id branch of `(*ClientConnection).resolveArticleArg(args)` (current group via `database.NNTPArticleByMessageID`, `local430`, processor + `visibleInOtherGroup`; it sends `rateLimitOnError()` + `430 No article with that message-id` itself and gives reply number 0 outside the current group). Wave 0 records its exact return signature. Message-id syntax: `common.ValidMessageID(s string) bool` (package `internal/common`, defined by `nntp-intake-injection` S1). | `rfc-overview`, `rfc-stream` |
| K-H8 | Group DB of the selected group: `c.server.DB.GetGroupDB(c.currentGroup)` (the name passed `selectableGroup` at GROUP/LISTGROUP), `defer groupDB.Return()`; faults → `403 Internal fault` (no delay); a DB error after a multi-line status line returns the error (connection closed, decision 15). | `rfc-reader`, `rfc-overview` |
| K-H9 | LIST: `(*Database).NNTPActiveNewsgroups() ([]*models.Newsgroup, error)` (`name, description, last_article, low_water, COALESCE(status,'y')` of active groups, ordered by name); ACTIVE line `name last_article max(low_water,1) status`, status = `newsgroups.status` when it is `y`/`m` **and** `c.postingAllowed()`, else `n` (decision 14, client-dependent); NEWSGROUPS line `name description` with TAB/CR/LF in the description replaced by spaces; status text `215 …` as hardened `handleList`. No name predicate parameter. | `rfc-lists` |
| K-H10 | Visibility predicate: the literal `AND hide = 0` (decision 2; every NNTP read query). | `rfc-reader` |
| K-H11 | Connection plumbing: the `writer` field is **removed** (one writer: `c.textConn.W`); `readCommandLine()` reads `c.textConn.R` (a 4096-byte `bufio.Reader` from `textproto.NewConn`) on every loop iteration; `var errQuit` makes `Handle` return nil without a reply; `Stop()` closes the raw `net.Conn`s it tracked at accept and never reads `c.conn`, so swapping `c.conn` needs no mutex; the TLS-port config is built inline in `Start()` (no reusable builder). | `rfc-starttls` |
| K-H12 | Greeting `sendWelcome()`; `UpdateDeadlines()` sets the **read** deadline only (write deadlines via `setWriteDeadline` per flush); `commandMaxArgs map[string]int` in `internal/nntp/nntp-server-commands.go` holds **maximum** argument counts (checked by `argCountOK` before dispatch: `501 Syntax error: too many arguments`); `readerCommands` already lists NEXT, LAST, OVER, HDR, NEWGROUPS, NEWNEWS (480 under `-readauth`); `knownStatCommands` already contains DATE NEXT LAST NEWGROUPS NEWNEWS OVER HDR CHECK STARTTLS. | wave-1 tests, wave 2 step A |

### Contracts
- **K1: stable names this plan adds (reserved; free at 2fb7741; wave 0 re-checks at the merged base):**
  | Slice | Production names |
  |----|----|
  | `rfc-reader` | `handleDate`, `handleNext`, `handleLast`, `moveCurrentArticle`; DB `NNTPNextArticle`, `NNTPPrevArticle`, `query_NNTPNextArticle`, `query_NNTPPrevArticle` |
  | `rfc-lists` | `wildmatMatch`, `wildmatValid`, `wildmatSplit`, `wildmatGlob`, `handleListWildmat`, `activeLine`, `newsgroupsLine`, `handleNewGroups`, `parseNNTPDateTime`; DB `NNTPNewGroupsSince`, `query_NNTPNewGroupsSince` |
  | `rfc-overview` | `handleOver`, `handleHdr`, `handleListOverviewFmt`, `handleListHeaders`, `overviewFmtLines`, `listHeadersLines`, `hdrMetadata`, `isMessageIDArg` |
  | `rfc-stream` | `handleModeStream`, `handleCheck`, `streamingAllowed` |
  | `rfc-starttls` | `handleStartTLS`, `starttlsAvailable`, `starttlsConfigFor`, `starttlsCache`, `starttlsEntry`, `starttlsHandshakeTimeout`, `resetSessionState` |
  | reserved, not created | `handleNewNews`, `parseNewNewsDate` (D3) |

  Signatures (the wave-2 dispatch calls exactly these):
  ```go
  func (c *ClientConnection) handleDate(args []string) error
  func (c *ClientConnection) handleNext(args []string) error
  func (c *ClientConnection) handleLast(args []string) error
  func (c *ClientConnection) handleNewGroups(args []string) error
  func (c *ClientConnection) handleListWildmat(keyword string, args []string) error // keyword "ACTIVE" | "NEWSGROUPS", args = the words after it
  func (c *ClientConnection) activeLine(g *models.Newsgroup) string                // method: the status depends on c.postingAllowed()
  func newsgroupsLine(g *models.Newsgroup) string
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
- **K2: no struct changes.** No slice adds fields to `ClientConnection` or `NNTPServer`. State that must survive is a
  package-level cache (`starttlsCache`) or an existing field (`isTLS`, `conn`, `textConn`, `authenticated`, `user`,
  `authUsername`, `currentGroup`, `currentFirst`, `currentLast`, `currentArticle`).
- **K3: tests.**
  - Prefixes (free at 2fb7741; wave 0 re-checks): `rdr`/`TestRdr` (`rfc-reader`), `lst`/`TestLst`/`FuzzLst`
    (`rfc-lists`), `ovh`/`TestOvh` (`rfc-overview`), `stm`/`TestStm` (`rfc-stream`), `stls`/`TestStls`
    (`rfc-starttls`), `rfc`/`TestRfc` (`rfc-wire`). The same prefixes in the slice's `internal/database` test files. In
    `cmd/nntp-server` the wave-2 e2e file uses `e2eRfc`/`TestE2EBinaryRFC`.
  - Groups are named `rc<N>.<test>` through `hGroup` (`N`: 1 `rfc-reader`, 2 `rfc-lists`, 3 `rfc-overview`, 4
    `rfc-stream`, 5 `rfc-starttls`, 6 `rfc-wire`), message-ids via `hMessageID`, users via `hUser`.
  - The harness is read-only. No `t.Parallel()`, no package global mutated after `TestMain`, no write to a running
    server's fields or `Config` (build the server with the fields set, then `Serve`), no sleep over 100 ms.
  - **Wave-1 slices can't use the real dispatch** (edited only in wave 2), so each wave-1 test file defines a test-local
    session that serves one connection of a harness server and routes the slice's commands to the new handlers and
    everything else to the hardened `handleCommand`. Copy it with the slice prefix (K5 forbids sharing):
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
    					if !cc.argCountOK(strings.ToUpper(f[0]), f[1:]) { // what the real dispatch does first
    						cc.rateLimitOnError()
    						_ = cc.sendResponse(501, "Syntax error: too many arguments")
    						continue
    					}
    					if h(cc, f[1:]) != nil { // errQuit included
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
    (Until step A adds the new rows, `argCountOK` returns true for the new words; the handlers check their own
    argument counts.)
  - Removing a marker means replacing `knownBug(t, "RFC-1", <expr>)` by `if err := <expr>; err != nil { t.Fatal(err) }`
    (or `t.Fatal(err)` on the existing `err`), keeping the assertion. Step A does this inline so the tree is never red.
- **K4:** nobody changes `go.mod`/`go.sum`, `appVersion.txt`, `FuncStructList.txt`, migrations, `internal/config`, the
  harness, or files outside their ownership list. `gofmt` runs only on owned files (baseline:
  `internal/database/embedded_migrations.go`, `internal/web/web_admin_provider.go`, plus whatever the dependencies'
  Outcomes list).
- **K5:** no slice calls a helper another slice of the same wave adds (e.g. `rfc-overview` doesn't call `wildmatValid`;
  `rfc-starttls` doesn't call `streamingAllowed`).
- **K6: ids.** RFC-1 stays canonical. Bugs a slice finds that aren't RFC-1 get `T-rfc-commands-<n>` and are listed in
  its report (fixed only when they are in the slice's files and the fix is trivial).

### Component design (exact behavior per command)
Common rules for every new handler:
- Error delays: D10 (hardening decision 7) exactly. In the table, **(d)** marks a reply preceded by
  `c.rateLimitOnError()`; every other reply has no delay.
- Status lines never echo client input (A-nntp-rfc-6), except the message-id in the streaming replies.
- Internal/DB faults → `403 Internal fault`. A DB error **after** a multi-line status line returns the error (the
  connection closes); never a second status line inside a data block.
- Multi-line blocks go through `c.newDataBlock()` (`Line`/`Close`) or `c.sendDataBlock` (K-H5d).
- Argument counts above the maximum are answered by the dispatch table (step A rows) before the handler; the handlers
  also check their exact counts (minimum and maximum).

| Command | Arguments | Replies (in check order) | Data block |
|----|----|----|----|
| `DATE` | none (else (d) `501 Syntax error`) | `111 <time.Now().UTC().Format("20060102150405")>` | — |
| `NEXT` | none (else (d) 501) | (d) `412` via `requireGroup`; (d) `420 Current article number is invalid` when `currentArticle == 0`; `ArticleCmdDelay` sleep; `GetGroupDB` (fault → `403`); `NNTPNextArticle(cur)`: `sql.ErrNoRows` → `421 No next article in this group` (no delay); other error → `403`; else `currentArticle = n` and `223 n <message-id> Article found` | — |
| `LAST` | none (else (d) 501) | as NEXT with `NNTPPrevArticle`; none → `422 No previous article in this group` (no delay). On 412/420/421/422/403 the current group and article are unchanged (§6.1.3.2) | — |
| `NEWGROUPS` | `date time [GMT]` (2 or 3 words; 3rd must be `GMT`, case-insensitive; else (d) `501 Syntax: NEWGROUPS date time [GMT]`) | `parseNNTPDateTime` error → (d) `501 Invalid date or time`; DB error → `403`; else `231 List of new newsgroups follows` | `c.activeLine(g)` per group, ordered by name |
| `LIST ACTIVE <wildmat>` / `LIST NEWSGROUPS <wildmat>` | exactly one wildmat (else (d) 501) | `!wildmatValid` → (d) `501 Invalid wildmat`; `NNTPActiveNewsgroups` error → `403`; else `215` with hardened `handleList`'s status text | `c.activeLine(g)` / `newsgroupsLine(g)` for every group with `wildmatMatch(p, g.Name)`, in the source's order |
| `LIST OVERVIEW.FMT` | none (else (d) 501) | `215 Order of fields in overview database` | `Subject:`, `From:`, `Date:`, `Message-ID:`, `References:`, `:bytes`, `:lines` |
| `LIST HEADERS [MSGID\|RANGE]` | 0 or 1 word, `MSGID` or `RANGE` (case-insensitive), else (d) 501; both forms alike (§8.6.2 "MUST ignore any argument") | `215 Headers and metadata items supported` | `:`, `:bytes`, `:lines` |
| `OVER <message-id>` | `<…>` | `ArticleCmdDelay` sleep; `resolveArticleArg` message-id branch (it sends (d) `430` on a miss); `224 Overview information follows` | one `overviewLine` built from the article, number = the reply number (0 outside the current group, §8.3.2) |
| `OVER [range]` | 0 or 1 word (else (d) 501) | (d) `412` via `requireGroup`; no arg: (d) `420` when `currentArticle == 0`; range: `parseRangeArg` error → (d) `501 Syntax error in range`, `Empty()` → (d) `423 No articles in that range`; `GetGroupDB` fault → `403`; chunk loop (lazy status): no row at all → (d) `423` (range) or (d) `420` (no-arg form, current article gone); else `224 Overview information follows` | `overviewLine` per row, ascending |
| `HDR field <message-id>` | field + `<…>` | metadata other than `:bytes`/`:lines` → (d) `503 Metadata item not supported`; `ArticleCmdDelay` sleep; `resolveArticleArg` message-id branch ((d) `430` on a miss); `225 Headers follow` | one line `n value` (just `n` when the value is empty) |
| `HDR field [range]` | field + 0/1 word (no field or more → (d) 501) | (d) 503 as above; then as OVER with `GetHeaderFieldRange` and `225 Headers follow` | `n value` per row (just `n` when empty, hardening decision 3) |
| `MODE STREAM` | nothing after STREAM (else (d) 501, normally answered by the table) | no processor → (d) `501 MODE STREAM not supported`; `!streamingAllowed()` → `502 Streaming not permitted`; else `203 Streaming permitted` | — |
| `CHECK <id>` | exactly 1 (else (d) 501) | processor nil → (d) `500 <id> CHECK not supported on this server` (`500 CHECK not supported …` without an argument); `len(args) != 1` → (d) `501 CHECK requires exactly one message-id`; not authenticated → `480 <id> Transfer permission denied`; user without posting → `502 <id> Transfer not permitted`; `!common.ValidMessageID(id)` → `438 <id> Rejected (invalid message-id)`; `CheckMessageID`: Pass `238 <id> Send article`, Dupes `438 <id> Not wanted`, Retry `431 <id> Retry later`, Error `431 <id> Retry later` (log `[NNTP] CHECK history error for <id>`) | — |
| `STARTTLS` | none (else (d) `501 STARTTLS takes no arguments`) | `c.isTLS` → `502 STARTTLS not allowed with active TLS layer`; authenticated → `502 STARTTLS not allowed after authentication`; no cert → `502 STARTTLS not available`; cert load error → `580 Can not initiate TLS negotiation` (log); discard buffered input; `382 Continue with TLS negotiation`; handshake | — |

HDR value sources: range forms pass the field to `GetHeaderFieldRange` exactly as XHDR does (`:bytes` → `"bytes"`,
`:lines` → `"lines"` via `hdrMetadata(field) (column string, ok bool)`). Message-id form: `:bytes`/`:lines` →
`strconv.Itoa(a.Bytes|a.Lines)`; any other field → `sanitizeOverviewField(database.HeaderValueFromJSON(a.HeadersJSON,
field))`.

### Hand-over to the orchestrator (wave 2 step A applies these inline)
**Dispatch** (`handleCommand` in `internal/nntp/nntp-server-cliconns.go`, the hardened form of hardening D4). Replace
the `MODE` and `LIST` cases and add the rest before `default:`; `strings` is already imported:
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

**`commandMaxArgs`** (`internal/nntp/nntp-server-commands.go`, hardening D3/D6: this plan adds its rows; maximum only):
```go
	"DATE": 0, "NEXT": 0, "LAST": 0, "NEWGROUPS": 3, "OVER": 1, "HDR": 2, "CHECK": 1, "STARTTLS": 0,
```
Consequences to know: `CHECK a b` gets the table's `501 Syntax error: too many arguments` before the handler's
no-processor `500`; `MODE STREAM x` gets the table's 501 (`MODE` max 1); `LIST ACTIVE <wildmat>` and `LIST HEADERS
MSGID` fit `LIST` max 2.

**CAPABILITIES** (`getServerCapabilities`, K-H4). Replace hardening's line
`capabilities = append(capabilities, "LIST ACTIVE NEWSGROUPS", "XOVER", "XHDR")` by:
```go
	capabilities = append(capabilities, "LIST ACTIVE NEWSGROUPS OVERVIEW.FMT HEADERS", "XOVER", "XHDR", "OVER MSGID", "HDR")
```
and append after the `POST`/`IHAVE` block:
```go
	if c.streamingAllowed() { // == postingAllowed(): RFC 4644 §2.2
		capabilities = append(capabilities, "STREAMING")
	}
	if c.starttlsAvailable() { // cert configured, plaintext, before AUTHINFO: RFC 4642 §2.2.2
		capabilities = append(capabilities, "STARTTLS")
	}
	// DATE, NEXT, LAST and NEWGROUPS are part of READER. NEWNEWS is not advertised (D3).
```
Expected lists afterwards (VERSION 2, READER first; `AUTHINFO USER` per hardening's policy):
| State | Added by this plan | Never |
|----|----|----|
| plaintext, before auth, processor, cert set | LIST … OVERVIEW.FMT HEADERS, OVER MSGID, HDR, STREAMING, STARTTLS | NEWNEWS, TAKETHIS |
| plaintext, before auth, no cert | LIST …, OVER MSGID, HDR, STREAMING (with processor) | STARTTLS |
| after AUTHINFO, posting user | LIST …, OVER MSGID, HDR, STREAMING | STARTTLS |
| after AUTHINFO, user without posting | LIST …, OVER MSGID, HDR | STREAMING, STARTTLS |
| no processor | LIST …, OVER MSGID, HDR | STREAMING |
| TLS port, or after STARTTLS | LIST …, OVER MSGID, HDR, STREAMING (as above) | STARTTLS |

---

## Waves

### Wave 0 (inline, orchestrator on `plan-nntp-rfc-commands`)
Owned files: this plan file (`## Progress`) only.

1. **Preflight** as in the skill. `git mv .claude/plans/queued/nntp-rfc-commands.md .claude/plans/wip/`.
2. **P0 dependencies:** `.claude/plans/done/nntp-server-hardening.md` and `.claude/plans/done/nntp-intake-injection.md`
   exist; `git log --oneline testing-001 | grep -iE 'hardening|intake-injection'` shows both merges;
   `internal/nntp/nntp-cmd-range.go` exists and `grep -n 'func ValidMessageID' internal/common/*.go` finds intake's
   validator. Otherwise stop: this plan must not run first.
3. **Verify the K-H record** (pre-filled in `## Progress`): for each line, check the merged code and mark it `ok` or
   write the deviation. Helpful commands:
   ```bash
   sed -n '1,80p' internal/nntp/nntp-cmd-range.go
   grep -n 'func (c \*ClientConnection) \(requireGroup\|getServerCapabilities\|sendWelcome\|UpdateDeadlines\|handleCommand\|handleList\|resolveArticleArg\|visibleInOtherGroup\|newDataBlock\|sendDataBlock\|postingAllowed\|argCountOK\|readCommandLine\)' internal/nntp/*.go
   grep -n 'func overviewLine\|func sanitizeOverviewField\|errQuit\|commandMaxArgs\|knownStatCommands' internal/nntp/*.go
   grep -n 'func (db \*Database) \(GetOverviewsRange\|GetHeaderFieldRange\|NNTPActiveNewsgroups\|NNTPArticleByMessageID\)\|func HeaderValueFromJSON\|OverviewRangeChunk' internal/database/*.go
   grep -n 'writer\b' internal/nntp/nntp-server-cliconns.go           # K-H11: no writer field
   PUGLEAF_KNOWN_BUGS=1 go test -count=1 -run 'TestGrpGroupSeeded|TestArtNoArgument|TestArtCurrentArticle' ./internal/nntp/   # K-H3: must pass
   ```
   Record `resolveArticleArg`'s exact return signature (K-H7). A required item that differs → stop and ask.
4. **Names free:**
   ```bash
   for n in handleDate handleNext handleLast moveCurrentArticle NNTPNextArticle NNTPPrevArticle query_NNTPNextArticle \
     query_NNTPPrevArticle wildmatMatch wildmatValid wildmatSplit wildmatGlob handleListWildmat activeLine newsgroupsLine \
     handleNewGroups parseNNTPDateTime NNTPNewGroupsSince query_NNTPNewGroupsSince handleOver handleHdr \
     handleListOverviewFmt handleListHeaders overviewFmtLines listHeadersLines hdrMetadata isMessageIDArg \
     handleModeStream handleCheck streamingAllowed handleStartTLS starttlsAvailable starttlsConfigFor starttlsCache \
     starttlsEntry starttlsHandshakeTimeout resetSessionState handleNewNews parseNewNewsDate; do
     grep -rnw "$n" --include=*.go internal cmd; done                  # prints nothing
   grep -rnoE '\b(rdr|lst|ovh|stm|stls|rfc|e2eRfc)[A-Z]' --include=*_test.go internal cmd   # prints nothing
   ```
   If a dependency took a name, rename ours with an `rfc` infix and record the rename.
5. **Baseline** `## Checks` on the untouched branch; record PASS/FAIL and the `PUGLEAF_KNOWN_BUGS=1` id list (RFC-1 must
   be in it; the rest is what the dependencies' Outcomes left).
6. Commit on `plan-nntp-rfc-commands`: `plan(nntp-rfc-commands): wave-0 contract check`. Record the base SHA.

### Wave 1 (5 parallel `pugleaf-implementer` slices)

Common rules for every wave-1 slice:
- Read the Context rows for your labels, the Design (component table, K-H record as verified in `## Progress`) and your
  owned files' neighbors before editing. Follow K1–K6 and D10.
- Don't touch `nntp-server-cliconns.go` or `nntp-server-commands.go`: the dispatch comes in wave 2. Test through
  `<p>Session` (K3).
- Checks (in your worktree):
  ```bash
  gofmt -l internal/nntp internal/database cmd/nntp-server   # none of your owned files listed
  go vet ./...
  go build ./...
  go test -race -count=3 -timeout 600s -run '<YourTestPrefix>' ./internal/nntp/ ./internal/database/
  go test -race -count=1 -timeout 900s ./internal/nntp/... ./internal/database/... ./cmd/nntp-server/...
  git status --short                                         # clean
  ```
- **Reviewer task:** each reply code and delay against the component table, D10 and the RFC section; no write after a
  status line on a DB error; `defer rows.Close()`/`groupDB.Return()`, `rows.Err()`; no data race (`-race -count=3`);
  no edit outside the ownership list.

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
1. **`nntp-cmd-date.go`:** `handleDate(args)`: `len(args) != 0` → `rateLimitOnError()` + `501 Syntax error`; else
   `c.sendResponse(111, time.Now().UTC().Format("20060102150405"))`. The doc comment cites RFC 3977 §7.1.2 (same clock
   as article arrival and group creation: the DB defaults `CURRENT_TIMESTAMP` are UTC from the same host clock).
2. **`internal/database/nntp_nextlast.go`** (package `database`), written literally (K-H10):
   ```go
   const query_NNTPNextArticle = `SELECT article_num, message_id FROM articles WHERE article_num > ? AND hide = 0 ORDER BY article_num ASC LIMIT 1`
   const query_NNTPPrevArticle = `SELECT article_num, message_id FROM articles WHERE article_num < ? AND hide = 0 ORDER BY article_num DESC LIMIT 1`
   ```
   Both functions use `RetryableQueryRowScan(groupDB.DB, q, []interface{}{cur}, &num, &msgID)` and return its error
   unchanged (`sql.ErrNoRows` when none). `article_num` is the rowid primary key, so each call is one index seek. No
   logging per call.
3. **`nntp-cmd-nextlast.go`:** `handleNext`/`handleLast` call `moveCurrentArticle(args []string, next bool) error`:
   1. `len(args) != 0` → `rateLimitOnError()` + `501 Syntax error`.
   2. `if !c.requireGroup() { return nil }` (412, delayed by `requireGroup`).
   3. `c.currentArticle == 0` → `rateLimitOnError()` + `420 Current article number is invalid`.
   4. `if c.server.ArticleCmdDelay > 0 { time.Sleep(c.server.ArticleCmdDelay) }` (K-H6).
   5. `groupDB, err := c.server.DB.GetGroupDB(c.currentGroup)`; error → log `[NNTP]` + `403 Internal fault` (no delay);
      `defer groupDB.Return()`.
   6. `NNTPNextArticle`/`NNTPPrevArticle(groupDB, c.currentArticle)`: `errors.Is(err, sql.ErrNoRows)` → `421 No next
      article in this group` / `422 No previous article in this group` (**no delay**, D10); other error → log `[NNTP]
      NEXT/LAST in <group>: <err>` + `403 Internal fault`. `currentArticle` stays unchanged on every error path.
   7. Success: `c.currentArticle = num`; `c.sendResponse(223, fmt.Sprintf("%d %s Article found", num, msgID))`.
4. **Tests** `internal/nntp/nntp-rfc-reader_test.go` (prefix `rdr`; `rdrSession` with extra `DATE`, `NEXT`, `LAST`):
   - `TestRdrDate`: `111` + 14 digits, parses with `time.Parse("20060102150405", …)`, within 5 min of UTC now;
     `DATE x` → 501.
   - `TestRdrNextLast` on `hGroup(t, "rc1.nextlast", 6, …)` with 1, 2, 5 skipped (articles 3, 4, 6):
     - no group: NEXT → 412, LAST → 412;
     - after GROUP (current = 3): LAST → 422, NEXT → `223 4 <id4>`, NEXT → `223 6 <id6>`, NEXT → 421, LAST →
       `223 4 <id4>`, bare STAT → `223 4` (K-H3), `NEXT x` → 501;
     - after a failing NEXT (421) at 6, STAT still answers `223 6`;
     - empty group (`hGroup(t, "rc1.empty", 0, nil)`): NEXT → 420, LAST → 420;
     - `ARTICLE <id of 6>` after `STAT 3` leaves the current article at 3: NEXT → `223 4`.
   - `TestRdrNextLastDelays`: on a server with `hServerOpts{ErrorDelay: 300 * time.Millisecond}`, a NEXT that gets 421
     and a LAST that gets 422 each answer within 150 ms; a NEXT without a group (412) takes at least 300 ms.
   - `TestRdrNextLastHidden`: `UPDATE articles SET hide = 1 WHERE article_num = 4` on `GetGroupDB(g).DB`; from 3 NEXT →
     `223 6`, from 6 LAST → `223 3`.
   - `internal/database/nntp_nextlast_test.go` (prefix `rdr`, `w0DB`/`w0Name`): a group DB from
     `GetGroupDB(w0Name("rdr.next"))` with articles 10, 20, 30 inserted by raw SQL (all NOT NULL columns as in
     `nntp-audit-tests` D2) and 25 inserted with `hide = 1`: next of 10 → 20, next of 20 → 30 (skips hidden 25), next
     of 30 → `sql.ErrNoRows`, prev of 10 → `sql.ErrNoRows`, prev of 30 → 20, next of 0 → 10.

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
     `!` (the first element too: lenient extension, D8), which sets `neg`; after that optional `!` the element must be
     non-empty and must not contain `!`, `\`, `[`, `]`, a C0 control (0x00–0x1F) or DEL (0x7F); `ok=false` on any
     violation or when the pattern is not valid UTF-8 (`utf8.ValidString`).
   - `wildmatValid(pattern string) bool`: `pattern != ""` and `wildmatSplit` ok.
   - `wildmatGlob(pat, name string) bool`: anchored match over **runes** (`[]rune` of both), `?` = exactly one rune, `*` =
     zero or more runes, everything else literal. Iterative star backtracking (remember the last `*` and the name
     position it matched; on mismatch retry from there): O(len(pat)·len(name)), never recursive, so a hostile pattern
     can't blow the stack or go exponential.
   - `wildmatMatch(pattern, name string) bool`: walk the elements **from the right**; the first (rightmost) element
     whose `wildmatGlob` matches decides: not negated → true, negated → false; none matches → false (§4.2). Invalid
     patterns return false (callers check `wildmatValid` first).
2. **`nntp-wildmat_test.go`** (prefix `lst`):
   - `TestLstWildmatRFCExamples`: the §4.2/§4.4 table: `a*,!*b,*c*` with `aaa`→true, `abb`→false, `ccb`→true,
     `xxx`→false; `abc` matches only `abc`; `abc,def` matches `abc` and `def`; `"£"` matches `"£"` (2 octets, one
     character) and `?` matches `"£"`; `a*` / `a*b` / `a*,*b` / `a*,!*b` / `a*,!*b,c*` per §4.4; `*` matches `""`;
     `t3.*` matches `t3.x.y` but not `t3`.
   - `TestLstWildmatInvalid`: `""`, `a,,b`, `a,`, `,a`, `!`, `a!b`, `a\b`, `[ab]`, `a]`, `"\xff"`, `"a\tb"`, `"a\x00"`,
     `"a\x7f"` → `wildmatValid` false; `!a*` → valid, matches nothing.
   - `FuzzLstWildmat` (seed corpus only during `go test`): for valid patterns, `wildmatMatch` equals a reference built per
     element with `regexp.MustCompile("(?s)^" + <QuoteMeta with \* → .* and \? → .> + "$")` with the rightmost-match
     rule; never panics; finishes within 1 s for 512-byte inputs.
3. **`internal/database/nntp_newgroups.go`** (the column set of `NNTPActiveNewsgroups`, K-H9, plus the date filter):
   ```go
   const query_NNTPNewGroupsSince = `SELECT name, COALESCE(description,''), COALESCE(last_article,0),
   	COALESCE(low_water,1), COALESCE(status,'y')
   	FROM newsgroups
   	WHERE active = 1 AND created_at IS NOT NULL AND datetime(created_at) >= datetime(?)
   	ORDER BY name`
   ```
   `NNTPNewGroupsSince(since time.Time)` passes `since.UTC().Format("2006-01-02 15:04:05")`, uses `RetryableQuery` on
   the main DB handle the other `queries.go` main-DB readers use, `defer rows.Close()`, scans into
   `models.Newsgroup{Name, Description, LastArticle, LowWater, Status}` with `Active = true`, checks `rows.Err()`.
   `datetime()` normalizes both the `CURRENT_TIMESTAMP` form and Go's `2006-01-02 15:04:05.999999999-07:00` form.
4. **`nntp-cmd-list-wildmat.go`:**
   - `func (c *ClientConnection) activeLine(g *models.Newsgroup) string`: hardening's expression (K-H9, decision 14):
     ```go
     low := int64(g.LowWater)
     if low < 1 {
     	low = 1
     }
     status := "n"
     if c.postingAllowed() && (g.Status == "y" || g.Status == "m") {
     	status = g.Status
     }
     return fmt.Sprintf("%s %d %d %s", g.Name, g.LastArticle, low, status)
     ```
     If hardened `handleListActive` already formats through a helper, call that helper instead (same result).
   - `func newsgroupsLine(g *models.Newsgroup) string`: `g.Name + " " + strings.NewReplacer("\t", " ", "\r", " ", "\n",
     " ").Replace(g.Description)` (hardening's TAB/CR/LF → space cleanup).
   - `handleListWildmat(keyword string, args []string) error`: `len(args) != 1` → `rateLimitOnError()` + `501 Syntax
     error`; `!wildmatValid(args[0])` → `rateLimitOnError()` + `501 Invalid wildmat`; `c.server.DB.NNTPActiveNewsgroups()`
     (error → log `[NNTP]` + `403 Internal fault`); send hardened `handleList`'s 215 status text; write
     `c.activeLine(g)`/`newsgroupsLine(g)` for every matching group through `c.newDataBlock()`; `Close()`. A write error
     after the 215 returns the error.
5. **`nntp-cmd-newgroups.go`:**
   - `parseNNTPDateTime(date, clock string, gmt bool, now time.Time) (time.Time, error)`:
     - `date` is 6 or 8 ASCII digits, `clock` exactly 6; anything else → error.
     - 8 digits: year `yyyy` in 1900..9999 (§7.3.2 "xx … (19-99)"). 6 digits: `yy`; `cy := now.UTC().Year()`; year =
       `cy - cy%100 + yy` if `yy <= cy%100`, else that minus 100.
     - month 01-12, day 01-31, hour 00-23, minute 00-59, second 00-60 (60 → 59, leap second).
     - `loc := time.UTC` if gmt else `time.Local`; `t := time.Date(y, m, d, hh, mi, ss, 0, loc)`; reject when
       `t.Day() != d` or `t.Month() != m` (e.g. 20260231).
   - `handleNewGroups(args []string) error`: `len(args)` 2 or 3 and `args[2]` (if present) `GMT`
     (`strings.EqualFold`), else `rateLimitOnError()` + `501 Syntax: NEWGROUPS date time [GMT]`; parse with
     `now = time.Now()` (error → `rateLimitOnError()` + `501 Invalid date or time`); `NNTPNewGroupsSince(t)` (error →
     log `[NNTP]` + `403 Internal fault`); `231 List of new newsgroups follows`; `c.activeLine(g)` per row through the
     data-block writer; `Close()`.
6. **Tests** `internal/nntp/nntp-rfc-lists_test.go` (prefix `lst`; `lstSession` with extra `NEWGROUPS` and `LIST` → a
   wrapper that routes `ACTIVE|NEWSGROUPS <wildmat>` to `handleListWildmat` and everything else to `cc.handleList(args)`):
   - `TestLstListWildmat`: groups `rc2.lw.<tok>.a`, `.b`, `rc2.other.<tok>`; `LIST ACTIVE rc2.lw.<tok>.*` lists a and b
     only; `LIST NEWSGROUPS rc2.lw.<tok>.a` lists only a with description `harness group <a>`; `LIST ACTIVE
     rc2.lw.<tok>.*,!*.b` lists only a; `LIST ACTIVE a,,b` → 501.
   - `TestLstMatchesList`: for a seeded group g, its line in `LIST ACTIVE <g>` equals its line in plain `LIST ACTIVE`;
     the same for NEWSGROUPS; `LIST ACTIVE *` returns exactly the lines of `LIST ACTIVE` (same order); with a processor
     and without one (status `y` vs `n` follows `postingAllowed`, decision 14).
   - `TestLstNewGroups`: g = `hGroup(t, "rc2.ng", 2, nil)`; `NEWGROUPS <now-48h as yyyymmdd hhmmss> GMT` → 231 listing g
     as `g 2 1 <status>`, equal to g's `LIST ACTIVE` line; `NEWGROUPS 20991231 235959 GMT` → 231 empty; the 6-digit
     form `NEWGROUPS <yymmdd of now-48h> <hhmmss> GMT` lists g; `NEWGROUPS 20261301 000000 GMT` → 501; `NEWGROUPS
     20260101 000000 UTC` → 501; `NEWGROUPS 20260101` → 501.
   - `TestLstParseNNTPDateTime`: table incl. the century rule with `now` = 2026-10-11 (`261011` → 2026, `270101` →
     1927, `991231` → 1999, `000101` → 2000), `20260230` → error, `235960` → second 59, `gmt=false` uses `time.Local`.
   - `internal/database/nntp_newgroups_test.go` (prefix `lst`; the shared `w0DB` holds other tests' groups, so assert
     contains / doesn't contain, never exact counts): insert `w0Name("lst.ng")` groups A, B, C (C inactive) with
     `InsertNewsgroup`; set A's `created_at` to `'2001-02-03 04:05:06'`, B's to the Go format `'2001-02-03
     05:05:06.123456789+01:00'` (04:05:06 UTC), C's to `'2001-02-03 04:05:06'`, and A's `last_article` to NULL, by raw
     SQL on the main DB; `NNTPNewGroupsSince(2001-02-03 04:05:00 UTC)` contains A and B, not C, and A has
     `LastArticle == 0`; since 04:05:07 UTC contains neither A nor B.

Acceptance: E03, E04 (through `lstSession`).

---

#### Slice `rfc-overview`: OVER, HDR, LIST OVERVIEW.FMT, LIST HEADERS
Labels: RFC-1g, RFC-1h, RFC-1i, RFC-1j. E-tag `rfc-overview`.

Owned files (all new):
- `internal/nntp/nntp-cmd-over-hdr.go`
- `internal/nntp/nntp-rfc-overview_test.go`

Changes (`nntp-cmd-over-hdr.go`):
1. `isMessageIDArg(s string) bool`: `len(s) >= 3 && s[0] == '<' && s[len(s)-1] == '>'` (the test hardened
   `resolveArticleArg` uses).
2. `hdrMetadata(field string) (column string, ok bool)`: `:bytes` → `"bytes"`, `:lines` → `"lines"` (case-insensitive);
   any other value starting with `:` → `ok = false`.
3. `handleOver(args)`:
   1. `len(args) > 1` → `rateLimitOnError()` + `501 Syntax error`.
   2. Message-id form (`len(args) == 1 && isMessageIDArg(args[0])`): `ArticleCmdDelay` sleep (K-H6); call the
      message-id branch of `resolveArticleArg` (K-H7 signature from the record); a miss returns (it sent the 430);
      build `&models.Overview{ArticleNum: num, Subject: a.Subject, FromHeader: a.FromHeader, DateString: a.DateString,
      MessageID: a.MessageID, References: a.References, Bytes: a.Bytes, Lines: a.Lines}`; `c.sendDataBlock(224,
      "Overview information follows", []string{overviewLine(o)})`. The message-id form never changes `currentArticle`.
   3. Otherwise `if !c.requireGroup() { return nil }`. No argument: `c.currentArticle == 0` → `rateLimitOnError()` +
      `420 Current article number is invalid`, else `r = articleRange{First: cur, Last: cur}`, `emptyCode = 420`. With
      an argument: `r, err := c.parseRangeArg(args[0])`; err → `rateLimitOnError()` + `501 Syntax error in range`;
      `r.Empty()` → `rateLimitOnError()` + `423 No articles in that range`; `emptyCode = 423`.
   4. The chunk loop (step 5) with `GetOverviewsRange` and `overviewLine`, status `224 Overview information follows`.
4. `handleHdr(args)`:
   1. `len(args) == 0 || len(args) > 2` → `rateLimitOnError()` + `501 Syntax error`.
   2. `strings.HasPrefix(args[0], ":")` and `!hdrMetadata` ok → `rateLimitOnError()` + `503 Metadata item not supported`.
   3. Message-id form (`len(args) == 2 && isMessageIDArg(args[1])`): `ArticleCmdDelay` sleep; `resolveArticleArg`
      message-id branch; value per the Design ("HDR value sources"); `c.sendDataBlock(225, "Headers follow",
      []string{line})` with `line = strconv.FormatInt(num, 10)` plus `" " + value` when the value is non-empty.
   4. Range/current forms as OVER step 3 (argument `args[1]`, field = `hdrMetadata` column or `args[0]`), then step 5
      with `GetHeaderFieldRange` and `225 Headers follow`; each line `n value` with `sanitizeOverviewField(value)`, or
      just `n` when the value is empty (hardening decision 3).
5. **Chunk loop** (mandatory: hardening has no shared core, K-H5f). `groupDB, err := c.server.DB.GetGroupDB(c.currentGroup)`
   (error → log `[NNTP]` + `403 Internal fault`); `defer groupDB.Return()`. `first := r.First`. Read the **first** chunk
   (`limit = database.OverviewRangeChunk`) before any status line: a DB error → log + `403 Internal fault`; zero rows →
   `rateLimitOnError()` + the `emptyCode` reply (`423 No articles in that range` / `420 Current article number is
   invalid`). Otherwise send the status line, then loop: write every row through one `c.newDataBlock()`; let `last` be
   the article number of the chunk's last row; **stop** when the chunk has fewer than `OverviewRangeChunk` rows, or
   `last == math.MaxInt64`, or `last >= r.Last`; else read the next chunk from `last+1` to `r.Last`. A DB error after
   the status line returns the error (connection closed, hardening decision 15). After the loop `Close()` the block.
   No clamping to the group's marks: `n-` (`Last = MaxInt64`) ends at the last row.
6. `overviewFmtLines()` returns the 7 fixed lines (no `Xref:full`, K-H5a); `handleListOverviewFmt(args)`: `len(args) !=
   0` → `rateLimitOnError()` + 501; else `c.sendDataBlock(215, "Order of fields in overview database",
   overviewFmtLines())`.
7. `listHeadersLines()` returns `":"`, `":bytes"`, `":lines"` (any-header policy, K-H5b); `handleListHeaders(args)`:
   more than one word, or a word other than `MSGID`/`RANGE` → `rateLimitOnError()` + 501; else
   `c.sendDataBlock(215, "Headers and metadata items supported", listHeadersLines())`.
8. **Tests** `internal/nntp/nntp-rfc-overview_test.go` (prefix `ovh`; `ovhSession` with extra `OVER`, `HDR` and `LIST` →
   wrapper routing `OVERVIEW.FMT`/`HEADERS` to the new handlers and everything else to `cc.handleList`):
   - `TestOvhOver` on `hGroup(t, "rc3.over", 5, …)` with 2 skipped: no group → 412; after GROUP: `OVER` (current = low)
     → 224 with one line numbered 1; `OVER 1-5` → numbers 1, 3, 4, 5; `OVER 3-` → 3, 4, 5; `OVER 2` → 423; `OVER 5-3`
     → 423; `OVER 50-60` → 423; `OVER x` → 501; every line has exactly 8 TAB-separated fields with field 1 the number.
   - `TestOvhOverMsgID`: `OVER <id of 3>` with the group selected → first field `3`; with no group selected and an
     `hFakeProcessor` whose `findFn` returns the article (its `NewsgroupsPtr` set to the group, as hardening's
     `visibleInOtherGroup` re-reads it there) → first field `0`; unknown id with `findErr` wrapping
     `ErrArticleNotFound` → 430.
   - `TestOvhHdr`: `HDR Subject 1-5` → 225 and `n Test article n` for 1, 3, 4, 5; `HDR :lines 3` → `3 <lines>`; `HDR
     :foo 3` → 503; `HDR Subject 50-60` → 423; `HDR Subject` in an empty group → 420; `HDR Subject <id of 4>` → `4 Test
     article 4` (group selected) or `0 Test article 4` (no group, via `findFn`); `HDR` → 501.
   - `TestOvhHdrAnyHeader`: an article whose `headers_json` holds `X-Ovh-Test: v1` → `HDR X-Ovh-Test 1` → `1 v1` (range
     form) and the same by message-id; an article without it → the line `n` or `n ` (both accepted, RFC 3977 §8.5.2).
   - `TestOvhOverviewFmt`: `LIST OVERVIEW.FMT` → 215 and exactly the 7 fixed lines in order; `LIST OVERVIEW.FMT x` → 501.
   - `TestOvhListHeaders`: 215 with exactly `:`, `:bytes`, `:lines` (any order); `LIST HEADERS MSGID` / `RANGE` give the
     same set; `LIST HEADERS FOO` → 501.
   - `TestOvhLargeRange`: 2 500 articles inserted in one transaction (copy the approach of `ovrBulkGroup`,
     `nntp-server-overview_test.go:143`, with the `ovh` prefix), two of them `hide = 1`: `OVER 1-` → 2 498 lines in
     order (crosses two chunk boundaries) and `HDR Subject 1-` → 2 498 lines, each within 10 s; `OVER
     2499-9223372036854775807` → the remaining lines and returns promptly (no walk over unused numbers).

Acceptance: E05, E06 (through `ovhSession`).

---

#### Slice `rfc-stream`: MODE STREAM, CHECK
Labels: RFC-1k, RFC-1l. E-tag `rfc-stream`.

Owned files (all new):
- `internal/nntp/nntp-cmd-stream.go`
- `internal/nntp/nntp-rfc-stream_test.go`

Changes (`nntp-cmd-stream.go`):
1. `func (c *ClientConnection) streamingAllowed() bool { return c.postingAllowed() }`. Doc comment: RFC 4644 §2.2 and
   D5; the single condition for the STREAMING capability and the 203.
2. `handleModeStream(args)`: `len(args) != 0` → `rateLimitOnError()` + `501 Syntax error`; `c.server.Processor == nil`
   → `rateLimitOnError()` + `501 MODE STREAM not supported`; `!c.streamingAllowed()` → `502 Streaming not permitted`
   (no delay); else `203 Streaming permitted`. No state change (§2.3.2).
3. `handleCheck(args)`, in intake's IHAVE gate order (D5): `id := ""; if len(args) == 1 { id = args[0] + " " }`;
   processor nil → `rateLimitOnError()` + `500 ` + id + `CHECK not supported on this server`; `len(args) != 1` →
   `rateLimitOnError()` + `501 CHECK requires exactly one message-id`; `!c.authenticated` → `480 <id> Transfer
   permission denied`; `c.user != nil && !c.user.Posting` → `502 <id> Transfer not permitted`;
   `!common.ValidMessageID(args[0])` → `438 <id> Rejected (invalid message-id)`; then `switch
   c.server.Processor.CheckMessageID(args[0])`: `history.CasePass` → `238 <id> Send article`, `history.CaseDupes` →
   `438 <id> Not wanted`, `history.CaseRetry` → `431 <id> Retry later`, default → log `[NNTP] CHECK history error for
   <id>` + `431 <id> Retry later`. No delay on 480/502/238/431/438 (D10). CHECK never reads from the connection. If the
   merged IHAVE gates differ from intake's plan text, mirror the merged ones and record the difference.
4. **Tests** `internal/nntp/nntp-rfc-stream_test.go` (prefix `stm`; `stmSession` with extra `MODE` → wrapper: `STREAM`
   → `cc.handleModeStream(args[1:])`, else `cc.handleMode(args)`, and `CHECK`):
   - `TestStmModeStream`: with `hFakeProcessor` → 203; no processor → 501; authenticated non-posting user
     (`hUser(t, false, true)`) → 502; authenticated posting user → 203; `MODE READER` still answered by hardened
     `handleMode`.
   - `TestStmCheck`: posting user authenticated; `checkFn` returns per id: Pass → `238 <id>`, Dupes → `438 <id>`, Retry
     → `431 <id>`, `history.CaseError` → `431 <id>`; each reply's second word equals the id; `CHECK not-an-id` →
     `438 not-an-id` and `CheckMessageID` not called for it; unauthenticated → `480 <id> …`; non-posting → `502 <id> …`;
     no processor → `500 <id> …`; `CHECK` → 501; the fake recorded exactly the ids of the four CHECKs that passed the
     gates (`checked`).
   - `TestStmPipelined`: 50 CHECKs in one write (`c.send`) → 50 replies in order, each carrying its id (RFC 4644 §2.1);
     on a server with `hServerOpts{ErrorDelay: 200 * time.Millisecond}` and a `checkFn` returning `history.CaseDupes`,
     the 50 `438` replies arrive within 1 s (no delay on 431/438).
   - `TestStmCheckThenTakethis`: CHECK `<new>` → 238, then TAKETHIS of the same id with a valid article (the fake's
     `processResult = history.CasePass`) → `239 <id>`: CHECK leaves no state that breaks TAKETHIS.

Acceptance: E07 (through `stmSession`).

---

#### Slice `rfc-starttls`: STARTTLS and the cmd/nntp-server certificate flags
Label: RFC-1n. E-tag `rfc-starttls`.

Owned files:
- `internal/nntp/nntp-cmd-starttls.go` (new)
- `internal/nntp/nntp-rfc-starttls_test.go` (new)
- `cmd/nntp-server/main.go` (hardening's file, merged first: only the TLS flag help texts and the TLS block, located by
  the `nntpcertFile`/`nntpkeyFile` variables; at 2fb7741 `:40-41` and `:58-67`)

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
   - `starttlsAvailable() bool`: `!c.isTLS && !c.authenticated && c.server.Config != nil && c.server.Config.NNTP.TLSCert
     != "" && c.server.Config.NNTP.TLSKey != ""`.
   - `resetSessionState()`: `authenticated = false`, `user = nil`, `authUsername = ""`, `currentGroup = ""`,
     `currentFirst = 0`, `currentLast = 0`, `currentArticle = 0`, plus any other per-session field the merged
     `ClientConnection` has (RFC 4642 §2.2.2 "MUST discard any knowledge obtained from the client"). If a per-user
     connection slot was taken at AUTHINFO (hardening's `EnforceUserMaxConns`) it can't be: STARTTLS is refused after
     authentication.
   - `handleStartTLS(args)` in this order: `len(args) != 0` → `rateLimitOnError()` + `501 STARTTLS takes no arguments`;
     `c.isTLS` → `502 STARTTLS not allowed with active TLS layer`; `c.authenticated` → `502 STARTTLS not allowed after
     authentication`; no cert → `502 STARTTLS not available` (the three 502s without delay, D10); `cfg, err :=
     starttlsConfigFor(...)`; err → log `[NNTP] STARTTLS certificate: <err>` and `580 Can not initiate TLS negotiation`
     (no delay); `if n := c.textConn.R.Buffered(); n > 0 { log "[NNTP] STARTTLS from <addr>: discarding <n> pipelined
     bytes"; _, _ = c.textConn.R.Discard(n) }`; send `382 Continue with TLS negotiation`; `raw := c.conn`;
     `_ = raw.SetDeadline(time.Now().Add(starttlsHandshakeTimeout))`; `tc := tls.Server(raw, cfg)`; `tc.Handshake()`
     error → log `[NNTP] STARTTLS handshake with <addr> failed: <err>`, `_ = raw.Close()`, `return errQuit` (K-H11:
     `Handle` returns nil, no reply). Success: `_ = raw.SetDeadline(time.Time{})`; `c.conn = tc`; `c.textConn =
     textproto.NewConn(tc)` (the same construction as hardened `NewClientConnection`; no `writer` field any more; no
     mutex: `Stop()` closes the raw conn it tracked, which also ends the TLS conn); `c.isTLS = true`;
     `c.resetSessionState()`; `c.UpdateDeadlines()`; return nil **without** a greeting (§2.2.2).
2. **`cmd/nntp-server/main.go`:**
   - Help texts: `-nntpcertfile` → `"NNTP TLS certificate file for the TLS port and STARTTLS (/path/to/fullchain.pem)"`,
     `-nntpkeyfile` → `"NNTP TLS key file for the TLS port and STARTTLS (/path/to/privkey.pem)"`.
   - Replace the TLS block with (keep any hardening lines inside it that are not about cert/key):
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
     and add `"crypto/tls"` to the imports. Hardening's `Start()` validation (TLS port without cert → error) and its
     inline TLS-port config stay unchanged.
3. **Tests** `internal/nntp/nntp-rfc-starttls_test.go` (prefix `stls`):
   - `stlsWriteCert(t) (certFile, keyFile string, der []byte)`: ECDSA P-256 self-signed for `127.0.0.1`/`localhost` (as
     `sessSelfSignedTLS`, `nntp-server-session_test.go:131`), PEM-encoded into `t.TempDir()`.
   - `stlsNewServer(t, certFile, keyFile string, proc ArticleProcessor) *hServer`: a copy of `newTestServer`
     (`nntp-server-harness_test.go:169-203`) whose `cfg.NNTP.TLSCert/TLSKey` are set **before** `NewNNTPServer`.
   - `stlsSession` with extra `STARTTLS`; `stlsUpgrade(t, c *hClient)`: `tc := tls.Client(c.Conn,
     &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})` (`//nolint:gosec // test client`),
     `tc.Handshake()`, then `c.Conn = tc; c.R = bufio.NewReaderSize(tc, 1<<20)`.
   - `TestStlsStartTLS`: GROUP g → 211; `STARTTLS` → 382; upgrade; the peer certificate equals `der`; a bare `STAT` → 412 (the session
     reset dropped the current group; NEXT isn't routed by `stlsSession`); `STARTTLS` → 502; `AUTHINFO USER/PASS`
     over TLS → 281; `QUIT` → 205.
   - `TestStlsRefusals`: server without cert → `STARTTLS` → 502; with cert after `AUTHINFO` (posting user) → 502;
     `STARTTLS x` → 501; with a cert path that doesn't exist → 580 and the session continues in plaintext (`GROUP g` →
     211 on the same connection). Delay check (D10): on `newTestServer(t, hServerOpts{ErrorDelay:
     300 * time.Millisecond})` (no cert) `STARTTLS` → 502 within 150 ms, while `STARTTLS x` → 501 takes at least 300 ms.
   - `TestStlsPipelinedDiscarded`: send `"STARTTLS\r\nGROUP <g>\r\n"` in one write; read `382`; upgrade; `CAPABILITIES`
     over TLS returns `101` as the **first** reply (no stray `211`).
   - `TestStlsHandshakeFailure`: after `382` the client writes `"GARBAGE\r\n"` instead of a ClientHello; the server
     closes the connection within 2 s (EOF/reset), and a new connection to the same server still gets its greeting.
   - `TestStlsAvailable`: table over `isTLS`, `authenticated`, cert set/unset: `a, b := net.Pipe()` (both closed in
     `t.Cleanup`), `cc := NewClientConnection(a, hs.S, isTLS)` on a server from `stlsNewServer` (cert set) or
     `newTestServer` (unset), set `cc.authenticated`, compare `cc.starttlsAvailable()` with the expectation.

Acceptance: E09 (through `stlsSession`).

---

### Wave 2

#### Step A (inline, orchestrator on `plan-nntp-rfc-commands`, after merging wave 1)
Owned (inline only, exactly these edits):
- `internal/nntp/nntp-server-cliconns.go`: the dispatch switch and `getServerCapabilities` (the D4 exception).
- `internal/nntp/nntp-server-commands.go`: the `commandMaxArgs` literal (hardening D6).
- `internal/nntp/nntp-server-group_test.go`, `nntp-server-overview_test.go`, `nntp-server-session_test.go`: the 10
  RFC-1 marker lines, the group test's header comment, and `sessFuzzMultiCodes`.
- `internal/nntp/nntp-cmd-basic.go` (`handleHelp` lines only) and `internal/nntp/README.md` (command list only).

1. Merge the wave-1 branches in slice order (`rfc-reader`, `rfc-lists`, `rfc-overview`, `rfc-stream`, `rfc-starttls`),
   running `## Checks` after each merge (the dispatch is unchanged, so the RFC-1 markers still skip).
2. Apply the **Dispatch**, **`commandMaxArgs`** and **CAPABILITIES** hand-overs exactly as written in Design.
3. In the same commit, remove the 10 RFC-1 markers (K3 replacement; find each by test name, lines at 2fb7741), so the
   tree is never red:
   | File | Test | Line | Replacement |
   |----|----|----|----|
   | `nntp-server-session_test.go` | `TestSessCapabilities/processor-streaming-complete` | 272 | `if err != nil { t.Fatal(err) }` |
   | `nntp-server-group_test.go` | `TestGrpListWildmat/{active,newsgroups}` | 596 | `if err := errors.Join(errs...); err != nil { t.Fatal(err) }` |
   | same | `TestGrpListOverviewFmt` | 637 | `if err != nil { t.Fatal(err) }` |
   | same | `TestGrpDate` | 656 | `if err != nil { t.Fatal(err) }` |
   | same | `TestGrpNextLast/no-group` | 690 | `if err := errors.Join(expect(t, c, "NEXT", 412, 0), expect(t, c, "LAST", 412, 0)); err != nil { t.Fatal(err) }` |
   | same | `TestGrpNextLast/walk` | 710 | `if err != nil { t.Fatal(err) }` (the RFC-3 note wrapper at `:706-708` may stay; it only adds text) |
   | same | `TestGrpNextLast/empty-group` | 720 | as `no-group` with 420 |
   | same | `TestGrpNewGroups` | 751 | `if err := errors.Join(errs...); err != nil { t.Fatal(err) }` |
   | `nntp-server-overview_test.go` | `TestOvrOverHdr/OVER` | 688 | `if err != nil { t.Fatal(err) }` |
   | same | `TestOvrOverHdr/HDR` | 698 | `if err != nil { t.Fatal(err) }` |

   Group test header comment (`:4-10`): "the READER commands the server does not implement yet" → "the READER commands
   DATE (§7.1), NEXT/LAST (§6.1.3, §6.1.4), NEWGROUPS (§7.3) and NEWNEWS (§7.4; not implemented, not advertised)"; drop
   `RFC-1` from the id list. `sessFuzzMultiCodes` (`nntp-server-session_test.go:1001`): add `231: true` (NEWGROUPS
   now answers with a block; 215/224/225 are already there) and extend the comment with "NEWGROUPS 231". If any other
   marker in these tests now reports "no longer reproduces", remove it the same way and note it in `## Progress`.
4. HELP and README: add one line each for `DATE`, `NEXT`, `LAST`, `NEWGROUPS date time [GMT]`, `LIST
   ACTIVE|NEWSGROUPS [wildmat]`, `LIST OVERVIEW.FMT`, `LIST HEADERS`, `OVER [range|<message-id>]`, `HDR field
   [range|<message-id>]`, `MODE STREAM`, `CHECK <message-id>`, `STARTTLS` (only when a certificate is configured) to
   hardening's `handleHelp` list and to the command list of `internal/nntp/README.md`, in the existing style.
5. `## Checks` (all green: no RFC-1 marker is left to fail).
6. Commit: `nntp: dispatch, CAPABILITIES and arg table for the RFC 3977/4644/4642 commands (nntp-rfc-commands wave 2)`.
   Record the SHA as the base of step B.

#### Step B: slice `rfc-wire` (1 `pugleaf-implementer`)
Labels: RFC-1 (wire-level), RFC-1m (verification). E-tag `rfc-wire`.

Owned files:
- `internal/nntp/nntp-server-rfccmds_test.go` (new)
- `cmd/nntp-server/e2e_binary_rfc_test.go` (new, `//go:build e2e`)
- `scripts/test-nntp-server.sh` (only the certificate setup, the `start_server` flags, the N50–N59 block and the header
  comment)

Changes:
1. **`nntp-server-rfccmds_test.go`** (prefix `rfc`; real dispatch through `newTestServer`/`hs.dial`). Helper
   `rfcNewCertServer(t, certFile, keyFile string, tlsCfg *tls.Config, proc ArticleProcessor) *hServer`: a copy of
   `newTestServer` with `cfg.NNTP.TLSCert/TLSKey` set before `NewNNTPServer`; when `tlsCfg != nil` it wraps the
   listener itself (`tls.NewListener`) and calls `Serve(l, true)` (implicit TLS); no `Config` write after `Serve`.
   - `TestRfcDispatch`: one subtest per command proving it reaches its handler: `DATE` → 111; `DATE x` → 501 (table);
     `NEXT`/`LAST` without a group → 412; `NEWGROUPS 20991231 235959 GMT` → 231 + empty block; `LIST ACTIVE rc6.none.*`
     → 215 + empty block; `LIST OVERVIEW.FMT` → 215; `LIST HEADERS` → 215; `OVER`/`HDR Subject` without a group → 412;
     `MODE STREAM` with a processor → 203; `CHECK <x@y>` unauthenticated with a processor → `480 <x@y> …`; `CHECK a b`
     → 501 (table, before the no-processor 500); `STARTTLS` without cert → 502; `NEWNEWS rc6.* 20000101 000000 GMT` →
     500 (D3); `LIST ACTIVE` without a wildmat and `MODE READER` still answered by the hardened handlers.
   - `TestRfcCapabilities`: the "Expected lists" table of the Design, one subtest per row: present labels present,
     "Never" labels absent, `VERSION 2` first, `NEWNEWS` never listed.
   - `TestRfcStreamingConsistent`: in the states processor/unauthenticated, posting user, non-posting user, no
     processor: `STREAMING` listed iff `MODE STREAM` → 203.
   - `TestRfcStartTLSWire`: on `rfcNewCertServer` (plaintext): `STARTTLS` listed before auth; `STARTTLS` → 382; upgrade;
     CAPABILITIES over TLS lacks `STARTTLS`; a second `STARTTLS` → 502; `AUTHINFO` → 281. On `rfcNewCertServer` with a
     `tlsCfg` (implicit TLS): `STARTTLS` is not listed and answers 502.
   - `TestRfcReaderHonest`: `READER` is listed and DATE, NEXT, LAST, NEWGROUPS all answer with a non-500 code (RFC 3977
     §3.3.3 "MUST implement the extension").
2. **`cmd/nntp-server/e2e_binary_rfc_test.go`** (`//go:build e2e`, package main, prefix `e2eRfc`): `TestE2EBinaryRFC`
   skips without `PUGLEAF_E2E_ADDR`; reuses `e2eDialAddr`, `e2eEnv`, `e2eParseCode`, `e2eClient.tryCmd`, `tryDotLines`,
   `tryAuth` from the existing e2e files; its own `e2eRfcReport` (a copy of `e2eReport` that prints the `[rfc]` tag)
   prints `PASS|FAIL|INFO N5x [rfc] <text>`:
   - **N50** `DATE` → `111` + 14 digits within 5 min of UTC now.
   - **N51** poll `GROUP $PUGLEAF_E2E_GROUP` up to 20 s until the count is ≥ 2 (N10's TAKETHIS commits asynchronously);
     parse `211 count low high`; `LAST` → 422; `NEXT` repeated until 421 gives `count-1` replies `223` with strictly
     increasing numbers between low and high; then `LAST` → 223 when count ≥ 2, or 422 when the poll ended with count
     1 (INFO line noting the timeout).
   - **N52** `LIST OVERVIEW.FMT` → 215 and the 7 fixed lines.
   - **N53** `OVER 1-` → 224 and ≥ 1 line starting with a number; `OVER 999999999-` → 423.
   - **N54** `HDR Subject 1` → 225 and one line starting `1 `; `LIST HEADERS` → 215 with `:bytes`.
   - **N55** `MODE STREAM` → 203; `AUTHINFO` → 281; `CHECK <fresh id>` → `238 <fresh id>`; `HDR Message-ID 1` gives the
     stored id; `CHECK <that id>` → `438 <that id>`.
   - **N56** `NEWGROUPS <now-2 days yyyymmdd hhmmss> GMT` → 231 listing the group; `LIST ACTIVE <group>` → exactly one
     line equal to the group's NEWGROUPS line; `LIST ACTIVE e2e.test.*,!*.b` lists `e2e.test.a` but not `e2e.test.b`.
   - **N57** with `PUGLEAF_E2E_STARTTLS=1`: a new connection; CAPABILITIES lists `STARTTLS`; `STARTTLS` → 382; TLS
     handshake (`InsecureSkipVerify`, TLS 1.2 minimum); CAPABILITIES over TLS lacks `STARTTLS`; `STARTTLS` → 502.
     Without it: CAPABILITIES lacks `STARTTLS` and `STARTTLS` → 502 (PASS) plus `INFO N57 [rfc] no certificate
     configured`.
3. **`scripts/test-nntp-server.sh`** (additive; keep the other plans' blocks intact):
   - Check-id ranges in this script: N01–N11 (`nntp-audit-tests`), N20–N28 and N30 (`nntp-server-hardening`, which
     reserves N20–N39), N40–N49 (`nntp-intake-injection`), **N50–N59 (this plan; N58–N59 spare)**. Never reuse another
     plan's range.
   - Header comment: add "N50-N57 (nntp-rfc-commands) come from `go test -tags e2e -run TestE2EBinaryRFC`".
   - After `mkdir -p "$DATA/run"` and before the first `start_server`: if `command -v openssl`, generate
     `"$DATA_ABS/run/tls.key"`/`tls.crt` with `openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1
     -nodes -days 2 -subj /CN=localhost -keyout … -out … >/dev/null 2>&1` and set `TLSFLAGS=(-nntpcertfile
     "$DATA_ABS/run/tls.crt" -nntpkeyfile "$DATA_ABS/run/tls.key")` and `E2E_STARTTLS=1`; else `TLSFLAGS=()` and
     `E2E_STARTTLS=0` with `info N57 rfc "openssl missing: STARTTLS checked without a certificate"` (counted with the
     other INFO lines, not as one of the 8 N50–N57 PASS/FAIL lines).
   - `start_server`: append `${TLSFLAGS[@]+"${TLSFLAGS[@]}"}` to the server command line (safe under `set -u`).
   - After the N40–N49 block (or after N01–N11 if that block is absent): run `PUGLEAF_E2E_ADDR=… PUGLEAF_E2E_USER=…
     PUGLEAF_E2E_PASS=… PUGLEAF_E2E_GROUP=e2e.test.a PUGLEAF_E2E_STARTTLS=$E2E_STARTTLS timeout 300 go test -tags e2e
     -count=1 -v -run '^TestE2EBinaryRFC$' ./cmd/nntp-server/ >"$DATA/e2e-rfc-go-test.log" 2>&1`, count its
     `PASS|FAIL|INFO N5[0-7]` lines into the totals as the N01–N11 block does, and `fail N00 rfc "go test reported $n of
     8 rfc steps"` unless exactly 8 `PASS|FAIL N5[0-7]` lines were printed; `alive || setup_fail "server died during the
     rfc steps"`.
   - `bash -n scripts/test-nntp-server.sh` must pass.
4. Checks: the common wave-1 list plus `go vet -tags e2e ./cmd/nntp-server/`, `PUGLEAF_KNOWN_BUGS=1 go test -count=1
   -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 | grep -o 'known bug [A-Za-z0-9-]* still open' | sort
   -u` (no `RFC-1`), `grep -rn '"RFC-1"' internal/nntp cmd/nntp-server` (no output).

Acceptance: E01–E12.

### Wave 3 (inline): verify and finish
Per the skill: `pugleaf-verifier` runs `## Checks` and `## End-to-end` on the main checkout (`plan-nntp-rfc-commands`).
The Outcome lists decisions D1–D12, the verified K-H record, the out-of-scope items and any `T-rfc-commands-*` ids.

---

## Acceptance (E-checks)
| Id | Check | Tag |
|----|----|----|
| E01 | `TestGrpDate` passes without a marker; `TestRdrDate` passes | rfc-reader / step A |
| E02 | `TestGrpNextLast` (3 subtests), `TestRdrNextLast`, `TestRdrNextLastDelays`, `TestRdrNextLastHidden` pass | rfc-reader / step A |
| E03 | `TestGrpNewGroups`, `TestLstNewGroups`, `TestLstParseNNTPDateTime` pass; the NEWGROUPS line equals the LIST ACTIVE line | rfc-lists / step A |
| E04 | `TestGrpListWildmat`, `TestLstListWildmat`, `TestLstMatchesList`, `TestLstWildmat*`, `FuzzLstWildmat` (seed corpus) pass | rfc-lists / step A |
| E05 | `TestGrpListOverviewFmt`, `TestOvhOverviewFmt`, `TestOvhListHeaders` pass | rfc-overview / step A |
| E06 | `TestOvrOverHdr`, `TestOvhOver`, `TestOvhOverMsgID`, `TestOvhHdr`, `TestOvhHdrAnyHeader`, `TestOvhLargeRange` pass | rfc-overview / step A |
| E07 | `TestSessCapabilities/processor-streaming-{complete,consistent}`, `TestRfcStreamingConsistent`, `TestStm*` pass | rfc-stream / step A / rfc-wire |
| E08 | `TestGrpNewNews` passes and `TestRfcDispatch/NEWNEWS` gets 500; NEWNEWS never in CAPABILITIES | rfc-wire |
| E09 | `TestStls*` and `TestRfcStartTLSWire` pass | rfc-starttls / rfc-wire |
| E10 | `TestRfcCapabilities`, `TestRfcReaderHonest`, `TestRfcDispatch`, `FuzzSessCommandLine` (seed corpus) pass | step A / rfc-wire |
| E11 | the `PUGLEAF_KNOWN_BUGS=1` id list no longer contains `RFC-1`; `grep -rn '"RFC-1"' internal/nntp cmd/nntp-server` is empty | step A |
| E12 | End-to-end: N50–N57 PASS (N57 with a certificate; with openssl missing, PASS for the 502 plus INFO) and no FAIL among the other plans' checks | verifier |

## Checks
On every merged tree (after wave 0, after each merge in waves 1 and 2):
```bash
gofmt -l ./cmd ./internal            # only the baseline files (2 at 2fb7741 plus the dependencies' Outcome lists)
go vet ./...
go vet -tags e2e ./cmd/nntp-server/
go build ./...
go test -race -count=1 -timeout 900s ./internal/database/... ./internal/web/... ./internal/history/... \
  ./internal/nntp/... ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/... ./cmd/nntp-server/...
PUGLEAF_KNOWN_BUGS=1 go test -count=1 -timeout 900s ./internal/nntp/... ./cmd/nntp-server/... 2>&1 \
  | grep -o 'known bug [A-Za-z0-9-]* still open' | sort -u
    # from step A on: RFC-1 absent; the rest equals the list recorded in wave 0 minus RFC-1
grep -rn '"RFC-1"' internal/nntp cmd/nntp-server   # from step A on: no output
bash -n scripts/test-nntp-server.sh
git status --short                   # no data-test-*, zz_*, active.out or analysis_*.txt leftovers
```
Merge order in wave 1: `rfc-reader`, `rfc-lists`, `rfc-overview`, `rfc-stream`, `rfc-starttls`. Every wave-1 file is
new except `cmd/nntp-server/main.go`, so textual conflicts aren't expected. Semantic interplay to watch: the
NEWGROUPS/LIST wildmat lines vs hardened LIST ACTIVE (equality tests), the CHECK gates vs merged IHAVE, and STARTTLS vs
hardened connection plumbing (K-H11).

## End-to-end
Runner: `pugleaf-verifier` on the main checkout (`plan-nntp-rfc-commands`), after wave 2.
```bash
./build_nntp-server.sh && ./build_nntpmgr.sh
PORT=21129 DATA=./data-test-nntp-rfc-commands scripts/test-nntp-server.sh
```
- Guards and setup are the script's (DATA must match `./data-test-*`, DNS for `-nntphostname example.org`, `sqlite3`,
  `timeout`, `nc`, `go`; `openssl` optional for N57).
- The server runs with `-data <abs path of ./data-test-nntp-rfc-commands> -nntptcpport 21129 -nntphostname example.org`
  plus `-nntpcertfile/-nntpkeyfile` when openssl generated a certificate (STARTTLS on the plain port; no TLS port).
- **Expected:** N01–N11 PASS; N50–N57 PASS (N57: STARTTLS 382 + handshake with a certificate; PASS for the 502 plus
  INFO without openssl); N20–N28/N30 and N40–N49 as the dependencies' Outcomes state; `SUMMARY … fail=0`.
- Manual spot check (optional, documented in the verifier report):
  ```bash
  printf 'DATE\r\nLIST OVERVIEW.FMT\r\nMODE STREAM\r\nQUIT\r\n' | timeout 5 nc 127.0.0.1 21129
  ```
  gives `111 …`, `215` + 7 lines + `.`, `203 Streaming permitted`, `205`.

---

## Progress

### Wave 0 contract record (pre-filled 2026-10-11 from the final queued plans; wave 0 marks each line `ok` or writes the deviation)
- K-H1: `articleRange{First, Last int64; Open bool}`, `Empty()`, `errRangeSyntax`; `parseRangeArg` sends nothing, caller
  sends `rateLimitOnError()` + `501 Syntax error in range`; `n-` → `Last = math.MaxInt64`; no clamping. — verify:
- K-H2: `requireGroup()` = `rateLimitOnError()` + `412 No newsgroup selected`, returns false. — verify:
- K-H3: yes (GROUP/LISTGROUP set `currentArticle = low`, 0 when empty; number forms set it; bare STAT uses it). — verify:
- K-H4: variable `capabilities`; `VERSION 2`, `READER` unconditional; one append of `"LIST ACTIVE NEWSGROUPS", "XOVER",
  "XHDR"`; `POST`, `IHAVE` under `postingAllowed()`. — verify:
- K-H5: (a) `overviewLine` 8 fields, no Xref → no `Xref:full`; (b) any-header policy → LIST HEADERS `:`, `:bytes`,
  `:lines`; `database.HeaderValueFromJSON`, `sanitizeOverviewField`; (c) `GetOverviewsRange(…, limit)`,
  `GetHeaderFieldRange(…, limit) []HeaderFieldValue`, `OverviewRangeChunk = 1000`; (d) `newDataBlock().Line/Close`,
  `sendDataBlock`; (e) `math.MaxInt64`; (f) no shared core → `rfc-overview`'s loop. — verify:
- K-H6: `if c.server.ArticleCmdDelay > 0 { time.Sleep(c.server.ArticleCmdDelay) }`. — verify:
- K-H7: `resolveArticleArg` (message-id branch: current group, `local430`, processor + `visibleInOtherGroup`, sends
  `430 No article with that message-id`), `database.NNTPArticleByMessageID`; validator `common.ValidMessageID`. —
  verify, and record `resolveArticleArg`'s return signature:
- K-H8: `GetGroupDB(c.currentGroup)` after `selectableGroup`; faults → `403 Internal fault`. — verify:
- K-H9: `NNTPActiveNewsgroups`; no name predicate; status client-dependent (`postingAllowed()`). — verify:
- K-H10: literal `AND hide = 0`. — verify:
- K-H11: `writer` field removed; `errQuit`; no mutex needed (`Stop()` closes tracked raw conns); TLS config built inline
  in `Start()`. — verify:
- K-H12: `sendWelcome`, `UpdateDeadlines` (read deadline only), `commandMaxArgs` (maximum only), `knownStatCommands`
  already holds the new words. — verify:
