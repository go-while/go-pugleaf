package database

// C6: the main DB pool (MaxOpenConns 100, MaxIdleConns 25, db_init.go:98-99, :306-307)
// was never measured for SQLite. BenchmarkLo2PoolMix runs a synthetic web-ish mix
// (many short reads, a few small writes) against a freshly migrated main DB for a
// fixed wall-clock duration at several pool sizes, and reports throughput, read and
// write latency quantiles and the number of SQLITE_BUSY/LOCKED retries.
//
// Nothing here changes production code or a package global: the pool is a local
// *sql.DB opened with driverNameMain, so the pragma list OpenDatabase stored
// (busy_timeout 30000, foreign_keys ON, synchronous NORMAL, cache_size -16384,
// temp_store MEMORY, WAL) applies exactly as in production.
//
// Benchmarks never run under a plain `go test`; run them explicitly with
//
//	go test -run '^$' -bench Lo2PoolMix -benchtime 10s ./internal/database/
//
// -benchtime must be a duration, not a count: each sub-benchmark runs the mix for
// lo2PoolDuration() (default 10s, override with LO2POOL_DURATION), which already
// exceeds the requested benchtime, so the framework stops after b.N == 1.

import (
	"database/sql"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/models"
)

const (
	lo2PoolReaders  = 32 // concurrent readers
	lo2PoolWriters  = 2  // concurrent writers
	lo2PoolUsers    = 500
	lo2PoolNewsRows = 200
	// lo2PoolNewsEvery: one visible-site_news query per N user lookups. The home page
	// is a fraction of the requests, and in production GetVisibleSiteNews is behind
	// uiCache; here it always hits the DB, so keep it a minority of the read mix.
	lo2PoolNewsEvery = 8

	lo2PoolDefaultDuration = 10 * time.Second

	// Same backoff values as sqlite_retry.go, kept local so this benchmark does not
	// depend on that file (another slice is changing when its retry clock starts).
	lo2PoolRetryBase     = 10 * time.Millisecond
	lo2PoolRetryMax      = 2500 * time.Millisecond
	lo2PoolRetryAttempts = 100
)

// lo2PoolSizes are the MaxOpenConns values under test; MaxIdleConns is min(n, 25).
var lo2PoolSizes = []int{4, 8, 16, 32, 100}

