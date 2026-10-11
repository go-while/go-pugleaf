# Audit audit-nntp-client (base 64a53aa)

Security, robustness and correctness audit of the **outbound** NNTP client, the connection pool,
the streaming transfer engine (CHECK/TAKETHIS), the response demuxer, the transfer-progress DB, the
peering manager and the `cmd/nntp-transfer` tool (its web UI, pprof and Redis use).

Server-side command handling is `audit-nntp-rfc`; server security/DoS/TLS is `audit-nntp-sec`;
intake/header-reconstruction on the write path is `audit-intake`; the fetcher/importer tools are
`audit-tools`. This report follows `docs/audit/README.md` (D1) and K6. All `path:line` anchors are at
64a53aa and were re-checked with `grep -n` on the symbol.

## Scope and method

Files read in full at 64a53aa:

- `internal/nntp/nntp-client.go`, `nntp-client-commands.go`, `nntp-backend-pool.go`, `proxy.go`
- `internal/nntp/nntp-transfer.go`, `nntp-transfer-demuxer.go`, `transfer-progress.go`,
  `transfer-progress-utils.go`
- `internal/nntp/nntp-peering.go`, `nntp-peering-pattern.go`, `nntp-peering-struct.go.bak` (dead file)
- `cmd/nntp-transfer/main.go` (3431 lines: flags, `main`, `getNewsgroupsToTransfer`, `runTransfer`,
  `transferNewsgroup`, `processBatch`, `sendArticlesBatchViaTakeThis`, `BootConnWorkers`,
  `CHTTWorker`, `startWebServer`/`handleIndex`/`handleResults`, `monitorMemoryStats`)
- callers of the client API: `internal/processor/{proc_DLArt.go, proc_DLXHDR.go, analyze.go}`,
  `internal/postmgr/postmgr.go`, `cmd/test-nntp/main.go` (read only; hardcoded to an external server,
  **not run**)
- `internal/common/headers.go` `ReconstructHeaders`/`GetHeaderFirst` (CLI-1 part; the full header
  pipeline is `audit-intake`)

Tools (from `docs/audit/tools/`, filtered to this scope):
- **staticcheck**: `nntp-client.go:346,356` `xSetReadDeadline`/`xSetWriteDeadline` unused (U1000);
  `nntp-peering.go:389` `enhancedReverseDNSLookup` unused; `nntp-transfer.go:570` `UseCHECK` S1008;
  `cmd/nntp-transfer/main.go:36` `dbBatchSize`, `:1086` `matchesAnyPattern` unused; `:1877`,`:2732`
  SA4006 dead assignments (`minLoad`, `wantedArticles`).
- **golangci-lint (gosec)**: `main.go:12` G108 pprof auto-exposed on the default mux; `:551`,`:2940`
  G114 `http.ListenAndServe` with no timeouts; `:311`,`:1404` G115 int overflow conversions.
- **govulncheck**: the reachable NNTP hits are all on this slice's write path
  (`nntp-client-commands.go:1226,1259` `PostArticle`→`bufio.Flush`; `proxy.go:60` `DialTLS`→
  `tls.Conn.Handshake`; `main.go:314` `redis.NewClient`→`tls.DialWithDialer`; `main.go:2936,2940`
  the web server). They are stdlib/`x/net`/`quic-go` version issues (go1.27.1→27.2), not logic bugs;
  noted under `## Not covered`.

Repros: a tiny scripted fake NNTP server plus a temporary
`internal/nntp/zz_audit_nntp-client_test.go` (wave-0 harness types are server-side, so the client
repros used `net.Listen` directly). Both were **deleted before commit** (D1 / K5). The demuxer
command-id defect (CLI-4) and the nil-deref (CLI-2) were confirmed by code trace plus a scripted
transcript; see `## Confirmation details`. No external NNTP server was contacted.

## Summary

Counts by severity: **critical 1, high 4, medium 7, low 6** (18 findings). By category:
bug 6, security 4, robustness 4, dead-code 3, wrong-function 1.

| severity | findings |
|----|----|
| critical | CLI-2 |
| high | CLI-1, CLI-3, CLI-4, A-nntp-client-1 |
| medium | CLI-5, OPS-3, A-nntp-client-2, A-nntp-client-3, A-nntp-client-4, A-nntp-client-5, A-nntp-client-6 |
| low | CLI-6, A-nntp-client-7, A-nntp-client-8, A-nntp-client-9, A-nntp-client-10, A-nntp-client-11 |

Five most important:

1. **CLI-2 (critical)**: nil-pointer dereference in `XHdrStreamedBatch` when the remote answers GROUP
   with 411 — `groupInfo` is nil but `groupInfo.Last` is read immediately. Crashes the fetcher on a
   single missing/renamed upstream group. CONFIRMED.
2. **A-nntp-client-1 (high)**: no read/write/handshake deadlines on any client socket, so a silent or
   malicious upstream (including over Tor/SOCKS) wedges a pooled connection and its goroutines
   forever; `readMultilineResponse` also buffers up to 256 Ki lines of unbounded length into RAM.
   Extends CLI-3. CONFIRMED.
3. **CLI-1 (high)**: `ReconstructHeaders` emits the `Path` header **twice** on every TAKETHIS/IHAVE
   and folds `References`/`Path` incorrectly (the whole line is re-appended per field). The peer sees
   a malformed, duplicated Path. CONFIRMED.
4. **CLI-4 (high)**: the demuxer keys ordering on textproto command ids starting at `LastID+1==1`, but
   with `-username` the AUTHINFO USER/PASS + MODE STREAM commands consume ids 0–2, so the first CHECK
   (id 3) never matches and the worker stalls in the out-of-order backlog. CONFIRMED.
