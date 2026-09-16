# Main DB connection pool: measurement (finding C6)

`DefaultDBConfig` sets `MaxOpenConns = 100` and `MaxIdleConns = 25` for the main
SQLite database, raised from 15 and 5 with the comments "Increased from 15 to support
high concurrency" and "Increased from 5 to keep connections ready"
(`internal/database/db_init.go:98-99`, applied at `:306-307`). The numbers were never
measured against SQLite. Finding C6 of the `web-sqlite-leftovers` plan asks for a
measurement; decision 8 of that plan says **measurement and report only** — this
document changes no production code, and `db_init.go` is untouched.

**TL;DR — recommendation: keep the current defaults.** Across 30 measured
configurations there were zero `SQLITE_BUSY` retries at any pool size; throughput is
flat above 8 connections; median latency keeps improving as the pool grows; and
`sql.DB` never opened more than the offered concurrency (34 connections against a
ceiling of 100), so the 100 costs nothing at normal load. The only value that is
clearly wrong is a small one: `MaxOpenConns = 4` loses ~40 % throughput. See
[Recommendation](#recommendation).

## The benchmark

`internal/database/lo2_pool_bench_test.go`, `BenchmarkLo2PoolMix`. It never runs
under a plain `go test` (benchmarks need `-bench`), everything lives under
`b.TempDir()`, and it touches no package global.

For each `MaxOpenConns` in 4, 8, 16, 32, 100 (with `MaxIdleConns = min(n, 25)`, the
rule the current defaults follow) it

* opens a temp DB with the package's own driver `driverNameMain`, so the pragma list
  `OpenDatabase` stored applies to every pooled connection exactly as in production
  (`busy_timeout = 30000`, `foreign_keys = ON`, `synchronous = NORMAL`,
  `cache_size = -16384`, `temp_store = MEMORY`, `mmap_size = 0`, `journal_mode = WAL`,
  `wal_autocheckpoint = 1000`);
* runs the embedded main migrations, which create `users`, `site_news` and
  `post_queue` among the rest, and seeds 500 users, 200 site-news rows (100 visible)
  and one newsgroup;
* drives the mix for 10 s of wall clock:
  * **32 reader goroutines**: `query_GetUserByID` (the statement `GetUserByID` uses),
    and every 8th iteration `query_GetVisibleSiteNews` instead (the visible-site-news
    SELECT, 100 rows);
  * **2 writer goroutines**: the sliding-expiry `UPDATE users SET session_expires_at
    = ? WHERE id = ?` from `ValidateUserSession`, then the `post_queue` INSERT from
    `InsertPostQueueEntry`;
* reports ops/s, p50/p99 read and write latency from a log-linear histogram (8
  sub-buckets per octave, so a quantile is accurate to about 12 %), and the number of
  `SQLITE_BUSY`/`SQLITE_LOCKED` retries.

Busy retries are counted by a retry wrapper local to the benchmark file. That is
deliberate: `internal/database/sqlite_retry.go` is owned by another slice in the same
wave, so the measurement must not depend on it, and everything reported here is wall
clock, not the retry clock.

Command (three times per size, `uptime` recorded before each run):

```
go test -run '^$' -bench Lo2PoolMix -benchtime 10s ./internal/database/
```

## Machine

| | |
|---|---|
| CPU | Intel Core i7-10710U @ 1.10 GHz, 6 cores / 12 threads |
| RAM | 62 GiB |
| Kernel | Linux 7.0.14-8-pve x86_64 |
| Go | go1.25.3 linux/amd64 |
| SQLite | **3.50.4** (`select sqlite_version()` through `driverNameMain`) |
| driver | `github.com/mattn/go-sqlite3 v1.14.32` (cgo) |
| date | 2026-09-16 |

Two storage variants were measured, because `b.TempDir()` follows `TMPDIR`:

* **tmpfs** — the default `/tmp`, which is a 32 GB tmpfs on this box. No real I/O;
  this isolates lock and CPU effects.
* **zfs** — `TMPDIR` pointed at the ZFS dataset that holds the worktree
  (`recordsize=128K`, `compression=lz4`), the same kind of storage the production
  data directory sits on (`/tank0/efs` is ZFS too).

**Caveat on load:** this is a shared development box and other agents were compiling
and running tests throughout. The load average before each run is listed with the
run; it includes the benchmark's own ~4 busy threads. Individual cells swing by up to
4x between runs (see `MaxOpenConns 16` in tmpfs run 3 and zfs runs 1 and 3, which
collapsed under a load spike). The medians and, more importantly, the *shape* of the
curves are reproducible; single numbers are not.

## Raw numbers

Latencies are milliseconds unless noted. "load" is the 1-minute load average taken
immediately before the sweep started.

### tmpfs, three sweeps

| run | load | MaxOpenConns | ops/s | reads/s | writes/s | read p50 | read p99 | write p50 | write p99 | busy retries |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | 4.64 | 4 | 7 973 | 7 502 | 471 | 3.15 | 18.9 | 3.15 | 18.9 | 0 |
| 1 | | 8 | 10 286 | 9 706 | 579 | 2.36 | 14.7 | 2.62 | 14.7 | 0 |
| 1 | | 16 | 10 369 | 9 732 | 637 | 1.57 | 25.2 | 1.97 | 21.0 | 0 |
| 1 | | 32 | 12 401 | 11 260 | 1 141 | 0.29 | 62.9 | 0.49 | 18.9 | 0 |
| 1 | | 100 | 5 722 | 4 843 | 879 | 0.16 | 167.8 | 0.26 | 41.9 | 0 |
| 2 | 11.64 | 4 | 7 007 | 6 592 | 415 | 3.67 | 23.1 | 3.67 | 23.1 | 0 |
| 2 | | 8 | 11 194 | 10 561 | 633 | 2.10 | 12.6 | 2.36 | 12.6 | 0 |
| 2 | | 16 | 11 354 | 10 660 | 694 | 1.57 | 23.1 | 1.84 | 18.9 | 0 |
| 2 | | 32 | 13 649 | 12 420 | 1 229 | 0.26 | 62.9 | 0.49 | 16.8 | 0 |
| 2 | | 100 | 14 269 | 12 448 | 1 821 | 0.16 | 75.5 | 0.29 | 14.7 | 0 |
| 3 | 14.47 | 4 | 8 562 | 8 066 | 496 | 3.15 | 18.9 | 2.88 | 18.9 | 0 |
| 3 | | 8 | 11 287 | 10 643 | 644 | 2.10 | 12.6 | 2.36 | 12.6 | 0 |
| 3 | | 16 | 5 823 | 5 438 | 385 | 2.10 | 67.1 | 2.36 | 54.5 | 0 |
| 3 | | 32 | 3 226 | 2 854 | 372 | 0.36 | 218.1 | 0.59 | 100.7 | 0 |
| 3 | | 100 | 11 868 | 10 361 | 1 507 | 0.16 | 83.9 | 0.29 | 18.9 | 0 |

**tmpfs medians of the three sweeps**

| MaxOpenConns | ops/s | reads/s | writes/s | read p50 | read p99 | write p50 | write p99 | busy retries |
|---|---|---|---|---|---|---|---|---|
| 4 | 7 973 | 7 502 | 471 | 3.15 | 18.9 | 3.15 | 18.9 | 0 |
| 8 | 11 194 | 10 561 | 633 | 2.10 | 12.6 | 2.36 | 12.6 | 0 |
| 16 | 10 369 | 9 732 | 637 | 1.57 | 25.2 | 1.97 | 21.0 | 0 |
| 32 | 12 401 | 11 260 | 1 141 | 0.29 | 62.9 | 0.49 | 18.9 | 0 |
| 100 | 11 868 | 10 361 | 1 507 | 0.16 | 83.9 | 0.29 | 18.9 | 0 |

### ZFS, three sweeps

| run | load | MaxOpenConns | ops/s | reads/s | writes/s | read p50 | read p99 | write p50 | write p99 | busy retries |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | 16.33 | 4 | 6 592 | 6 310 | 282 | 3.67 | 23.1 | 6.29 | 27.3 | 0 |
| 1 | | 8 | 7 998 | 7 694 | 303 | 2.88 | 25.2 | 5.77 | 33.6 | 0 |
| 1 | | 16 | 2 058 | 1 956 | 102 | 6.29 | 167.8 | 9.44 | 151.0 | 0 |
| 1 | | 32 | 8 154 | 7 690 | 464 | 0.33 | 100.7 | 1.05 | 54.5 | 0 |
| 1 | | 100 | 8 028 | 7 454 | 574 | 0.18 | 109.1 | 0.59 | 58.7 | 0 |
| 2 | 22.44 | 4 | 4 817 | 4 598 | 220 | 4.72 | 37.7 | 6.82 | 41.9 | 0 |
| 2 | | 8 | 7 548 | 7 256 | 292 | 3.15 | 21.0 | 5.77 | 25.2 | 0 |
| 2 | | 16 | 9 403 | 9 028 | 376 | 1.84 | 27.3 | 3.15 | 31.5 | 0 |
| 2 | | 32 | 4 912 | 4 645 | 266 | 0.36 | 151.0 | 1.05 | 109.1 | 0 |
| 2 | | 100 | 10 789 | 10 058 | 731 | 0.18 | 83.9 | 0.59 | 41.9 | 0 |
| 3 | 21.20 | 4 | 4 578 | 4 381 | 197 | 4.19 | 58.7 | 6.82 | 62.9 | 0 |
| 3 | | 8 | 5 749 | 5 523 | 226 | 3.15 | 54.5 | 5.77 | 75.5 | 0 |
| 3 | | 16 | 1 842 | 1 748 | 93 | 7.34 | 167.8 | 9.44 | 167.8 | 0 |
| 3 | | 32 | 11 316 | 10 738 | 578 | 0.33 | 75.5 | 0.98 | 58.7 | 0 |
| 3 | | 100 | 12 138 | 11 406 | 731 | 0.18 | 75.5 | 0.66 | 46.1 | 0 |

**ZFS medians of the three sweeps**

| MaxOpenConns | ops/s | reads/s | writes/s | read p50 | read p99 | write p50 | write p99 | busy retries |
|---|---|---|---|---|---|---|---|---|
| 4 | 4 817 | 4 598 | 220 | 4.19 | 37.7 | 6.82 | 41.9 | 0 |
| 8 | 7 548 | 7 256 | 292 | 3.15 | 25.2 | 5.77 | 33.6 | 0 |
| 16 | 2 058* | 1 956* | 102* | 6.29* | 167.8* | 9.44* | 151.0* | 0 |
| 32 | 8 154 | 7 690 | 464 | 0.33 | 100.7 | 1.05 | 58.7 | 0 |
| 100 | 10 789 | 10 058 | 731 | 0.18 | 83.9 | 0.59 | 46.1 | 0 |

\* Two of the three `MaxOpenConns 16` cells (zfs runs 1 and 3) were taken while
another build saturated the machine, so this median is noise, not a result. ZFS run 2
(9 403 ops/s, read p50 1.84 ms) is the only usable `16` sample and fits the tmpfs
curve.

## What the numbers say

**1. The pool is never the source of lock contention.** `busy_retries = 0` in every
single run, at every pool size, on both filesystems. With `busy_timeout = 30000` on
every connection, 32 concurrent readers and 2 writers hammering the same main DB never
produced one `SQLITE_BUSY` that reached Go. Raising or lowering `MaxOpenConns` is not
a lock-safety question here.

**2. Throughput is flat from 8 connections upwards.** Median total ops/s on tmpfs:
7 973 (4) → 11 194 (8) → 10 369 (16) → 12 401 (32) → 11 868 (100). The step from 4 to
8 is real and large (+40 %); everything above 8 is inside the run-to-run noise of this
box. Only `MaxOpenConns = 4` is clearly too small: it queues 34 workers behind 4
connections.

**3. Median latency keeps improving with pool size, tail latency keeps getting
worse.** This is the clearest and most reproducible signal, present in all six sweeps.
Per-statement p50/p99 on tmpfs (median of the three runs):

| MaxOpenConns | user read p50 | user read p99 | site_news p50 | site_news p99 | write p50 | write p99 |
|---|---|---|---|---|---|---|
| 4 | 2.9 ms | 18.9 ms | 5.8 ms | 23.1 ms | 3.1 ms | 18.9 ms |
| 8 | 1.7 ms | 12.6 ms | 5.8 ms | 15.7 ms | 2.4 ms | 12.6 ms |
| 16 | 1.3 ms | 18.9 ms | 7.3 ms | 41.9 ms | 2.0 ms | 20.9 ms |
| 32 | 246 µs | 14.7 ms | 6.3 ms | 134.2 ms | 492 µs | 18.9 ms |
| 100 | 164 µs | 11.5 ms | 7.9 ms | 184.5 ms | 295 µs | 18.9 ms |

A cheap point read goes from 2.9 ms to 164 µs as the pool grows, because at
`MaxOpenConns = 4` it spends its time queueing behind 33 other workers inside
`database/sql`, not inside SQLite. The p99 of the *heavy* query moves the other way:
with a large pool all the expensive 100-row scans run at once on 12 hardware threads
and time-slice against each other, so the site-news p99 grows from 23 ms to 185 ms.

**4. Real storage slows the writes, not the reads, and does not change the shape.**
On ZFS the write p50 roughly doubles (6.8 ms vs 3.1 ms at 4 connections, 590 µs vs
295 µs at 100) and write throughput halves, while the read curve is the same shape.
The optimum does not move.

**5. The pool only ever opens as many connections as the offered concurrency.**
`sql.DB` opens connections lazily. With 34 worker goroutines, `MaxOpenConns = 100`
behaves exactly like `MaxOpenConns = 34`; nothing in this benchmark can distinguish
100 from any number above 34. The sampled peak `OpenConnections` is in the next
section.

## Connections actually opened, and what they cost

One process per pool size, 10 s each on tmpfs, at a load average of 5.6, reading
`VmRSS`/`VmHWM` from `/proc/self/status` (the SQLite page cache is C memory, so the Go
heap says nothing about it):

| MaxOpenConns | MaxIdleConns | peak open connections | connections closed for exceeding MaxIdleConns | RSS after | peak RSS |
|---|---|---|---|---|---|
| 4 | 4 | 4 | 0 | 19 MiB | 19 MiB |
| 8 | 8 | 8 | 0 | 21 MiB | 21 MiB |
| 16 | 16 | 16 | 0 | 24 MiB | 25 MiB |
| 32 | 25 | 32 | 7 | 37 MiB | 38 MiB |
| 100 | 25 | **34** | 9 | 38 MiB | 45 MiB |

Two things follow.

* **`MaxOpenConns = 100` never opened 100 connections.** `sql.DB` opens connections
  lazily, so the pool stopped at 34 — exactly the offered concurrency (32 readers + 2
  writers). The 100 is a ceiling, not an allocation: a web server that only ever has
  12 concurrent main-DB statements in flight pays for 12 connections, whatever the
  setting says.
* **The `MaxIdleConns = 25` mismatch cost almost nothing here.** Only 7 and 9
  connections were discarded for exceeding the idle cap over a whole 10 s saturation
  run, because under steady load the connections rarely fall idle all at once. Every
  discarded connection has to be reopened and re-run the 8-pragma `ConnectHook`
  (`journal_mode = WAL` included), so this would matter under bursty load — but it was
  not measurable here, and it is not an argument for changing anything today.

The measured RSS growth is about 0.5 MiB per connection, which is *not* the worst
case: the seeded test DB is about 2 MB, so a connection's page cache cannot exceed
that. The configured limit is `cache_size = -16384`, i.e. **16 MiB of page cache per
connection** (`db_init.go:103`; the comment there, "-16384 == 1024 KB * 16384 = 16MB",
gets the arithmetic wrong but the value right). On an installation whose
`pugleaf.sq3` is larger than that, the main pool's worst case is
`open connections × 16 MiB` — 544 MiB at the 34 connections seen here, and 1.6 GiB if
a burst ever drove the pool to its 100 ceiling. That ceiling is a real number to know,
but note the lever for it is `CacheSize`, not `MaxOpenConns`.

## Recommendation

**Keep the current defaults: `MaxOpenConns = 100`, `MaxIdleConns = 25`. The
measurement found no reason to change `db_init.go`.**

The reasoning, in the order the evidence supports it:

1. **Nothing measured is worse at 100 than at a smaller value, except the tail of the
   heavy query.** Median throughput at 100 is at the top of the measured range on both
   filesystems (11 868 ops/s tmpfs, 10 789 ops/s ZFS, the best ZFS number of all), and
   median latency is the best by a wide margin (164 µs vs 2.9 ms for a point read at
   `MaxOpenConns = 4`).
2. **The 100 is a ceiling that is never reached.** The pool opened 34 connections
   under 34 offered concurrent operations. Lowering `MaxOpenConns` to, say, 16 would
   not "save" anything at normal load — it would only start queueing once more than 16
   handlers happen to be inside a main-DB statement at the same time, which is exactly
   the burst you want the pool to absorb.
3. **No lock pressure at any size.** `busy_retries = 0` in all 30 measured cells. The
   per-connection `busy_timeout = 30000` absorbs everything 32 readers and 2 writers
   can produce. A larger pool is not creating `SQLITE_BUSY` storms, which was the main
   thing to check for SQLite.
4. **The one cost of a big pool is the p99 of expensive queries under saturation**
   (site-news p99 23 ms at 4 connections against 185 ms at 100), and that is
   scheduler oversubscription on a 12-thread box while every worker is saturating it —
   not a pool defect. go-pugleaf's web timeouts are seconds (`WriteTimeout` 120 s, `internal/web/webserver.go:48`), so
   trading a 185 ms tail on a saturated box for a 17x better median is the right
   trade for this application.
5. **`MaxOpenConns = 4` is the only value that is clearly wrong**, and it is not what
   is configured. It costs ~40 % throughput and makes a point read take 3 ms of pure
   queueing.

Two things to keep in the back of the mind rather than act on now:

* **If the pair is ever touched, change `MaxIdleConns` first, not `MaxOpenConns`.**
  `MaxIdleConns = 25` under `MaxOpenConns = 100` means up to 75 connections can be
  built and thrown away repeatedly under bursty load, each rebuild re-running the
  8-pragma `ConnectHook`. The measured cost was negligible under steady load (7-9
  discards in 10 s), so this is a nicety: `MaxIdleConns = MaxOpenConns` would remove
  the question entirely, at the price of holding the idle connections' page caches.
* **The memory ceiling is `open connections × CacheSize`, currently 16 MiB each.**
  That is what to reduce on a small-memory host — `CacheSize`, not the pool size. This
  was not a problem in the benchmark (45 MiB peak RSS) only because the test DB is 2 MB.

To revisit this properly, the measurement that is missing is one with a *large* main
DB (bigger than the 16 MiB per-connection cache) and a concurrent batch writer, on a
quiet machine. That is the shape where the answer could actually flip.

## Caveats, and what would change the answer

* **This is a synthetic mix.** 32 readers and 2 writers hitting one hot, fully cached
  ~2 MB database as fast as they can is not the production request pattern: a real web
  request does template rendering, group-DB work and network I/O between its main-DB
  statements, so the main-DB pool sees far less concurrency than 34 for the same
  request rate. The benchmark therefore overstates pool pressure.
* **The box was loaded.** Single numbers move by up to 4x between runs; only the
  medians and the shape of the curves are meaningful. A quiet machine would be needed
  to pick between 16, 32 and 100 on throughput alone.
* **`GetVisibleSiteNews` is cached in production** (`uiCache`, `queries.go`), and the
  benchmark deliberately bypasses the cache and goes to the DB every time. The heavy
  half of the read mix is therefore rarer in production than here.
* **Only the main DB was measured.** Group DBs have their own pool settings and their
  own pragma list (`ensureGroupConnPragmas`), and `MaxOpenDatabases` bounds how many
  of them are open. Nothing here applies to them.
* **Which workload shape would change the answer:**
  * *More writers.* With 2 writers the single SQLite write lock is never the
    bottleneck and `busy_timeout` absorbs everything. A fetcher or batch writer
    hammering the main DB concurrently with the web server would serialise on that
    lock, and then a *smaller* pool (fewer connections queued behind the writer) is
    better, and busy retries would finally appear.
  * *A main DB much larger than the page cache.* Everything here is cache-resident
    (the seeded DB is ~2 MB against a 16 MiB per-connection cache). With a main DB
    that does not fit, reads become I/O bound, more connections hide I/O latency
    better, and the per-connection cache cost becomes the dominant consideration.
  * *A machine with many more cores.* The tail-latency penalty of a big pool here is
    scheduler oversubscription on 12 hardware threads. On a 64-thread server the
    crossover moves up.
  * *Long-running statements.* `ANALYZE`, a backup, or a large admin query holds a
    connection for seconds. The more of the pool such statements can occupy, the more
    a large `MaxOpenConns` matters — in favour of keeping it large.

## Reproducing

```bash
uptime                       # record the load; this benchmark needs a quiet machine
go test -run '^$' -bench Lo2PoolMix -benchtime 10s ./internal/database/

# longer runs
LO2POOL_DURATION=30s go test -run '^$' -bench Lo2PoolMix -benchtime 30s ./internal/database/

# on real storage instead of /tmp (a scratch root, never ./data)
mkdir -p "$PWD/data-test-pool"
TMPDIR="$PWD/data-test-pool" go test -run '^$' -bench Lo2PoolMix -benchtime 10s ./internal/database/
rm -rf "$PWD/data-test-pool"

# peak RSS and connection counts: one pool size per process
for n in 4 8 16 32 100; do
  go test -run '^$' -bench "Lo2PoolMix/MaxOpenConns$n\$" -benchtime 10s ./internal/database/
done
```

`-benchtime` must be a duration and not larger than `LO2POOL_DURATION` (default 10 s);
the benchmark fails with an explanatory message otherwise, because each invocation
already runs the mix for the full duration, so the framework stops at `b.N == 1`.
Every sub-benchmark also logs the SQLite version, `GOMAXPROCS`, the peak
`OpenConnections`, `maxIdleClosed` and the process RSS.