// lo2PoolDuration returns how long one sub-benchmark drives the mix.
func lo2PoolDuration() time.Duration {
	if v := os.Getenv("LO2POOL_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return lo2PoolDefaultDuration
}

// --- latency histogram -------------------------------------------------------

// lo2PoolHist is a log-linear latency histogram: 8 sub-buckets per octave, so a
// bucket is at most ~12% wide. Each worker owns one (no sharing, no atomics on the
// hot path); they are merged after the workers have stopped.
type lo2PoolHist struct {
	buckets [512]int64
	count   int64
	sum     int64
}

// lo2PoolBucket maps a nanosecond value to its bucket index.
func lo2PoolBucket(ns int64) int {
	if ns < 8 {
		if ns < 0 {
			return 0
		}
		return int(ns)
	}
	e := bits.Len64(uint64(ns)) - 1 // >= 3
	sub := int((uint64(ns) >> (e - 3)) & 0x7)
	idx := (e-2)*8 + sub
	if idx > 511 {
		idx = 511
	}
	return idx
}

// lo2PoolBucketUpper returns the largest nanosecond value that falls into bucket idx.
func lo2PoolBucketUpper(idx int) int64 {
	if idx < 8 {
		return int64(idx)
	}
	e := idx/8 + 2
	sub := int64(idx % 8)
	width := int64(1) << (e - 3)
	return (8+sub)*width + width - 1
}

func (h *lo2PoolHist) add(d time.Duration) {
	ns := int64(d)
	h.buckets[lo2PoolBucket(ns)]++
	h.count++
	h.sum += ns
}

func (h *lo2PoolHist) merge(o *lo2PoolHist) {
	for i := range o.buckets {
		h.buckets[i] += o.buckets[i]
	}
	h.count += o.count
	h.sum += o.sum
}

// quantile returns the upper bound of the bucket that holds the q-quantile.
func (h *lo2PoolHist) quantile(q float64) time.Duration {
	if h.count == 0 {
		return 0
	}
	target := int64(q*float64(h.count) + 0.5)
	if target < 1 {
		target = 1
	}
	var seen int64
	for i := range h.buckets {
		seen += h.buckets[i]
		if seen >= target {
			return time.Duration(lo2PoolBucketUpper(i))
		}
	}
	return time.Duration(lo2PoolBucketUpper(len(h.buckets) - 1))
}

func (h *lo2PoolHist) mean() time.Duration {
	if h.count == 0 {
		return 0
	}
	return time.Duration(h.sum / h.count)
}

// lo2PoolUS formats a duration as microseconds for a reported metric.
func lo2PoolUS(d time.Duration) float64 {
	return float64(d) / float64(time.Microsecond)
}

// --- cheap per-goroutine RNG -------------------------------------------------

type lo2PoolRand uint64

func (r *lo2PoolRand) next() uint64 {
	x := uint64(*r)
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	*r = lo2PoolRand(x)
	return x
}

func (r *lo2PoolRand) intn(n int) int {
	return int(r.next() % uint64(n))
}

// --- retry wrapper that counts busy retries ----------------------------------

// lo2PoolRetry runs op and retries SQLITE_BUSY / SQLITE_LOCKED the way the
// Retryable* helpers do, counting every retry in busy. It is deliberately local:
// sqlite_retry.go must not be touched, and the measurement must not depend on when
// that file starts its retry clock. Everything reported here is wall clock.
func lo2PoolRetry(busy *atomic.Int64, op func() error) error {
	for attempt := 0; ; attempt++ {
		err := op()
		if !isRetryableSQLiteError(err) {
			return err
		}
		busy.Add(1)
		if attempt >= lo2PoolRetryAttempts-1 {
			return err
		}
		d := time.Duration(attempt+1) * lo2PoolRetryBase
		if d > lo2PoolRetryMax {
			d = lo2PoolRetryMax
		}
		time.Sleep(d)
	}
}

// --- the measured statements -------------------------------------------------

// lo2PoolSlideSession is the sliding-expiry UPDATE of ValidateUserSession
// (internal/database/db_sessions.go).
const lo2PoolSlideSession = `UPDATE users SET session_expires_at = ? WHERE id = ?`

// lo2PoolInsertQueue is the INSERT of InsertPostQueueEntry
// (internal/database/db_post_queue.go).
const lo2PoolInsertQueue = `INSERT INTO post_queue (newsgroup_id, message_id, posted_to_remote, in_processing) VALUES (?, ?, 0, 0)`

func lo2PoolReadUser(db *sql.DB, busy *atomic.Int64, id int64) error {
	var u models.User
	return lo2PoolRetry(busy, func() error {
		return db.QueryRow(query_GetUserByID, id).Scan(&u.ID, &u.Username, &u.Email,
			&u.PasswordHash, &u.DisplayName, &u.Verified, &u.Disabled, &u.NoPosting,
			&u.PostCount, &u.LastPostUnix, &u.CreatedAt)
	})
}

func lo2PoolReadNews(db *sql.DB, busy *atomic.Int64) error {
	return lo2PoolRetry(busy, func() error {
		rows, err := db.Query(query_GetVisibleSiteNews)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item models.SiteNews
			var visible int
			if err := rows.Scan(&item.ID, &item.Subject, &item.Content,
				&item.DatePublished, &visible, &item.CreatedAt, &item.UpdatedAt); err != nil {
				return err
			}
		}
		return rows.Err()
	})
}

// --- setup -------------------------------------------------------------------

