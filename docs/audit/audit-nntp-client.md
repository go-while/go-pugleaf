# Audit audit-nntp-client (base 64a53aa)

Security, robustness and correctness audit of the **outbound** NNTP client, the connection pool,
the streaming transfer engine (CHECK/TAKETHIS), the response demuxer, the transfer-progress DB, the
peering manager and the `cmd/nntp-transfer` tool (its web UI, pprof and Redis use).

Server-side command handling is `audit-nntp-rfc`; server security/DoS/TLS is `audit-nntp-sec`
(A-nntp-sec-5 covers the server `tls.Config`); intake and header reconstruction on the write path is
`audit-intake`; the fetcher/importer tools are `audit-tools`. This report follows `docs/audit/README.md`
(D1) and K6. All `path:line` anchors are at 64a53aa and were re-checked with `grep -n`/`sed -n`.

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
  `internal/postmgr/postmgr.go`, `cmd/nntp-fetcher/main.go` (group selection), `cmd/test-nntp/main.go`
  (read only; hardcoded to an external server, **not run**)
- `internal/common/headers.go` `ReconstructHeaders`/`GetHeaderFirst` (CLI-1; the full header pipeline
  is `audit-intake`); `cmd/history-rebuild/main.go` pprof flag (OPS-3 part)

Tools (from `docs/audit/tools/`, filtered to this scope):
- **staticcheck**: `nntp-client.go:346,356` `xSetReadDeadline`/`xSetWriteDeadline` unused (U1000);
  `nntp-peering.go:389` `enhancedReverseDNSLookup` unused; `nntp-transfer.go:570` `UseCHECK` S1008;
  `cmd/nntp-transfer/main.go:36` `dbBatchSize`, `:1086` `matchesAnyPattern` unused; `:1877`,`:2732`
  SA4006 dead assignments (`minLoad`, `wantedArticles`).
- **golangci-lint (gosec)**: `main.go:12` G108 pprof auto-exposed on the default mux; `:551`,`:2940`
  G114 `http.ListenAndServe` with no timeouts; `:311`,`:1404` G115 integer conversions.
- **govulncheck**: the reachable NNTP hits are on this slice's write path
  (`nntp-client-commands.go:1226,1259` `PostArticle`→`bufio.Flush`; `proxy.go:60` `DialTLS`→
  `tls.Conn.Handshake`; `main.go:314` `redis.NewClient`; `main.go:2936,2940` the web server). They are
  stdlib/`x/net`/`quic-go` version issues (go1.27.1→27.2), not client logic; see `## Not covered`.

**Verification method.** Every verdict here rests on an exact code trace (cited per finding). This
slice did **not** run its own repro: the planned fake-server program was never executed, and no
`zz_audit_*` file or `data-test-*` directory was created. In review, the reviewer reproduced CLI-1,
CLI-2, CLI-4 and CLI-5 against a scripted fake NNTP server; the CLI-1 Newsgroups output below
comes from that run. No external NNTP server was contacted and `cmd/nntp-transfer` was not run.

## Summary

Counts by severity: **critical 0, high 5, medium 4, low 7** (16 findings). By category:
robustness 6, bug 4, security 4, dead-code 2.

| severity | findings |
|----|----|
| high | CLI-1, CLI-2, CLI-3, CLI-4, OPS-3 |
| medium | CLI-5, A-nntp-client-1, A-nntp-client-3, A-nntp-client-6 |
| low | CLI-6, A-nntp-client-2, A-nntp-client-4, A-nntp-client-7, A-nntp-client-9, A-nntp-client-10, A-nntp-client-11 |

Retired ids (not reused, K6): **A-nntp-client-5** was rejected in review (see `## Seed items rejected`);
**A-nntp-client-8** (final empty body line dropped) is merged into seed CLI-1 as part 3.

Five most important:

1. **OPS-3 (high)**: `nntp-transfer -web-port` serves the default mux on all interfaces, and
   `net/http/pprof` is registered on it. `/debug/pprof/cmdline` gives the `-password` and
   `-redis-pass` flag values to anyone on the network, unauthenticated. history-rebuild's `-pprof`
   accepts any address.
2. **CLI-1 (high)**: `ReconstructHeaders` sends two `Path` headers on every TAKETHIS (the first holds
   the value twice). It also re-sends folded `Newsgroups` continuation lines (POST too), and the body
   writers drop a final empty body line.
3. **CLI-4 (high)**: the demuxer expects the first registered command to be textproto id 1. With
   `-username`, AUTHINFO USER/PASS and MODE STREAM use ids 0–2, so the first CHECK (id 3) waits in
   the out-of-order backlog forever and authenticated transfers stall.
4. **CLI-2 (high)**: `XHdrStreamedBatch` dereferences a nil `groupInfo` when a per-batch GROUP returns
   411. The panic happens in a pool goroutine with no `recover` and crashes the fetcher.
5. **CLI-3 (high)**: no read/write deadlines, no TLS handshake timeout, and `Connect` leaks the socket
   on its error paths. A silent or hostile upstream wedges a pooled connection and its goroutines
   forever.

## Findings

### CLI-1: duplicate Path, duplicated Newsgroups continuation, dropped final body line

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-1 | CLI-1 | high | bug | `common/headers.go:266-299,393-406`, `nntp-client-commands.go:1114,1149-1153,1206,1271-1275` | CONFIRMED |

