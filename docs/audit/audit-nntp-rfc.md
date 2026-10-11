# Audit audit-nntp-rfc (base 66c36f8)

Command-by-command RFC conformity audit of the go-pugleaf NNTP server. Base: `plan-nntp-audit-tests` @ 66c36f8
(= `testing-001` @ 84c2081 plus the wave-0 hooks and harness). Line numbers below are **at 66c36f8**. They differ from
the seed table in the plan, which was written at 84c2081. The wave-0 hooks moved `nntp-server.go` and
`nntp-server-cliconns.go` by a few lines.

## Scope and method

- **Files read in full:** `internal/nntp/{nntp-server.go, nntp-server-cliconns.go, nntp-server-statistics.go,
  nntp-cmd-article.go, nntp-cmd-auth.go, nntp-cmd-basic.go, nntp-cmd-body.go, nntp-cmd-group.go, nntp-cmd-head.go,
  nntp-cmd-helpers.go, nntp-cmd-list.go, nntp-cmd-posting.go, nntp-cmd-reader.go, nntp-cmd-stat.go, nntp-cmd-xhdr.go,
  nntp-cmd-xover.go, nntp-article-common.go, nntp-cache-local.go, nntp-auth-manager.go, README.md}`,
  `cmd/nntp-server/{main.go, processor_adapter.go}`, `internal/nntp/nntp-server-harness_test.go`.
- **Callees read:** `database/queries.go` (`MainDBGetNewsgroup` :213, `GetOverviews` :698, `GetArticleByNum` :892,
  `GetArticleByMessageID` :919, `GetActiveNewsgroups` :1030, `GetOverviewsRange` :1810, `GetHeaderFieldRange` :1872),
  `database/thread_cache.go:309` (`GetOverviewByArticleNum`), `database/db_batch.go:999,1036` (`headers_json` insert),
  `nntp/nntp-client-commands.go:850-952` (`ParseLegacyArticleLines`, `ParseHeaders`), `common/headers.go:580-601`,
  `processor/threading.go:56-283` (return codes of `processArticle`), `history/history_config.go:19-25`,
  `migrations/0001_single_db_schema.sql:11-29`.
- **RFCs fetched from rfc-editor.org (2026-10-11):** RFC 3977, 4643, 4644, 4642, 2980, 6048, 5536 (section 3 only, for
  served headers). The quotes below are taken from those texts.
- **Tools:** `docs/audit/tools/*.txt` filtered to the scope. The only hit in a scope production file is
  `nntp-cmd-helpers.go:25: parseArticleHeadersShort is unused` (staticcheck U1000, golangci unused), recorded under FN-1.