5. **CLI-3 (high)**: connection/socket leaks and dead deadline helpers — `Connect` returns after
   `c.conn = conn` on several error paths without closing the socket, and the renamed
   `xSetReadDeadline`/`xSetWriteDeadline` are never called. CONFIRMED.

The peering subsystem (`nntp-peering.go`, CLI-6) is entirely unreachable dead code with a
self-deadlocking DNS limiter and append-aliasing bugs; it is confirmed but low severity because
nothing constructs a `PeeringManager`.

## Findings

### CLI-2: nil dereference in XHdrStreamedBatch on a 411 GROUP reply

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-2 | CLI-2 | critical | bug | `nntp-client-commands.go:636-642` | CONFIRMED |

**Scenario:** the fetcher calls `proc.Pool.XHdrStreamed(ng, "message-id", …)` → `XHdrStreamedBatch`.
The upstream no longer carries the group (or returns 411 for any reason). `SelectGroup` returns
`(nil, 411, err)`. The code treats 411 as non-fatal but then dereferences the nil `groupInfo`.

**Evidence:**
```go
groupInfo, code, err := c.SelectGroup(groupName)     // :636  returns (nil, 411, err)
if err != nil && code != 411 {                        // :637  411 is tolerated → not returned
    return fmt.Errorf(...)
}
if end > groupInfo.Last {                              // :640  groupInfo == nil → panic
    return ErrOutOfRange
}
```
`SelectGroup` (`:382-388`) returns `nil, code, err` whenever `ReadCodeLine(211)` fails, which it does
for 411. The panic propagates out of the goroutine started at `nntp-backend-pool.go:128-136`
(`go func(){ client.XHdrStreamed(...) }`), so `recover` in the caller cannot catch it and the whole
`nntp-fetcher`/`nntp-transfer` process aborts. Unlike `XOver`/`XHdr` (which discard `groupInfo` and
never dereference it) this path reads `.Last`.

A scripted fake server that answers `GROUP x` with `411 no such group` and then any line triggers:
`panic: runtime error: invalid memory address or nil pointer dereference` at `nntp-client-commands.go:640`.

**Fix (S):** after `SelectGroup`, `if code == 411 || groupInfo == nil { return ErrNewsgroupNotFound }`
before touching `groupInfo.Last`. Effort S.

---

### A-nntp-client-1: no socket deadlines and unbounded response buffering against a hostile/stalled server

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new (extends CLI-3) | A-nntp-client-1 | high | security | `nntp-client.go:150-240,345-362`, `nntp-client-commands.go:780-847`, `proxy.go:50-69` | CONFIRMED |

**Scenario:** an upstream the client connects to (a peer, a provider, or a Tor hidden service) is slow
or malicious. It accepts the TCP/TLS connection, then (a) never sends the greeting, (b) sends a
multi-line response that never terminates with `.`, or (c) trickles bytes one per minute. Nothing
times out, so the pooled `BackendConn` and every goroutine blocked on it (`readAndDispatch`, the
CHECK/TAKETHIS workers) hang until the process is killed. Resource: goroutines + sockets + heap.

**Evidence:**
- `Connect` sets no deadline on `conn` before `ReadCodeLine` (`:211`). `DialTLS` runs
  `tlsConn.Handshake()` (`proxy.go:60`) with **no handshake deadline** — the server's dialers set a
  `ConnectTimeout` on the TCP dial only (`proxy.go:74,87,108`); the TLS handshake and all subsequent
  reads/writes are unbounded.
- The only deadline helpers, `xSetReadDeadline`/`xSetWriteDeadline` (`nntp-client.go:346,356`), are
  **never called** (staticcheck U1000) and `UpdateLastUsed` (`:365`) has its deadline calls commented
  out (`:369-370`). The per-operation deadline in `GetArticle` is also commented out (`:76-87`).
- `readMultilineResponse` (`:780`) loops `c.TextConn.ReadLine()` with only a *line-count* cap
  (`MaxReadLinesArticle = 256*1024`, `:21`) and no per-line length cap and no deadline. A server
  sending 256 Ki lines of 1 MiB each, or an infinite non-`.`-terminated stream of short lines, pins
  memory/CPU in the pooled goroutine. `ListGroupsLimited` (`:314`) and `XHdrStreamedBatch` (`:689`)
  read line by line with the same lack of deadline.

**Fix (M):** wire `SetReadDeadline`/`SetWriteDeadline` per command (restore the dead helpers), set a
handshake deadline in `DialTLS` (`tlsConn.SetDeadline` around `Handshake`), and add an octet budget
to `readMultilineResponse`. Effort M.

---

### CLI-1: ReconstructHeaders duplicates Path and mis-folds References/Path

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-1 | CLI-1 | high | bug | `common/headers.go:244-264,266-299`, `nntp-client-commands.go:1114,1206` | CONFIRMED |

**Scenario:** every outbound article (TAKETHIS via `SendTakeThisArticleStreaming` `:1114`, POST via
`PostArticle` `:1206`) is serialized with `ReconstructHeaders`. With `withPath==true` the function
appends the Path line **twice**, and both the References and Path folding loops re-append the entire
accumulated line per whitespace token instead of folding at the 1000-char boundary.

**Evidence:** in the `withPath==true` branch (`:266-299`):
```go
paths := strings.Fields(pathline)
for i, path := range paths {
    if i > 0 {
        if len(pathline)+1+len(path) > 1000 { headers = append(headers, pathline); pathline = " "+path }
        else { pathline += " " + path }
    } else { pathline += " " + path }          // note: pathline already contains "Path: host!.TX!..."
}
if strings.TrimSpace(pathline) != "" { headers = append(headers, pathline) }   // first append
...
headers = append(headers, "Path: "+*nntphostname+"!.TX!"+article.Path)         // :293 second append
```
So the folding loop builds `pathline` by appending every token to a string that *already holds the
full line*, duplicating the content, then `:293` appends a third, un-folded copy. The References
branch (`:244-264`) has the same append-the-whole-line-per-token structure. The receiving peer gets
two `Path:` headers (RFC 5536 §3.1.5 allows one) and corrupted folding. Confirmed by
`ReconstructHeaders(art, true, &"news.test.invalid", "g")` returning two `Path:` lines in the repro.