Every outbound article is serialized by `ReconstructHeaders`: TAKETHIS via
`SendTakeThisArticleStreaming` (`:1114`, `withPath=true`) and POST via `PostArticle` (`:1206`,
`withPath=false`).

**Part 1 — two Path headers (TAKETHIS).**
- **Scenario:** any article with a non-empty `Path` sent by TAKETHIS reaches the peer with two
  `Path:` headers. The first one holds the value twice.
- **Evidence (`headers.go:266-299`):**
  - `pathline` starts as the **complete** line `"Path: " + host + "!.TX!" + article.Path`.
  - `paths := strings.Fields(pathline)` splits that same line. The loop then appends every token
    (`"Path:"`, then the value) to the accumulator, which already holds the whole line. Result:
    `"Path: h!.TX!p Path: h!.TX!p"`.
  - That line is appended at `:291`. A second, unfolded `"Path: …"` line is appended at `:294`
    (`:296` without a hostname).
- **RFC:** RFC 5536 §3.1.5 allows one Path header.
- **Not affected:** the References loop (`:244-264`). `refline` starts as `"References:"` without
  tokens, so its folding is correct.

**Part 2 — folded Newsgroups continuation sent twice (TAKETHIS and POST).**
- **Scenario:** the stored headers contain a folded `Newsgroups:` header. The reconstructed output
  keeps the merged line and also re-sends the continuation line on its own.
- **Evidence (`headers.go:396-406`):** the `getLines` loop merges continuation lines into
  `headerLine` and does `i++`. That loop sits inside `for i, headerLine := range moreHeaders`, and
  `range` re-assigns `i` on every iteration, so the continuation line is not skipped. On the next
  iteration it is a spaced line with `ignoreLine == false`, and it is appended again.
- **Reviewer repro output:** `"Newsgroups: g.a, g.b"` followed by a duplicate `" g.b"`.

**Part 3 — final empty body line dropped (formerly A-nntp-client-8).**
- **Scenario:** a body that ends with an empty line loses that line on retransmission.
- **Evidence:**
  - `ParseLegacyArticleLines` joins body lines with `"\n"` (`:876`), so the body lines
    `["foo",""]` are stored as `"foo\n"`.
  - Both writers split on `"\n"` and `break` on an empty last element (`:1152-1154`, `:1274-1276`).
    The retransmitted body is therefore one line shorter than the received one.
  - Related: `article.Bytes = len(BodyText)` is the LF-joined length (INT-5, `audit-intake`).

**Fix (M):**
- Part 1: build the Path line once from `article.Path` tokens and append it once.
- Part 2: use an index loop (`for i := 0; i < len(moreHeaders); i++`) so the `i++` takes effect.
- Part 3: keep the body byte-exact (store raw lines, or don't drop the last element when the
  original had it).
- Shared with `audit-intake` (the same function).

---

### CLI-2: nil dereference in XHdrStreamedBatch on a 411 GROUP reply

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-2 | CLI-2 | high | bug | `nntp-client-commands.go:636-642`, `nntp-backend-pool.go:128-136`, `processor/proc_DLArt.go:124-126` | CONFIRMED |

**Scenario:** `XHdrStreamedBatch` re-selects the group for every batch of up to 1000 articles
(`XHdrStreamed` loop, `:581-619`). If one of those GROUP commands gets `411`, `SelectGroup` returns
`(nil, 411, err)`. The code tolerates 411 and then dereferences the nil `groupInfo`.

**Evidence:**
```go
groupInfo, code, err := c.SelectGroup(groupName)     // :636  returns (nil, 411, err)
if err != nil && code != 411 {                        // :637  411 tolerated, no return
    return fmt.Errorf(...)
}
if end > groupInfo.Last {                              // :640  nil groupInfo -> panic
```
- `SelectGroup` returns `nil, code, err` whenever `ReadCodeLine(211)` fails (`:382-388`).
- The call chain has no `recover`:
  - the pool wrapper goroutine (`nntp-backend-pool.go:128-136`),
  - the fetcher goroutine (`proc_DLArt.go:124-126`),
  - the fetcher itself.

  So the panic aborts the `nntp-fetcher` process.

**Reachability (why high, not critical):**
- The fetcher first calls `proc.Pool.SelectGroup` (`cmd/nntp-fetcher/main.go:417`) and skips the
  group on `ErrNewsgroupNotFound` (411/480).
- The crash therefore needs a **later** per-batch GROUP, possibly on another pooled connection, to
  get 411. Possible causes:
  - the group is removed upstream during the run,
  - an inconsistent backend farm behind one hostname,
  - a hostile provider.
- `nntp-transfer` never calls `XHdrStreamed`.
- The reviewer reproduced the panic at `:640` with a fake server.

**Fix (S):** when `code == 411 || groupInfo == nil`, return `ErrNewsgroupNotFound` before reading
`groupInfo.Last`. Effort S.

---

### CLI-3: no deadlines, no TLS handshake timeout, socket leaks on connect errors

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-3 | CLI-3 | high | robustness | `nntp-client.go:193-239,345-371`, `proxy.go:50-69`, `nntp-client-commands.go:76-87`, `nntp-backend-pool.go:268-279` | CONFIRMED |

**Scenario:** an upstream that the client connects to (peer, provider or Tor hidden service) accepts
the TCP connection and then does one of three things:
- it never completes the TLS handshake,
- it never sends the greeting,
- it stops answering mid-response.

