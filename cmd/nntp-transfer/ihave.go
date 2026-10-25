package main

// IHAVE fallback for peers without streaming (plan transfer-ihave-fallback, slice
// ihave-tool). The streaming path (CHTTWorker, CHECK/TAKETHIS, the demuxer) is
// untouched: this is a second, synchronous sender that consumes the same jobs.
//
// Mode decision (-ihave, made once per run):
//
//	never   today's behaviour: streaming only, Pool.Get(MODE_STREAM_MV) is retried
//	auto    streaming, but the first MODE STREAM refusal (nntp.ErrStreamingUnsupported)
//	        switches the whole run to IHAVE
//	always  IHAVE from the start (for peers whose TAKETHIS is broken)
//
// In IHAVE mode -check-first and -batch-check have no effect: every article is
// offered with a single IHAVE and there is no pipelining.

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/go-pugleaf/internal/processor"
)

// -ihave policy values.
const (
	ihavePolicyNever  int32 = 0
	ihavePolicyAuto   int32 = 1
	ihavePolicyAlways int32 = 2
)

var (
	// ihavePolicy is the parsed -ihave flag; set once in main() before the workers boot.
	ihavePolicy atomic.Int32
	// ihaveActive is true once the run sends with IHAVE instead of CHECK/TAKETHIS.
	ihaveActive atomic.Bool
	// ihaveRefused is set by IHaveWorker when the peer refuses IHAVE itself.
	ihaveRefused atomic.Bool
	// ihaveStopOnce keeps the "neither streaming nor IHAVE" stop to one log line.
	ihaveStopOnce sync.Once
	// transferExitCode is the process exit code main() uses after the run drained.
	transferExitCode atomic.Int32
)

// ihaveConnMode is the pool mode IHAVE connections are fetched with: no mode switch at
// all. Real innd hands a MODE READER connection to nnrpd, which has no IHAVE, and
// BackendConn.IHave refuses a reader-mode connection.
var ihaveConnMode = nntp.MODE_NONE_MV

// setIHavePolicy parses the -ihave flag value. "always" starts the run in IHAVE mode.
func setIHavePolicy(value string) error {
	switch value {
	case "never":
		ihavePolicy.Store(ihavePolicyNever)
		ihaveActive.Store(false)
	case "auto":
		ihavePolicy.Store(ihavePolicyAuto)
		ihaveActive.Store(false)
	case "always":
		ihavePolicy.Store(ihavePolicyAlways)
		ihaveActive.Store(true)
	default:
		return fmt.Errorf("invalid -ihave value '%s' (want: auto, never or always)", value)
	}
	return nil
}

// usingIHave reports whether this run sends articles with IHAVE.
func usingIHave() bool {
	return ihaveActive.Load()
}

// transferModeName names the send mode for the end-of-run summary.
func transferModeName() string {
	if usingIHave() {
		return "ihave"
	}
	return "streaming"
}

// noteStreamingUnsupported switches the run to IHAVE when -ihave auto meets a peer
// that refuses MODE STREAM. It logs the fallback once and reports whether the caller
// should retry the connection at once (in IHAVE mode) instead of backing off.
func noteStreamingUnsupported(peer string, err error) bool {
	if ihavePolicy.Load() != ihavePolicyAuto || !errors.Is(err, nntp.ErrStreamingUnsupported) {
		return false
	}
	if ihaveActive.CompareAndSwap(false, true) {
		log.Printf("[TRANSFER] peer %s has no streaming (MODE STREAM: %v): sending with IHAVE", peer, err)
	}
	return true
}

// noteIHaveRefused records that the peer refuses IHAVE as well (500/501/502 or 480).
func noteIHaveRefused() {
	ihaveRefused.Store(true)
}

// ihavePeerRefused reports whether a worker met a peer that refuses IHAVE.
func ihavePeerRefused() bool {
	return ihaveRefused.Load()
}

// stopRunNoIHave ends the run: a peer with neither streaming nor IHAVE is a
// configuration error, not something a reconnect fixes. The running jobs drain
// through the normal shutdown path and main() exits 1.
func stopRunNoIHave() {
	ihaveStopOnce.Do(func() {
		log.Printf("[TRANSFER] peer has neither streaming nor IHAVE (or refuses it): stopping")
		transferExitCode.Store(1)
		common.ForceShutdown()
	})
}

// transferFailed reports whether main() must exit non-zero.
func transferFailed() bool {
	return transferExitCode.Load() != 0
}

