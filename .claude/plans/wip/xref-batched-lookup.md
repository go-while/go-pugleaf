# Plan: xref-batched-lookup

## Context
Plan `xref-history` (done, merged 2026-10-16 as 9c03e85) serves a local `Xref:` header from the history
index. In `-xref full` every served line of an OVER/XOVER or HDR/XHDR range costs one history point lookup
(`History.LookupLocations` → `routeHash` → one `SELECT newsgroups ... WHERE message_id = ?` on one of
16 shard DBs × 256 tables). A 1000-line range is 1000 single-row queries, each crossing the `database/sql`
pool, prepare and scan separately.

A locations cache was rejected (2026-10-16): a normal reader never asks for the same range twice, and a
cache would need invalidation on expire trims. The user asked instead for a batched lookup behind a flag
`-xref-batched-lookup`, so both paths can be compared on real data.

Expected gain: the ids of a range spread over up to 4096 tables, so a 1000-line range still becomes a
few hundred queries; the saving is per-query overhead (one prepare/scan per table instead of per id),
roughly 3-5× on large ranges, not 1000×. Nothing changes in the stored format or in the served output.

## Design
- `internal/history`: `LookupLocationsBatch(messageIDs []string) (map[string][]Location, error)`.
  Routes every id with `routeHash`, groups by `(dbIndex, tableName)`, and runs per group
  `SELECT message_id, newsgroups FROM <table> WHERE message_id IN (?, ...)` in chunks of at most 500 ids
  (stays below SQLite's variable limit on every supported build). Absent ids are simply missing from
  the map (same meaning as `nil, nil` today). `stats.lookups` grows by the number of ids, `duplicates`
  by the number of hits. Disabled/closed/empty input behave like `LookupLocations`. One failing table
  query fails the whole call (the caller falls back to per-id or to partial, see below).
- `internal/processor`: `XrefLocationsBatch(messageIDs []string) (map[string][]XrefLocation, error)`:
  history batch, then the same group-ID → name resolution as `XrefLocations` (`xrefResolve`, negative
  cache `xrefMiss`), building the per-id slices in history row order.
- `internal/nntp`:
  - `ArticleProcessor` gains `XrefLocationsBatch`. Test fakes (`hFakeProcessor` in
    `nntp-server-harness_test.go`, `wiringTestProcessor` in `nntp-wiring_test.go`) and
    `cmd/nntp-server/processor_adapter.go` implement it (the fake counts batch calls and ids separately).
  - New server switch `xrefBatched` (atomic bool, `SetXrefBatchedLookup`/`XrefBatchedLookup`), default
    true in `NewNNTPServer`.
  - A helper `xrefOthersBatch(mode, messageIDs) map[string][]XrefLocation` in `nntp-xref.go`: nil unless
    `XrefFull` and batched; on error logs once (same `errLogged` flag) and returns nil so the callers
    fall back to the per-id `xrefOthers` path unchanged.
  - Range call sites pass the prefetched map into the existing per-line formatting: OVER/XOVER
    (`nntp-cmd-xover.go:113` area), HDR range (`nntp-cmd-over-hdr.go:247`), XHDR range
    (`nntp-cmd-xhdr.go:172`). Single-article HEAD/ARTICLE/OVER-by-message-id keep the one-shot path.
    Chunked range handlers batch per chunk (the chunk the DB layer already returns), never the whole range.
- `cmd/nntp-server/main.go`: `-xref-batched-lookup` (bool, default true): "in -xref full, look up the
  locations of an OVER/XOVER/HDR/XHDR range with one history query per shard table instead of one per
  line". Shown in the startup log line. Ignored in `off`/`partial` (no lookups there anyway).
- Byte-exact output: the served lines must be identical with the flag on and off (tests assert it).

## Waves
### Wave 0 (inline, done on plan-xref-batched-lookup)
- `internal/nntp/nntp-server.go`: interface method `XrefLocationsBatch(messageIDs []string) (map[string][]XrefLocation, error)`;
  `NewNNTPServer` calls `SetXrefBatchedLookup(true)`. `internal/nntp/nntp-xref.go`: `xrefModeStore.batched`
  (atomic.Bool), `SetXrefBatchedLookup`/`XrefBatchedLookup`. `internal/processor/processor.go`: `XrefLocationsBatch`
  stub (one `XrefLocations` per id; slice serve-batch replaces it with the history batch). `cmd/nntp-server/processor_adapter.go`
  forwards. Fakes: `wiringTestProcessor` returns nil; `hFakeProcessor` serves `xrefLocs` for the given ids, fails with
  `xrefBatchErr`, counts `xrefBatchCalls`/`xrefBatchIDs` (`xrefBatchCount()`).
  Checks: gofmt empty, vet, build, race tests of nntp/processor/nntp-server green.
### Wave 1
- slice `history-batch`: `internal/history/history.go` (`LookupLocationsBatch`, chunking, stats),
  `internal/history/xref_test.go` (batch == per-id results for mixed plain/numbered rows, absent ids,
  >500 ids chunking, disabled/closed). Checks: gofmt, vet, build, `go test -race ./internal/history/...`.
- slice `serve-batch`: `internal/processor/processor.go` (`XrefLocationsBatch`, reuse `xrefResolve`),
  `internal/processor/proc_reuse_test.go` (resolution + negative cache in batch), `internal/nntp/nntp-xref.go`
  (`xrefOthersBatch`, error fallback), `internal/nntp/nntp-cmd-xover.go`, `internal/nntp/nntp-cmd-over-hdr.go`,
  `internal/nntp/nntp-cmd-xhdr.go` (range sites), `internal/nntp/nntp-xref_test.go` (flag on: one batch
  call per chunk and zero per-id calls; flag off: unchanged counts; output byte-identical both ways; batch
  error falls back to per-id), `cmd/nntp-server/main.go` (flag + log line), `cmd/nntp-server/processor_adapter.go`,
  `README.md` (-xref paragraph: one sentence on the flag), `cmd/nntp-server` e2e test extended with one
  `-xref full -xref-batched-lookup=false` run comparing OVER output to the batched run.
  Depends only on the wave 0 interface, not on the history slice (the processor calls
  `history.LookupLocationsBatch`, so build order: merge `history-batch` first).

## Checks
gofmt -l internal/history internal/processor internal/nntp cmd/nntp-server; go vet ./...; go build ./...;
the full CLAUDE.md race list; `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` still lists only
T-nntp-article-1.

## End-to-end
Reuse the xref-history scenario (`cmd/nntp-server/e2e_xref_test.go`, or the verifier's scripts kept in
`./data-test-xref/`) with `-data ./data-test-xref-batch`, hostname example.org, port 11191:
1. `-xref full` (batched default): `OVER 1-2` in both groups shows the 9th field
   `Xref: example.org xref.test.a:1 xref.test.b:1` (serving group first); HDR/XHDR Xref ranges the same.
2. `-xref full -xref-batched-lookup=false`: byte-identical OVER/HDR/XHDR output.
3. `-xref partial`/`off` with and without the flag: unchanged output, no history lookups.
4. Startup log line contains `xref=full xref-batched-lookup=true|false`.
Expected: all identical outputs; no new warnings in the server log.

## Benchmark
- `internal/history/xref_bench_test.go`: `BenchmarkLookupLocations` vs `BenchmarkLookupLocationsBatch` on a
  scratch index (`t.TempDir`) filled with e.g. 100k numbered rows, measuring 1000-id lookups per op
  (`go test -run xxx -bench 'LookupLocations' -benchmem ./internal/history/`). A benchmark, not an assert:
  no wall-clock expectations in tests (see memory `no-timing-tests`).
- On real data (user only, never an agent: `./data` is production): start `pugleaf-nntp-server` on a spare port
  with `-xref full` once with and once with `-xref-batched-lookup=false`, and time `OVER 1-1000` style ranges
  in a big crossposted group, e.g. with `time (printf 'GROUP <g>\r\nOVER <lo>-<hi>\r\nQUIT\r\n' | nc 127.0.0.1 <port> | wc -l)`.
  Record the two timings in the Outcome. Only when the batched path wins on this comparison does it stay
  the default; otherwise flip the default to false before merging.
