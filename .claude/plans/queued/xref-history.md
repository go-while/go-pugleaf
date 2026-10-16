# Plan: local Xref: header from the history index (`-xref off|partial|full`)

## Context

The NNTP server serves no `Xref:` header today: `articleHeaderLines` (internal/nntp/nntp-article-common.go:253)
writes `headers_json` verbatim (foreign Xref is stripped on intake), `overviewLine` (nntp-cmd-xover.go:87) has no
Xref field, `overviewFmtLines` (nntp-cmd-over-hdr.go:44) lists no `Xref:full`, `hdrArticleValue` and the HDR/XHDR
range query answer "" for xref (nntp-cmd-over-hdr.go:221, internal/database/queries.go:1946, nntp_overview.go:82).
Audit items BUG-5 ("no Xref") and INT-4 ("no local Xref is generated") were closed as "not implemented by design"
(BUGS.md:40). Readers (Thunderbird, tin, slrn, Pan) use `Xref:full` to mark crossposts read in every group.

User decisions (2026-10-16):
- The fetcher stays in bulk mode and files only the fetched group (origin order per group). Crossposts are never
  filed on arrival; other groups get the article when they are fetched (crosspost reuse). Not touched by this plan.
- No per-article Xref column in the group DBs and no cross-group linking that opens other group DBs
  ("opening N databases ... does not scale"). The article numbers go into the history index instead.
- Behaviour is selectable: `-xref off|partial|full` on `cmd/nntp-server`.

Facts the design relies on:
- History row: `message_id TEXT PRIMARY KEY, newsgroups TEXT` (CSV of main-DB group IDs), 16 files x 256 tables,
  WITHOUT ROWID (internal/history/history sqlite3_sharded.go:205). Ops are `historyOp{op, messageID, groupID}`
  (history.go:61), queued by `AddArticle`/`RemoveArticle` (history.go:133/138), applied per shard file in one
  transaction (`flushBatch` history.go:403, `executeDBTransaction` :505-560) with `sqlAddArticle` (append-if-absent via
  `instr(',' || newsgroups || ',', ',' || id || ',')`), `sqlRemoveArticle` (string replace), `sqlDeleteEmpty`
  (history.go:75-78). Readers: `Exists` (:172), `LookupGroups` (:199) + `parseGroupIDs` (:225).
- The number is known where the add happens: db_batch PHASE 3 (internal/database/db_batch.go:975-1001) reads
  `article.ArticleNums[task.Newsgroup]` (filled by `query_processOverviewBatch2`, :1138/:1214) and calls
  `sq.proc.AddArticleToHistory(messageID, historyGroupID)` (interface at db_batch.go:77, impl
  internal/processor/processor.go:136). `cmd/history-rebuild` `processGroup` selects `message_id, article_num`
  (main.go:320) and calls `h.AddArticle(messageID, group.ID)` (:419). Removers: `cmd/expire-news` trim (main.go:653,
  message-ids from `selectMessageIDs` of the deleted rows), history-rebuild `-trim` (main.go:565/580, `trimRow{messageID,
  newsgroups}` :503).
- Server side: `NNTPServer.Config.Hostname` (set from `processor.LocalNNTPHostname`, cmd/nntp-server/main.go:145) is
  the Xref hostname. `NNTPServer.Processor ArticleProcessor` (nntp-server.go:23-31) is the server's only way to the
  history (`FindArticleByMessageID` already walks `History.LookupGroups` + `NewsgroupDBsIDcache.GetNewsgroupNameByID`,
  processor.go:242-253). Options are wired like `ReadRequiresAuth`/`SetLocal430TTL` (main.go:185-187). The test
  harness is `newTestServer(t, hServerOpts{Proc, ...})` (internal/nntp/nntp-server-harness_test.go:152/173) with a
  fake processor.

## Design

### Index encoding (backward compatible, no migration)
- Each CSV token becomes `groupID:articleNum` (`12:4711,57:88`). A token without `:` (old rows) parses as number 0 =
  unknown. `parseGroupIDs` keeps returning the IDs; new `parseLocations` returns `[]Location{GroupID, ArticleNum}`.