**Fix (M):** build the folded line from the field tokens only (not the accumulator) and append the
Path line exactly once. Shared with `audit-intake` (same function). Effort M.

---

### CLI-4: demuxer command-id ordering breaks authenticated streaming transfers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-4 | CLI-4 | high | bug | `nntp-transfer-demuxer.go:19,185-191`, `nntp-client.go:243-281,1315-1391` | CONFIRMED |

**Scenario:** with `-username`/`-password` set, a streaming transfer stalls: no CHECK/TAKETHIS ever
dispatches, the worker sits in the out-of-order backlog, `OffsetQueue.Wait` never returns.

**Evidence:** `ResponseDemuxer.LastID` starts at 0 (`:19`). `readAndDispatch` only advances when
`d.LastID+1 == cmdInfo.CmdID` (`:185`); otherwise it stores the command in `outoforderBacklog` and
spins. The command ids come from `textproto.Conn.Cmd`, whose `Pipeline.Next()` returns a global
counter starting at 0 for the **first command on the connection**. On an authenticated streaming
connection the first three commands are AUTHINFO USER (id 0) and AUTHINFO PASS (id 1) inside
`authenticate()` (`nntp-client.go:245,263`) during `Connect`, then MODE STREAM (id 2) in
`SwitchToModeStream` (`:1372`). The demuxer never registers those (only CHECK/TAKETHIS call
`RegisterCommand`), so the first *registered* command is the first CHECK with id 3. The demuxer waits
for id 1, receives id 3, backlogs it and never catches up. Without `-username` the ids are MODE STREAM
= 0, first CHECK = 1, which matches `LastID+1==1` — hence the defect is specific to authenticated
transfers, exactly as the seed states.

**Fix (M):** seed `LastID` from the first registered command id (or make the demuxer relative to the
first `RegisterCommand`), rather than assuming the stream begins at id 1. Effort M.

---

### CLI-3: socket leaks on connect error, dead deadline helpers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-3 | CLI-3 | high | robustness | `nntp-client.go:206-239,345-362`, `nntp-client-commands.go:78`, `proxy.go:59-60` | CONFIRMED |

**Scenario:** `Connect` assigns `c.conn = conn` at `:206` and wraps it in `textproto.NewConn` at
`:207`, then returns `err` from the welcome read (`:214`), the welcome-code check (`:219`), or
authentication (`:232`) **without closing `conn`/`TextConn`**. `createConnection`
(`nntp-backend-pool.go:469-480`) only closes `pconn.conn` when `createConnection` itself returns an
error, but `Connect` returns `(c, nil)`? No — `createConnection` returns `(nil, err)` after a failed
`Connect`, and the `Get` caller closes `pconn.conn` *only when `pconn != nil`* (`:270-271`); here
`pconn` is nil, so the socket opened inside `Connect` leaks. Over Tor/SOCKS (slow auth failures,
`time.Sleep(15s)` at `:231`) this accumulates fast.

**Evidence:** `:214 return err`, `:219 return fmt.Errorf(...)`, `:232 return err` all run after
`c.conn = conn` with no `conn.Close()`. The deadline helpers `xSetReadDeadline`/`xSetWriteDeadline`
(`:346,356`) and the per-op deadline in `GetArticle` (`:78`, commented) are dead (staticcheck U1000) —
see A-nntp-client-1 for the exposure they would have mitigated.

**Fix (S/M):** `defer` a close-on-error in `Connect` (close `c.conn`/`c.TextConn` on any error path
before returning), and either delete or wire up the deadline helpers. Effort M.

---

### CLI-5: client response-code handling (201 greeting, 281 AUTHINFO, 480, unreachable branches)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-5 | CLI-5 | medium | bug | `nntp-client.go:211-219,251-259`, `nntp-backend-pool.go:189-204`, `nntp-client-commands.go:51-54,150-153,206-209` | CONFIRMED (partly closed) |

**Scenario / evidence:**
- **201 greeting rejected:** `Connect` accepts only 200–201 via the range check (`:217`,
  `NNTPWelcomeCodeMin/Max = 200/201`), so a 201 "no posting" greeting is actually accepted here — but
  `ReadCodeLine(NNTPWelcomeCodeMin)` at `:211` passes `200` as the *expected* code, and
  `textproto.ReadCodeLine` returns a `*textproto.Error` when the actual code (201) ≠ expected, with
  `code` still set to 201. The code then relies on `code != 0` style checks elsewhere but here
  `err != nil` is returned at `:214` only when `code == 0`? No: `:212 if err != nil { return err }`
  returns unconditionally. So a **201 greeting fails `Connect`** because `ReadCodeLine(200)` errors.
  CONFIRMED.
- **281 to AUTHINFO USER:** `authenticate` expects 381 after USER (`:251,258`); a server that replies
  281 (already authenticated / no password needed, RFC 4643) is treated as failure (`:259`). CONFIRMED.
- **480 mapped to "group not found":** `pool.SelectGroup` maps both 411 and 480 to
  `ErrNewsgroupNotFound` (`nntp-backend-pool.go:198-201`), hiding "authentication required". CONFIRMED.