// lo2PoolOpen creates a temp main DB with the production driver and pool settings
// for maxOpen connections, runs the embedded main migrations (which create users,
// site_news and post_queue) and seeds it. It returns the pool, the seeded user IDs
// and the newsgroup id that post_queue rows reference.
func lo2PoolOpen(b *testing.B, maxOpen int) (*sql.DB, []int64, int64) {
	b.Helper()
	if mainConnPragmas.Load() == nil {
		b.Fatal("mainConnPragmas not set: TestMain must have opened the shared DB")
	}
	dir := b.TempDir()
	pool, err := sql.Open(driverNameMain, filepath.Join(dir, "lo2pool-main.sq3"))
	if err != nil {
		b.Fatalf("open main DB: %v", err)
	}
	b.Cleanup(func() { _ = pool.Close() })

	idle := maxOpen
	if idle > 25 {
		idle = 25
	}
	pool.SetMaxOpenConns(maxOpen)
	pool.SetMaxIdleConns(idle)
	pool.SetConnMaxLifetime(0)
	if err := pool.Ping(); err != nil {
		b.Fatalf("ping main DB: %v", err)
	}

	cfg := DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = filepath.Join(dir, "backups")
	fresh := &Database{mainDB: pool, dbconfig: cfg}
	if err := fresh.migrateMainDB(); err != nil {
		b.Fatalf("main migrations: %v", err)
	}

	var ngID int64
	userIDs := make([]int64, 0, lo2PoolUsers)
	tx, err := pool.Begin()
	if err != nil {
		b.Fatalf("begin seed tx: %v", err)
	}
	seedErr := func() error {
		res, err := tx.Exec(`INSERT INTO newsgroups (name, description, last_article, message_count, active) VALUES (?, '', 0, 0, 1)`,
			"lo2pool.bench")
		if err != nil {
			return fmt.Errorf("seed newsgroup: %w", err)
		}
		if ngID, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("newsgroup id: %w", err)
		}
		expires := time.Now().UTC().Add(time.Hour)
		for i := 0; i < lo2PoolUsers; i++ {
			name := fmt.Sprintf("lo2pool_u%d", i)
			res, err := tx.Exec(`INSERT INTO users
				(username, email, password_hash, display_name, session_id, session_expires_at,
				 login_attempts, verified, disabled, no_posting, post_count, lastpost_unix)
				VALUES (?, ?, 'x', ?, ?, ?, 0, 1, 0, 0, 0, 0)`,
				name, name+"@test.invalid", name, fmt.Sprintf("%064x", i), expires)
			if err != nil {
				return fmt.Errorf("seed user %d: %w", i, err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("user id %d: %w", i, err)
			}
			userIDs = append(userIDs, id)
		}
		published := time.Now().UTC().Add(-24 * time.Hour)
		for i := 0; i < lo2PoolNewsRows; i++ {
			visible := 0
			if i%2 == 0 {
				visible = 1
			}
			if _, err := tx.Exec(`INSERT INTO site_news (subject, content, date_published, is_visible)
				VALUES (?, ?, ?, ?)`,
				fmt.Sprintf("lo2pool news %d", i),
				fmt.Sprintf("body of site news entry %d, long enough to look like a real entry", i),
				published.Add(time.Duration(i)*time.Minute), visible); err != nil {
				return fmt.Errorf("seed site_news %d: %w", i, err)
			}
		}
		return nil
	}()
	if seedErr != nil {
		if rerr := tx.Rollback(); rerr != nil {
			b.Fatalf("%v; rollback: %v", seedErr, rerr)
		}
		b.Fatal(seedErr)
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit seed tx: %v", err)
	}
	return pool, userIDs, ngID
}

func lo2PoolSQLiteVersion(b *testing.B) string {
	b.Helper()
	pool, err := sql.Open(driverNameMain, filepath.Join(b.TempDir(), "lo2pool-version.sq3"))
	if err != nil {
		b.Fatalf("open version DB: %v", err)
	}
	defer func() { _ = pool.Close() }()
	var v string
	if err := pool.QueryRow("select sqlite_version()").Scan(&v); err != nil {
		b.Fatalf("sqlite_version: %v", err)
	}
	return v
}

// --- the benchmark -----------------------------------------------------------

func BenchmarkLo2PoolMix(b *testing.B) {
	b.Logf("sqlite_version=%s GOMAXPROCS=%d NumCPU=%d readers=%d writers=%d duration=%v",
		lo2PoolSQLiteVersion(b), runtime.GOMAXPROCS(0), runtime.NumCPU(),
		lo2PoolReaders, lo2PoolWriters, lo2PoolDuration())
	for _, n := range lo2PoolSizes {
		b.Run(fmt.Sprintf("MaxOpenConns%d", n), func(b *testing.B) {
			lo2PoolRunMix(b, n)
		})
	}
}