- `sqlAddArticle`: the absent-check becomes a prefix test `instr(',' || coalesce(newsgroups,'') || ',', ',' || ? || ':')
  = 0` with the group id as the probe and `id:num` as the appended token. A re-add of a known group never appends
  (idempotent as today); a changed number for a known group is only corrected by a rebuild (recover-db renumbering
  already requires one). Old rows without `:` for the same group: the prefix probe does not match `,12,`; add a second
  condition `instr(..., ',' || ? || ',') = 0` so an old-format token is not duplicated either (the number then stays
  unknown until a rebuild).
- `sqlRemoveArticle` with a known number replaces the exact token `id:num` (same statement, probe `id:num`). Remove with
  number 0 (old callers / unknown) is a Go read-modify-write inside the same transaction: SELECT the CSV, drop every
  token whose id matches (either format), UPDATE. `sqlDeleteEmpty` unchanged.
- `history-rebuild` (full run) rewrites every row with numbers: `processGroup` passes `article_num`. `-validate-only`
  also reports rows/tokens without a number (count only). `-trim` passes the number parsed from the row's own token.

### History API
```go
type Location struct{ GroupID, ArticleNum int64 }
func (h *History) AddArticle(messageID string, groupID, articleNum int64)    // articleNum 0 = unknown
func (h *History) RemoveArticle(messageID string, groupID, articleNum int64) // articleNum 0 = strip by id
func (h *History) LookupLocations(messageID string) ([]Location, error)     // nil,nil = not found
func (h *History) LookupGroups(messageID string) ([]int64, error)           // unchanged semantics
```
`historyOp` gains `articleNum int64`. `AddArticleToHistory(messageID string, groupID, articleNum int64)` in the
db_batch interface and the processor.

### Server modes (`-xref off|partial|full`, cmd/nntp-server)
- `off`: today's behaviour (no Xref line, no `Xref:full`, HDR xref empty).
- `partial`: `Xref: <host> <group>:<num>` with the serving group only, zero lookups. Needs the article's number in the
  current group: HEAD/ARTICLE by number have it; by message-id inside the current group `resolveArticleArg` has
  `replyNum`; by message-id outside any group (replyNum 0, RFC 3977 §6.2.1) the line is omitted in partial mode.
- `full`: `partial` plus the other groups from `ArticleProcessor.XrefLocations(messageID) ([]XrefLocation, error)`
  (new interface method; `XrefLocation{Group string, ArticleNum int64}`; the processor implements it with
  `History.LookupLocations` + `NewsgroupDBsIDcache.GetNewsgroupNameByID`, skipping ids with number 0, unknown names
  and the current group). Processor nil or history disabled or lookup error => behave like `partial` (log once).
  By message-id outside any group, full mode lists all known locations. One shard primary-key lookup per served
  article; for OVER/XOVER one per line (this is the accepted cost of `full`; a locations cache is a leftover).
- Where the line is built: one helper `xrefLine(hostname, group string, num int64, others []XrefLocation) string`
  in internal/nntp used by `articleHeaderLines` (HEAD/ARTICLE: append `Xref:` as the last header line; also drop any
  stored `Xref:` line defensively), `overviewLine` (9th field `Xref: host g:n ...`; empty field in `off`),
  `overviewFmtLines` (`Xref:full` appended when mode != off; LIST OVERVIEW.FMT must match the OVER lines),
  `hdrArticleValue`/`GetHeaderFieldRange` "xref" (the value without the `Xref: ` prefix), XHDR same as HDR.
  Hidden articles are not served, so they never get an Xref line; a hidden copy in another group is still listed by
  `full` (acceptable, same as INN after a cancel).
- Mode type `XrefMode` on `NNTPServer` with `SetXrefMode`; default `partial` (decision: free and harmless; set `off`
  in the flag default if the user prefers the exact old output). Logged at startup with the other options.
- Outbound (nntp-transfer, post-queue): unchanged, `RelayHeaders` keeps dropping Xref (RFC 5537 §3.6).

## Waves