- **Unreachable 430/451 branches:** in `StatArticle`/`GetHead`/`GetBody`, `ReadCodeLine(223|221|222)`
  returns a non-nil `err` for any other code, so the `switch code` 430/451 arms at `:56-63,155-165,
  211-221` are reached only when `err` is nil, which never happens for those codes — the function
  returns the generic error at `:52-53,151-152,206-209` first. `GetArticle` (`:96-114`) is the one
  path that inspects `code` before the error (`:97 if err != nil && code == 0`), so its 430/451 arms
  *are* reachable — matching the seed's "PARTLY". CONFIRMED.

**Fix (M):** branch on `code` (as `GetArticle` does) instead of trusting `ReadCodeLine`'s expected-code
error; accept 201 greetings and 281 AUTHINFO; map 480 distinctly. Effort M.

---

### OPS-3: pprof auto-registered on a public mux; transfer web server on all interfaces, no timeouts

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| OPS-3 | OPS-3 | medium | security | `cmd/nntp-transfer/main.go:12,544-551,2935-2944`, `:198` | CONFIRMED |

**Scenario:** `-web-port N` starts an HTTP server with `http.ListenAndServe(fmt.Sprintf(":%d", port), nil)`
(`:2940`) — the **default mux** on **all interfaces**. Because `net/http/pprof` is imported for side
effects (`:12`), `/debug/pprof/*` is registered on that same default mux. So enabling the read-only
results web UI silently exposes full profiling (heap, goroutine, `cmdline`, `profile`) to the network,
unauthenticated. The dedicated `-pprof-port` listener is correctly `localhost`-bound (`:544-551`), but
`-web-port` is not, and both use `http.ListenAndServe` with no `ReadTimeout`/`WriteTimeout`/`IdleTimeout`
(gosec G114 at `:551` and `:2940`).

**Evidence:** `main.go:12 _ "net/http/pprof"`; `startWebServer` (`:2935`) `http.HandleFunc("/", …)`
then `http.ListenAndServe(":<port>", nil)`; the web server logs `Starting web server on
http://ANY_ADDR:...` (`:2939`), confirming it binds all interfaces. gosec flags both.

**Fix (S):** serve the web UI on a private `*http.ServeMux` (not the default mux) wrapped in an
`http.Server{ReadHeaderTimeout, ...}`, and bind it to the configured address (default localhost).
Keep pprof only behind the explicit `-pprof-port`. Effort S. Shared with `fixplan-nntp-client`.

---

### A-nntp-client-2: transfer-progress DB and peering DNS use `database/sql` directly, bypassing SQLite tuning

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-2 | medium | robustness | `transfer-progress.go:50-59,469,519`, `transfer-progress-utils.go:469,519` | CONFIRMED |

**Scenario:** `OpenTransferProgressDB` opens its own `sql.Open("sqlite3", …)` (`:50`) with only
`PRAGMA journal_mode=WAL` (`:56`) — no busy_timeout, no foreign_keys, none of the PRAGMAs the main DB
applies. Writes go through `database.RetryableExec`/`RetryableQuery` (good), but two read methods,
`GetTransferStatsByNewsgroup` (`transfer-progress-utils.go:469`) and `GetAllTransferStats` (`:519`),
call `tpdb.db.QueryRow(...)` **directly**, bypassing the retry helper (CLAUDE.md requires the helpers).
Under a concurrent writer they can return `SQLITE_BUSY` instead of retrying. `createTables` uses
`RetryableExec` on a multi-statement string (OK for the driver, but relies on it).

**Evidence:** `transfer-progress.go:50-59` (bare `sql.Open` + one PRAGMA); direct `db.QueryRow` at
`transfer-progress-utils.go:469` and `:519` vs `RetryableQueryRowScan` used elsewhere in the same file
family. `GetTransferStatsByNewsgroup`/`GetAllTransferStats` are also themselves unused (see
A-nntp-client-9).

**Fix (S):** route both reads through `RetryableQueryRowScan`; set `busy_timeout` on open. Effort S.

---

### A-nntp-client-3: CR/LF / space injection into client commands via message-ids and group names

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-3 | medium | security | `nntp-client-commands.go:43,88,142,198,374,450,515,654,736,1066,1126`, `nntp-client.go:245,263` | PLAUSIBLE |

**Scenario:** the client builds commands with `c.TextConn.Cmd("ARTICLE %s", *messageID)`,
`Cmd("GROUP %s", groupName)`, `Cmd("CHECK %s", *msgID)`, `Cmd("TAKETHIS %s", article.MessageID)`,
`Cmd("AUTHINFO USER %s", username)`, etc. `textproto.Writer.PrintfLine` appends CRLF but does **not**
reject embedded CR/LF or spaces in the argument. A message-id, group name, or username containing a
CR/LF would inject an extra protocol line to the upstream (command injection); one containing a space
would split into extra arguments.

**Missing step (why PLAUSIBLE, not CONFIRMED):** message-ids and group names on this path originate
from (a) the local DB (`GetArticlesByIDs`, `GetMessageIDsWithDateFilter`), and (b) remote XHDR/XOVER
responses parsed by `parseHeaderLine`/`parseOverviewLine` — both read with `textproto.ReadLine`, which
splits on LF, so a *single* stored/returned value cannot already contain a raw CR or LF. Injection
therefore requires a stored message-id/group name that a *different* ingestion path allowed to contain
CR/LF (not proven here) or a username/password from config containing CR/LF (operator-controlled).
Spaces, however, are reachable: a crafted TAKETHIS `article.MessageID` with a space is passed verbatim.

**Fix (S):** validate message-id (angle-bracketed, no WSP/CTL) and group name (no WSP/CTL) before
every `Cmd`; reject or sanitize. Effort S. Coordinate with `audit-intake` (stored-value validation).

---

### A-nntp-client-4: pool Get/Put mode-switch and timeout gaps

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-4 | medium | robustness | `nntp-backend-pool.go:210-334,330-333,497`, `nntp-client.go:43` | CONFIRMED |