// transferGetConn gets one connection for the run's send mode. In IHAVE mode the
// connection is taken without MODE STREAM. With -ihave auto a peer that refuses
// MODE STREAM switches the run to IHAVE and fellBack is true: the caller retries at
// once (in IHAVE mode) instead of backing off.
func transferGetConn(pool *nntp.Pool, peer string) (conn *nntp.BackendConn, fellBack bool, err error) {
	if usingIHave() {
		conn, err = pool.Get(ihaveConnMode)
		return conn, false, err
	}
	conn, err = pool.Get(nntp.MODE_STREAM_MV)
	if err != nil && noteStreamingUnsupported(peer, err) {
		return nil, true, err
	}
	return conn, false, err
}

// ihaveSender offers one article to the peer and returns its reply code, the bytes
// written and an error. Production uses (*nntp.BackendConn).IHave; the worker tests
// pass their own to drive the bookkeeping without a peer.
type ihaveSender func(article *models.Article, newsgroup string) (code int, txBytes int, err error)

// IHaveWorker is the IHAVE counterpart of CHTTWorker: it consumes the same jobs from
// CheckQueues[workerID] and offers every article of a job with a single IHAVE. The
// producer side is unchanged: OffsetBatchDone() when the job is taken, Response()
// when it is done, and an unfinished job stays in rs.jobs so BootConnWorkers requeues
// it after the worker exits.
func IHaveWorker(db *database.Database, workerID int, conn *nntp.BackendConn, rs *ReturnSignal) {
	send := rs.ihaveSend
	if send == nil {
		send = func(article *models.Article, newsgroup string) (int, int, error) {
			return conn.IHave(article, &processor.LocalNNTPHostname, newsgroup)
		}
	}
	QueuesMutex.RLock()
	jobsChan := CheckQueues[workerID]
	QueuesMutex.RUnlock()

	defer func() {
		conn.ForceCloseConn()
		rs.ExitChan <- rs
	}()
	log.Printf("IHaveWorker (%d): started", workerID)

	for {
		if common.WantShutdown() {
			log.Printf("IHaveWorker (%d): WantShutdown, exiting", workerID)
			return
		}
		var job *nntp.CHTTJob
		var ok bool
		select {
		case job, ok = <-jobsChan:
			if !ok {
				// the queue is closed: no more work for this worker
				log.Printf("IHaveWorker (%d): job queue closed, exiting", workerID)
				return
			}
		case <-common.ShutdownChan:
			log.Printf("IHaveWorker (%d): shutdown, exiting", workerID)
			return
		}
		if job == nil {
			log.Printf("IHaveWorker (%d): got nil job, skipping", workerID)
			continue
		}
		if job.OffsetQ != nil {
			job.OffsetQ.OffsetBatchDone()
		}
		if len(job.MessageIDs) == 0 {
			log.Printf("IHaveWorker (%d): job #%d has no message ids, skipping", workerID, job.JobID)
			job.Response(true, fmt.Errorf("got job without message ids"))
			continue
		}
		newsgroup := *job.Newsgroup
		rs.Mux.Lock()
		rs.jobs = append(rs.jobs, job)
		rs.Mux.Unlock()

		stop, err := ihaveJob(db, workerID, conn, rs, send, job, newsgroup)
		if stop {
			// the job stays in rs.jobs: BootConnWorkers requeues it when the worker exits
			log.Printf("Newsgroup: '%s' | IHaveWorker (%d): job #%d not finished, requeueing and exiting: %v", newsgroup, workerID, job.JobID, err)
			return
		}
		rs.Mux.Lock()
		for i, queued := range rs.jobs {
			if queued == job {
				rs.jobs = append(rs.jobs[:i], rs.jobs[i+1:]...)
				break
			}
		}
		rs.Mux.Unlock()
		if err != nil {
			job.Response(false, err)
		} else {
			job.Response(true, nil)
		}
		WorkerQueueLengthMux.Lock()
		if workerID < len(WorkerQueueLength) {
			WorkerQueueLength[workerID]++
		}
		WorkerQueueLengthMux.Unlock()
	}
}

