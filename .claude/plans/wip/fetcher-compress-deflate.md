# Plan: fetcher-compress-deflate — COMPRESS DEFLATE (RFC 8054) on provider connections

## Context
go-pugleaf's NNTP client (`internal/nntp`, used by cmd/nntp-fetcher, cmd/nntp-transfer, cmd/post-queue and the
web gateway's provider connections) never sends COMPRESS. INN's nnrpd (2.6+) and several text providers offer
`COMPRESS DEFLATE`; the fetcher pulls text groups where deflate saves most of the bandwidth. The server side of
go-pugleaf stays without COMPRESS (not in scope; decision 2026-10-25). nntp-testkit v0.7.0+ has COMPRESS in the
kit client and in every fake server mode (`StoreOptions.Compress`, `CompressOutputOnly`, `PeerOptions.Compress`,
`Faults.Override "COMPRESS"`), so the client can be tested against a compressing fake.

The receiving side cannot use `compress/flate.NewReader` as-is: its reader passes decoded bytes on only at an
empty stored block (sync flush, `00 00 ff ff`), when the window is full or at the end; a peer that flushes with
zlib's `Z_PARTIAL_FLUSH` (an empty fixed block, what INN's nnrpd sends; RFC 8054 §4 recommends it) stays unreadable
until it sends more. The kit solved this with `nntp-testkit/internal/inflate` (`Run(src *bufio.Reader, out
func([]byte) error) error`, 434 lines, same author): port it into go-pugleaf as `internal/nntp/inflate` (package
doc names the origin); go-pugleaf must not import a test kit in production code, and the kit's package is
internal anyway.

The kit's fake server flushes with `flate.Writer.Flush` (sync flush) only, so a partial-flush stream for the test
has to be produced in the test itself (a `compress/flate` writer cannot emit `Z_PARTIAL_FLUSH`; craft the bytes:
compress a reply with a `flate.Writer` at `flate.NoCompression`/`BestSpeed` and append the empty fixed block
`0x02 0x00`-style partial flush by hand, or replay a captured nnrpd stream as a test fixture) and fed to the
decoder directly; the decoder test is where that matters, the connection tests use the kit.

## Design
1. **Config.** `config.Provider.Compress bool` (`json:"compress"`), `models.Provider.Compress bool`
   (`db:"compress"`), migration `0033_main_providers_compress.sql`: `ALTER TABLE providers ADD COLUMN compress
   BOOLEAN NOT NULL DEFAULT false;`. Plumbing like `proxy_enabled`: `query_AddProvider`, `query_UpdateProvider`,
   `query_GetProviders`, `query_GetProviderByName`, `query_GetProviderByID` (+ their Scan lists), web admin
   provider add/update (`c.PostForm("compress")`, checkbox in `web/templates/admin_providers.html` next to the
   proxy checkbox in both the list row form and the edit form), `cmd/nntp-fetcher/backend.go`
   `fetcherBackendConfig` copies it, `internal/database/db_init.go` default-provider seeding copies it.
   `nntp.BackendConfig.Compress bool` (set from the provider everywhere a BackendConfig is built from one; grep
   `nntp.BackendConfig{`). Off by default: a provider without COMPRESS keeps working unchanged.
2. **Wire.** In `BackendConn.Connect`, after the greeting and after AUTHINFO (RFC 8054 §2.2.2: COMPRESS after
   authentication; a 480 on COMPRESS means authenticate first), when `Backend.Compress`: send `COMPRESS DEFLATE`;
   on 206 switch `c.conn`/`c.TextConn` to a compressing connection; on 403/500/501/502/503 log once per provider
   (`[NNTP-CONN] provider '%s': COMPRESS DEFLATE refused (%d %s), continuing uncompressed`) and continue plain —
   a refusal is not a connection failure. The pool's `createConnection` needs no change (Connect does it).