**Scenario / evidence:**
- `Get` waits at most a hardcoded `30 * time.Second` for a free connection (`:330`) and returns a
  generic error; there is no caller-supplied context/deadline, so a saturated pool blocks each caller
  30 s then fails without backpressure signalling.
- The idle-expiry is `DefaultConnExpire = 25 * time.Second` (`nntp-client.go:43`) checked in
  `isConnectionValid` (`:497`), i.e. a connection idle >25 s is silently discarded; combined with the
  5 s `startCleanupWorker` tick (`:549`) and no server-side keepalive this churns connections on a
  slow feed.
- On a mode-switch failure in the *wait* branch (`:305-310`) the code `goto newConn`, but
  `activeConns` was already decremented by `closeConn(pconn,true)` at `:307` while a slot was never
  re-reserved — the subsequent `newConn` re-checks `activeConns < maxConns`, so this is self-correcting
  but relies on the counter staying consistent across the two `closeConn`/`createConnection` paths; the
  `failedConns`/`activeConns` bookkeeping is duplicated in three places (`:273-276`, `:282-285`,
  `:390-393`) and easy to get wrong in future edits.

**Fix (M):** take a context in `Get`; make idle-expiry and the wait timeout configurable; centralize
the active/failed accounting. Effort M.

---

### A-nntp-client-5: PostArticle retry-after-401 path leaks a response and double-defers EndResponse

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-5 | medium | bug | `nntp-client-commands.go:1218-1255` | CONFIRMED |

**Scenario:** `PostArticle` reads the POST reply with `StartResponse(id)` / `ReadCodeLine(340)` /
`EndResponse(id)` (`:1218-1221`). On a `401 "mode reader"` it re-sends POST inside the switch
(`:1238-1245`) with its **own** `StartResponse(id)` and a `defer c.TextConn.EndResponse(id)`
(`:1242-1243`) — a `defer` inside a `switch` inside the method, so `EndResponse` for the second
request runs at *function* exit, after the body has been written and the final `ReadCodeLine(240)`
(`:1302`) issued with no surrounding `StartResponse`. The textproto pipeline sequencing is thus
violated: the final response is read without an active `StartResponse`, and the retry's `EndResponse`
fires late. For the normal 340 path the final `ReadCodeLine(240)` at `:1302` also runs with **no**
`StartResponse` (the first `EndResponse` already ran at `:1221`), so the pipeline id accounting is off
by one on every POST.

**Evidence:** `:1218-1221` Start/Read/End for the 340 reply; `:1302 code, _, err = c.TextConn.ReadCodeLine(240)`
with no matching `StartResponse`. In single-connection POST use (postmgr) this usually works because
there is no pipelining, but it is incorrect and fragile. `postmgr.go:182` is the only live caller.

**Fix (M):** wrap the final response read in `StartResponse(id)`/`EndResponse(id)` for the *current*
request id; restructure the 401 retry to not defer inside the switch. Effort M.

---

### A-nntp-client-6: unbounded per-process pools of recycled structs/channels never shrink

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-6 | medium | robustness | `nntp-transfer.go:27,90,209,241,288`, `nntp-transfer-demuxer.go:33-35` | CONFIRMED |

**Scenario:** the transfer engine uses package-global recycle channels sized `1024*1024`
(`ResponseDataPool`, `TTSetupPool`, `TTResponsePool`, `TTResponseChans`, `ReadRequestsPool` — each
`make(chan *T, 1024*1024)`), and each demuxer allocates `cmdIDChan` 64 Ki + two response channels of
`1024*1024` entries (`nntp-transfer-demuxer.go:33-35`). With `-max-threads N`, N demuxers each reserve
~2M channel slots. These are capacity reservations (cheap when empty) but the `GetX`/`RecycleX`
discard-on-full pattern means under burst load up to ~1M live `*ResponseData`/`*ReadRequest` structs
can be retained in the global pools and never freed for the process lifetime. On a many-group,
many-thread run this is a steady multi-hundred-MB floor (consistent with the BUGS.md fetcher growth
note, though that is `audit-tools`).

**Evidence:** the `make(chan *T, 1024*1024)` literals at the cited lines; `NewResponseDemuxer`
allocates per worker. No pool is ever drained on worker exit (the cleanup in `BootConnWorkers` nils
per-worker maps but not the global pools).

**Fix (M):** size the pools from `-max-threads`/`-batch-check`, or use `sync.Pool`; drain demuxer
channels on worker exit. Effort M. (Measurement of steady-state RSS belongs to `audit-tools`'s
fetcher-growth item; here it is a design finding.)

---

### CLI-6: PeeringManager is unused, unfinished, and buggy

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-6 | CLI-6 | low | dead-code | `nntp-peering.go:165-205,248-268,341-363,389-432`, `nntp-peering-struct.go.bak`, `cmd/test-nntp` | CONFIRMED |

**Scenario / evidence:** nothing in the tree constructs a `PeeringManager` (`grep` shows only the
definitions and the `_test.go` pattern tests). The code is dead and defective:
- **`LoadConfiguration` is a stub** (`:196-205`): "TODO: Load from database first"; returns the default
  config and never reads DB or file.
- **DNS limiter self-deadlocks** (`:341-363` `reverseDNSLookup`): it takes the 1-slot
  `dnsQueryLimiter` (`pm.dnsQueryLimiter <- struct{}{}`, `:344`) and, while holding it, calls
  `validateHostnameForward` (`:357`) which takes the **same** 1-slot channel again (`:416`) → deadlock
  on the second send. `enhancedReverseDNSLookup` (`:389`, itself unused per staticcheck) has the same
  pattern. The limiter is created with capacity 1 (`:172`).