- **Repro:** a temporary `internal/nntp/zz_audit_nntp-rfc_test.go` (deleted before the commit) built on the wave-0
  harness (`newTestServer`, `hGroup`, `hUser`, `hFakeProcessor`, `runIsolated`). Run with
  `PUGLEAF_TEST_LOG=1 go test -count=1 -run TestZZ -v ./internal/nntp/`; every subtest passed (it only logs the transcript).
  The pipelined-QUIT child crashed its process, so it was also run on its own with
  `PUGLEAF_ISOLATED=TestZZAuditRFC/pipelined-quit PUGLEAF_TEST_DATADIR=/tmp/pugleaf-zz-audit-rfc go test -count=1 -run TestZZAuditRFC/pipelined-quit -v ./internal/nntp/`
  to capture the panic. The trimmed transcript is in [Appendix A](#appendix-a-repro-transcript). Everything ran
  in-process on `127.0.0.1:0` against a temp data dir. Nothing touched `./data`.

## Summary

| Severity | Count | Ids |
|----|----|----|
| critical | 1 | A-nntp-rfc-1 |
| high | 1 | A-nntp-rfc-2 |
| medium | 7 | RFC-1, RFC-3, RFC-5, RFC-6, BUG-4, BUG-5, BUG-6 |
| low | 12 | RFC-2, RFC-4, BUG-3, BUG-9, A-nntp-rfc-3, A-nntp-rfc-4, A-nntp-rfc-5, A-nntp-rfc-6, A-nntp-rfc-7, A-nntp-rfc-8, A-nntp-rfc-10, A-nntp-rfc-12 |
| info | 3 | FN-1, A-nntp-rfc-9, A-nntp-rfc-11 |

| Category | Count |
|----|----|
| rfc | 15 (RFC-1…6, BUG-4, BUG-5, BUG-6, BUG-9, A-3, A-4, A-5, A-6, A-8) |
| security | 2 (A-1, A-2) |
| bug | 1 (A-7) |
| robustness | 3 (BUG-3, A-10, A-12) |
| missing | 1 (A-9) |
| wrong-function | 1 (A-11) |
| dead-code | 1 (FN-1) |

Every seed in scope (RFC-1…6, BUG-3…6, BUG-9, FN-1) is **CONFIRMED**. Two sub-claims are rejected (see
`## Seed items rejected`).

**The 5 most important findings:**
1. **A-nntp-rfc-1 (critical):** `QUIT\r\n<any command>\r\n` sent in one write makes the server process panic and exit:
   `handleQuit` sets `c.conn = nil`, and the next buffered line reaches `UpdateDeadlines`, which dereferences nil. No
   auth is needed, and one packet kills the whole `nntp-server` binary.
2. **A-nntp-rfc-2 (high):** multi-line responses can be injected and the stream desynced. A header line `..` that a
   poster or peer sends is stored as `.` and served unstuffed by HEAD/ARTICLE. Repeated `Subject:` headers are joined
   with a bare LF and served by XHDR/XOVER. Readers then take attacker-chosen lines as status lines.
3. **RFC-5 (medium):** IHAVE answers 436 ("retry later") to permanent rejects (`CaseError`, garbled article). Peers
   retry the same bad article forever. POST answers 502 instead of 440, and unknown MODE/AUTHINFO variants get 500
   instead of 501.
4. **RFC-1 (medium):** READER is advertised, but the READER-mandatory DATE, NEXT, LAST and NEWGROUPS give 500, and LIST
   ACTIVE/NEWSGROUPS ignore the wildmat. MODE STREAM and CHECK are missing although TAKETHIS is offered.
5. **BUG-4 (medium):** GROUP reports low=1 and never sets (or resets) the current article. LISTGROUP has a bare 211
   line, doesn't select the group, ignores the range, and answers 211 for unknown groups (SEC-2). LIST ACTIVE reports
   low=1 and status `n`.

## Findings

### A-nntp-rfc-1: pipelined command after QUIT crashes the server process

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-1 | critical | security | `internal/nntp/nntp-cmd-basic.go:53-64`, `internal/nntp/nntp-server-cliconns.go:77-81,60` | CONFIRMED |

- **Scenario:** an unauthenticated client sends `QUIT\r\nHELP\r\n` in one TCP write. The server answers 205, closes
  the socket and sets `c.conn = nil` (`nntp-cmd-basic.go:63`). `handleQuit` returns nil, so `Handle` loops.
  `textConn.ReadLine()` (`cliconns.go:77`) returns `HELP` from its bufio buffer without touching the socket, and
  `c.UpdateDeadlines()` (`:81`) calls `c.conn.SetReadDeadline` (`:60`) on a nil interface. The goroutine
  (`handleConnection`, no `recover`) panics, and the panic takes down the whole process: every client is dropped and
  `cmd/nntp-server` exits.
- **Evidence:** the child process of the repro (Appendix A, "pipelined-quit"):
  ```
  C: "QUIT\r\nHELP\r\n" in one write
  S: "205 Goodbye"
  panic: runtime error: invalid memory address or nil pointer dereference
  [signal SIGSEGV: segmentation violation code=0x1 addr=0x40 pc=0x701c5b]
  goroutine 82 [running]:
  github.com/go-while/go-pugleaf/internal/nntp.(*ClientConnection).UpdateDeadlines(...)
  	internal/nntp/nntp-server-cliconns.go:60 +0x5b
  github.com/go-while/go-pugleaf/internal/nntp.(*ClientConnection).Handle(...)
  	internal/nntp/nntp-server-cliconns.go:81 +0x173
  github.com/go-while/go-pugleaf/internal/nntp.(*NNTPServer).handleConnection(...)
  	internal/nntp/nntp-server.go:211 +0x119
  FAIL	github.com/go-while/go-pugleaf/internal/nntp	0.080s
  ```
- **RFC:** RFC 3977 §3.5 allows pipelining, and §5.4.2 says "The server MUST acknowledge the QUIT command and then
  close the connection to the client." Lines after QUIT must be discarded, not executed.
- **Fix:** make QUIT end the loop. `handleQuit` returns a sentinel (e.g. `errQuit`) that `Handle` treats as a clean
  exit (this also fixes the "QUIT logged as connection error" half of BUG-7). Don't nil `c.conn` while the loop can
  still run. Also add a `defer recover()` in `handleConnection` that logs and closes only that connection, so no
  handler panic can kill the server. Add a regression test with `runIsolated`. Effort S.
- **Note for consolidation:** same as A-nntp-sec-1 (the same QUIT crash found by audit-nntp-sec). It is an
  unauthenticated remote DoS; the consolidated report should list it once.

### A-nntp-rfc-2: multi-line response injection / stream desync via stored header data

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new (extends BUG-5, BUG-6) | A-nntp-rfc-2 | high | security | `internal/nntp/nntp-cmd-helpers.go:20-23`, `nntp-article-common.go:231-235,266-271`, `nntp-cmd-xhdr.go:97-103`, `nntp-cmd-helpers.go:68-80`, `nntp-cmd-posting.go:242-245`, `nntp-client-commands.go:950`, `common/headers.go:580-601` | CONFIRMED |

- **Scenario A (`.` header line):** a poster (POST) or peer (IHAVE/TAKETHIS) sends an article with a header line `..`.
  `readArticleLines` un-stuffs it to `.` (`nntp-cmd-posting.go:243-245`). `ParseHeaders` skips the line as malformed
  but stores **all** header lines in `HeadersJSON` (`nntp-client-commands.go:950`), and the batch writes that verbatim
  to `articles.headers_json` (`db_batch.go:999,1036`). `processArticle` checks only that Newsgroups, Subject, From and
  Date are present (`processor/threading.go:122-166`). HEAD and ARTICLE send header lines with `sendLine` and no
  dot-stuffing (`nntp-article-common.go:231-235,266-271`). Every reader of the article then sees `.` as the end of
  the block, and the remaining header lines (attacker-chosen) as the status lines of its next commands.
- **Scenario B (bare LF):** an article with two `Subject:` headers (RFC 5536 §3.1 allows exactly one) gets
  `article.Subject = "first\n."`, because `GetHeaderFirst` joins repeated values with `"\n"` (`common/headers.go:580-588`).
  XHDR sends `"4 first\n.\r\n"` and XOVER sends the LF inside the line. A reader that splits on LF (most do) takes
  `.` as the terminator and is out of sync from then on.
- **Evidence:** repro "format" (server side, headers seeded via SQL) and "intake-parse" (parse side, pure function):
  ```
  C: "HEAD 3"
  S: "221 3 <t9-rfc-fmt-4-3@test.invalid> Headers follow"
  S| "Path: test.invalid!not-for-mail\r\n"
  S| ".Dotted-Header: x\r\n"        <- not stuffed: the client drops the dot (BUG-6)
  S| ".\r\n"                        <- stored header line "." ends the block
  -- next status line read by the client after HEAD 3 (desync):
  S: "223 0 <injected@evil.invalid> faked"
  S: <bad status: bad status line "Message-ID: <t9-rfc-fmt-4-3@test.invalid>">

  C: "XHDR subject 4"
  S: "221 Header subject follows"
  S| "4 first\n"
  S| ".\r\n"
  C: "DATE"
  S: <error short status line ".">              <- the real terminator, read as the DATE reply
  C: "XOVER 4"
  S: "500 Command not recognized: DATE"         <- every later reply is shifted by one

  parseIncomingArticleLines(head with "Subject: first","Subject: .",".") ->
  article.Subject="first\n."
  article.HeadersJSON="Path: ...\nSubject: first\nSubject: .\n.\nMessage-ID: <p@test.invalid>"
  ```
- **RFC:** RFC 3977 §3.1.1: "the stream MUST NOT include the octets NUL, LF, or CR" and "If any line of the data block
  begins with the "termination octet" ("." or %x2E), that line MUST be "dot-stuffed"". RFC 3977 §8.3.2: servers
  "SHOULD check for these characters and replace each one by a single space".
- **Fix:** (1) send every multi-line block through one writer that dot-stuffs and strips CR/LF inside a line:
  `textConn.DotWriter()` as `sendMultilineResponse` already does, or a shared `writeDataLine`. Use it for HEAD, ARTICLE,
  BODY, XOVER, XHDR, LISTGROUP and LIST. (2) Sanitize overview/XHDR fields (TAB/CR/LF to space, BUG-5). (3) On intake,
  reject or drop header lines without a colon and articles with repeated mandatory headers (audit-intake). Effort M.

### A-nntp-rfc-3: connection limit closes the socket without a 400 greeting

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-3 | low | rfc | `internal/nntp/nntp-server.go:188-192` | CONFIRMED |

- **Scenario:** with `MaxConns` connections active, a new client is accepted and closed at once. It reads EOF
  instead of a greeting, so clients can't tell "full, retry later" from a crash.
- **Evidence:** repro "maxconns" (`MaxConns: 1`): `conn 1 greeting: "200 go-pugleaf NNTP server ready"`, then
  `conn 2: greeting: EOF (no 400 greeting)`. Also, `activeConnections` is incremented inside the handler goroutine
  (`nntp-server.go:206`), not in the accept loop, so a burst of accepts can exceed the limit.
- **RFC:** RFC 3977 §5.1.2: "Otherwise, the server MUST present a 400 or 502 greeting code and then immediately close
  the connection. 400 SHOULD be used if the issue is only temporary (for example, because of load)".
- **Fix:** write `400 too many connections, try later\r\n` (with a short write deadline) before `conn.Close()`. Count
  the connection in the accept loop. Effort S.

### A-nntp-rfc-4: internal faults answered with 503, 411 or 500 instead of 403

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-4 | low | rfc | `nntp-cmd-group.go:62`, `nntp-cmd-list.go:30,50`, `nntp-cmd-xover.go:74,82`, `nntp-cmd-xhdr.go:80,88`, `nntp-article-common.go:84,166`, `nntp-server-cliconns.go:89` | CONFIRMED (code trace) |

- **Scenario:** a DB error in LIST, LISTGROUP, XOVER or XHDR gives `503 Failed to retrieve …`. A `GetGroupDB`
  failure in ARTICLE-by-number, XOVER or XHDR gives `411 No such newsgroup`, a code those commands don't define and
  that a client reads as "the group is gone". A handler that returns an error (a write failure) makes `Handle` send
  `500 Internal server error` and close.
- **Evidence:** code trace: the `sendResponse(503|411|500, …)` calls at the listed lines run on DB, `GetGroupDB` or write errors (see `## Response code table`); not triggered in the repro.
- **RFC:** RFC 3977 §3.2.1: "If the server experiences an internal fault … the response code 403 MUST be returned."
  503 means "recognizes the command but does not provide an optional feature". 500 is reserved for unknown commands,
  and §3.2.1 also says "If the server has to terminate the connection for some reason, it MUST give a 400 response".
- **Fix:** use 403 for every DB or internal error. After a write error, just close (the peer is gone); use 400 when
  the server deliberately drops a live connection. Effort S.

### A-nntp-rfc-5: surplus arguments silently ignored

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-5 | low | rfc | `nntp-cmd-group.go:20-35`, `nntp-article-common.go:101-119`, `nntp-cmd-basic.go:15-26,53`, `nntp-cmd-list.go:9-22`, `nntp-cmd-xover.go:21-70` | CONFIRMED |

- **Scenario:** `GROUP g extra-arg` → 211, `ARTICLE 5 extra` → 220, `MODE READER extra` → 200, `QUIT now` → 205
  (repro). Handlers read `args[0]` (or `args[1]`) and never check `len(args)`.
- **Evidence:** Appendix A, "session-noproc" (`MODE READER extra` → 200, `QUIT now` → 205) and "reader" (`GROUP t9.rfc.a.1 extra-arg` → 211, `ARTICLE 5 extra` → 220).
- **RFC:** RFC 3977 §3.2.1: "If there is a syntax error in the arguments of a recognized command, including the case
  where more arguments are provided than the command specifies …, the response code 501 MUST be returned."
- **Fix:** check `len(args)` per command in a small dispatch table (min/max args). Effort S.

### A-nntp-rfc-6: status lines echo client input without bound (over 512 octets)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-6 | low | rfc | `nntp-server-cliconns.go:155`, `nntp-cmd-basic.go:25`, `nntp-cmd-list.go:21`, `nntp-cmd-auth.go:21,45` | CONFIRMED |

- **Scenario:** `XYZZY` followed by 600×`A` → `500 Command not recognized: XYZZYAAAA…` with a 633-octet status line
  (repro). The same echo happens in `Unknown MODE`, `Unknown LIST type`, `Password required for <username>` and
  `Unknown AUTHINFO subcommand`. Echoing the username also reflects user-controlled data into logs.
- **Evidence:** Appendix A, "session-noproc": `XYZZYAAAA…` → a status line of 633 octets + CRLF.
- **RFC:** RFC 3977 §3.1: "The initial line of the response MUST NOT exceed 512 octets, which includes the response
  code and the terminating CRLF pair". The command itself exceeds 512 octets and should get 501 (SEC-3).
- **Fix:** don't echo arguments, or truncate them to 64 bytes. The line limit itself belongs to SEC-3. Effort S.

### A-nntp-rfc-7: IHAVE/TAKETHIS accept a syntactically invalid message-id

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-7 | low | bug | `nntp-cmd-posting.go:56-72,134-137` | CONFIRMED |

- **Scenario:** `IHAVE not-a-message-id` → `335 Send me` (repro). The id goes unchecked to `CheckMessageID` and is the
  only key for history. An article whose header has no Message-ID gets the bogus id via `reconcileMessageID`
  (`:115-121`).
- **Evidence:** Appendix A, "posting": `IHAVE not-a-message-id` → `335 Send me`.
- **RFC:** RFC 3977 §3.2.1 (501 for argument syntax errors); the message-id syntax is in RFC 3977 §9.8 and RFC 5536 §3.1.3.
- **Fix:** validate `<…@…>` (printable ASCII, ≤250 octets) before the history check: 501 for IHAVE, and 439 with the
  id for TAKETHIS (the article must still be consumed). Effort S.

### A-nntp-rfc-8: TAKETHIS response details (missing message-id, temporary errors)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-8 | low | rfc | `nntp-cmd-posting.go:131-152,160-165` | CONFIRMED |

- **Scenario:** unauthenticated TAKETHIS → `480 Transfer permission denied …` (repro), and a user without posting
  rights → 502. Both lines, plus the 501 and no-processor 502, lack the message-id, so a streaming peer can't
  correlate pipelined replies. A history error (`CaseError` from `CheckMessageID`, temporary) → `439 <id> Retry later
  (history error)` (repro): 439 means "do not retry", so the article is lost for that peer.
- **Evidence:** Appendix A, "posting": unauthenticated TAKETHIS → `480 Transfer permission denied …` (no id); history error → `439 <tt2@test.invalid> Retry later (history error)`.
- **RFC:** RFC 4644 §2.5.1: "The first parameter of the 239 and 439 responses MUST be the message-id". §2.5.2: "If the
  server encounters a temporary error that prevents it from processing the article but does not want to reject the
  article, it MUST reply with a 400 response to the client and close the connection."
- **Fix:** for TAKETHIS, map history-error and in-flight cases to 400 + close (or keep 439 only for "already have
  it"). Reject unauthenticated or non-permitted streaming once at MODE STREAM/CAPABILITIES time, and keep 480/502
  for that. Effort S.

### A-nntp-rfc-9: read-only mode (no processor) can't find articles by message-id outside the current group

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-9 | info | missing | `nntp-article-common.go:122-143` | CONFIRMED |

- **Scenario:** `NewNNTPServer(..., nil)` (allowed, `nntp-server.go:78`). `STAT <id>` with no group selected, or for
  an article in another group, → `430 NotF0` although the article exists (repro: `STAT <t9-rfc-b-2-1@…>` while
  `t9.rfc.a.1` is selected → 430). The reply texts `Cache says no!`, `NotF0`, `NotF1` are debugging leftovers.
  `cmd/nntp-server` always passes a processor, so only embedded and test servers are affected.
- **Evidence:** Appendix A, "reader": `STAT <t9-rfc-b-2-1@test.invalid>` with `t9.rfc.a.1` selected and no processor → `430 NotF0`; code path `nntp-article-common.go:138-142`.
- **RFC:** RFC 3977 §6.2.1.2: "If the argument is a message-id and no such article exists, a 430 response MUST be
  returned". The article does exist.
- **Fix:** document that message-id lookups need the history index, or fall back to the history lookup without the
  processor. Use plain 430 texts ("No article with that message-id"). Effort S.

### A-nntp-rfc-10: one write syscall per response line, two writers on one socket

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-10 | low | robustness | `nntp-server-cliconns.go:165-173,159-162`, `nntp-article-common.go:231-250` | CONFIRMED (code trace) |

- **Scenario:** `sendLine` writes to `c.writer` (a `bufio.Writer` on the raw conn) and flushes after **every** line.
  An article of 2 000 lines costs 2 000 `write(2)` calls, and XOVER over 1 000 rows costs 1 000. Status lines go
  through a second writer (`textConn.W`), which also flushes per call. Ordering is correct only because both flush
  every time, which is fragile.
- **Evidence:** code trace: `sendLine` calls `c.writer.Flush()` per line (`nntp-server-cliconns.go:165-173`); `sendResponse` uses `textConn.PrintfLine` (`:159-162`), a second buffered writer on the same conn.
- **Fix:** use one writer per connection (`textConn.W` or `DotWriter`), flush once per response (this also gives
  A-nntp-rfc-2's dot-stuffing). Effort S.

### A-nntp-rfc-11: AUTHINFO argument handling

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-11 | info | wrong-function | `nntp-cmd-auth.go:10-14` | CONFIRMED (code trace) |

- **Scenario:** `strings.Fields` plus `argument := args[1]` cut `AUTHINFO PASS correct horse` down to the password
  `correct`, so a user whose stored password contains a space can never log in. Surplus arguments are ignored (A-5).
- **Evidence:** code trace: `handleCommand` splits with `strings.Fields(line)` (`nntp-server-cliconns.go:107,113`) and `handleAuthInfo` takes `argument := args[1]` (`nntp-cmd-auth.go:15`); every password word after the first is dropped.
- **RFC:** RFC 4643 §2.3.2: "a server MAY (but is not required to) allow white space characters in usernames and
  passwords. A server implementation MAY blindly split command arguments at white space". Allowed, but `nntpmgr`
  and the web admin accept such passwords.
- **Fix:** take the raw remainder of the line after `AUTHINFO PASS ` as the password, or reject whitespace in
  passwords at creation time. Effort S.

### A-nntp-rfc-12: cmd/nntp-server startup and flag inconsistencies

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-rfc-12 | low | robustness | `cmd/nntp-server/main.go:43,51-66,76,133`, `internal/nntp/nntp-server.go:108-139` | CONFIRMED (code trace) |

- **Scenario:**
  - Without `-nntptcpport` and `-nntptlsport`, both ports are 0. `Start()` skips both listeners and still sets
    `running = true` and logs "NNTP server started successfully". The process then waits for SIGINT and serves nothing.
  - `-maxconnections` claims "allow max of N authenticated connections", but `MaxConns` counts every connection,
    unauthenticated ones included (`nntp-server.go:188`).
  - `mainConfig.Server.Hostname` is set (`main.go:76`), but the server never reads it: the greeting hardcodes
    `go-pugleaf` (RFC-4).
- **Evidence:** code trace: `main.go:51-66` sets both ports to 0 without flags; `Start()` (`nntp-server.go:108-139`) then skips both listeners and still sets `running = true` (`:137`); `MaxConns` is compared with all active connections (`nntp-server.go:188`).
- **Fix:** fail `Start()` when no listener was started. Fix the flag text. Use `processor.LocalNNTPHostname` (or
  `Config.Hostname`) in the greeting. Effort S.

### RFC-1: missing mandatory and advertised commands

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| RFC-1 | RFC-1 | medium | rfc | `nntp-server-cliconns.go:119-156`, `nntp-cmd-list.go:9-22,25-58` | CONFIRMED |

- **Scenario:** READER is advertised, but `DATE`, `NEXT`, `LAST`, `NEWGROUPS …` → `500 Command not recognized`
  (repro). `LIST ACTIVE t9.rfc.b.*` and `LIST NEWSGROUPS t9.rfc.b.*` return **all** groups (the wildmat is never read:
  `handleList` passes nothing beyond `args[0]`). `MODE STREAM` and `CHECK` → 500 although TAKETHIS is offered.
  `LIST OVERVIEW.FMT` → 501 although XOVER is advertised. `OVER`, `HDR`, `NEWNEWS`, `STARTTLS`, `LIST HEADERS` → 500/501.
  Those are optional and not advertised, so the replies are conformant, but they are missing features.
- **Evidence:** repro "session-noproc" and "reader". Dispatch: `CAPABILITIES, MODE, AUTHINFO, QUIT, HELP, LIST, GROUP,
  LISTGROUP, STAT, HEAD, BODY, ARTICLE, XOVER, XHDR, POST, IHAVE, TAKETHIS` (`cliconns.go:119-156`).
- **RFC:** RFC 3977 §3.3.3: "If the server advertises a capability defined by a registered extension, it MUST
  implement the extension so as to fully conform with the specification". DATE (§7.1.1), LAST (§6.1.3.1), NEXT
  (§6.1.4.1) and NEWGROUPS (§7.3.1) are "Indicating capability: READER". §7.6.3: "If no wildmat is specified, the
  server MUST include every group …" (the wildmat must filter). RFC 4644 §2.3: "Servers MUST accept the MODE STREAM
  command for backwards compatibility". RFC 2980 §2.8: "The LIST OVERVIEW.FMT command should be implemented if XOVER
  is implemented."
- **Fix:** see `nntp-rfc-commands.md` (wave 4): DATE (S), NEXT/LAST (S, need BUG-1/BUG-4 for the current article),
  NEWGROUPS (S, `newsgroups.created_at`), the wildmat matcher (S, RFC 3977 §4), LIST OVERVIEW.FMT (S), MODE STREAM +
  CHECK (S, reuse `CheckMessageID`), OVER/HDR as aliases of XOVER/XHDR with RFC 3977 codes (M), STARTTLS (M).
  Effort M overall.

### RFC-2: CAPABILITIES content

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| RFC-2 | RFC-2 | low | rfc | `nntp-server-cliconns.go:200-217` | CONFIRMED (one sub-claim rejected, see below) |

- **Scenario:** the list is static per server. It has `READER` **and** `MODE-READER`, and `AUTHINFO USER` both on
  plaintext and after a successful AUTHINFO (repro, "posting": the CAPABILITIES after `281` still lists `AUTHINFO
  USER`). `POST`/`IHAVE`/`TAKETHIS` are listed whenever a processor exists, even for an authenticated user without
  posting rights. `TAKETHIS` is not a registered label: RFC 4644 defines `STREAMING`. `XOVER`/`XHDR` start with `X`,
  so they are acceptable private labels.
- **Evidence:** Appendix A, "session-noproc" (list with `READER`, `MODE-READER` and `AUTHINFO USER` on plaintext) and "posting" (identical list after `281`: still `AUTHINFO USER`, `POST`, `TAKETHIS`).
- **RFC:** RFC 3977 §3.3.2: "MODE-READER: This capability indicates that the server is mode-switching (Section 3.4.2)
  and that the MODE READER command needs to be used to enable the READER capability". §3.4.2, transit mode of a
  mode-switching server: "It MUST advertise the MODE-READER capability. It MUST NOT advertise the READER capability."
  Listing both is therefore contradictory in every state (the server is not mode-switching: MODE READER changes
  nothing). RFC 4643 §2.1: "this capability MUST NOT be advertised after successful authentication" and "The "USER"
  argument MUST NOT be advertised, and the AUTHINFO USER/PASS commands SHOULD NOT be provided, unless a strong
  encryption layer (e.g., Transport Layer Security (TLS) [NNTP-TLS]) is in use or backward compatibility dictates
  otherwise" (so plaintext `AUTHINFO USER` is a deployment decision, not a hard violation; after auth it is a MUST).
  RFC 3977 §3.3.3: private-extension command keywords "SHOULD begin with "X"" (the sentence right before says a
  listed private extension's "capability label MUST begin with "X""). The `TAKETHIS` label is treated here as a
  SHOULD-level naming issue: the command keyword itself comes from RFC 4644, and the fix is to advertise the registered
  label `STREAMING` once MODE STREAM and CHECK exist.
- **Fix:** build the list from connection state; see `## CAPABILITIES`. Effort S (inline in the dispatch file per
  Appendix B).

### RFC-3: ARTICLE/HEAD/BODY/STAT third form and invalid numbers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| RFC-3 | RFC-3 | medium | rfc | `nntp-article-common.go:95-98,112-119,161,195-196` | CONFIRMED |

- **Scenario:** `STAT` with no argument → `501 No article specified`, both before and after GROUP (repro).
  `ARTICLE 0` and `ARTICLE -1` → `502 Article not retrieved` (repro). 502 tells the client to reconnect with other
  credentials. `ARTICLE 99999999999999999999` → 501, which is acceptable (more than 16 digits is a syntax error).
- **Evidence:** Appendix A, "session-noproc" (`STAT` → 501) and "reader" (`STAT` after GROUP → 501, `ARTICLE 0`/`ARTICLE -1` → 502, `ARTICLE 99999999999999999999` → 501).
- **RFC:** RFC 3977 §6.2.1.1, third form: `220 n message-id`, `412 No newsgroup selected`, `420 Current article number
  is invalid`. HEAD and STAT are mandatory for every server (§6.2.2.1, §6.2.4.1: "This command is mandatory."). §3.2.1
  on 502: "It is necessary to terminate the connection and to start a new one with the appropriate authority".
- **Fix:** no argument → 412 without a group, 420 when `currentArticle == 0`, else look up `currentArticle`. A number
  ≤ 0 → 423 (or 501). Note that `retrieveArticleCommon` reads `args[0]` at `:58`; the no-argument form must not reach
  that line, or it will panic. Effort S.

### RFC-4: greeting and MODE READER always say "posting allowed"

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| RFC-4 | RFC-4 | low | rfc | `nntp-server-cliconns.go:96-103`, `nntp-cmd-basic.go:23` | CONFIRMED |

- **Scenario:** a server without a processor greets with `200 go-pugleaf NNTP server ready`, and `MODE READER` →
  `200 Hello, you can post` (repro "session-noproc"), while POST → 502. The hostname is the literal `go-pugleaf`;
  neither `Config.Hostname` nor `processor.LocalNNTPHostname` is used.
- **Evidence:** Appendix A, "session-noproc": greeting `200 go-pugleaf NNTP server ready`, `MODE READER` → `200 Hello, you can post`, `POST` → 502; hostname literal at `nntp-server-cliconns.go:98`.
- **RFC:** RFC 3977 §5.1.2: "If the server will accept further commands from the client, but the client is not
  authorized to post articles using the POST command, the server MUST present a 201 greeting code." §5.3.2: MODE READER
  "MUST return a 200 or 201 response with the same meaning as for the initial greeting".
- **Fix:** 201 when `Processor == nil`; the same choice in MODE READER. Put the configured hostname in the greeting.
  Effort S.

### RFC-5: wrong response codes for MODE, AUTHINFO, POST and IHAVE

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| RFC-5 | RFC-5 | medium | rfc | `nntp-cmd-basic.go:25`, `nntp-cmd-auth.go:45`, `nntp-cmd-posting.go:17,27,53,95,109` | CONFIRMED |

- **Scenario (repro "session-noproc" and "posting"):**
  - `MODE FOO` and `MODE STREAM` → 500 (501, and for STREAM 203; see RFC-1).
  - `AUTHINFO FOO bar` and `AUTHINFO SASL PLAIN` → 500 (501).
  - POST without a processor → 502, and POST as a non-posting user → `502 Posting not permitted for this user` (440).
  - IHAVE without a processor → 502 (500, since IHAVE isn't offered).
  - IHAVE with an article the processor rejects permanently (`CaseError`: no Date, no Newsgroups, bad From…, see
    `processor/threading.go:109-166`) → `436 Transfer failed (processing error)`. A garbled article (no Newsgroups) →
    `436 Bad`. Both should be 437.
- **Evidence:** `processArticle` returns `history.CaseError` for every validation failure; `handleIHave` maps
  everything except Pass/Dupes to 436 (`:103-110`). A peer such as INN or another pugleaf retries 436 forever.
- **RFC:** RFC 3977 §3.2.1: "501 MUST be used when the base command is implemented but the requested variant is not,
  and 500 MUST be used only when the base command itself is not implemented." §6.3.1.2: "If posting is prohibited for
  some installation-dependent reason, a 440 response MUST be returned." §6.3.2.2: "a 437 response, indicating that the
  article was rejected".
- **Fix:** variant → 501. POST: no processor or no permission → 440. IHAVE: no processor → 500. After the transfer,
  `CaseError` with a validation error → 437, I/O or DB trouble → 436. This needs a distinguishable error from
  `processArticle` (intake plan). Effort S (codes) / M (error classes).

### RFC-6: XOVER/XHDR range and variant handling

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| RFC-6 | RFC-6 | medium | rfc | `nntp-cmd-xhdr.go:35-74,97-104`, `nntp-cmd-xover.go:35-70,86`, `database/queries.go:1905-1910` | CONFIRMED (the "not rate limited" detail rejected) |

- **Scenario (repro "reader"):**
  - `XHDR Newsgroups 3-5` → `221` with an empty block. Only subject, from, date, message-id, references, bytes and
    lines are served (`queries.go:1886-1909`); any other header silently returns nothing (`:1910`).
  - `XHDR subject <msgid>` and `XOVER <msgid>` → `501 Invalid range format`.
  - `XOVER 10-20` and `XHDR subject 10-20` (no articles) → `224`/`221` with empty blocks.
  - `XOVER 5-3` / `XHDR subject 5-3` → `501 Invalid range: start greater than end`.
  - For `n-`, the open end is `currentLast` from the last GROUP, so `XOVER 9-` with `currentLast=5` → 501. LISTGROUP
    doesn't update `currentLast`.
- **Evidence:** Appendix A, "reader": `XHDR Newsgroups 3-5`, `XOVER 10-20` and `XHDR subject 10-20` → empty blocks; `XOVER 5-3`/`XHDR subject 5-3` → 501; message-id forms → `501 Invalid range format`.
- **RFC:** RFC 2980 §2.6: "The optional message-id argument indicates a specific article" and "If no articles are in
  the range specified, a 420 error response is returned"; responses `221/412/420/430/502`. RFC 2980 §2.8 (XOVER):
  "If no articles are in the range specified, a 420 error response is returned". RFC 3977 §8.3.2 (OVER, the
  successor): "If the argument is a range and no articles in that number range exist … including the case where the
  second number is less than the first one, a 423 response MUST be returned."
- **Fix:** one shared range parser (`nntp-cmd-range.go`, Appendix B). An empty or reversed range → 420 for
  XOVER/XHDR (423 for OVER/HDR). Add the message-id form (430 when absent). Serve any header from `headers_json`
  (articles lacking it are omitted), or answer 503 for headers the server doesn't index, as HDR does (RFC 3977
  §8.5.2). Effort M.

### BUG-3: hardcoded sleeps

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-3 | BUG-3 | low | robustness | `nntp-server.go:38-42,90-93`, `nntp-article-common.go:46-48`, `nntp-cmd-group.go:10-12`, `nntp-cmd-helpers.go:12-17`, `nntp-cache-local.go:15-20` | CONFIRMED |

- **Scenario:** the wave-0 hooks made the delays per-server fields, but the defaults are unchanged
  (repro: `DefaultArticleCmdDelay=200ms DefaultGroupCmdDelay=333.333333ms DefaultErrorDelay=1s`), and no config key or
  flag sets them. Every ARTICLE/HEAD/BODY/STAT sleeps 200 ms before the lookup, so a pipelined reader gets at most 5
  articles/s per connection. `CronLocal430` still sleeps 15 s per tick in an endless loop with no stop channel
  (`nntp-cache-local.go:15-20`; the goroutine leak is BUG-7). The 1 s error delay is applied inconsistently: 481
  (`nntp-cmd-auth.go:34`), GROUP 411 (`nntp-cmd-group.go:22,26`), unknown commands (`cliconns.go:155`), LIST 501, MODE
  500 and the XHDR reversed range (`nntp-cmd-xhdr.go:72-74`) are not delayed.
- **Evidence:** Appendix A, "session-noproc" (printed defaults); code trace of the sleeps at the listed lines.
- **RFC:** none violated. RFC 3977 §3.5 pipelining is slowed, not broken.
- **Fix:** expose the delays in `config.ServerConfig.NNTP` (default 0 for article commands; a token bucket per
  connection instead of a fixed sleep). Apply the error delay in one place (`sendResponse` for 4xx/5xx) or not at all.
  Effort S.

### BUG-4: GROUP / LISTGROUP / LIST ACTIVE state and fields

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-4 | BUG-4 | medium | rfc | `nntp-cmd-group.go:29-35,39-73`, `nntp-cmd-list.go:36-37` | CONFIRMED (extended) |

- **Scenario (repro "reader", group with articles 3, 4, 5):**
  - `GROUP t9.rfc.a.1` → `211 3 1 5`: low is hardcoded to 1 (`:31`), the real low water mark is 3.
  - `XOVER` with no argument right after GROUP → `420 Current article number is invalid`: GROUP never sets
    `currentArticle`.
  - **Extension:** GROUP doesn't *reset* it either. `STAT 3` in group A, then `GROUP B` and `XOVER` → `224` for
    article 3 **of group B** (an empty block here, because B has only 1-2). The current article leaks across groups.
  - `LISTGROUP` → `211 Article numbers follow for t9.rfc.a.1` (no count/low/high). `LISTGROUP t9.rfc.a.1 4-` still
    lists 3, 4, 5 (range ignored). `GROUP B; LISTGROUP A; STAT 3` → 423, because the group wasn't selected.
  - **Extension:** `LISTGROUP t9.rfc.nosuch.listgroup` → `211 …` with an empty list instead of 411, because
    `GetGroupDB` creates the group DB (SEC-2). LISTGROUP also doesn't check `active`.
  - `LIST` → `t9.rfc.a.1 5 1 n`: low=1 and status `n` for every group (`nntp-cmd-list.go:36-37`), while the
    `newsgroups.status` column (`y` for the seeded groups) and `low_water` are ignored. `GetActiveNewsgroups` doesn't
    even select them.
- **Evidence:** Appendix A, "reader" (the GROUP, STAT, XOVER, LISTGROUP and LIST lines quoted in the scenario).
- **RFC:** RFC 3977 §6.1.1.2: "the current article number MUST be set to the first article in the group (this applies
  even if the group is already the currently selected newsgroup)". The reported low water mark is "the article
  numbers of the first and last articles". §6.1.2.2: LISTGROUP's "arguments on the initial response line are the same
  as for the GROUP command", "If the optional range argument is specified, only articles within the range are
  included", and "If the group specified is not available on the server, a 411 response MUST be returned". §7.6.3:
  status `y`/`n`/`m` "MUST NOT use them with any other meaning". RFC 6048 §3.1 adds `x`, `j` and `=group`.
- **Fix:** GROUP: low = `low_water` (or `MIN(article_num)`), set `currentArticle = low` (0 for an empty group).
  LISTGROUP: `MainDBGetNewsgroup` first (411), then the same 211 line as GROUP, select the group, apply the range in
  SQL (`nntp_listgroup.go`, SEC-7). LIST ACTIVE: select `low_water` and `status`, and print the status (`y` when
  posting is possible and the group allows it). Effort M.

### BUG-5: overview fields not sanitized, trailing TAB, no Xref

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-5 | BUG-5 | medium | rfc | `nntp-cmd-helpers.go:68-80`, `common/headers.go:580-588` | CONFIRMED |

- **Scenario (repro "format"):**
  - Subject `tab\there` → `1\ttab\there\t…`, which shifts every following field by one for the client.
  - Subject `cr\rhere` → a bare CR inside the line.
  - Subject `first\n.` (from repeated Subject headers) → a bare LF (A-nntp-rfc-2).
  - Every line ends in `\t` (`"%d\t…\t%d\t"`, `:70`): an empty 9th field that LIST OVERVIEW.FMT doesn't announce
    (OVERVIEW.FMT doesn't exist). No `Xref:` field is ever served.
- **Evidence:** Appendix A, "format": `XOVER 1` (TAB in subject), `XOVER 6` (bare CR), `XHDR subject 4` (bare LF); "reader": `XOVER 3-5` (trailing `\t` on every line).
- **RFC:** RFC 3977 §8.3.2: "the value is processed by first removing all CRLF pairs … and then replacing each TAB with
  a single space" and "servers SHOULD check for these characters and replace each one by a single space". RFC 2980
  §2.8: "any tab and end-of-line characters in any header data that is returned will be converted to a space character."
- **Fix:** a `sanitizeOverviewField` (TAB/CR/LF/NUL → space) for all five header fields and for XHDR values. Drop the
  trailing TAB, or announce `Xref:full` in LIST OVERVIEW.FMT and fill it from the local numbers (INT-4). Effort S.

### BUG-6: HEAD/ARTICLE/BODY data block formatting

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-6 | BUG-6 | medium | rfc | `nntp-cmd-helpers.go:20-23,49-53`, `nntp-article-common.go:231-247,266-271` | CONFIRMED |

- **Scenario (repro "format"):**
  - Header lines are sent raw: `.Dotted-Header: x` goes out unstuffed, so the client strips the dot. A header line
    `.` ends the block (A-nntp-rfc-2).
  - Empty `headers_json` → `HEAD 2` sends `"\r\n.\r\n"`, an empty line inside the header block, because
    `strings.Split("", "\n")` returns `[""]`.
  - Empty body → `BODY 1` sends `"\r\n.\r\n"` (a body of one empty line instead of an empty block), and `ARTICLE 1`
    ends with `"\r\n\r\n.\r\n"` (two blank lines) because `parseArticleBody` returns `[]string{""}` (`:50-52`).
  - Body dot-stuffing is correct (`BODY 5` → `..dot`, `..`, `...two`).
- **Evidence:** Appendix A, "format": `HEAD 3` (unstuffed `.Dotted-Header`), `HEAD 2` (`\r\n.\r\n`), `BODY 1`/`ARTICLE 1` (extra blank line), `BODY 5` (correct body stuffing).
- **RFC:** RFC 3977 §3.1.1 (quoted in A-nntp-rfc-2). §3.6: an article consists of "the headers, an empty line, and the
  body"; a header block contains no empty line.
- **Fix:** split `headers_json` with empty-string handling (no lines when empty), stuff header lines through the shared
  data-block writer, return `nil` for an empty body. Effort S.

### BUG-9: idle timeout and write deadline

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| BUG-9 | BUG-9 | low | rfc | `nntp-server-cliconns.go:15,54-63,81`, `nntp-server.go:93`, `nntp-cmd-posting.go:221-260` | CONFIRMED |

- **Scenario:** `DefaultNNTPcliconnTimeout = 60 s` (repro prints `1m0s`), and `NewNNTPServer` copies it into
  `IdleTimeout`. A newsreader that is idle for a minute while the user reads is disconnected. `UpdateDeadlines` runs
  once per command and sets **both** deadlines, so a whole response (a large LISTGROUP, XOVER or article to a slow
  client) must be written within 60 s of the command. The read deadline is not refreshed while a POST/IHAVE/TAKETHIS
  article is being received (`readArticleLines`), so a large article on a slow link is cut off after 60 s.
- **Evidence:** Appendix A, "session-noproc" prints `DefaultNNTPcliconnTimeout=1m0s`; code trace: `UpdateDeadlines` (`nntp-server-cliconns.go:54-63`) runs once per command (`:81`), never inside `readArticleLines` (`nntp-cmd-posting.go:221-260`).
- **RFC:** RFC 3977 §3.1: "An NNTP server MAY have an inactivity autologout timer. Such a timer SHOULD be of at least
  three minutes' duration".
- **Fix:** default idle timeout ≥ 3 min (config key). Refresh the read deadline per line while receiving an article
  (with a separate total cap). Set the write deadline per write chunk, not per command. Effort S.

### FN-1: stub files, unused functions, misleading README

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| FN-1 | FN-1 | info | dead-code | see `## Function inventory` | CONFIRMED |

- **Evidence:**
  - `nntp-cmd-{article,body,head,stat}.go` are 3-line `// stub` files, and `nntp-cmd-reader.go` holds only `package
    nntp`. The handlers live in `nntp-article-common.go`, MODE in `nntp-cmd-basic.go`, LISTGROUP in `nntp-cmd-group.go`.
  - Repo-wide grep (non-test `.go`): no callers for `parseArticleHeadersShort`, `ServerStats.{GetTotalConnections,
    GetCommandCount, GetAllCommandCounts, GetAuthStats, GetUptime, Reset}`, `AuthManager.{CheckGroupAccess, CanPost,
    IsAdmin, CheckConnectionLimit}` or `ClientConnection.RemoteAddr`. `ClientConnection.capabilities`, `.created` and
    `.lastCommand` are written and never read.
  - `README.md`:
    - points at `examples/nntp-server` (`ls examples` → no such directory)
    - lists the stub files as handlers, puts LISTGROUP in `nntp-cmd-list.go` (line 22) and MODE READER in
      `nntp-cmd-reader.go` (line 23)
    - claims "Graceful shutdown with timeout" (`Stop()` waits on nothing, `nntp-server.go:238-251`)
    - says auth "Leverages `GetUserByUsername()` and `GetUserPermissions()`" (it uses `AuthenticateNNTPUser`)
    - lists POST and AUTHINFO as TODO although both exist
    - ends with "production-ready base for implementing the complete RFC 3977 protocol!"
  - `HELP` text (`nntp-cmd-basic.go:31-47`) claims `STAT/HEAD/BODY/ARTICLE [<msgid>|<num>]` with an optional argument
    (that form gives 501, RFC-3) and omits POST/IHAVE/TAKETHIS.
- **Fix:** delete the stub files, the dead functions (or wire `CheckConnectionLimit`/`CanPost` where SEC-5 needs them),
  and rewrite README from the dispatch table. Effort S.

## Command matrix

Legend: **Impl** yes / partial / no. **Codes OK** lists the wrong codes as `sent → expected`. "MUST" in
"Mandatory when" means mandatory for every server. "—" means not applicable.

| Command / variant | RFC § | Mandatory when | Impl | Syntax OK | Codes OK (wrong ones) | Multi-line OK | Findings |
|----|----|----|----|----|----|----|----|
| (greeting) | 3977 5.1 | MUST | yes | — | no: without processor 200 → 201. MaxConns: close → 400 | — | RFC-4, A-3 |
| CAPABILITIES [kw] | 3977 5.2 | MUST | yes | yes (keyword ignored, allowed) | yes (101) | yes (DotWriter) | RFC-2 |
| MODE READER | 3977 5.3 | READER / MODE-READER | yes | no: surplus args ignored | 200 → 201 without processor | — | RFC-4, A-5 |
| MODE STREAM | 4644 2.3 | STREAMING (TAKETHIS offered) | no | — | 500 → 203 | — | RFC-1, RFC-5 |
| MODE \<other\> | 3977 3.2.1 | — | — | — | 500 → 501 | — | RFC-5 |
| QUIT | 3977 5.4 | MUST | yes | surplus args ignored | 205 ok. Pipelined next command crashes the process | — | A-1, A-5, BUG-7 |
| HELP | 3977 7.2 | MUST | yes | yes | 100 ok | yes (DotWriter) | FN-1 (text) |
| DATE | 3977 7.1 | READER | no | — | 500 → 111 | — | RFC-1 |
| GROUP | 3977 6.1.1 | READER | partial | surplus args ignored | 211 low=1 (wrong value), current article not set/reset | — | BUG-4, A-5 |
| LISTGROUP [g [range]] | 3977 6.1.2 | READER | partial | range ignored | 211 line lacks fields. Unknown group 211 → 411. DB error 503 → 403 | numbers raw, ok | BUG-4, SEC-2, SEC-7, A-4 |
| LAST | 3977 6.1.3 | READER | no | — | 500 → 223/412/420/422 | — | RFC-1 |
| NEXT | 3977 6.1.4 | READER | no | — | 500 → 223/412/420/421 | — | RFC-1 |
| ARTICLE \<msgid\> | 3977 6.2.1 | READER | yes | yes | 220 0\|n ok, 430 ok (no processor: 430 for existing articles outside the group) | no: header lines unstuffed, empty-body blank line | BUG-6, A-2, A-9 |
| ARTICLE n | 3977 6.2.1 | READER | yes | 0/negative not rejected | 0/neg 502 → 423. GetGroupDB error 411 → 403. Current article set to 0 | as above | RFC-3, BUG-1, A-4 |
| ARTICLE (none) | 3977 6.2.1 | READER | no | — | 501 → 220/412/420 | — | RFC-3 |
| HEAD \<msgid\>/n/(none) | 3977 6.2.2 | MUST | msgid, n | as ARTICLE | as ARTICLE | no: header lines unstuffed, empty headers_json → blank line | RFC-3, BUG-6, A-2 |
| BODY \<msgid\>/n/(none) | 3977 6.2.3 | READER | msgid, n | as ARTICLE | as ARTICLE | empty body → `\r\n.\r\n` | RFC-3, BUG-6 |
| STAT \<msgid\>/n/(none) | 3977 6.2.4 | MUST | msgid, n | as ARTICLE | as ARTICLE | — | RFC-3 |
| POST | 3977 6.3.1 | POST | yes | yes | no processor 502 → 440 (or 500). No permission 502 → 440 | input un-stuffed ok | RFC-5, SEC-3, INT-1 |
| IHAVE \<msgid\> | 3977 6.3.2 | IHAVE | yes | msgid not validated | permanent reject 436 → 437. Garbled 436 → 437. No processor 502 → 500 | input ok | RFC-5, A-7, INT-2 |
| NEWGROUPS | 3977 7.3 | READER | no | — | 500 → 231 | — | RFC-1 |
| NEWNEWS | 3977 7.4 | NEWNEWS (not advertised) | no | — | 500 ok | — | RFC-1 |
| LIST / LIST ACTIVE [wildmat] | 3977 7.6.3 | READER | partial | wildmat ignored | 215 ok. Low=1, status always `n`. DB error 503 → 403 | raw, ok for valid names | BUG-4, RFC-1, A-4 |
| LIST NEWSGROUPS [wildmat] | 3977 7.6.6 | READER | partial | wildmat ignored | 215 ok. DB error 503 → 403 | raw | RFC-1, A-4 |
| LIST ACTIVE.TIMES / DISTRIB.PATS | 3977 7.6.4-5 | optional (not advertised) | no | — | 501 ok | — | — |
| LIST OVERVIEW.FMT | 3977 8.4, 2980 2.1.7 | OVER. SHOULD with XOVER | no | — | 501 (feature missing) | — | RFC-1 |
| LIST HEADERS | 3977 8.6 | HDR (not advertised) | no | — | 501 ok | — | RFC-1 |
| LIST COUNTS/DISTRIBUTIONS/MODERATORS/MOTD/SUBSCRIPTIONS | 6048 2.2-2.6 | optional (not advertised) | no | — | 501 ok | — | — |
| OVER | 3977 8.3 | OVER (not advertised) | no | — | 500 ok | — | RFC-1 |
| HDR | 3977 8.5 | HDR (not advertised) | no | — | 500 ok | — | RFC-1 |
| XOVER [range] | 2980 2.8 | XOVER advertised | yes | msgid form n/a in 2980 | empty range 224 → 420. Reversed 501 → 420. 411/503 → 403 | no: fields unsanitized, trailing TAB | BUG-5, RFC-6, BUG-2, SEC-1, A-2, A-4 |
| XHDR hdr [range\|msgid] | 2980 2.6 | XHDR advertised | partial | msgid form missing (501) | unknown header → empty 221. Empty/reversed range 221/501 → 420. 411/503 → 403 | no: LF in values | RFC-6, BUG-5, BUG-2, SEC-1, A-2 |
| AUTHINFO USER/PASS | 4643 2.3 | AUTHINFO advertised | yes | password with spaces truncated | after auth 381/281 → 502. Plaintext → 483 not used. 481/482 ok | — | SEC-5, A-11 |
| AUTHINFO SASL / \<other\> | 4643 2.4 | AUTHINFO SASL (not advertised) | no | — | 500 → 501 | — | RFC-5 |
| STARTTLS | 4642 2.2 | STARTTLS (not advertised) | no | — | 500 ok | — | RFC-1, SEC-5 |
| CHECK | 4644 2.4 | STREAMING (TAKETHIS offered) | no | — | 500 → 238/431/438 | — | RFC-1 |
| TAKETHIS \<msgid\> | 4644 2.5 | STREAMING | yes (label `TAKETHIS`) | msgid not validated | 480/501/502 lack the msgid. Temporary error 439 → 400 + close | input ok | RFC-2, A-7, A-8 |
| \<unknown\> / empty line | 3977 3.2.1 | — | — | — | 500 ok. Echoed status line > 512 octets | — | A-6, SEC-4 |
| command line > 512 octets | 3977 3.1 | — | — | not enforced | 501 never sent | — | SEC-3 |

## Response code table

Every code the server sends (at 66c36f8) against the codes the RFC allows for that command. Generic codes
(400, 403, 480, 483, 500, 501, 502, 503) are allowed only with their §3.2.1 meaning. **Bold** marks a violation.

| Command | Code sent | Where | RFC-allowed codes | Verdict |
|----|----|----|----|----|
| greeting | 200 | `cliconns.go:100,102` | 200, 201, 400, 502 (3977 5.1.1) | **200 without processor (201)**. No 400 on MaxConns (`nntp-server.go:190`) |
| (any) on handler error | 500 | `cliconns.go:89` | 403 (fault), 400 (terminating) | **wrong** |
| empty line | 500 | `cliconns.go:109` | 500/501 | ok |
| unknown | 500 | `cliconns.go:155` | 500 | ok (line > 512 octets possible, A-6) |
| CAPABILITIES | 101 | `basic.go:11` | 101, 501 (non-keyword arg) | ok |
| MODE | 501 (no arg) | `basic.go:17` | 501 | ok |
| MODE READER | 200 | `basic.go:23` | 200, 201, 502 | **200 without processor** |
| MODE \<other\> | 500 | `basic.go:25` | 501. STREAM: 203 (4644 2.3) | **wrong** |
| HELP | 100 | `basic.go:49` | 100 | ok |
| QUIT | 205 | `basic.go:54` | 205 | ok |
| GROUP | 501 | `group.go:16` | 501 | ok |
| GROUP | 411 | `group.go:22,26` | 211, 411 | ok |
| GROUP | 211 | `group.go:34` | 211 number low high group | **low wrong (1)** |
| LISTGROUP | 412 | `group.go:47` | 211, 411, 412 | ok |
| LISTGROUP | 411 | `group.go:54` | 411 | ok, but unreachable for unknown names (GetGroupDB creates them, SEC-2) |
| LISTGROUP | 503 | `group.go:62` | 403 | **wrong** |
| LISTGROUP | 211 | `group.go:67` | 211 number low high group | **fields missing** |
| ARTICLE/HEAD/BODY/STAT | 500 | `article-common.go:84` | 403 | **wrong** (unreachable) |
| ARTICLE/HEAD/BODY/STAT | 501 (no arg) | `article-common.go:97` | 220-223, 412, 420 (third form) | **wrong** |
| ARTICLE/HEAD/BODY/STAT | 412 | `article-common.go:108` | 412 | ok |
| ARTICLE/HEAD/BODY/STAT | 501 (bad number) | `article-common.go:115` | 501 | ok |
| ARTICLE/HEAD/BODY/STAT | 430 | `article-common.go:133,141,155` | 430 | ok (texts are debug strings) |
| ARTICLE/HEAD/BODY/STAT | 411 | `article-common.go:166` | 403 (DB fault) | **wrong** |
| ARTICLE/HEAD/BODY/STAT | 423 | `article-common.go:176,189` | 423 | ok |
| ARTICLE/HEAD/BODY/STAT | 502 (n ≤ 0) | `article-common.go:196` | 423 or 501 | **wrong** |
| ARTICLE | 220 | `article-common.go:226` | 220 | ok |
| HEAD | 221 | `article-common.go:262` | 221 | ok |
| BODY | 222 | `article-common.go:286` | 222 | ok |
| STAT | 223 | `article-common.go:307` | 223 | ok |
| XOVER | 412 | `xover.go:12` | 224, 412, 420, 502 (2980 2.8) | ok |
| XOVER | 420 | `xover.go:23` | 420 | ok |
| XOVER | 501 (syntax) | `xover.go:34,40,50,58` | 501 | ok |
| XOVER | 501 (reversed range) | `xover.go:67` | 420 (2980), 423 (OVER) | **wrong** |
| XOVER | 411 | `xover.go:74` | 403 | **wrong** |
| XOVER | 503 | `xover.go:82` | 403 | **wrong** |
| XOVER | 224 (empty block for an empty range) | `xover.go:86` | 420 (2980), 423 (OVER) | **wrong** |
| LIST | 501 (variant) | `list.go:21` | 501 | ok |
| LIST ACTIVE/NEWSGROUPS | 503 | `list.go:30,50` | 403 | **wrong** |
| LIST ACTIVE/NEWSGROUPS | 215 | `list.go:33,53` | 215 | ok (content: BUG-4, RFC-1) |
| XHDR | 412 | `xhdr.go:13` | 221, 412, 420, 430, 502 (2980 2.6) | ok |
| XHDR | 501 | `xhdr.go:18,41,47,57,65` | 501 | ok (msgid form → 501: RFC-6) |
| XHDR | 420 | `xhdr.go:30` | 420 | ok |
| XHDR | 501 (reversed) | `xhdr.go:73` | 420 | **wrong** |
| XHDR | 411 | `xhdr.go:80` | 403 | **wrong** |
| XHDR | 503 | `xhdr.go:88` | 403 (fault) | **wrong** |
| XHDR | 221 (empty block for an empty range or unknown header) | `xhdr.go:92` | 420 / values | **wrong** (RFC-6) |
| AUTHINFO | 501 | `auth.go:11` | 501 | ok |
| AUTHINFO USER | 381 | `auth.go:21` | 281, 381, 481, 482, 502 (4643 2.3.1) | **after auth: 502** (SEC-5) |
| AUTHINFO PASS | 482 | `auth.go:26` | 482 | **after auth: 502** (repro: `482` to `AUTHINFO PASS again`) |
| AUTHINFO PASS | 481 | `auth.go:34` | 481 | ok |
| AUTHINFO PASS | 281 | `auth.go:42` | 281 | ok. **Plaintext: 483 expected by default** (4643 2.3.2 SHOULD) |
| AUTHINFO \<other\> | 500 | `auth.go:45` | 501 | **wrong** |
| POST | 502 (no processor) | `posting.go:17` | 440 (or 500) | **wrong** |
| POST | 480 | `posting.go:22` | 480 | ok |
| POST | 502 (no permission) | `posting.go:27` | 440 | **wrong** |
| POST | 340 / 240 / 441 | `posting.go:31,46,39,43` | 340, 440 / 240, 441 | ok |
| IHAVE | 502 (no processor) | `posting.go:53` | 500 (not offered) | **wrong** |
| IHAVE | 501 | `posting.go:57` | 501 | ok |
| IHAVE | 480 / 502 (no permission) | `posting.go:63,67` | 480 / 502 | ok |
| IHAVE | 435 / 436 (initial) | `posting.go:76,79,83` | 335, 435, 436 | ok |
| IHAVE | 335 | `posting.go:87` | 335 | ok (also for invalid ids, A-7) |
| IHAVE | 436 (unreadable/garbled) | `posting.go:95` | 437 (garbled) / 436 (I/O) | **wrong for garbled** |
| IHAVE | 437 | `posting.go:99,107` | 437 | ok |
| IHAVE | 235 | `posting.go:105` | 235 | ok |
| IHAVE | 436 (processing error) | `posting.go:109` | 437 for permanent rejects | **wrong** (RFC-5) |
| TAKETHIS | 502 (no processor) | `posting.go:132` | 500 (not offered) | **wrong** |
| TAKETHIS | 501 | `posting.go:135` | 501 | ok |
| TAKETHIS | 439 (read error) | `posting.go:141` | 439 / 400 | ok (but sent on a closed conn after "too large", BUG-7) |
| TAKETHIS | 480 / 502 | `posting.go:147,151` | generic codes allowed, but lack the msgid | weak (A-8) |
| TAKETHIS | 439 (dupe/retry/history error) | `posting.go:159,161,164` | 439 (dupe), 400 + close (temporary) | **history error wrong** (A-8) |
| TAKETHIS | 439 / 239 | `posting.go:170,174,178,181` | 239, 439 | ok |

## CAPABILITIES

**Today** (`cliconns.go:200-217`), identical in every state except that the last three depend on the processor:

```
VERSION 2 / READER / AUTHINFO USER / LIST ACTIVE NEWSGROUPS / XOVER / XHDR / MODE-READER / [POST / IHAVE / TAKETHIS]
```

**Expected** with the commands that exist today plus the fixes in this report. Items in *italics* appear only once
the corresponding RFC-1 command is implemented. `READER` is honest only after DATE/NEXT/LAST/NEWGROUPS exist
(RFC-1); until then there is no conformant way to advertise reading.

| State | Expected lines (VERSION 2 first) | Must not appear |
|----|----|----|
| plaintext, before auth, with processor | VERSION 2, READER, IHAVE, POST, LIST ACTIVE NEWSGROUPS *OVERVIEW.FMT*, XOVER, XHDR (private X labels are allowed) or *OVER* / *HDR*, *STREAMING*, *STARTTLS*, IMPLEMENTATION go-pugleaf \<version\> (optional), `AUTHINFO USER` **only** if plaintext AUTHINFO is explicitly enabled | MODE-READER, TAKETHIS (unregistered label), AUTHINFO USER by default (RFC 4643 §2.1) |
| plaintext, after auth (posting user) | as above, minus AUTHINFO and STARTTLS | AUTHINFO, STARTTLS (RFC 4642 §2.1), MODE-READER |
| plaintext, after auth (user without posting) | as above, minus POST, IHAVE, STREAMING (recommended: 3977 §5.2 allows state-dependent lists) | AUTHINFO, STARTTLS, POST |
| plaintext, no processor (read-only) | VERSION 2, READER, LIST ACTIVE NEWSGROUPS…, XOVER, XHDR / *OVER*, *HDR*, AUTHINFO as above. Greeting and MODE READER → 201 | POST, IHAVE, STREAMING/TAKETHIS, MODE-READER |
| TLS (implicit, TLS port), before auth | as the plaintext row, plus `AUTHINFO USER` (strong encryption in use) | STARTTLS (TLS already active), MODE-READER |
| TLS, after auth | as the TLS row, minus AUTHINFO | AUTHINFO, STARTTLS, MODE-READER |
| after STARTTLS (once implemented) | as the TLS rows | STARTTLS, MODE-READER (RFC 4642 §2.2.2) |

`ClientConnection` already has `isTLS`, `authenticated`, `user` and `server.Processor`, so the list can be built per
call. The unused `capabilities` field (FN-1) can go.

## Served headers (RFC 5536 §3, for HEAD/ARTICLE)

HEAD and ARTICLE serve `headers_json` verbatim (`nntp-cmd-helpers.go:20-23`): whatever was received or fetched,
including malformed lines (A-nntp-rfc-2). The server never adds or rewrites a header. RFC 5536 §3.1: "Each Netnews
article conformant with this specification MUST have exactly one of each of the following header fields: Date, From,
Message-ID, Newsgroups, Path, and Subject."

| Header | Served from | Issue | Owner |
|----|----|----|----|
| Path | original line in `headers_json` | the local path identity is stored only in the `path` column, never in the served header | INT-1 (audit-intake) |
| Message-ID / Date | original lines | POST without them → 441 instead of injecting them | INT-1 |
| Subject (repeated) | all copies served, joined with LF in the `subject` column | XOVER/XHDR desync | A-nntp-rfc-2, BUG-5 |
| Xref | the **foreign** Xref is kept in `headers_json` (`ParseHeaders` skips it only for the map, `nntp-client-commands.go:936-939,950`) | shows another server's numbers. No local Xref is generated | INT-4 |
| web-posted articles | `headers_json` lacks Subject/Date/Message-ID/Path | non-conformant articles served | INT-7 (audit-web) |
| empty `headers_json` | one empty line | blank line inside HEAD | BUG-6 |

## Function inventory

| function/file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `nntp-cmd-article.go`, `nntp-cmd-body.go`, `nntp-cmd-head.go`, `nntp-cmd-stat.go` | yes | | | yes (README names them as handlers) | | 3 lines: `package nntp` + `// stub` |
| `nntp-cmd-reader.go` | yes | | | yes (README: "MODE READER and reader mode logic") | | 1 line: `package nntp` |
| `parseArticleHeadersShort` (`nntp-cmd-helpers.go:25`) | | yes | yes | | | no caller; staticcheck U1000 |
| `ServerStats.GetTotalConnections/GetCommandCount/GetAllCommandCounts/GetAuthStats/GetUptime/Reset` (`nntp-server-statistics.go:50,64,71,97,104,111`) | | | yes | | | no caller outside the file (stats are collected, never read) |
| `AuthManager.CheckGroupAccess` (`nntp-auth-manager.go:47`) | | | yes | yes | | always true for any non-nil user; never consulted |
| `AuthManager.CanPost` (`:58`) | | | yes | | | handlers read `c.user.Posting` directly |
| `AuthManager.IsAdmin` (`:66`) | | | yes | | | always false |
| `AuthManager.CheckConnectionLimit` (`:77`) | | | yes | | | per-user `MaxConns` never enforced (SEC-5) |
| `ClientConnection.RemoteAddr` (`cliconns.go:220`) | | | yes | | | no caller |
| `ClientConnection.capabilities/created/lastCommand` (`cliconns.go:31-33`) | | | yes | | | written, never read |
| `ClientConnection.Close` (`basic.go:59`) | | | | | yes | sets `c.conn = nil` while `Handle` keeps using it → A-nntp-rfc-1 |
| `handleHelp` (`basic.go:30`) | | | | yes | | advertises optional article arguments (RFC-3), omits POST/IHAVE/TAKETHIS |
| `getServerCapabilities` (`cliconns.go:200`) | | | | yes | yes | static list (RFC-2) |
| `handleListGroup` (`group.go:39`) | | | | | yes | no selection, no range, bare 211 (BUG-4) |
| `parseArticleBody` (`helpers.go:49`) | | | | | yes | `[""]` for an empty body (BUG-6) |
| `formatOverviewLine` (`helpers.go:68`) | | | | | yes | no sanitizing, trailing TAB (BUG-5) |
| `Local430.CronLocal430` (`cache-local.go:15`) | | | | yes ("NewLocal430 creates" comment) | | endless loop without stop (BUG-7) |
| `nntp-cache-local.go:54-146` (`CacheMessageIDNumtoGroup`) | | yes | | | | commented-out block |
| `NNTPServer.Stop` (`nntp-server.go:217`) | | | | yes | | the "wait with timeout" goroutine closes `done` immediately (BUG-7) |
| `NNTPServer.IsRunning` (`nntp-server.go:258`) | | | yes | | | no caller in `cmd/` or `internal/` outside tests |
| `internal/nntp/README.md` | | | | yes | | non-existent `examples/nntp-server`; wrong file map; stale TODOs; "graceful shutdown"; wrong auth functions (FN-1) |
| `cmd/nntp-server` `-maxconnections` text | | | | yes | | counts all connections, not authenticated ones (A-12) |

## Seed items rejected

No seed in scope is rejected as a whole. Two sub-claims are rejected:

- **RFC-2, "STREAMING, OVER, HDR and IMPLEMENTATION are never listed":** not a defect as stated. OVER and HDR are not
  implemented, so they must **not** be listed (RFC 3977 §3.3.3). IMPLEMENTATION is optional ("This capability MAY be
  provided"). STREAMING may only be listed once MODE STREAM and CHECK exist (RFC 4644 §2). The real defects are the
  unregistered `TAKETHIS` label, `MODE-READER` next to `READER`, and `AUTHINFO USER` after auth and on plaintext; those
  remain CONFIRMED.
- **RFC-6, "no message-id form (fails `ParseInt` → 501, not rate limited)":** the 501 is confirmed, but both
  message-id paths **are** rate limited. A message-id with two or more `-` hits `rateLimitOnError()` before
  `501 Invalid range format` (`nntp-cmd-xhdr.go:39-41`), one with exactly one `-` hits it before `501 Invalid start
  number` (`:44-47`; the part before `-` starts with `<`), and one without `-` hits it before `501 Invalid article
  number` (`:62-65`). All three branches are rate limited. The only undelayed 501 in XHDR is the reversed range (`:72-74`).

## Not covered / follow-ups

- **Owned by audit-nntp-sec, referenced here only:** SEC-1 (XHDR spin), SEC-2 (LISTGROUP group creation, seen in the
  repro as `211` for an unknown group), SEC-3 (line limits; no 501 for >512-octet commands), SEC-4, SEC-5 (481 delay,
  second AUTHINFO accepted, seen in the repro), SEC-7, BUG-1 (`ARTICLE 4` then a bare `XOVER` → 420, seen in the
  repro), BUG-2, BUG-7 and BUG-8. A-nntp-rfc-1 should be cross-listed there.
- **Not run:** the TLS listener and implicit-TLS CAPABILITIES (code read only: `isTLS` changes only the greeting
  text). The real `processor.Processor` (the repro used `hFakeProcessor`; the IHAVE 436 mapping is traced in
  `processor/threading.go`). The idle timeout at its 60 s default (code trace plus printed value). Large-range
  behavior of XOVER/XHDR (BUG-2/SEC-1).
- **Intake-side follow-ups (audit-intake):** whether `processArticle` should reject header lines without a colon and
  repeated mandatory headers (the root of A-nntp-rfc-2 scenario B), and whether the fetcher path
  (`nntp-client-commands.go`) can also store a bare `.` header line from an upstream server.
- **Tests:** wave 2 should add a byte-exact test for the `.` header line and the LF subject (A-nntp-rfc-2), and a
  `runIsolated` test for `QUIT\r\nHELP\r\n` (A-nntp-rfc-1). Neither id is in the wave-2 test lists yet.

## Appendix A: repro transcript

Trimmed output of `TestZZAuditRFC` (`C:` = client line, `S:` = status line, `S|` = raw data-block line with CRLF
shown). Server log lines are removed. The groups seeded by `hGroup` are:

- `t9.rfc.a.1`: articles 3-5 (1-2 skipped)
- `t9.rfc.b.2`: articles 1-2
- `t9.rfc.empty.3`: no articles
- `t9.rfc.fmt.4`: special articles

```
=== RUN   TestZZAuditRFC/session-noproc
greeting: "200 go-pugleaf NNTP server ready"
C: "CAPABILITIES"
S: "101 Capability list:"
S| "VERSION 2\r\n"
S| "READER\r\n"
S| "AUTHINFO USER\r\n"
S| "LIST ACTIVE NEWSGROUPS\r\n"
S| "XOVER\r\n"
S| "XHDR\r\n"
S| "MODE-READER\r\n"
S| ".\r\n"
C: "MODE READER"            S: "200 Hello, you can post"
C: "MODE STREAM"            S: "500 Unknown MODE: STREAM"
C: "MODE FOO"               S: "500 Unknown MODE: FOO"
C: "MODE READER extra"      S: "200 Hello, you can post"
C: "HELP"                   S: "100 Help text follows"   (16 lines, dot-terminated)
C: "DATE"                   S: "500 Command not recognized: DATE"
C: "NEXT"                   S: "500 Command not recognized: NEXT"
C: "LAST"                   S: "500 Command not recognized: LAST"
C: "NEWGROUPS 20260101 000000 GMT"   S: "500 Command not recognized: NEWGROUPS"
C: "NEWNEWS * 20260101 000000 GMT"   S: "500 Command not recognized: NEWNEWS"
C: "OVER"                   S: "500 Command not recognized: OVER"
C: "HDR subject 1-2"        S: "500 Command not recognized: HDR"
C: "LIST OVERVIEW.FMT"      S: "501 Unknown LIST type: OVERVIEW.FMT"
C: "LIST HEADERS"           S: "501 Unknown LIST type: HEADERS"
C: "LIST ACTIVE.TIMES"      S: "501 Unknown LIST type: ACTIVE.TIMES"
C: "STARTTLS"               S: "500 Command not recognized: STARTTLS"
C: "CHECK <x@test.invalid>" S: "500 Command not recognized: CHECK"
C: "foo"                    S: "500 Command not recognized: FOO"
C: ""                       S: "500 Empty command"
C: "XYZZYAAAA…(605 octets)" S: "500 Command not recognized: XYZZYAAAA…" (status line length 633 octets + CRLF)
C: "POST"                   S: "502 Posting not supported on this server"
C: "IHAVE <a@test.invalid>" S: "502 Transfer not supported on this server"
C: "AUTHINFO FOO bar"       S: "500 Unknown AUTHINFO subcommand: FOO"
C: "AUTHINFO SASL PLAIN"    S: "500 Unknown AUTHINFO subcommand: SASL"
C: "AUTHINFO PASS x"        S: "482 AUTHINFO USER required first"
C: "STAT"                   S: "501 No article specified"
C: "STAT <nosuch@test.invalid>"  S: "430 NotF0"
C: "QUIT now"               S: "205 Goodbye"
DefaultNNTPcliconnTimeout=1m0s DefaultArticleCmdDelay=200ms DefaultGroupCmdDelay=333.333333ms DefaultErrorDelay=1s

=== RUN   TestZZAuditRFC/reader
C: "GROUP t9.rfc.a.1"       S: "211 3 1 5 t9.rfc.a.1"
C: "STAT"                   S: "501 No article specified"
C: "XOVER"                  S: "420 Current article number is invalid"
C: "STAT 3"                 S: "223 3 <t9-rfc-a-1-3@test.invalid> Article exists"
C: "GROUP t9.rfc.b.2"       S: "211 2 1 2 t9.rfc.b.2"
C: "XOVER"                  S: "224 Overview information follows"
S| ".\r\n"                                   <- current article 3 leaked from group a.1
C: "GROUP t9.rfc.a.1 extra-arg"   S: "211 3 1 5 t9.rfc.a.1"
C: "ARTICLE 4"              S: "220 4 <t9-rfc-a-1-4@test.invalid> Article follows"
S| "Path: test.invalid!not-for-mail\r\n"
S| "From: Tester 4 <tester4@test.invalid>\r\n"
S| "Newsgroups: t9.rfc.a.1\r\n"
S| "Subject: Test article 4\r\n"
S| "Date: Sat, 11 Oct 2026 10:04:00 +0000\r\n"
S| "Message-ID: <t9-rfc-a-1-4@test.invalid>\r\n"
S| "\r\n"
S| "body of article 4\r\n"
S| "line two\r\n"
S| ".\r\n"
C: "XOVER"                  S: "420 Current article number is invalid"   <- BUG-1: ARTICLE n set it to 0
C: "HEAD 2"                 S: "423 No such article number"
C: "ARTICLE 0"              S: "502 Article not retrieved"
C: "ARTICLE -1"             S: "502 Article not retrieved"
C: "ARTICLE 99999999999999999999"  S: "501 Invalid article number"
C: "ARTICLE 5 extra"        S: "220 5 <t9-rfc-a-1-5@test.invalid> Article follows" (block as above)
C: "STAT <t9-rfc-a-1-4@test.invalid>"  S: "223 4 <t9-rfc-a-1-4@test.invalid> Article exists"
C: "STAT <t9-rfc-b-2-1@test.invalid>"  S: "430 NotF0"     <- exists in b.2; no processor
C: "GROUP t9.rfc.empty.3"   S: "211 0 1 0 t9.rfc.empty.3"
C: "GROUP no.such.group"    S: "411 No such newsgroup"
C: "GROUP t9.rfc.a.1"       S: "211 3 1 5 t9.rfc.a.1"
C: "LISTGROUP"              S: "211 Article numbers follow for t9.rfc.a.1"
S| "3\r\n"  S| "4\r\n"  S| "5\r\n"  S| ".\r\n"
C: "LISTGROUP t9.rfc.a.1 4-"   S: "211 Article numbers follow for t9.rfc.a.1"
S| "3\r\n"  S| "4\r\n"  S| "5\r\n"  S| ".\r\n"           <- range ignored
C: "GROUP t9.rfc.b.2"       S: "211 2 1 2 t9.rfc.b.2"
C: "LISTGROUP t9.rfc.a.1"   S: "211 Article numbers follow for t9.rfc.a.1"  (3, 4, 5)
C: "STAT 3"                 S: "423 No such article number"   <- LISTGROUP did not select a.1
C: "LISTGROUP t9.rfc.nosuch.listgroup"  S: "211 Article numbers follow for t9.rfc.nosuch.listgroup"
S| ".\r\n"                                   <- 211 instead of 411 (SEC-2)
C: "LIST"                   S: "215 List of newsgroups follows"
S| "t9.rfc.a.1 5 1 n\r\n"
S| "t9.rfc.b.2 2 1 n\r\n"
S| "t9.rfc.empty.3 0 1 n\r\n"
S| ".\r\n"
C: "LIST ACTIVE t9.rfc.b.*"     S: "215 …"  (same 3 lines: wildmat ignored)
C: "LIST NEWSGROUPS t9.rfc.b.*" S: "215 …"  (all 3 groups: wildmat ignored)
C: "LIST FOO"               S: "501 Unknown LIST type: FOO"
C: "GROUP t9.rfc.a.1"       S: "211 3 1 5 t9.rfc.a.1"
C: "XOVER 3-5"              S: "224 Overview information follows"
S| "3\tTest article 3\tTester 3 <tester3@test.invalid>\tSat, 11 Oct 2026 10:03:00 +0000\t<t9-rfc-a-1-3@test.invalid>\t\t26\t2\t\r\n"
S| "4\t… \t26\t2\t\r\n"
S| "5\t… \t26\t2\t\r\n"
S| ".\r\n"
C: "XOVER 5-3"              S: "501 Invalid range: start greater than end"
C: "XOVER 10-20"            S: "224 Overview information follows"
S| ".\r\n"
C: "XOVER 4-"               S: "224 Overview information follows"  (4, 5)
C: "XOVER <t9-rfc-a-1-3@test.invalid>"   S: "501 Invalid range format"
C: "XHDR subject 3-5"       S: "221 Header subject follows"
S| "3 Test article 3\r\n"  S| "4 Test article 4\r\n"  S| "5 Test article 5\r\n"  S| ".\r\n"
C: "XHDR Newsgroups 3-5"    S: "221 Header newsgroups follows"
S| ".\r\n"
C: "XHDR subject 5-3"       S: "501 Invalid range: start greater than end"
C: "XHDR subject 10-20"     S: "221 Header subject follows"
S| ".\r\n"
C: "XHDR subject <t9-rfc-a-1-3@test.invalid>"   S: "501 Invalid range format"
C: "QUIT"                   S: "205 Goodbye"

=== RUN   TestZZAuditRFC/format
C: "GROUP t9.rfc.fmt.4"     S: "211 6 1 6 t9.rfc.fmt.4"
C: "XOVER 1"                S: "224 Overview information follows"
S| "1\ttab\there\tTester 1 <tester1@test.invalid>\tSat, 11 Oct 2026 10:01:00 +0000\t<t9-rfc-fmt-4-1@test.invalid>\t\t0\t0\t\r\n"
S| ".\r\n"
C: "BODY 1"                 S: "222 1 <t9-rfc-fmt-4-1@test.invalid> Body follows"
S| "\r\n"
S| ".\r\n"
C: "ARTICLE 1"              S: "220 1 <t9-rfc-fmt-4-1@test.invalid> Article follows"
S| "Path: test.invalid!not-for-mail\r\n"
S| "From: Tester 1 <tester1@test.invalid>\r\n"
S| "Newsgroups: t9.rfc.fmt.4\r\n"
S| "Subject: tab\there\r\n"
S| "Date: Sat, 11 Oct 2026 10:01:00 +0000\r\n"
S| "Message-ID: <t9-rfc-fmt-4-1@test.invalid>\r\n"
S| "\r\n"
S| "\r\n"
S| ".\r\n"
C: "HEAD 2"                 S: "221 2 <t9-rfc-fmt-4-2@test.invalid> Headers follow"
S| "\r\n"
S| ".\r\n"
C: "BODY 5"                 S: "222 5 <t9-rfc-fmt-4-5@test.invalid> Body follows"
S| "..dot\r\n"
S| "..\r\n"
S| "...two\r\n"
S| ".\r\n"
C: "XOVER 6"                S: "224 Overview information follows"
S| "6\tcr\rhere\tTester 6 <tester6@test.invalid>\tSat, 11 Oct 2026 10:06:00 +0000\t<t9-rfc-fmt-4-6@test.invalid>\t\t26\t2\t\r\n"
S| ".\r\n"
C: "HEAD 3"                 S: "221 3 <t9-rfc-fmt-4-3@test.invalid> Headers follow"
S| "Path: test.invalid!not-for-mail\r\n"
S| ".Dotted-Header: x\r\n"
S| ".\r\n"
-- next status line read by the client after HEAD 3 (desync):
S: "223 0 <injected@evil.invalid> faked"
S: <bad status: bad status line "Message-ID: <t9-rfc-fmt-4-3@test.invalid>">
C: "GROUP t9.rfc.fmt.4"     S: "211 6 1 6 t9.rfc.fmt.4"       (second connection)
C: "XHDR subject 4"         S: "221 Header subject follows"
S| "4 first\n"
S| ".\r\n"
-- client sends DATE; what it reads as the DATE reply:
C: "DATE"                   S: <error short status line ".">
C: "XOVER 4"                S: "500 Command not recognized: DATE"

=== RUN   TestZZAuditRFC/posting        (hFakeProcessor: CheckMessageID=CasePass, ProcessIncomingArticle=CaseError)
greeting (processor set): "200 go-pugleaf NNTP server ready"
C: "CAPABILITIES"           S: "101 Capability list:"
S| VERSION 2 / READER / AUTHINFO USER / LIST ACTIVE NEWSGROUPS / XOVER / XHDR / MODE-READER / POST / IHAVE / TAKETHIS / .
C: "MODE READER"            S: "200 Hello, you can post"
C: "MODE STREAM"            S: "500 Unknown MODE: STREAM"
C: "POST"                   S: "480 Authentication required for posting"
C: TAKETHIS <tt0@test.invalid> + article (unauthenticated)
S: "480 Transfer permission denied (authentication required)"
C: "AUTHINFO USER t0user0006"      S: "381 Password required for t0user0006"     (user without posting)
C: "AUTHINFO PASS pw-t0user0006"   S: "281 Authentication accepted for user t0user0006"
C: "CAPABILITIES"           S: "101 Capability list:"   (identical list, still AUTHINFO USER and POST)
C: "AUTHINFO USER t0user0006"      S: "381 Password required for t0user0006"     <- 502 expected
C: "POST"                   S: "502 Posting not permitted for this user"         <- 440 expected
C: "IHAVE <x@test.invalid>" S: "502 Transfer not permitted for this user"
C: "QUIT"                   S: "205 Goodbye"
C: "AUTHINFO USER t0user0007"      S: "381 …"                                      (posting user)
C: "AUTHINFO PASS pw-t0user0007"   S: "281 Authentication accepted for user t0user0007"
C: "IHAVE <ih1@test.invalid>"      S: "335 Send me"
C: article (processor returns CaseError = permanent reject)
S: "436 Transfer failed (processing error)"                                        <- 437 expected
C: "IHAVE <ih2@test.invalid>"      S: "335 Send me"
C: garbled article without Newsgroups header
S: "436 Bad"                                                                       <- 437 expected
C: "IHAVE not-a-message-id"        S: "335 Send me"                                <- 501 expected
C: article with Message-ID <other@test.invalid>
S: "437 Rejected (Message-ID mismatch)"
C: TAKETHIS <tt1@test.invalid> + article
S: "439 <tt1@test.invalid> Transfer failed"
C: TAKETHIS <tt2@test.invalid> + article (history error = temporary)
S: "439 <tt2@test.invalid> Retry later (history error)"                            <- 400 + close expected
C: "POST"                   S: "340 Send article to be posted. End with <CR-LF>.<CR-LF>"
C: article                  S: "441 Posting failed (processing error)"
C: "AUTHINFO PASS again"    S: "482 AUTHINFO USER required first"                  <- 502 expected
C: "QUIT"                   S: "205 Goodbye"

=== RUN   TestZZAuditRFC/intake-parse
parseIncomingArticleLines: err=<nil> newsgroups=["t9.x"]
article.Subject="first\n."
article.HeadersJSON="Path: peer.invalid!not-for-mail\nFrom: a <a@test.invalid>\nNewsgroups: t9.x\nSubject: first\nSubject: .\n.\nMessage-ID: <p@test.invalid>"

=== RUN   TestZZAuditRFC/maxconns       (MaxConns: 1)
conn 1 greeting: "200 go-pugleaf NNTP server ready"
conn 2: greeting: EOF (no 400 greeting)

=== RUN   TestZZAuditRFC/pipelined-quit (child process, run directly)
C: "QUIT\r\nHELP\r\n" in one write
S: "205 Goodbye"
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x40 pc=0x701c5b]
goroutine 82 [running]:
github.com/go-while/go-pugleaf/internal/nntp.(*ClientConnection).UpdateDeadlines(0x1bca961780c0)
	internal/nntp/nntp-server-cliconns.go:60 +0x5b
github.com/go-while/go-pugleaf/internal/nntp.(*ClientConnection).Handle(0x1bca961780c0)
	internal/nntp/nntp-server-cliconns.go:81 +0x173
github.com/go-while/go-pugleaf/internal/nntp.(*NNTPServer).handleConnection(0x1bca9612a210, {0xc35358, 0x1bca96182010}, 0x0)
	internal/nntp/nntp-server.go:211 +0x119
created by github.com/go-while/go-pugleaf/internal/nntp.(*NNTPServer).serve in goroutine 68
	internal/nntp/nntp-server.go:196 +0x205
FAIL	github.com/go-while/go-pugleaf/internal/nntp	0.080s
```

In the parent run, `runIsolated` reported `child failed: exit status 2`; its 40-line output tail held only the
startup log lines, so the child was run again on its own to capture the panic shown above.