3. **Compressing connection** `internal/nntp/nntp-client-compress.go`: `type deflateConn struct` wrapping the
   `*deadlineConn` (keep deadlines and `maxLine` semantics: the line cap applies to the inflated stream, so the
   inflater output feeds the existing line-cap logic, i.e. the deflateConn sits *under* `deadlineConn`:
   `deadlineConn.Conn = deflateConn{raw: socket}`; check `newDeadlineConn` and `setMaxLine` keep working on the
   inflated bytes). Writes: `flate.NewWriter(raw, flate.DefaultCompression)`, write + `Flush()` (sync flush) per
   `Write` call, sent in one socket write. Reads: an inflater goroutine runs `inflate.Run` over
   `bufio.NewReader(raw)` into a bounded pending buffer (1 MiB cap like the kit: the inflater waits while that
   much is unread, so an endless server cannot fill memory) with a cond var; `Read` waits for bytes, EOF or
   error; a read deadline applies to the inflated stream (socket read has none; `Close` ends the goroutine).
   Corrupt input → error wrapping `flate.CorruptInputError`, connection force-closed. `Close` is idempotent.
   The existing textproto `c.TextConn` must be re-created on the new reader/writer after the 206 (nothing is
   pending at that point: Connect is sequential). Bytes buffered by the textproto reader after the 206 line
   belong to the compressed stream only if the server wrote them after the 206; RFC 8054 says compression
   starts right after the 206 CRLF, so pass `TextConn.R.Buffered()` bytes into the inflater as its first input
   (the kit does the same).
4. **Accounting / logging.** `BackendConn` counts `txBytesRaw/txBytesWire` and `rxBytesRaw/rxBytesWire`
   (atomic); the fetcher's existing per-provider stats log line (find where bytes/sec are printed in
   cmd/nntp-fetcher or `Pool.Stats`) gains `compressed=%.1f%%` when Compress is on. Keep it to one line.
5. **Decisions pinned:** the client never sends COMPRESS twice; `STARTTLS` does not exist in this client (TLS is
   the dial) so the RFC's STARTTLS-after-COMPRESS rule does not arise; MODE READER/STREAM switching happens on
   the compressed stream as any other command; `ForceCloseConn` closes the raw socket (ends the inflater);
   the hostile-peer bounds (`MaxResponseLineBytes`, `MaxReadLines*`, octet budgets) are measured on the
   **inflated** data, so a 1 KiB compressed bomb is still cut by the inflated caps (test it).

## Waves
### Wave 0 (inline, on `plan-fetcher-compress-deflate`)
- `internal/nntp/inflate/inflate.go` (+ `inflate_test.go` ported from the kit: `git show main:internal/inflate/*`
  in /tank0/efs/GO/src/github.com/go-while/nntp-testkit, package doc credits the origin).
- `nntp.BackendConfig.Compress`, `config.Provider.Compress`, `models.Provider.Compress`, migration 0033.
  Commit so wave 1 slices compile against the fields.
### Wave 1 (parallel)
- slice `compress-conn`: `internal/nntp/nntp-client-compress.go` (new), `internal/nntp/nntp-client.go` (Connect:
  the COMPRESS step, counters), `internal/nntp/nntp-client-conn.go` only if the deadline/maxLine layering needs
  it, `internal/nntp/nntp-client-compress_test.go` (new; kit Store server with `StoreOptions.Compress`: GROUP /
  ARTICLE / XHDR / XOVER round-trips compressed, `Compressed()`-style assertion via the counters; `Faults.Override
  "COMPRESS" {"403 ..."}` → plain and working; `CompressOutputOnly` → the client's commands are still understood?
  no: that fake reads the client uncompressed, so the client's compressed commands fail — pin that the client
  detects the broken server (500/501 on the next command or a read error) and force-closes instead of hanging;
  `MaxConns`+COMPRESS; a compressed endless `Drip`/`Endless` reply still hits `ErrResponseTooLarge`; partial-flush
  decoder test with a hand-made stream; `Pool` round trip with `Compress: true`). Owned: those files.
- slice `compress-config`: migration `internal/database/migrations/0033_main_providers_compress.sql`,
  `internal/database/queries.go` (provider queries/scans), `internal/database/db_init.go` (seed copy),
  `internal/web/web_admin_provider.go`, `web/templates/admin_providers.html`, `cmd/nntp-fetcher/backend.go`,
  every other `nntp.BackendConfig{` built from a provider (grep; cmd/nntp-transfer, cmd/post-queue, internal/web),
  `internal/database/providers_compress_test.go` (new: add/update/get round-trip of the flag),
  `internal/web/...` admin form test if a provider form test exists (extend, else skip). Owned: those files.