- **`AddPeer` append-aliasing** (`:262-263`): stores `&pm.peers[len(pm.peers)-1]` into `peersMap`; the
  next `append` to `pm.peers` can reallocate the backing array, leaving every map pointer dangling to
  the old array. `CheckConnectionACL` (`:291-306`) also returns `&peer` of the **range loop copy**
  (`for _, peer := range pm.peers`), i.e. a pointer to a loop-local variable, not the stored peer.
- **`CreateDefaultPeer`** double-allocates (`make(...,len)` then `copy`), harmless but sloppy.
- `nntp-peering-struct.go.bak` is a dead `.bak` file (not compiled; `LegacyPeeringManager`, duplicate
  `RemoteAddr`/`StrIsIPv4`/`StrIsIPv6` that would collide if renamed to `.go`).
- `cmd/test-nntp` is hardcoded to `news.blueworldhosting.com:563` and most of its body is commented
  out (`:79-125`); it calls `ListGroupsLimited`/`SelectGroup`/`StatArticle` against an external server.
  Read only; **not run**.

**Fix (M/L):** either finish and wire the peering manager or delete it plus the `.bak` and `test-nntp`.
If kept: fix the limiter (separate channel or `sync.Mutex`), return `&pm.peers[i]` only after
preallocating, and store pointers that survive `append` (use `[]*Peer` or map of values). Effort L.

---

### A-nntp-client-7: parseOverviewLine/parseHeaderLine accept malformed remote data silently

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-7 | low | robustness | `nntp-client-commands.go:987-1032` | CONFIRMED |

**Scenario:** `parseOverviewLine` (`:989`) splits on TAB and ignores parse errors for `article_num`,
`bytes`, `lines` (`_` discards from `strconv.ParseInt`, `:995-999`), so a hostile XOVER line with a
non-numeric article number yields `ArticleNum=0` silently; `XOver` then appends it. `parseHeaderLine`
(`:1016`) splits on the first space and requires ≥2 parts, but a line `0 value` yields `ArticleNum=0`
which `XHdr` drops (`:548`) but `XHdrStreamedBatch` forwards to the channel (`:702-715`) without the
`>0` filter. Mixed trust: overview rows from the remote drive DB inserts downstream.

**Evidence:** `_` error discards at `:995-996,999`; the `>0` guard present in `XHdr` (`:548`) but
absent in `XHdrStreamedBatch`'s forward loop (`:715`).

**Fix (S):** reject lines whose article number fails to parse or is ≤0 on both paths. Effort S.

---

### A-nntp-client-8: body dot-stuffing/trailing-line handling drops a legitimate final empty line

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new (relates CLI-1) | A-nntp-client-8 | low | bug | `nntp-client-commands.go:1149-1169,1271-1289`, `:876` | CONFIRMED |

**Scenario:** when sending a body (`SendTakeThisArticleStreaming` `:1149`, `PostArticle` `:1271`), the
code splits `article.BodyText` on `\n` and **breaks** on the last element if it is empty
(`:1152,1274`), which silently drops a trailing blank line that is part of the article body. The
inverse parse (`ParseLegacyArticleLines` `:876`) joins body lines with `\n` and sets
`article.Bytes = len(article.BodyText)` — the LF-joined length, not the CRLF wire length
(cross-refs INT-5, `audit-intake`). So a received-then-retransmitted article can differ by its final
newline and its byte count is wrong.

**Evidence:** `:1152-1154` `if i == len(bodyLines)-1 && line == "" { break }`; same at `:1274`.

**Fix (M):** preserve the body byte-exactly (store raw bytes / use `textproto.DotWriter`). Effort M.
Overlaps CLI-1 and INT-5.

---

### A-nntp-client-9: dead/unused transfer-progress and client helpers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-9 | low | dead-code | `transfer-progress-utils.go:410,444,495`, `transfer-progress.go:219`, `nntp-client-commands.go:282`, `:894` | CONFIRMED |

**Scenario / evidence:** `PrintRecentTransfers` (`transfer-progress-utils.go:410`),
`GetTransferStatsByNewsgroup` (`:444`), `GetAllTransferStats` (`:495`) and `GetRecentTransfers`
(`transfer-progress.go:219`) have no callers (grep). `ListGroupsLimited` (`nntp-client-commands.go:282`)
is used only by the hardcoded `cmd/test-nntp`. `MultiLineHeaderToMergedString` (`:894`) duplicates
`common.multiLineHeaderToMergedString`. `IgnoreNewsgroupProgress` (`cmd/nntp-transfer/main.go:967`) is
unused (the fast path uses `GetAllProgressNewsgroups`), and `matchesAnyPattern`/`dbBatchSize` are
unused (staticcheck). None are harmful; they inflate the API surface.

**Fix (S):** delete or mark; low priority. Effort S.

---

### A-nntp-client-10: transfer web page reflects server-controlled fields into HTML (minor XSS surface)

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-10 | low | security | `cmd/nntp-transfer/main.go:2946-3243,3243-3414` | CONFIRMED |

**Scenario:** `handleIndex` renders an `html/template` (`:3243 template.New("index")`,
`:3411 tmpl.Execute`), which auto-escapes, so stored newsgroup names / result strings are safe in text
context. The page `<title>` interpolates `{{.NewsgroupsToProcess}}:{{.ServerHostName}}` — all values
come from the operator's own flags/DB, not from a remote peer, so there is no external injection. The
real exposure is that the page (and `handleResults`, plain text) is served unauthenticated on all
interfaces (see OPS-3) and leaks operational data (hostnames, group names, transfer counts).

**Evidence:** `html/template` used (auto-escaping) — confirmed not a raw-HTML sink; the concern is
exposure (OPS-3), so this is low. No `template.HTML`/`safeHTML` usage in the file.

