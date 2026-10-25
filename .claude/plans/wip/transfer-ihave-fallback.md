# Plan: transfer-ihave-fallback — IHAVE for peers without streaming

## Context
`cmd/nntp-transfer` feeds a peer with pipelined CHECK/TAKETHIS (RFC 4644) through `Pool.Get(MODE_STREAM_MV)`,
`SendCheckMultiple`, `SendTakeThisArticleStreaming` and the `ResponseDemuxer` (`CHTTWorker`). Against a peer
without streaming (innd with streaming off, MODE STREAM → 501, CHECK/TAKETHIS → 500, no `STREAMING` capability)
the pool's Get fails with the 501, nothing is sent, and `BootConnWorkers` retries the Get with a backoff of up to a
minute for as long as the tool runs (pinned by `TestCliTxNoStreamPeer`, testing-001 c1af9bf). innfeed and innxmit
fall back to IHAVE (RFC 3977 §6.3.2) in that case; nntp-testkit v0.6.0+ models both the peer's IHAVE
(335/235/435/436/437, `Defer436Once`, `Reject`, `Have`) and `PeerOptions.NoStream`.

Decision (user, 2026-10-25): add the IHAVE fallback. It is a second, simple, synchronous sender path next to the
streaming machinery, not a change to it.

## Design
1. **Client command** `internal/nntp/nntp-client-commands.go`: `func (c *BackendConn) IHave(article *models.Article,
   nntphostname *string, newsgroup string) (code int, txBytes int, err error)`. Synchronous under `c.mux`: headers
   via the same relay/reconstruct logic as `SendTakeThisArticleStreaming` (factor the header selection into a
   helper `transferHeaders(article, nntphostname, newsgroup) ([]string, error)` used by both; no behaviour change
   for TAKETHIS), `validMessageIDArg`, `IHAVE <id>` → read code: **335** → `writeArticleLines` + flush → read
   **235** (accepted) / **437** (rejected: counted, not retried) / **436** (transfer failed, retry later) /
   **480** → `authRequiredErr` (same mapping as the reader commands); **435** (not wanted: counted like a 438) and
   **436** before the article (deferred: retry later) / **480** / **500/501** (no IHAVE: `ErrIHaveUnsupported`,
   the caller stops) / **502** (permission denied: stop). Any read error, EOF or unexpected code force-closes
   the connection (the article may be half-sent). Return code lets the caller count. `TxBytes` counted as for
   TAKETHIS.
2. **Mode decision** in `cmd/nntp-transfer`: new flag `-ihave` with values `auto` (default: streaming, fall back to
   IHAVE when the first `Pool.Get(MODE_STREAM_MV)` fails with a `501`/`500` on MODE STREAM — detect through a
   sentinel `nntp.ErrStreamingUnsupported` returned by `SwitchToModeStream` for 500/501, wrapped by the pool's
   Get), `never` (today's behaviour: streaming only, retry forever), `always` (IHAVE even when the peer streams:
   for peers whose TAKETHIS is broken). The decision is made once per run and logged once:
   `[TRANSFER] peer %s has no streaming (MODE STREAM: %s): sending with IHAVE`.
3. **IHAVE worker** `cmd/nntp-transfer/ihave.go` (new): `IHaveWorker(db, workerID, conn, rs)` consumes the same
   `rs.jobs` (`CHTTJob` with `MessageIDs`) that `CHTTWorker` does, so `BootConnWorkers` only chooses which
   worker to start (`go IHaveWorker(...)` instead of `go CHTTWorker(...)`; the pool Get uses `MODE_READER_MV`?
   No: IHAVE needs neither mode — add `nntp.MODE_NONE_MV` handling or Get with `MODE_STREAM_MV` and accept the
   501 — simplest: a `Pool.GetPlain()` that dials without switching mode; check `Pool.Get` for a mode value that
   skips the switch). Per job: for each message-id load the article (`db` as the TAKETHIS path does),
   `conn.IHave`; counters on `job.NGTProgress` (accepted → the TAKETHIS transferred counter, 435 → unwanted,
   437 → rejected, 436 → requeue once at the end of the job then count as retry), Redis cache marks as the
   TAKETHIS path (`redisKey`, same TTL), `job.OffsetQ.OffsetBatchDone()` and `job.Response(...)` as CHTTWorker
   does so the producer side is unchanged. `ErrIHaveUnsupported`/502/auth error → requeue the job, exit the
   worker, `BootConnWorkers` logs and stops the run for that peer (no endless loop: with `-ihave auto` a peer
   that has neither streaming nor IHAVE is a configuration error, exit code 1 after the running jobs drained).
4. **Progress/summary**: the end-of-run summary line names the mode used (`mode=streaming|ihave`).
5. **Pinned:** no pipelining of IHAVE (RFC 3977 allows it only loosely; innd answers 436 when paused — the
   synchronous path keeps the retry logic simple); the streaming path is untouched except for the shared header
   helper and the new sentinel; `-check-first`, `-batch-check`, Redis flags apply to both modes where they make
   sense (Redis yes, CHECK batching no).