func lo2PoolRunMix(b *testing.B, maxOpen int) {
	d := lo2PoolDuration()
	if b.N > 1 {
		// One invocation already runs the mix for d, so the framework stops at
		// b.N == 1 whenever -benchtime is a duration <= d. Anything else would
		// report the last of several runs under a meaningless iteration count.
		b.Fatalf("Lo2Pool benchmarks run for a fixed duration of %v: pass -benchtime <duration <= %v> (or set LO2POOL_DURATION)", d, d)
	}
	pool, userIDs, ngID := lo2PoolOpen(b, maxOpen)

	var busy atomic.Int64
	var failures atomic.Int64
	userHists := make([]lo2PoolHist, lo2PoolReaders)
	newsHists := make([]lo2PoolHist, lo2PoolReaders)
	writeHists := make([]lo2PoolHist, lo2PoolWriters)

	var wg sync.WaitGroup
	b.ResetTimer()
	start := time.Now()
	deadline := start.Add(d)

	for i := 0; i < lo2PoolReaders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rnd := lo2PoolRand(0x9e3779b97f4a7c15 ^ uint64(i+1))
			uh, nh := &userHists[i], &newsHists[i]
			for n := 0; ; n++ {
				t0 := time.Now()
				if !t0.Before(deadline) {
					return
				}
				if n%lo2PoolNewsEvery == lo2PoolNewsEvery-1 {
					if err := lo2PoolReadNews(pool, &busy); err != nil {
						failures.Add(1)
					}
					nh.add(time.Since(t0))
					continue
				}
				if err := lo2PoolReadUser(pool, &busy, userIDs[rnd.intn(len(userIDs))]); err != nil {
					failures.Add(1)
				}
				uh.add(time.Since(t0))
			}
		}(i)
	}

	for j := 0; j < lo2PoolWriters; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			rnd := lo2PoolRand(0xda3e39cb94b95bdb ^ uint64(j+1))
			wh := &writeHists[j]
			seq := 0
			for {
				t0 := time.Now()
				if !t0.Before(deadline) {
					return
				}
				id := userIDs[rnd.intn(len(userIDs))]
				expires := time.Now().UTC().Add(time.Hour)
				if err := lo2PoolRetry(&busy, func() error {
					_, err := pool.Exec(lo2PoolSlideSession, expires, id)
					return err
				}); err != nil {
					failures.Add(1)
				}
				wh.add(time.Since(t0))

				t1 := time.Now()
				if !t1.Before(deadline) {
					return
				}
				seq++
				msgID := fmt.Sprintf("<lo2pool.%d.%d@test.invalid>", j, seq)
				if err := lo2PoolRetry(&busy, func() error {
					_, err := pool.Exec(lo2PoolInsertQueue, ngID, msgID)
					return err
				}); err != nil {
					failures.Add(1)
				}
				wh.add(time.Since(t1))
			}
		}(j)
	}

	wg.Wait()
	elapsed := time.Since(start)
	b.StopTimer()

	var userAll, newsAll, readAll, writeAll lo2PoolHist
	for i := range userHists {
		userAll.merge(&userHists[i])
		newsAll.merge(&newsHists[i])
	}
	for j := range writeHists {
		writeAll.merge(&writeHists[j])
	}
	readAll.merge(&userAll)
	readAll.merge(&newsAll)

	secs := elapsed.Seconds()
	reads, writes := readAll.count, writeAll.count

	b.ReportMetric(0, "ns/op")
	b.ReportMetric(float64(reads+writes)/secs, "ops/s")
	b.ReportMetric(float64(reads)/secs, "reads/s")
	b.ReportMetric(float64(writes)/secs, "writes/s")
	b.ReportMetric(lo2PoolUS(readAll.quantile(0.50)), "p50read_us")
	b.ReportMetric(lo2PoolUS(readAll.quantile(0.99)), "p99read_us")
	b.ReportMetric(lo2PoolUS(writeAll.quantile(0.50)), "p50write_us")
	b.ReportMetric(lo2PoolUS(writeAll.quantile(0.99)), "p99write_us")
	b.ReportMetric(float64(busy.Load()), "busy_retries")

	b.Logf("maxOpen=%d maxIdle=%d elapsed=%v reads=%d writes=%d busy=%d errors=%d",
		maxOpen, min(maxOpen, 25), elapsed.Round(time.Millisecond), reads, writes,
		busy.Load(), failures.Load())
	b.Logf("  user  read: mean=%v p50=%v p90=%v p99=%v p999=%v n=%d",
		userAll.mean().Round(time.Microsecond), userAll.quantile(0.50).Round(time.Microsecond),
		userAll.quantile(0.90).Round(time.Microsecond), userAll.quantile(0.99).Round(time.Microsecond),
		userAll.quantile(0.999).Round(time.Microsecond), userAll.count)
	b.Logf("  news  read: mean=%v p50=%v p90=%v p99=%v p999=%v n=%d",
		newsAll.mean().Round(time.Microsecond), newsAll.quantile(0.50).Round(time.Microsecond),
		newsAll.quantile(0.90).Round(time.Microsecond), newsAll.quantile(0.99).Round(time.Microsecond),
		newsAll.quantile(0.999).Round(time.Microsecond), newsAll.count)
	b.Logf("  write     : mean=%v p50=%v p90=%v p99=%v p999=%v n=%d",
		writeAll.mean().Round(time.Microsecond), writeAll.quantile(0.50).Round(time.Microsecond),
		writeAll.quantile(0.90).Round(time.Microsecond), writeAll.quantile(0.99).Round(time.Microsecond),
		writeAll.quantile(0.999).Round(time.Microsecond), writeAll.count)

	if failures.Load() > 0 {
		b.Fatalf("%d statement failures during the mix", failures.Load())
	}
}