### Wave 0 (inline on `plan-xref-history`, commit before wave 1)
Mechanical signature changes so the slices compile independently:
- `internal/history/history.go`: `Location`, `historyOp.articleNum`, `AddArticle`/`RemoveArticle` with `articleNum`,
  `LookupLocations` as a thin stub over the current CSV (numbers 0). SQL unchanged in wave 0.
- `internal/database/db_batch.go`: interface `AddArticleToHistory(messageID string, groupID, articleNum int64)`;
  PHASE 3 passes `article.ArticleNums[task.Newsgroup]` (read under the same RLock as `committed`).
- `internal/processor/processor.go`: `AddArticleToHistory` passes it through; `XrefLocations` stub (returns nil, nil).
- `internal/nntp/nntp-server.go`: `XrefLocation`, `ArticleProcessor.XrefLocations`, `XrefMode` + `SetXrefMode`
  (no behaviour yet); fake processors in `_test.go` files of internal/nntp and cmd/nntp-server get the method.
- `cmd/nntp-server/main.go`: `-xref` flag (validate values, `SetXrefMode`, startup log line).
- Callers: `cmd/history-rebuild/main.go` passes `article_num` in `processGroup`, 0 in trim; `cmd/expire-news/main.go`
  passes 0. `cmd/nntp-fetcher`, `cmd/rslight-importer`, `cmd/web` compile unchanged.
- Checks: gofmt, vet, build, `go test -race ./internal/history/... ./internal/database/... ./internal/nntp/... ./cmd/nntp-server/... ./cmd/history-rebuild/... ./cmd/expire-news/...`.

### Wave 1 (parallel)
- slice `xref-history`: encoding `id:num`, add/remove SQL + Go fallback remove, `parseLocations`, `LookupLocations`,
  `LookupGroups` compat, `Exists` unchanged; tests: old/new/mixed rows parse, add idempotent for both formats, remove
  by token and by id, lookup returns numbers, 16x256 routing unchanged.
  Owned: `internal/history/history.go`, `internal/history/history_test.go`, new `internal/history/xref_test.go`.
- slice `xref-rebuild-trim`: history-rebuild writes numbers (full run), `-validate-only` counts tokens without a number
  and prints the count, `-trim` passes the number from the row token; expire-news trim selects `message_id, article_num`
  of the deleted rows and passes the number. Tests: rebuild on a scratch group DB yields `id:num` rows; trim removes
  the exact token; expire trim removes the token.
  Owned: `cmd/history-rebuild/main.go`, `cmd/history-rebuild/*_test.go`, `cmd/expire-news/main.go`,
  `cmd/expire-news/*_test.go`, `cmd/history-rebuild/README.md`, `cmd/expire-news/README.md`.
  Constraint: uses only the wave-0 API (`LookupLocations` numbers may be 0 until xref-history merges; tests must not
  depend on the history returning numbers, only on what the slice writes/removes).
- slice `xref-serve`: `xrefLine`, modes in `articleHeaderLines`/`sendHeadContent`/`sendArticleContent` (pass the
  current group and replyNum), `overviewLine` 9th field, `overviewFmtLines`, `hdrArticleValue` + range query "xref"
  (internal/database/nntp_overview.go + queries.go:1946: return the article number so the server can build the value;
  the DB layer does not know the hostname), XHDR. Processor `XrefLocations` real implementation
  (`LookupLocations` + ID cache, filters). Tests (harness with fake processor returning locations): off/partial/full
  for HEAD, ARTICLE, OVER range and by message-id, HDR xref range, LIST OVERVIEW.FMT with/without `Xref:full`,
  by-message-id outside a group (partial omits, full lists), processor nil => partial, stored `Xref:` line dropped.
  No timing asserts (memory: no-timing-tests).
  Owned: `internal/nntp/nntp-article-common.go`, `internal/nntp/nntp-cmd-xover.go`, `internal/nntp/nntp-cmd-over-hdr.go`,
  `internal/nntp/nntp-cmd-xhdr.go`, `internal/nntp/nntp-server.go` (mode plumbing only), new `internal/nntp/nntp-xref.go`,
  new `internal/nntp/nntp-xref_test.go`, `internal/database/nntp_overview.go`, `internal/database/queries.go`
  (the "xref" branches only), `internal/processor/processor.go` (`XrefLocations` body only), `internal/nntp/README.md`.