### Wave 2
- slice `compress-stats`: the fetcher stats line with the compression ratio (file found in wave 1 report; likely
  `cmd/nntp-fetcher/main.go` or `internal/nntp/nntp-backend-pool.go` Stats) + `docs/` note (`docs/README` or the
  providers doc; one paragraph: what to tick, which providers offer it, that refusal is logged once).
## Checks
CLAUDE.md list; `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` still exactly
`nntp-article-430-cache-delay`; `./build_fetcher.sh`, `./build_webserver.sh`, `./build_nntp-transfer.sh`.
## End-to-end
1. `go test -race -run 'TestCliCompress' ./internal/nntp/ -count=3`.
2. Scratch DB: `./build/webserver -data ./data-test-compress -webport 11990 ...` is not needed; instead a small
   Go test or `cmd/nntp-fetcher -data ./data-test-compress -test-conn` against a kit Store server started by
   `go run` of a 20-line helper in the scratch dir (`fakeserver.New` with `StoreOpts.Compress: true`, a generated
   group of 1000 articles) with a provider row `compress=1` inserted through the admin form or `usermgr`-style
   SQL on the scratch DB: the fetcher log shows one `COMPRESS DEFLATE accepted` line per connection and the
   stats line with `compressed=…%`; the kit server's `Lines()` show every command once (no doubled COMPRESS).
3. Same against `StoreOpts.Compress: false`: one `refused (500 …)` line, articles still fetched.

## Progress
### Wave 0 (2026-10-25, inline b200eb1)
- `internal/nntp/inflate` ported from the kit (tests included), `Compress` on `models.Provider`/`config.Provider`/
  `nntp.BackendConfig`, migration 0033.
### Wave 1
- `worktree-agent-a4736a42745b6bbdf` (compress-config, 92702aa) reviewed MERGE, merged b297896. Column/Scan order of
  the five provider queries verified by the reviewer. CLI-built configs (cmd/nntp-transfer, cmd/nntp-analyze,
  processor analyze) have no provider row, so no flag: a `-compress` flag for nntp-transfer is a follow-up.
- Wave 2 picks up two review minors: extend `internal/web/adf_opt_admin_test.go` (provider create/update form)
  with the `compress` field and an update without it going back to false; move both checkboxes out of the
  "Proxy Configuration" block next to `posting_enabled`. Pre-existing: `GetProviderByName/ByID` use raw
  `QueryRow` instead of `RetryableQueryRowScan` (separate cleanup, not this plan).
- `worktree-agent-a12cfcbdaf7cae7d2` (compress-conn, b399e14 + review fixes c239e20) reviewed MERGE, merged ab061f9.
  Review fixes: COMPRESS errors wrapped; line cap disabled around the 206 and re-armed after the swap (a server
  pipelining compressed payload behind the 206 is pinned by TestCliCompressPipelinedAfter206); rxRaw counts the
  returned octets on a cap abort; Read after Close is net.ErrClosed. Decision: a 480 to COMPRESS stays a refusal
  (plain, logged every time with its own wording, no dedupe). flate.DefaultCompression hardcoded.
### Wave 2
- `worktree-agent-ac8f06835ae720b93` (compress-stats + the two wave-1 minors: 8896382 77b0f70 2f1fc81 99583bd)
  reviewed MERGE, merged (see git log) and polished inline 8d729e7 (no negative compressed share; closeConn
  lock=true note; docs "up to ~1 MiB"; byte totals count from a successful connect on). Pool: liveConns map +
  four totals under pool.mux, PoolStats byte fields; fetcher logs one `[FETCHER] provider '%s': rx %s (wire %s,
  compressed %.1f%%) tx %s` line per provider at shutdown; docs/COMPRESS.md + docs/README.md (new index).
  A stray first spawn landed in a kit worktree (my shell cwd was the kit tree): removed, no work lost.