Nothing times out. The pooled `BackendConn` and every goroutine blocked on it (the
`readAndDispatch` demuxer, the CHECK/TAKETHIS workers, fetcher downloads) hang until the process is
killed. Separately, failed connects leak their sockets.

**Evidence:**
- **No deadlines:**
  - `Connect` reads the greeting (`:211`) with no deadline.
  - The helpers `xSetReadDeadline`/`xSetWriteDeadline` (`:346,356`) are never called (staticcheck
    U1000).
  - `UpdateLastUsed` has its deadline calls commented out (`:369-370`).
  - The per-operation deadline in `GetArticle` is commented out (`nntp-client-commands.go:76-87`).
  - No other `SetDeadline` call exists on client sockets.
- **No TLS handshake timeout:** `DialTLS` runs `tlsConn.Handshake()` (`proxy.go:60`) with no deadline.
  The client's dialers set `ConnectTimeout` only on the TCP dial (`proxy.go:74,87,108`).
- **Socket leak:** `Connect` stores the socket (`c.conn = conn`, `:206`; `TextConn`, `:207`). It then
  returns on the welcome-read error (`:214`), the bad welcome code (`:219`) and the auth failure
  (`:232`) without closing it.
  - `createConnection` then returns `(nil, err)` (`nntp-backend-pool.go:474-477`).
  - `Get` closes `pconn.conn` only when `pconn != nil` (`:270-272`). That is never true here, so the
    socket opened inside `Connect` is never closed.
  - The auth-failure path also sleeps 15 s (`:231`) while it holds a pool slot.

**Fix (M):**
- Set read/write deadlines per command, restoring the dead helpers.
- Wrap `Handshake` in `tlsConn.SetDeadline(now+ConnectTimeout)`.
- Close `c.conn`/`c.TextConn` on every error path in `Connect`.

Effort M.

---

### CLI-4: demuxer command-id ordering breaks authenticated streaming transfers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-4 | CLI-4 | high | bug | `nntp-transfer-demuxer.go:19,185-191`, `nntp-client.go:243-281`, `nntp-client-commands.go:1315-1391` | CONFIRMED |

**Scenario:** with `-username`/`-password` set, a streaming transfer stalls. No CHECK or TAKETHIS
response is ever dispatched, the worker sits in the out-of-order backlog, and `OffsetQueue.Wait`
never returns.

**Evidence:**
- `ResponseDemuxer.LastID` starts at 0 (`:19`).
- `readAndDispatch` advances only when `d.LastID+1 == cmdInfo.CmdID` (`:185`). Any other id goes into
  `outoforderBacklog`.
- Command ids come from `textproto.Conn.Cmd`, whose `Pipeline.Next()` is a per-connection counter
  starting at 0.
- On an authenticated streaming connection the first three commands are never registered with the
  demuxer (only CHECK and TAKETHIS call `RegisterCommand`):
  - AUTHINFO USER, id 0 (`nntp-client.go:245`)
  - AUTHINFO PASS, id 1 (`:263`)
  - MODE STREAM, id 2 (`SwitchToModeStream`, `nntp-client-commands.go:1372`)
- So the first registered command is the first CHECK, with id 3. The demuxer waits for id 1 forever.
- Without auth, MODE STREAM is id 0 and the first CHECK is id 1, which matches. That is why only
  authenticated transfers stall.
- The reviewer reproduced the stall with a fake server.

**Fix (M):** take the first registered command id as the starting point (set `LastID` on the first
`RegisterCommand`) instead of assuming 1. Effort M.

---

### OPS-3: pprof on the public transfer web server leaks credentials; history-rebuild pprof on any address

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| OPS-3 | OPS-3 | high | security | `cmd/nntp-transfer/main.go:12,149,165,544-551,2935-2944`, `cmd/history-rebuild/main.go:12,75,97-101` | CONFIRMED |

**Scenario:**
- `nntp-transfer -web-port N` starts the results UI with `http.ListenAndServe(":N", nil)`
  (`main.go:2940`). That is the **default mux** on **all interfaces**.
- `net/http/pprof` is imported for its side effect (`:12`), so `/debug/pprof/*` is registered on the
  same mux.
- The tool takes `-password` (`:149`, the target NNTP password) and `-redis-pass` (`:165`) as
  command-line flags.
- Result: with `-web-port` set, `GET /debug/pprof/cmdline` returns the full argv, including both
  secrets, to anyone who can reach the port. No authentication is needed. Heap and goroutine dumps
  and CPU profiles are exposed the same way.
- The dedicated `-pprof-port` listener is correctly bound to `localhost` (`:544-551`).
- Neither server sets `ReadHeaderTimeout`/`WriteTimeout`/`IdleTimeout` (gosec G114 at `:551`,
  `:2940`).
- `cmd/history-rebuild` imports `net/http/pprof` (`:12`) and its `-pprof` flag (`:75`) is passed
  unchecked to `http.ListenAndServe(*pprofAddr, nil)` (`:97-101`). The flag's own example is `':6060'`
  (all interfaces).

**Evidence:**
- `main.go:12 _ "net/http/pprof"`.
- `startWebServer` (`:2935-2944`) registers `/` and `/results` on the default mux, logs
  `http://ANY_ADDR:...` and calls `ListenAndServe(":<port>", nil)`.
- pprof's `Cmdline` handler writes `os.Args`.
- gosec flags G108 and G114.

**Fix (S):**
- Serve the UI on a private `http.ServeMux` inside an `http.Server` with timeouts, bound to
  localhost by default.