### Wave 2
- slice `xref-e2e-docs`: binary e2e test in `cmd/nntp-server` (POST a crosspost to two groups, HEAD/OVER in each mode;
  reuse the `e2e_binary_*` fixtures), README.md nntp-server section (`-xref`), BUGS.md (move "local Xref generation"
  from "not implemented" to fixed-by-plan with the mode note), docs/audit/README.md BUG-5/INT-4 note, memory-facing
  Outcome notes.
  Owned: `cmd/nntp-server/e2e_xref_test.go` (new), `cmd/nntp-server/e2e_binary_test.go` (fixture reuse only),
  `README.md`, `BUGS.md`, `docs/audit/README.md`.

## Checks (merged tree)
- CLAUDE.md checks: `gofmt -l` empty, `go vet ./...`, `go build ./...`, the full `go test -race` list.
- `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` still lists only T-nntp-article-1.
- `go build -o /dev/null ./cmd/nntp-fetcher ./cmd/rslight-importer ./cmd/web ./cmd/recover-db` (signature change
  consumers).

## End-to-end (verifier; scratch data only)
Data root `./data-test-xref` (create fresh; never `./data`). Ports 11190 (TCP). Scripts in
`/tank0/efs/claude/tmp/claude-1000/-tank0-efs-GO-src-github-com-go-while-go-pugleaf/698bff14-0ada-4301-9aa4-caddf76f2a5b/scratchpad/`
(`nntppost.py` posts an article over a socket) may be reused or re-written in the scratchpad.
1. `./build_nntp-server.sh`, `./build_nntpmgr.sh`, `./build_webserver.sh`, `./build_history-rebuild.sh`, `./build_expire-news.sh`.
2. `./build/webserver -data ./data-test-xref -import-active <file with xref.test.a and xref.test.b, status y>` (one-shot, exits).
3. `./build/pugleaf-nntpmgr -data ./data-test-xref -create -username xreftest01 -password xreftestpass01 -posting`.
4. `./build/pugleaf-nntp-server -data ./data-test-xref -nntphostname xref.test -nntptcpport 11190 -xref full &`
   AUTHINFO, POST one article with `Newsgroups: xref.test.a,xref.test.b`, wait for the batch commit (poll
   `HEAD <msgid>` until 221). Expect `Xref: xref.test xref.test.a:1 xref.test.b:1` (order by group id) in HEAD and
   ARTICLE from both groups and by message-id without GROUP; `OVER 1` in xref.test.a has 9 fields, the 9th
   `Xref: xref.test xref.test.a:1 xref.test.b:1`; `LIST OVERVIEW.FMT` ends with `Xref:full`; `HDR Xref 1` gives
   `1 xref.test xref.test.a:1 xref.test.b:1`.
5. Restart with `-xref partial`: HEAD in xref.test.a gives `Xref: xref.test xref.test.a:1` only; by message-id without
   GROUP gives no Xref line. Restart with `-xref off`: no Xref line, no `Xref:full`, `HDR Xref 1` gives `1 ` (empty).
   Invalid `-xref maybe` exits non-zero with a usage error.
6. `./build/history-rebuild -data ./data-test-xref -validate-only` reports 0 misses and 0 tokens without a number;
   `sqlite3 ./data-test-xref/history/hashdb_*.sqlite3` shows the row as `<gid-a>:1,<gid-b>:1`.
7. `./build/expire-news -data ./data-test-xref -group xref.test.b -days 0 -prune ...` (or `-respect-expiry` with a
   0-day setting) removes the article from b and trims history: the row becomes `<gid-a>:1`; HEAD in a (full) now
   shows `Xref: xref.test xref.test.a:1`.
8. Stop the server (SIGINT), clean exit code 0/130, no goroutine leak log lines.

## Leftovers / decisions to confirm at wave 0
- Default mode `partial` (free) vs `off` (exact old output).
- `full` cost on OVER: one history lookup per line. A small locations cache keyed by message-id is a possible
  follow-up; not in this plan.
- Numbers for existing data appear only after a full `history-rebuild` run on the real data (user-run; never by agents).