// ihaveJob offers every article of one job. stop is true when the worker must requeue
// the job and exit (the peer refuses IHAVE, authentication is required, the connection
// broke or the tool is shutting down); with stop false a non-nil error is the job's
// Response error (a database error: the job is not retried).
func ihaveJob(db *database.Database, workerID int, conn *nntp.BackendConn, rs *ReturnSignal, send ihaveSender, job *nntp.CHTTJob, newsgroup string) (bool, error) {
	articles, err := db.GetArticlesByIDs(job.Newsgroup, job.MessageIDs)
	if err != nil {
		log.Printf("Newsgroup: '%s' | IHaveWorker (%d): error fetching articles for job #%d: %v", newsgroup, workerID, job.JobID, err)
		return false, err
	}
	if len(articles) == 0 {
		log.Printf("Newsgroup: '%s' | IHaveWorker (%d): no articles found for job #%d", newsgroup, workerID, job.JobID)
		return false, nil
	}
	// 436 (deferred by the peer): retried once at the end of the job, then counted as retry.
	var deferred []*models.Article
	for _, article := range articles {
		if article == nil {
			continue
		}
		if common.WantShutdown() {
			return true, fmt.Errorf("shutdown while sending job #%d", job.JobID)
		}
		stop, defer436, err := ihaveArticle(conn, rs, send, job, newsgroup, article)
		if stop {
			return true, err
		}
		if defer436 {
			deferred = append(deferred, article)
		}
	}
	for _, article := range deferred {
		if common.WantShutdown() {
			return true, fmt.Errorf("shutdown while retrying job #%d", job.JobID)
		}
		stop, defer436, err := ihaveArticle(conn, rs, send, job, newsgroup, article)
		if stop {
			return true, err
		}
		if defer436 {
			job.NGTProgress.Increment(nntp.IncrFLAG_RETRY, 1)
			if VERBOSE {
				log.Printf("Newsgroup: '%s' | IHaveWorker (%d): peer deferred '%s' twice (436), retry later", newsgroup, workerID, article.MessageID)
			}
		}
	}
	return false, nil
}

// ihaveArticle offers one article with IHAVE and books the result on the job's
// progress. defer436 is true when the peer answered 436 (transfer later).
func ihaveArticle(conn *nntp.BackendConn, rs *ReturnSignal, send ihaveSender, job *nntp.CHTTJob, newsgroup string, article *models.Article) (stop bool, defer436 bool, err error) {
	if !conn.IsConnected() {
		job.NGTProgress.Increment(nntp.IncrFLAG_CONN_ERRORS, 1)
		return true, false, fmt.Errorf("connection lost before IHAVE '%s'", article.MessageID)
	}
	code, txBytes, err := send(article, newsgroup)
	job.NGTProgress.Increment(nntp.IncrFLAG_TTSentCount, 1)
	if txBytes > 0 {
		job.Mux.Lock()
		job.TTxBytes += uint64(txBytes)
		job.Mux.Unlock()
		job.NGTProgress.AddNGTP(0, 1, uint64(txBytes))
	} else {
		job.NGTProgress.AddNGTP(0, 1, 0)
	}
	if err != nil {
		if errors.Is(err, nntp.ErrIHaveUnsupported) || errors.Is(err, nntp.ErrAuthRequired) {
			noteIHaveRefused()
			job.NGTProgress.Increment(nntp.IncrFLAG_TX_ERRORS, 1)
			return true, false, fmt.Errorf("peer refuses IHAVE '%s': %w", article.MessageID, err)
		}
		job.NGTProgress.Increment(nntp.IncrFLAG_CONN_ERRORS, 1)
		return true, false, fmt.Errorf("IHAVE '%s': %w", article.MessageID, err)
	}
	switch code {
	case 235: // accepted
		job.NGTProgress.Increment(nntp.IncrFLAG_TRANSFERRED, 1)
		ihaveMarkRedis(rs, newsgroup, article.MessageID)

	case 435: // not wanted
		job.NGTProgress.Increment(nntp.IncrFLAG_UNWANTED, 1)
		ihaveMarkRedis(rs, newsgroup, article.MessageID)

	case 437: // rejected, never retried
		job.NGTProgress.Increment(nntp.IncrFLAG_REJECTED, 1)
		ihaveMarkRedis(rs, newsgroup, article.MessageID)
		if VERBOSE {
			log.Printf("Newsgroup: '%s' | IHAVE rejected article '%s': response=%d", newsgroup, article.MessageID, code)
		}

	case 436: // transfer failed, try again later
		return false, true, nil

	default:
		log.Printf("ERROR Newsgroup: '%s' | IHAVE '%s': unknown response=%d", newsgroup, article.MessageID, code)
		job.NGTProgress.Increment(nntp.IncrFLAG_TX_ERRORS, 1)
	}
	return false, false, nil
}

// ihaveMarkRedis caches a finished message-id like the TAKETHIS path does.
func ihaveMarkRedis(rs *ReturnSignal, newsgroup string, messageID string) {
	if rs == nil || rs.redisCli == nil {
		return
	}
	if err := rs.redisCli.Set(redisCtx, redisKey(messageID), "1", REDIS_TTL).Err(); err != nil && VERBOSE {
		log.Printf("Newsgroup: '%s' | Failed to cache message ID in Redis: %v", newsgroup, err)
	}
}