- Register pprof only on the localhost-bound `-pprof-port` mux, explicitly, not through the
  blank-import default.
- Accept passwords from an environment variable or file instead of argv.
- history-rebuild: refuse non-loopback `-pprof` addresses, or bind loopback by default.

Effort S.

---

### CLI-5: client response-code handling

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-5 | CLI-5 | medium | bug | `nntp-client.go:211-219,251-259`, `nntp-backend-pool.go:189-204`, `nntp-client-commands.go:51-63,150-166,206-222,1231-1251` | CONFIRMED (partly closed) |

**Evidence (reproduced by the reviewer with a fake server):**
- **A 201 greeting fails `Connect`:** `ReadCodeLine(NNTPWelcomeCodeMin)` (`:211`) expects exactly
  200. For 201 it returns a `*textproto.Error`, and `:212-214` returns it. The later range check
  `200..201` (`:217`) is never reached for 201. RFC 3977 §5.1.1 allows 201 (posting prohibited).
- **281 to AUTHINFO USER counts as failure:** `authenticate` expects 381 (`:251,258`). RFC 4643
  §2.3.1 allows 281 directly after USER.
- **480 reported as "group not found":** `Pool.SelectGroup` maps 411 and 480 to
  `ErrNewsgroupNotFound` (`nntp-backend-pool.go:198-201`), which hides "authentication required".