**Fix (S):** bind the UI to localhost / add auth (folds into OPS-3). Effort S.

---

### A-nntp-client-11: ForceCloseConn spawns an unbounded goroutine per call; Put re-entrancy

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-11 | low | robustness | `nntp-client.go:313-322`, `nntp-backend-pool.go:337-375` | CONFIRMED |

**Scenario:** `ForceCloseConn` (`nntp-client.go:313`) launches `go func(){ … c.Pool.Put(c) }()` on
every call — an unbounded goroutine spawn under error storms, and it re-enters `pool.Put`, which may
again call `closeConn`. `Put` on a connection with `forceClose` set routes to `closeConn` (`:349-352`)
which decrements `activeConns`; if `ForceCloseConn` races with `ClosePool`'s drain
(`:415-431 conn.ForceCloseConn()`), two goroutines can `Put`/`closeConn` the same conn. The `hasClosed`
channel guard in `CloseFromPoolOnly` (`:288-304`) mostly prevents double-close, but the accounting
(`totalClosed`/`activeConns`) can be decremented twice.

**Evidence:** `nntp-client.go:314 go func(){...c.Pool.Put(c)}()`; `ClosePool` calls
`conn.ForceCloseConn()` during drain (`:425`) which itself re-enters `Put`.

**Fix (M):** make `ForceCloseConn` synchronous (or bounded) and idempotent; guard the accounting with
the `hasClosed` state. Effort M.

---

## Client command table

Commands the client issues (via `textproto.Conn.Cmd`), the codes it handles, limits and timeouts.
"✗ none" = unbounded / no deadline (see A-nntp-client-1, CLI-3).

| Command | Fn (file:line) | Success code expected | Other codes handled | Octet/line limit | Timeout | Findings |
|----|----|----|----|----|----|----|
| (greeting) | `Connect` nntp-client.go:211 | 200–201 | — (201 rejected by `ReadCodeLine(200)`) | one line, ✗ none | ✗ none (no handshake dl) | CLI-5, CLI-3, A-nntp-client-1 |
| AUTHINFO USER | `authenticate` :245 | 381 | — (281 → failure) | — | ✗ none | CLI-5, A-nntp-client-3 |
| AUTHINFO PASS | `authenticate` :263 | 281 | — | — | ✗ none | CLI-5 |
| STAT `<id>` | `StatArticle` :43 | 223 | 430,451 (unreachable) | — | ✗ none | CLI-5 |
| ARTICLE `<id>` | `GetArticle` :88 | 220 | 430→ErrNotFound,451→ErrRemoved (reachable) | 256Ki lines, ✗ per-line | ✗ none | A-nntp-client-1 |
| HEAD `<id>` | `GetHead` :142 | 221 | 430,451 (unreachable) | 1024 lines | ✗ none | CLI-5 |
| BODY `<id>` | `GetBody` :198 | 222 | 430,451 (unreachable) | 255Ki lines | ✗ none | CLI-5 |
| LIST | `ListGroups` :246 / `ListGroupsLimited` :293 | 215 | — | 1Mi lines (`MaxReadLines`) / maxGroups | ✗ none | A-nntp-client-1, A-nntp-client-9 |
| GROUP `<g>` | `SelectGroup` :374 | 211 | 411 (returns code), others→err | one line | ✗ none | CLI-2, A-nntp-client-3 |
| XOVER `n-m` | `XOver` :450 | 224 | 411 (tolerated via SelectGroup) | 1Mi lines, ✗ per-line | ✗ none | A-nntp-client-7 |
| XHDR `f n-m` | `XHdr` :515 / `XHdrStreamed(Batch)` :654 | 221 | 411 (tolerated); nil-deref on 411 in Batch | 1Mi lines | ✗ none | CLI-2, A-nntp-client-1, A-nntp-client-7 |
| LISTGROUP `<g>` | `ListGroup` :736 | 211 | others→err | 1Mi lines | ✗ none | A-nntp-client-3 |
| CHECK `<id>` | `SendCheckMultiple` :1066 | (async: 238/431/438 in demuxer) | 238,431,438; others logged | — | ✗ none | CLI-4, A-nntp-client-3 |
| TAKETHIS `<id>` | `SendTakeThisArticleStreaming` :1126 | (async: 239/439) | 239,439,400/480/500-504→TxErr | 16Ki buf | ✗ none | CLI-1, CLI-4, A-nntp-client-8 |
| POST | `PostArticle` :1213 | 340→240 | 401 "mode reader" retry; 441 | — | ✗ none | CLI-1, CLI-5, A-nntp-client-5, A-nntp-client-8 |
| MODE READER | `SwitchToModeReader` :1337 | 200–201 | others→err | — | ✗ none | — |
| MODE STREAM | `SwitchToModeStream` :1372 | 203 | others→err | — | ✗ none | CLI-4 |

## Confirmation details (repros, deleted before commit)

- **CLI-2 / CLI-4:** a `net.Listen("127.0.0.1:0")` fake server scripted to (a) answer `GROUP` with
  `411` then any line, and (b) accept AUTHINFO/MODE STREAM then CHECK. (a) panics the client at
  `nntp-client-commands.go:640` (nil `groupInfo`). For (b) the demuxer's `d.LastID+1 != cmdInfo.CmdID`
  branch logs `WARNING - out-of-order cmdID received, expected 1 got 3` and never dispatches — matching
  the authenticated-stall seed. The textproto command-id sequence (AUTHINFO USER=0, PASS=1, MODE
  STREAM=2, first CHECK=3) was confirmed from `net/textproto` `Pipeline.Next` starting at 0.