## Waves
### Wave 0 (inline): `nntp.ErrStreamingUnsupported`, `nntp.ErrIHaveUnsupported` sentinels in
`internal/nntp/nntp-client.go`; `SwitchToModeStream` wraps 500/501 in `ErrStreamingUnsupported`; the pool's Get
keeps the wrap (`%w`). `TestCliTxNoStreamPeer` gains `errors.Is(err, ErrStreamingUnsupported)`.
### Wave 1 (parallel)
- slice `ihave-client`: `internal/nntp/nntp-client-commands.go` (IHave, transferHeaders), `internal/nntp/nntp-client-ihave_test.go`
  (new; kit `PeerHandler` with `NoStream: true`: accepted 235, `Have` → 435, `Reject` → 437, `Defer436Once` → 436
  then 235 on the second try, 480 via `Faults.Override "IHAVE"`, a peer that closes after 335 → force-closed
  connection and an error, `Lines()` show `IHAVE <id>` then the article then nothing else; the relayed article
  equals the TAKETHIS wire form: compare `s.Articles()` of a TAKETHIS run and an IHAVE run of the same article).
- slice `ihave-tool`: `cmd/nntp-transfer/ihave.go` (new), `cmd/nntp-transfer/main.go` (flag, mode decision in
  `BootConnWorkers`, summary), `cmd/nntp-transfer/ihave_test.go` (new; the tool's worker against the kit
  `PeerHandler{NoStream: true}` with a scratch DB `-data ./data-test-ihave` seeded with two groups; `-ihave auto`
  sends everything by IHAVE and exits 0; `-ihave never` against the same peer keeps retrying (bounded by a test
  deadline, assert the retry log and no IHAVE sent); `-ihave auto` against a peer with streaming uses TAKETHIS
  (no IHAVE in `Lines()`)). The worker may be exercised in-process (the existing `debugserver_test.go` shows how
  the tool's pieces are driven) rather than through the binary.
### Wave 2
- slice `ihave-docs-verify`: README/docs paragraph for `-ihave`; `docs/` transfer doc; `BUGS.md` note removed if
  one exists about "no IHAVE"; a `cmd/nntp-transfer` e2e via the built binary against the kit peer started by
  `proc`-less helper (as in the compress plan) if wave 1 did it in-process only.
## Checks
CLAUDE.md list; `./build_nntp-transfer.sh`; `PUGLEAF_KNOWN_BUGS=1 ...` unchanged.
## End-to-end
`./build/nntp-transfer -data ./data-test-ihave -host 127.0.0.1 -port <kit peer, NoStream> -ihave auto -redis-cache=false
-max-threads 1 <group selection flags as the tool needs>`: log shows the fallback line once, `N articles sent via IHAVE`,
exit 0; the kit peer's `Have` holds every id. Same with `-ihave never`: the pool retry line appears, no IHAVE, the run
is stopped by the test's deadline (documented as the pre-plan behaviour).

## Progress
### Wave 0 (2026-10-25, inline 74a5302 + 279c631)
- `ErrStreamingUnsupported` (MODE STREAM 500/501, read from the code before the textproto error), `ErrIHaveUnsupported`;
  `TestCliTxNoStream*` assert the sentinel.
### Wave 1
- `worktree-agent-a2408d2df140d4c85` (ihave-client, ba83f7f) reviewed MERGE, merged 70fd1d9: `transferHeaders` factored
  out of TAKETHIS (wire byte-identical, pinned by TestCliIHaveWireEqualsTakeThis), `BackendConn.IHave`.
- `worktree-agent-a74ceff9750b31136` (ihave-tool, 5adef45) merged ee68585 (did not compile alone: needed IHave):
  `-ihave auto|never|always`, IHaveWorker, stop path with exit 1, summary `mode=`.
- Inline e4b69bf: `nntp.MODE_NONE_MV` (= 0; SwitchMode no-op, Pool.Get hands an idle connection out as is) so IHAVE
  runs on a transit connection (innd gives a MODE READER connection to nnrpd, which has no IHAVE); IHave refuses a
  reader-mode connection, accepts 437 before the article as "rejected by id", treats a textproto continuation reply
  ("335-...") as malformed (force-close); ihaveConnMode flipped from the MODE_READER_MV stopgap; TestTxIHaveWorkerPeer
  unskipped. Review minors applied from the client review (1, 2, 3, 4); informational: a sendCmd write failure leaves
  the textproto response sequence stuck until the asynchronous ForceCloseConn lands (pre-existing house pattern; the
  worker never reuses a failed connection).
- Kit note: PeerHandler ignores Faults (Store/Script only), so refusal cases use Script-mode peers.