- **Unreachable 430/451 branches:** in `StatArticle`, `GetHead` and `GetBody`,
  `ReadCodeLine(223|221|222)` returns an error for any other code. The function returns at
  `:52-53`, `:151-152`, `:207-209` before its `switch code` 430/451 arms. `GetArticle` checks
  `err != nil && code == 0` (`:97`), so its 430/451 arms are reachable (the seed's "PARTLY").
- **The 401 retry in `PostArticle` is dead code:** the check is
  `strings.ToLower(line) == "mode reader"` (`:1231-1232`). RFC 3977 §3.2.1 has the server reply
  `401 MODE-READER`, with the capability label as the text. After lowercasing that is
  `"mode-reader"`, which never equals `"mode reader"`. The MODE READER retry (`:1233-1250`) therefore
  never runs, and POST fails with "rejected (code 401)".

**Fix (M):**
- Branch on `code` the way `GetArticle` does, instead of on `ReadCodeLine`'s expected-code error.
- Accept 201 greetings and 281 after AUTHINFO USER.
- Map 480 to its own error.
- Compare the 401 text with `MODE-READER` (case-insensitive) or retry on any 401.

Effort M.

---

### A-nntp-client-1: unbounded line length and no octet budget on multi-line responses

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new (beyond CLI-3) | A-nntp-client-1 | medium | security | `nntp-client-commands.go:780-847,681-716`, `proxy.go:81-116` | CONFIRMED |

**Scenario:** a hostile upstream can drive client memory without bound even when it does terminate
its responses. CLI-3 covers the missing deadlines. This finding covers what happens to the bytes
that do arrive.

**Evidence:**
- `readMultilineResponse` (`:780-847`) reads with `textproto.Reader.ReadLine`, which has no length
  cap. One line without LF is buffered until EOF or OOM.
- The loop does have a line-count cap: 256 Ki lines for ARTICLE (`MaxReadLinesArticle`, `:21`), 1024
  for HEAD, about 255 Ki for BODY, and 1 Mi (`MaxReadLines`) for LIST/XOVER/XHDR/LISTGROUP. But there
  is no byte budget. 256 Ki lines of 1 MiB each is accepted as one article and kept fully in memory
  (`lines []string`, then `strings.Join`).
- `XHdrStreamedBatch` reads its multi-line response in its own loop (`:681-716`), which has **no line
  limit** and no length cap.
- SOCKS negotiation (`dialSOCKS4`/`dialSOCKS5`, `proxy.go:81-116`) goes through `x/net/proxy` `Dial`
  with a background context. Only the TCP dial to the proxy has a timeout, so a stalled proxy or Tor
  circuit hangs the SOCKS handshake too.

**Fix (M):**
- Read through a size-limited reader (an `io.LimitReader` per response, or a bufio line-length cap).
- Add an octet budget per response (for example the provider's `MaxArtSize`).
- Add a line cap to `XHdrStreamedBatch`.
- Use `proxy.ContextDialer` with a deadline context.

Effort M.

---

### A-nntp-client-3: CR / space injection into client commands via message-ids and group names

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-3 | medium | security | `nntp-client-commands.go:43,88,142,198,374,450,515,654,736,1066,1126`, `nntp-client.go:245,263` | PLAUSIBLE |

**Scenario:** the client builds its commands with `c.TextConn.Cmd(...)`:
- `Cmd("ARTICLE %s", *messageID)`, `Cmd("GROUP %s", groupName)`, `Cmd("CHECK %s", *msgID)`,
  `Cmd("TAKETHIS %s", article.MessageID)`, `Cmd("AUTHINFO USER %s", username)`, and so on.
- `textproto.Writer.PrintfLine` adds CRLF but doesn't reject CR, LF or spaces inside the argument.

**What reaches it:**
- Message-ids from remote XHDR/XOVER lines (`parseHeaderLine`, `parseOverviewLine`) are read with
  `textproto.ReadLine`. It splits on LF and trims only the trailing CRLF, so a **bare CR inside a
  line survives**.
- Such a value is stored and later sent verbatim, for example as `CHECK <a\rX>`. The fetcher's
  `CheckMessageIdFormat` check is commented out (`proc_DLArt.go`, inside the `xhdrChan` loop).
- Spaces are reachable the same way and split the command into extra arguments.

**Missing step (why PLAUSIBLE):**
- A raw LF cannot reach this path through line-based parsing.
- Whether a bare CR mid-line acts as a line break depends on the receiving server. Go's `textproto`
  does not split on it, and other implementations were not tested.
- So a full command injection is not shown. What is shown is malformed commands built from
  unvalidated remote data.

**Fix (S):** before every `Cmd`, validate message-ids (angle brackets, no WSP/CTL) and group names
(no WSP/CTL); reject anything else. Coordinate with `audit-intake` on stored values. Effort S.

---

### A-nntp-client-6: oversized recycle pools and per-demuxer channel buffers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-6 | medium | robustness | `nntp-transfer.go:27,90,209,241,288`, `nntp-transfer-demuxer.go:33-35`, `cmd/nntp-transfer/main.go:2185-2186` | CONFIRMED |

**Scenario / evidence:**
- **Package-level pools:** `make(chan *T, 1024*1024)` allocates the full 8 MiB pointer buffer when it
  runs. There are 5 such pools in `nntp-transfer.go`: `ResponseDataPool` `:27`, `TTSetupPool` `:90`,
  `TTResponsePool` `:209`, `TTResponseChans` `:241`, `ReadRequestsPool` `:288`. Together they reserve
  about 40 MiB in **every binary that imports `internal/nntp`** (web, fetcher, server, transfer).
- **Per demuxer:** `NewResponseDemuxer` adds about 16.5 MiB (`cmdIDChan` 64 Ki slots plus two 1 Mi
  response channels, `:33-35`).
- **Per `CHTTWorker`:** another 16 MiB (`readCHECKResponsesChan`, `readTAKETHISResponsesChan`,
  `main.go:2185-2186`).
- The memory is mostly virtual until touched, but the buffers are pointer-typed, so the GC has to
  account for them.
- With `-max-threads 500` the demuxer and worker buffers alone reserve about 16 GiB of address space.

**Fix (M):** size the pools and channels from `-max-threads`/`-batch-check`, or use `sync.Pool`.
Don't allocate the transfer pools in package-level vars of a package that every binary imports.
Effort M.

---

### CLI-6: PeeringManager is unused, unfinished and buggy

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| CLI-6 | CLI-6 | low | dead-code | `nntp-peering.go:165-205,248-268,280-319,341-363,389-432`, `nntp-peering-struct.go.bak`, `cmd/test-nntp` | CONFIRMED |

Nothing in the tree constructs a `PeeringManager`. `grep` finds only the definitions and the pattern
tests. The code is dead, and it has these defects:

- **`LoadConfiguration` is a stub** (`:196-205`): "TODO: Load from database first". It returns the
  default config.
- **The DNS limiter deadlocks itself:**
  - `reverseDNSLookup` takes the 1-slot `dnsQueryLimiter` (`:344`; the capacity is set at `:172`).
  - While it holds the slot, it calls `validateHostnameForward` (`:357`), which sends on the same
    channel (`:416`). That second send blocks forever.
  - `enhancedReverseDNSLookup` (`:389`, unused) has the same pattern.
  - `CheckConnectionACL` holds `pm.mux.RLock` across the DNS calls (`:287-315`).
- **`AddPeer` aliases into the slice:** it stores `&pm.peers[len(pm.peers)-1]` in `peersMap`
  (`:262-263`). A later `append` that reallocates the slice leaves those map pointers aimed at the
  old backing array.
- **`CheckConnectionACL` returns a loop copy:** `return &peer` inside `for _, peer := range pm.peers`
  (`:299,305`) points at the copy, not at the stored peer.
- **Dead `.bak` file:** `nntp-peering-struct.go.bak` isn't compiled. Its `MatchRDNS`/`MatchHost` have
  the same limiter deadlock, and its `RemoteAddr`/`StrIsIPv4`/`StrIsIPv6` would collide with
  `nntp-peering.go` if the file were renamed to `.go`.
- **`cmd/test-nntp`** is hardcoded to `news.blueworldhosting.com:563`, and its pool section is a
  comment block calling APIs that no longer exist (`:79-125`). Read only, not run.
  - `ListGroupsLimited(10)` closes the connection when the limit is hit (`:316`), so its following
    `SelectGroup` always fails on a server with more than 10 groups.

**Fix (L):** finish and wire up the peering manager, or delete it together with the `.bak` file and
`test-nntp`. If it stays:
- use a separate slot, or no limiter, for the forward lookup,
- store `[]*Peer`,
- return pointers to stored entries.

---

### A-nntp-client-2: transfer-progress DB lacks busy_timeout; two unused readers bypass the retry helper

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-2 | low | robustness | `transfer-progress.go:50-59`, `transfer-progress-utils.go:69,119` | CONFIRMED |

**Scenario / evidence:**
- `OpenTransferProgressDB` opens its own `sql.Open("sqlite3", …)` (`transfer-progress.go:50`) and
  sets only `PRAGMA journal_mode=WAL` (`:56`). There is no `busy_timeout`.
- The live paths all use `database.Retryable*`: `createTables`, `getOrCreateRemoteTransferProgress`,
  `InsertResult`, `NewsgroupExists`, `GetAllProgressNewsgroups`, `GetRecentTransfers`.
- Only `GetTransferStatsByNewsgroup` and `GetAllTransferStats` call `tpdb.db.QueryRow` directly
  (`transfer-progress-utils.go:69`, `:119`). Both are unused (A-nntp-client-9).

**Fix (S):** set `busy_timeout` when opening the DB. Route the two readers through
`RetryableQueryRowScan`, or delete them. Effort S.

---

### A-nntp-client-4: pool wait timeout and idle expiry are hardcoded

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-4 | low | robustness | `nntp-backend-pool.go:210-334,497,549`, `nntp-client.go:43` | CONFIRMED |

**Evidence:**
- `Get` waits at most a hardcoded `30 * time.Second` for a free connection (`:330`). It takes no
  context, so callers can't bound or cancel the wait.
- Idle expiry is `DefaultConnExpire = 25 * time.Second` (`nntp-client.go:43`), checked in
  `isConnectionValid` (`:497`) and by the 5 s cleanup tick (`:549`). On a slow feed, connections
  churn.
- The `activeConns`/`failedConns` bookkeeping is duplicated in three places (`:273-276`, `:282-285`,
  `:390-393`).

**Fix (M):** take a context in `Get`, make both durations configurable, and keep the accounting in
one place. Effort M.

---

### A-nntp-client-7: malformed remote overview/XHDR lines accepted silently

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-7 | low | robustness | `nntp-client-commands.go:987-1032,548,702-715` | CONFIRMED |

**Evidence:**
- `parseOverviewLine` throws away the parse errors for the article number, bytes and lines
  (`:995-996,999`). A non-numeric number becomes `ArticleNum=0` and the line is still appended.
- `XHdr` filters out `ArticleNum <= 0` (`:548`). `XHdrStreamedBatch` forwards such lines unfiltered
  (`:702-715`).

**Fix (S):** reject lines whose article number fails to parse or is `<= 0`, on both paths. Effort S.

---

### A-nntp-client-9: dead or unused transfer-progress and client helpers

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-9 | low | dead-code | `transfer-progress-utils.go:10,45,96`, `transfer-progress.go:220`, `nntp-client-commands.go:283,894`, `cmd/nntp-transfer/main.go:36,967,1086` | CONFIRMED |

**Evidence (grep for callers):**
- **No callers:** `PrintRecentTransfers` (`transfer-progress-utils.go:10`),
  `GetTransferStatsByNewsgroup` (`:45`), `GetAllTransferStats` (`:96`).
- **Used only by the unused printer:** `GetRecentTransfers` (`transfer-progress.go:220`). It would
  also fail on every row without dates: the columns default to `""` and are `Valid`, so
  `time.Parse("")` errors.
- **Used only by `cmd/test-nntp`:** `ListGroupsLimited` (`:283`).
- **Duplicate:** `MultiLineHeaderToMergedString` (`:894`) duplicates
  `common.multiLineHeaderToMergedString`.
- **Unused in `cmd/nntp-transfer`:** `IgnoreNewsgroupProgress` (`main.go:967`); `dbBatchSize` (`:36`)
  and `matchesAnyPattern` (`:1086`) per staticcheck.

**Fix (S):** delete them. Effort S.

---

### A-nntp-client-10: transfer web UI exposes operational data unauthenticated

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-10 | low | security | `cmd/nntp-transfer/main.go:2946-3414,3417-3431` | CONFIRMED |

**Evidence:**
- `handleIndex` renders with `html/template` (`:3243`, `:3411`), which auto-escapes. There is no
  `template.HTML` sink, and the values come from the operator's own flags and DB. There is no
  injection here.
- The issue is exposure. `/` and `/results` show target hostnames, newsgroup names and counters to
  anyone on the network (bind address: OPS-3).
- `handleIndex` holds `nntp.ResultsMutex.RLock` while it executes the template and writes to the
  client (`:2947-2948`), with no write timeout. A client that never reads can hold the read lock
  while the transfer goroutines wait for `ResultsMutex.Lock` (for example `:122`, `:1134`, `:1140`). This is PLAUSIBLE; it
  was not measured.

**Fix (S):** bind to localhost (OPS-3), render into a buffer before writing, and release the lock
before the write. Effort S.

---

### A-nntp-client-11: ForceCloseConn spawns a goroutine per call; double decrement of activeConns

| seed | id | severity | category | location | verdict |
|----|----|----|----|----|----|
| new | A-nntp-client-11 | low | robustness | `nntp-client.go:313-322`, `nntp-client-commands.go:684`, `nntp-backend-pool.go:132,378-396` | CONFIRMED |

**Evidence:**
- `ForceCloseConn` always starts `go func(){ …; c.Pool.Put(c) }()` (`nntp-client.go:314`).
- Concrete double-decrement path when shutdown arrives during an XHDR stream:
  1. `XHdrStreamedBatch` runs `go c.ForceCloseConn()` (`nntp-client-commands.go:684`) and returns an
     error.
  2. The pool wrapper then calls `client.ForceCloseConn()` again (`nntp-backend-pool.go:132`).
  3. Both `Put`s see `forceClose` and call `closeConn`.
  4. The second `CloseFromPoolOnly` returns early because the conn is no longer connected, but
     `closeConn` still runs `activeConns--` and `totalClosed++` (`:389-393`).
- `activeConns` then undercounts, and `Get` can open more than `maxConns` connections.
- `ClosePool` re-enters the same path through `conn.ForceCloseConn()` while it drains (`:425`).

**Fix (M):** make `ForceCloseConn` synchronous and idempotent. Decrement only when
`CloseFromPoolOnly` actually closed the connection, for example by having it return a `closed bool`.
Effort M.

## Client command table

The commands the client issues through `textproto.Conn.Cmd`, the codes each one handles, and its
limits and timeouts. No client command has a deadline (CLI-3). "lines" is the line-count cap; no
command has a per-line or octet cap (A-nntp-client-1).

| Command | Function (file:line) | Success code | Other codes handled | Limit | Timeout | Findings |
|----|----|----|----|----|----|----|
| (greeting) | `Connect` nntp-client.go:211 | 200 (201 is rejected) | — | one line | none, and no TLS handshake deadline | CLI-5, CLI-3 |
| AUTHINFO USER | `authenticate` :245 | 381 | 281 counts as failure | one line | none | CLI-5, A-nntp-client-3 |
| AUTHINFO PASS | `authenticate` :263 | 281 | — | one line | none | — |
| STAT `<id>` | `StatArticle` commands.go:43 | 223 | 430/451 arms unreachable | one line | none | CLI-5 |
| ARTICLE `<id>` | `GetArticle` :88 | 220 | 430→ErrArticleNotFound, 451→ErrArticleRemoved (reachable) | 256 Ki lines | none | A-nntp-client-1 |
| HEAD `<id>` | `GetHead` :142 | 221 | 430/451 arms unreachable | 1024 lines | none | CLI-5 |
| BODY `<id>` | `GetBody` :198 | 222 | 430/451 arms unreachable | ~255 Ki lines | none | CLI-5 |
| LIST | `ListGroups` :246 / `ListGroupsLimited` :293 | 215 | — | 1 Mi lines / maxGroups | none | A-nntp-client-1, A-nntp-client-9 |
| GROUP `<g>` | `SelectGroup` :374 | 211 | 411 returned as a code | one line | none | CLI-2, A-nntp-client-3 |
| XOVER `n-m` | `XOver` :450 | 224 | 411 from SelectGroup tolerated | 1 Mi lines | none | A-nntp-client-7 |
| XHDR `f n-m` | `XHdr` :515 / `XHdrStreamedBatch` :654 | 221 | 411 tolerated, then nil deref in the Batch variant | 1 Mi lines / **no limit** (Batch) | none | CLI-2, A-nntp-client-1, A-nntp-client-7 |
| LISTGROUP `<g> n-m` | `ListGroup` :736 | 211 | — | 1 Mi lines | none | A-nntp-client-3 |
| CHECK `<id>` | `SendCheckMultiple` :1066 | read by the demuxer, interpreted in `cmd/nntp-transfer/main.go:2497-2528`: 238 | 431 (counted, never retried), 438; others logged | — | none | CLI-4, A-nntp-client-3 |
| TAKETHIS `<id>` | `SendTakeThisArticleStreaming` :1126 (writer: `bufio.NewWriter(c.conn)`, 4 KiB default, :1119) | read by the demuxer, interpreted in `main.go:2642-2680`: 239 | 439; 400/480/500-504 → TX error, worker exits | — | none | CLI-1, CLI-4 |
| POST | `PostArticle` :1213 | 340, then 240 | 401 retry (dead), 441 returned with an error | — | none | CLI-1, CLI-5 |
| MODE READER | `SwitchToModeReader` :1337 | 200–201 | — | one line | none | — |
| MODE STREAM | `SwitchToModeStream` :1372 | 203 | — | one line | none | CLI-4 |

## Function inventory

| function / file | stub | dead | unused | misleading | wrong | evidence |
|----|----|----|----|----|----|----|
| `(*BackendConn).xSetReadDeadline` nntp-client.go:346 | | ✓ | ✓ | | | staticcheck U1000 |
| `(*BackendConn).xSetWriteDeadline` nntp-client.go:356 | | ✓ | ✓ | | | staticcheck U1000 |
| `(*BackendConn).UpdateLastUsed` nntp-client.go:365 | | | | ✓ | | deadline calls commented out (:369-370) |
| `PostArticle` 401 branch commands.go:1231-1250 | | ✓ | | | ✓ | the compared text never matches `MODE-READER` (CLI-5) |
| `MultiLineHeaderToMergedString` commands.go:894 | | | ✓ | | | duplicate of the common helper |
| `(*BackendConn).ListGroupsLimited` commands.go:283 | | | ✓ | | ✓ | only test-nntp uses it; closes the conn, then reads from it (:316,348-357) |
| `(*PeeringManager).LoadConfiguration` nntp-peering.go:196 | ✓ | ✓ | | ✓ | | TODO; returns defaults |
| `(*PeeringManager).reverseDNSLookup` :341 | | ✓ | | | ✓ | deadlocks on the 1-slot limiter (:344, :416) |
| `(*PeeringManager).enhancedReverseDNSLookup` :389 | | ✓ | ✓ | | ✓ | staticcheck U1000; same deadlock |
| `(*PeeringManager).AddPeer` :248 | | ✓ | | | ✓ | `&slice[i]` survives `append` |
| `(*PeeringManager).CheckConnectionACL` :280 | | ✓ | | | ✓ | returns `&loop-copy` (:299, :305) |
| `nntp-peering-struct.go.bak` | | ✓ | ✓ | | ✓ | not compiled; same limiter deadlock |
| `(*TransferProgressDB).PrintRecentTransfers` utils:10 | | | ✓ | | | no caller |
| `(*TransferProgressDB).GetTransferStatsByNewsgroup` utils:45 | | | ✓ | | ✓ | bypasses the retry helper (:69) |
| `(*TransferProgressDB).GetAllTransferStats` utils:96 | | | ✓ | | ✓ | bypasses the retry helper (:119) |
| `(*TransferProgressDB).GetRecentTransfers` progress.go:220 | | | ✓ | | ✓ | `time.Parse("")` fails on rows without dates |
| `IgnoreNewsgroupProgress` cmd/nntp-transfer/main.go:967 | | | ✓ | | | replaced by `GetAllProgressNewsgroups` |
| `matchesAnyPattern` main.go:1086, `dbBatchSize` main.go:36 | | | ✓ | | | staticcheck U1000 |
| `assignWorkerToNewsgroup` main.go:1858 | | | | ✓ | ✓ | the "least busy" loop `break`s at the first lower load (SA4006 on `minLoad`) |

## Seed items rejected

No seed row is rejected as a whole. CLI-1…6 and OPS-3 are all CONFIRMED (CLI-5 "partly closed").
Sub-items and new findings that were rejected or changed in review:

- **CLI-1, References folding:** rejected. `refline` starts as `"References:"` without tokens
  (`headers.go:247-264`), so the folding is correct. Only the Path loop folds an accumulator that
  already holds the whole line.
- **CLI-5, `GetArticle` 430/451:** not a defect. These branches are reachable (`:97`), as the seed's
  PARTLY says.
- **CLI-2, crash of `nntp-transfer`:** rejected. `nntp-transfer` never calls `XHdrStreamed`. The
  crash affects the fetcher, and only after the initial `SelectGroup` succeeded. Severity is lowered
  from critical to high.
- **A-nntp-client-5 (PostArticle response sequencing):** rejected. textproto's sequencer advances only
  on `EndResponse`. Reading the final 240/441 without `StartResponse` does not change the id
  accounting: the normal path calls `EndResponse(id)` at `:1221`, and the 401 path defers it. The
  real 401 defect is the dead MODE-READER comparison, now a CLI-5 sub-item.
- **A-nntp-client-8:** merged into CLI-1 part 3 (seed ids are canonical, K6).
- **A-nntp-client-1 deadline/handshake part:** moved back into seed CLI-3. A-1 keeps only the
  unbounded line length, the octet budget and SOCKS negotiation.

## Not covered / follow-ups

- **A-nntp-rfc-2 follow-up (handed to this slice):**
  - The fetcher also un-stuffs `..`→`.` on the read path: `readMultilineResponse`
    (`nntp-client-commands.go:838-840`) and `ListGroupsLimited` (`:333-335`).
  - `ParseHeaders` stores **all** header lines in `HeadersJSON` (`:950`), including malformed ones
    and a line that was `..` on the wire and is now `.`.
  - So an article fetched from an upstream can carry a `.` header line. When this server serves it
    without dot-stuffing (BUG-6, A-nntp-rfc-2), it desyncs readers.
  - On retransmission, `ReconstructHeaders` drops lines shorter than 4 characters and lines without
    a colon (`headers.go:335-356`), so the `.` line itself is not re-sent to peers. A header name
    that starts with `.` (`.X: y`) is sent un-stuffed, and the peer strips the dot.
  - Fix ownership: server dot-stuffing (`fixplan-nntp-server`) and intake header validation
    (`fixplan-nntp-intake`). Cited as A-nntp-rfc-2, no new id.
- **Bare LF inside stored fields:** `GetHeaderFirst` joins repeated Subject/From values with `"\n"`
  (`common/headers.go:580-601`; A-nntp-rfc-2 scenario B). `ReconstructHeaders` emits
  `"Subject: "+article.Subject` unchanged, so a stored `"first\n."` puts a bare LF plus `.` into the
  outbound TAKETHIS stream. A peer that splits lines on LF (Go `textproto`, as in this server's
  `readArticleLines`) would end the article early. Not reproduced here; the intake-side fix is
  `audit-intake`. Candidate for the client fix plan: strip CR/LF from every reconstructed header
  value.
- **CHECK 431 / TX errors versus the progress DB:**
  - 431 responses are counted but never re-queued (`main.go:2513-2522`).
  - The newsgroup is still written to the transfer-progress DB as done.
  - `getNewsgroupsToTransfer` skips groups that have progress rows (`main.go:867,899,933,951`), so a later
    run with the same date range skips those articles.
  - Code trace only; candidate for `fixplan-nntp-client`.
- **Redis key scope:** the keys are raw message-ids (`main.go:1629,1733` EXISTS; `:2508,2648` SET) with no prefix
  for the target host. Running transfers to two peers against one Redis DB within `REDIS_TTL` makes
  the second skip articles the first already sent. Code trace only.
- **govulncheck:** the `PostArticle`/`DialTLS`/redis hits are toolchain and dependency versions
  (go1.27.1 stdlib → 27.2, `x/net`, `quic-go`). Tracked in `docs/audit/tools/govulncheck.txt`.
- **Not measured:** RSS of the pools and demuxers in a real multi-group transfer
  (A-nntp-client-6), and the fetcher memory growth in BUGS.md (`audit-tools`).
- **Tests:** wave 2 has no client-side slice. The fix plan should add fake-server tests for CLI-2
  (411 nil deref), CLI-4 (authenticated id ordering), CLI-1 (single Path, Newsgroups continuation,
  final empty line), CLI-5 (201/281/401) and CLI-3 (deadlines).