- **CLI-1:** `common.ReconstructHeaders(art, true, &host, "g")` on an article with
  `Path="a!b"` returns `Path:` **twice** (the folded accumulator line at headers.go:288 and the
  verbatim line at :293).
- The repro server and `zz_audit_nntp-client_test.go` were removed before committing; `go build ./...`
  and `go vet ./internal/nntp/ ./cmd/nntp-transfer/` pass at HEAD.

## Function inventory

| function / file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `(*BackendConn).xSetReadDeadline` nntp-client.go:346 | | ✓ | ✓ | | | staticcheck U1000; never called |
| `(*BackendConn).xSetWriteDeadline` nntp-client.go:356 | | ✓ | ✓ | | | staticcheck U1000 |
| `(*BackendConn).UpdateLastUsed` nntp-client.go:365 | | | | ✓ | | deadline calls commented (:369-370) |
| `MultiLineHeaderToMergedString` nntp-client-commands.go:894 | | | ✓ | | | dup of common.multiLineHeaderToMergedString |
| `(*BackendConn).ListGroupsLimited` :282 | | | ✓ | | | only cmd/test-nntp (external, not run) |
| `(*PeeringManager).LoadConfiguration` nntp-peering.go:196 | ✓ | ✓ | | ✓ | | "TODO load from DB"; returns defaults |
| `(*PeeringManager).reverseDNSLookup` :341 | | ✓ | | | ✓ | deadlocks on 1-slot limiter (re-take at :416) |
| `(*PeeringManager).enhancedReverseDNSLookup` :389 | | ✓ | ✓ | | ✓ | staticcheck U1000; same deadlock |
| `(*PeeringManager).AddPeer` :248 | | ✓ | | | ✓ | stores &slice[i] across append (alias bug) |
| `(*PeeringManager).CheckConnectionACL` :280 | | ✓ | | | ✓ | returns &loop-copy (:299) |
| `(*PeeringManager).CreateDefaultPeer` :537 | | ✓ | | | | make+copy double alloc |
| whole `nntp-peering.go` / `.go.bak` | | ✓ | ✓ | | | no constructor caller in tree |
| `(*TransferProgressDB).PrintRecentTransfers` utils:410 | | | ✓ | | | no caller |
| `(*TransferProgressDB).GetTransferStatsByNewsgroup` utils:444 | | | ✓ | | ✓ | unused + bypasses retry helper |
| `(*TransferProgressDB).GetAllTransferStats` utils:495 | | | ✓ | | ✓ | unused + bypasses retry helper |
| `(*TransferProgressDB).GetRecentTransfers` tp.go:219 | | | ✓ | | | only used by the unused printer |
| `IgnoreNewsgroupProgress` cmd/nntp-transfer/main.go:967 | | | ✓ | | | superseded by GetAllProgressNewsgroups |
| `matchesAnyPattern` main.go:1086 | | | ✓ | | | staticcheck U1000 |
| `(*TakeThisMode).UseCHECK` nntp-transfer.go:567 | | | | | | S1008 (style only) |

## Seed items rejected

None. All seed rows for this slice end CONFIRMED:
- **CLI-1** CONFIRMED (double Path + mis-fold).
- **CLI-2** CONFIRMED (critical; nil deref on 411).
- **CLI-3** CONFIRMED (socket leaks + dead deadline helpers).
- **CLI-4** CONFIRMED (authenticated-stream demuxer stall).
- **CLI-5** CONFIRMED (201 greeting, 281 AUTHINFO, 480 mapping, unreachable 430/451 arms; `GetArticle`
  reachable, matching the seed's "PARTLY").
- **CLI-6** CONFIRMED (peering dead/broken; `test-nntp` external, read only).
- **OPS-3** CONFIRMED (pprof on public mux via `-web-port`; no timeouts).

## Not covered / follow-ups

- **A-nntp-rfc-2 follow-up (owned here):** the fetcher un-stuffs `..`→`.` lines on the **read** path
  too: `readMultilineResponse` (`nntp-client-commands.go:838-840`) and `ListGroupsLimited`
  (`:333-335`) strip the leading dot, and `ParseHeaders` (`:950`) stores **all** raw header lines in
  `HeadersJSON` including a bare `.`-derived line. So an article pulled from an upstream can carry a
  header line that, when later served over this server's HEAD/ARTICLE without dot-stuffing (BUG-6 /
  A-nntp-rfc-2), desyncs downstream readers. This confirms A-nntp-rfc's note that the un-stuffing is
  symmetric; the fix belongs with the server-side dot-stuffing fix (`fixplan-nntp-server`) plus
  intake header validation (`fixplan-nntp-intake`). Not re-numbered here (cite A-nntp-rfc-2).
- **govulncheck:** the `PostArticle`/`DialTLS`/`redis` vuln hits are toolchain/dependency version
  issues (go1.27.1 stdlib → 27.2, `x/net` v0.46→0.60, `quic-go`), not client logic; tracked in
  `audit-ops`/`docs/audit/tools/govulncheck.txt`.
- **Not measured:** steady-state RSS of the pool/demuxer/global pools under a real multi-group transfer
  (A-nntp-client-6) and the fetcher memory growth in BUGS.md — both need a live run and belong to
  `audit-tools`.
- **Redis:** `cmd/nntp-transfer` caches message-ids in Redis (`main.go:2508,2518,2648,2657`) with
  `REDIS_TTL` and `-redis-flushdb`; the TLS path to Redis (`main.go:314`) is govulncheck-flagged only.
  No injection (keys are message-ids). Not a finding beyond A-nntp-client-3's validation note.
- **Tests:** wave-2 has no client-side slice; a future plan should add fake-server tests for CLI-2
  (411 nil deref), CLI-4 (authenticated id ordering), CLI-1 (double Path) and A-nntp-client-1
  (deadline enforcement).
