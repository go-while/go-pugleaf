package main

// Tests of the transfer-ihave-fallback slice ihave-tool: the -ihave mode decision
// (never / auto / always) against a kit peer without streaming, and IHaveWorker's
// bookkeeping (counters, the single 436 retry, 437 never retried, the requeue when
// the peer refuses IHAVE).
//
// Databases live below t.TempDir(); ./data is never used. There is no wall-clock
// assertion: txIHaveWait (PUGLEAF_TEST_DEADLINE) is only a hang guard.

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/nntp"
	"github.com/go-while/nntp-testkit/fakeserver"
)

// txIHaveWait is the hang guard of these tests: it only stops a hung test, a passing
// test never waits on it. Override with PUGLEAF_TEST_DEADLINE (a Go duration, e.g.
// 120s); default 60 s.
var txIHaveWait = txIHaveWaitFromEnv(60 * time.Second)

func txIHaveWaitFromEnv(def time.Duration) time.Duration {
	if v := os.Getenv("PUGLEAF_TEST_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// txLogBuf collects the package's log output while a test runs.
type txLogBuf struct {
	mux sync.Mutex
	buf bytes.Buffer
}

func (l *txLogBuf) Write(p []byte) (int, error) {
	l.mux.Lock()
	defer l.mux.Unlock()
	return l.buf.Write(p)
}

func (l *txLogBuf) String() string {
	l.mux.Lock()
	defer l.mux.Unlock()
	return l.buf.String()
}

// txIHaveCaptureLog redirects the standard logger into a buffer for this test.
func txIHaveCaptureLog(t *testing.T) *txLogBuf {
	t.Helper()
	buf := &txLogBuf{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

// txIHaveMode sets the -ihave policy for this test and restores the default after it.
func txIHaveMode(t *testing.T, value string) {
	t.Helper()
	if err := setIHavePolicy(value); err != nil {
		t.Fatalf("setIHavePolicy(%q): %v", value, err)
	}
	ihaveRefused.Store(false)
	t.Cleanup(func() {
		ihaveRefused.Store(false)
		if err := setIHavePolicy("auto"); err != nil {
			t.Errorf("setIHavePolicy(auto): %v", err)
		}
	})
}

// txIHavePeer starts a kit transit peer and returns it.
func txIHavePeer(t *testing.T, o *fakeserver.PeerOptions) *fakeserver.Server {
	t.Helper()
	return fakeserver.New(t, fakeserver.Options{
		Handler:  fakeserver.PeerHandler(o),
		Deadline: txIHaveWait,
	})
}

// txIHavePool returns a connection pool for the peer, closed at the end of the test.
func txIHavePool(t *testing.T, s *fakeserver.Server) *nntp.Pool {
	t.Helper()
	pool := nntp.NewPool(&nntp.BackendConfig{
		Host:           s.Host(),
		Port:           s.Port(),
		MaxConns:       2,
		ConnectTimeout: 30 * time.Second,
		Provider:       &config.Provider{Name: "tx-ihave-peer", Host: s.Host(), Port: s.Port()},
	})
	t.Cleanup(func() {
		if err := pool.ClosePool(); err != nil {
			t.Errorf("ClosePool: %v", err)
		}
	})
	return pool
}

// txIHaveOpenDB opens a scratch main database below t.TempDir().
func txIHaveOpenDB(t *testing.T) *database.Database {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() < 1000 {
		t.Skip("OpenDatabase refuses to run with euid < 1000")
	}
	dir := t.TempDir()
	database.NO_CACHE_BOOT = true
	cfg := database.DefaultDBConfig()
	cfg.DataDir = dir
	cfg.BackupDir = dir + "/backups"
	db, err := database.OpenDatabase(cfg)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() {
		close(db.StopChan)
		db.WG.Wait()
		if err := db.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return db
}

// txIHaveSeed creates the newsgroup and n articles in it and returns their message-ids.
func txIHaveSeed(t *testing.T, db *database.Database, group string, n int) []*string {
	t.Helper()
	if err := db.InsertNewsgroup(&models.Newsgroup{Name: group, Active: true}); err != nil {
		t.Fatalf("InsertNewsgroup %s: %v", group, err)
	}
	gdb, err := db.GetGroupDB(group)
	if err != nil {
		t.Fatalf("GetGroupDB %s: %v", group, err)
	}
	defer gdb.Return()
	ids := make([]*string, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("<tx-ihave.%d@tx-ihave.invalid>", i)
		if _, err := gdb.DB.Exec(
			`INSERT INTO articles (article_num, message_id, subject, from_header, date_sent, date_string, "references", bytes, lines, path, headers_json, body_text)
			 VALUES (?, ?, 'tx-ihave', 'tester@tx-ihave.invalid', ?, ?, '', 10, 1, 'not-for-mail', '', 'body')`,
			i, id, "2024-01-01 00:00:0"+fmt.Sprint(i%10), "Mon, 01 Jan 2024 00:00:00 +0000",
		); err != nil {
			t.Fatalf("insert article %d: %v", i, err)
		}
		msgID := id
		ids = append(ids, &msgID)
	}
	return ids
}

// txIHaveQueues installs the per-worker job queues BootConnWorkers builds.
func txIHaveQueues(t *testing.T, workers int) {
	t.Helper()
	QueuesMutex.Lock()
	CheckQueues = make([]chan *nntp.CHTTJob, workers)
	TakeThisQueues = make([]chan *nntp.CHTTJob, workers)
	WorkerQueueLength = make([]int, workers)
	for i := range CheckQueues {
		CheckQueues[i] = make(chan *nntp.CHTTJob)
		TakeThisQueues[i] = make(chan *nntp.CHTTJob, MaxQueuedJobs)
	}
	QueuesMutex.Unlock()
	t.Cleanup(func() {
		QueuesMutex.Lock()
		CheckQueues = nil
		TakeThisQueues = nil
		WorkerQueueLength = nil
		QueuesMutex.Unlock()
	})
}

// txIHaveJob builds a job for the ids, as processBatch hands it to a worker.
func txIHaveJob(group *string, ids []*string, progress *nntp.NewsgroupTransferProgress, offsetQ *nntp.OffsetQueue) *nntp.CHTTJob {
	offsetQ.Add(1)
	return &nntp.CHTTJob{
		JobID:        1,
		Newsgroup:    group,
		MessageIDs:   ids,
		ResponseChan: nntp.GetTTResponseChan(),
		OffsetQ:      offsetQ,
		NGTProgress:  progress,
	}
}

// txIHaveStartWorker starts IHaveWorker with send in place of the client's IHave.
func txIHaveStartWorker(t *testing.T, db *database.Database, conn *nntp.BackendConn, send ihaveSender) *ReturnSignal {
	t.Helper()
	rs := &ReturnSignal{
		slotID:     0,
		ExitChan:   make(chan *ReturnSignal, 1),
		jobsQueued: make(map[*nntp.CHTTJob]uint64),
		jobsReadOK: make(map[*nntp.CHTTJob]uint64),
		jobMap:     make(map[*string]*nntp.CHTTJob),
		jobs:       make([]*nntp.CHTTJob, 0, MaxQueuedJobs),
		ihaveSend:  send,
	}
	go IHaveWorker(db, 0, conn, rs)
	return rs
}

// txIHaveSendJob queues the job and waits for the worker's response.
func txIHaveSendJob(t *testing.T, job *nntp.CHTTJob) *nntp.TTResponse {
	t.Helper()
	QueuesMutex.RLock()
	jobsChan := CheckQueues[0]
	QueuesMutex.RUnlock()
	select {
	case jobsChan <- job:
	case <-time.After(txIHaveWait):
		t.Fatalf("worker did not take job #%d", job.JobID)
	}
	select {
	case resp := <-job.ResponseChan:
		return resp
	case <-time.After(txIHaveWait):
		t.Fatalf("no response for job #%d", job.JobID)
	}
	return nil
}

// txIHaveStopWorker closes the job queue and waits for the worker to exit.
func txIHaveStopWorker(t *testing.T, rs *ReturnSignal) {
	t.Helper()
	QueuesMutex.RLock()
	jobsChan := CheckQueues[0]
	QueuesMutex.RUnlock()
	close(jobsChan)
	select {
	case <-rs.ExitChan:
	case <-time.After(txIHaveWait):
		t.Fatal("IHaveWorker did not exit after its queue was closed")
	}
}

// txIHaveProgress returns a progress counter and an offset queue for one newsgroup.
func txIHaveProgress(group *string) (*nntp.NewsgroupTransferProgress, *nntp.OffsetQueue) {
	return &nntp.NewsgroupTransferProgress{Newsgroup: group, Started: time.Now(), LastCronTX: time.Now()},
		&nntp.OffsetQueue{Newsgroup: group, MaxQueuedJobs: MaxQueuedJobs}
}

// TestTxIHavePolicyValues: -ihave takes auto, never and always; anything else is an
// error and only "always" starts the run in IHAVE mode.
func TestTxIHavePolicyValues(t *testing.T) {
	txIHaveMode(t, "auto")
	if usingIHave() || transferModeName() != "streaming" {
		t.Fatalf("auto: usingIHave=%v mode=%s, want streaming", usingIHave(), transferModeName())
	}
	txIHaveMode(t, "never")
	if usingIHave() {
		t.Fatal("never: run started in IHAVE mode")
	}
	txIHaveMode(t, "always")
	if !usingIHave() || transferModeName() != "ihave" {
		t.Fatalf("always: usingIHave=%v mode=%s, want ihave", usingIHave(), transferModeName())
	}
	if err := setIHavePolicy("yes"); err == nil {
		t.Fatal("setIHavePolicy(\"yes\") = nil, want an error")
	}
}

// TestTxIHaveModeNever: -ihave never keeps today's behaviour against a peer without
// streaming: the Get fails with ErrStreamingUnsupported, the run stays in streaming
// mode (BootConnWorkers retries) and the peer never sees IHAVE.
func TestTxIHaveModeNever(t *testing.T) {
	txIHaveMode(t, "never")
	s := txIHavePeer(t, &fakeserver.PeerOptions{NoStream: true})
	pool := txIHavePool(t, s)

	for i := 0; i < 2; i++ {
		conn, fellBack, err := transferGetConn(pool, s.Host())
		if err == nil {
			conn.ForceCloseConn()
			t.Fatal("transferGetConn succeeded against a peer without streaming")
		}
		if fellBack {
			t.Fatal("-ihave never fell back to IHAVE")
		}
		if !errors.Is(err, nntp.ErrStreamingUnsupported) {
			t.Fatalf("error = %v, want ErrStreamingUnsupported", err)
		}
	}
	if usingIHave() {
		t.Fatal("-ihave never switched the run to IHAVE")
	}
	if n := s.Count("IHAVE"); n != 0 {
		t.Fatalf("peer saw %d IHAVE commands, want 0; lines: %q", n, s.Lines())
	}
	if n := s.Count("MODE"); n != 2 {
		t.Fatalf("peer saw %d MODE commands, want 2; lines: %q", n, s.Lines())
	}
}

// TestTxIHaveModeAuto: -ihave auto switches the run to IHAVE on the first MODE STREAM
// refusal, logs it once and takes the next connection without MODE STREAM.
func TestTxIHaveModeAuto(t *testing.T) {
	txIHaveMode(t, "auto")
	logs := txIHaveCaptureLog(t)
	s := txIHavePeer(t, &fakeserver.PeerOptions{NoStream: true})
	pool := txIHavePool(t, s)

	conn, fellBack, err := transferGetConn(pool, s.Host())
	if err == nil {
		conn.ForceCloseConn()
		t.Fatal("first transferGetConn succeeded against a peer without streaming")
	}
	if !fellBack {
		t.Fatalf("first transferGetConn: fellBack=false, err=%v", err)
	}
	if !usingIHave() || transferModeName() != "ihave" {
		t.Fatalf("after the fallback: usingIHave=%v mode=%s", usingIHave(), transferModeName())
	}
	conn, fellBack, err = transferGetConn(pool, s.Host())
	if err != nil {
		t.Fatalf("second transferGetConn: %v", err)
	}
	if fellBack {
		t.Fatal("second transferGetConn reported a fallback again")
	}
	conn.ForceCloseConn()

	if n := strings.Count(logs.String(), "sending with IHAVE"); n != 1 {
		t.Fatalf("fallback logged %d times, want once:\n%s", n, logs.String())
	}
	modeStream := 0
	for _, line := range s.Lines() {
		if strings.EqualFold(strings.TrimSpace(line), "MODE STREAM") {
			modeStream++
		}
	}
	if modeStream != 1 {
		t.Fatalf("peer saw %d MODE STREAM commands, want 1; lines: %q", modeStream, s.Lines())
	}
}

// TestTxIHaveModeAlways: -ihave always never sends MODE STREAM, not even to a peer
// that offers streaming.
func TestTxIHaveModeAlways(t *testing.T) {
	txIHaveMode(t, "always")
	s := txIHavePeer(t, &fakeserver.PeerOptions{})
	pool := txIHavePool(t, s)

	conn, fellBack, err := transferGetConn(pool, s.Host())
	if err != nil {
		t.Fatalf("transferGetConn: %v", err)
	}
	if fellBack {
		t.Fatal("-ihave always reported a streaming fallback")
	}
	conn.ForceCloseConn()
	for _, line := range s.Lines() {
		if strings.EqualFold(strings.TrimSpace(line), "MODE STREAM") {
			t.Fatalf("-ihave always sent MODE STREAM; lines: %q", s.Lines())
		}
	}
	if n := s.Count("CHECK") + s.Count("TAKETHIS"); n != 0 {
		t.Fatalf("peer saw %d CHECK/TAKETHIS commands in IHAVE mode; lines: %q", n, s.Lines())
	}
}

// TestTxIHaveWorkerCounters: every article of a job is offered once; 235, 435 and 437
// land on the transferred, unwanted and rejected counters, every successful offer is
// booked once (TTSentCount and the bytes, as the TAKETHIS path books them), the job is
// answered and the worker runs without Redis (rs.redisCli == nil).
func TestTxIHaveWorkerCounters(t *testing.T) {
	txIHaveMode(t, "always")
	txIHaveQueues(t, 1)
	db := txIHaveOpenDB(t)
	group := "tx.ihave.counters"
	ids := txIHaveSeed(t, db, group, 3)
	s := txIHavePeer(t, &fakeserver.PeerOptions{NoStream: true})
	pool := txIHavePool(t, s)
	conn, _, err := transferGetConn(pool, s.Host())
	if err != nil {
		t.Fatalf("transferGetConn: %v", err)
	}

	var mux sync.Mutex
	offered := []string{}
	codes := map[string]int{*ids[0]: 235, *ids[1]: 435, *ids[2]: 437}
	send := func(article *models.Article, newsgroup string) (int, int, error) {
		mux.Lock()
		defer mux.Unlock()
		if newsgroup != group {
			t.Errorf("IHave newsgroup = %q, want %q", newsgroup, group)
		}
		offered = append(offered, article.MessageID)
		return codes[article.MessageID], 100, nil
	}
	rs := txIHaveStartWorker(t, db, conn, send)
	progress, offsetQ := txIHaveProgress(&group)
	job := txIHaveJob(&group, ids, progress, offsetQ)
	resp := txIHaveSendJob(t, job)
	if resp == nil || resp.Err != nil {
		t.Fatalf("job response: %+v", resp)
	}
	txIHaveStopWorker(t, rs)

	mux.Lock()
	got := append([]string(nil), offered...)
	mux.Unlock()
	if len(got) != 3 {
		t.Fatalf("offered %q, want all 3 articles once", got)
	}
	progress.Mux.RLock()
	defer progress.Mux.RUnlock()
	if progress.Transferred != 1 || progress.Unwanted != 1 || progress.Rejected != 1 {
		t.Errorf("transferred=%d unwanted=%d rejected=%d, want 1/1/1", progress.Transferred, progress.Unwanted, progress.Rejected)
	}
	if progress.Retry != 0 || progress.TxErrors != 0 || progress.ConnErrors != 0 {
		t.Errorf("retry=%d txErrors=%d connErrors=%d, want 0/0/0", progress.Retry, progress.TxErrors, progress.ConnErrors)
	}
	if progress.TTSentCount != 3 {
		t.Errorf("TTSentCount = %d, want 3 (one IHAVE per article)", progress.TTSentCount)
	}
	if progress.TXBytes != 300 {
		t.Errorf("TXBytes = %d, want 300", progress.TXBytes)
	}
	if n := len(rs.jobs); n != 0 {
		t.Errorf("rs.jobs = %d after a finished job, want 0 (nothing to requeue)", n)
	}
}

// TestTxIHaveWorker436Retry: a 436 is retried once at the end of the job; a second 436
// counts as retry, 437 is never retried.
func TestTxIHaveWorker436Retry(t *testing.T) {
	txIHaveMode(t, "always")
	txIHaveQueues(t, 1)
	db := txIHaveOpenDB(t)
	group := "tx.ihave.defer"
	ids := txIHaveSeed(t, db, group, 3)
	s := txIHavePeer(t, &fakeserver.PeerOptions{NoStream: true})
	pool := txIHavePool(t, s)
	conn, _, err := transferGetConn(pool, s.Host())
	if err != nil {
		t.Fatalf("transferGetConn: %v", err)
	}

	var mux sync.Mutex
	tries := map[string]int{}
	send := func(article *models.Article, newsgroup string) (int, int, error) {
		mux.Lock()
		defer mux.Unlock()
		tries[article.MessageID]++
		switch article.MessageID {
		case *ids[0]: // deferred once, accepted on the retry
			if tries[article.MessageID] == 1 {
				return 436, 0, nil
			}
			return 235, 50, nil
		case *ids[1]: // deferred every time
			return 436, 0, nil
		default: // rejected, never retried
			return 437, 50, nil
		}
	}
	rs := txIHaveStartWorker(t, db, conn, send)
	progress, offsetQ := txIHaveProgress(&group)
	job := txIHaveJob(&group, ids, progress, offsetQ)
	if resp := txIHaveSendJob(t, job); resp == nil || resp.Err != nil {
		t.Fatalf("job response: %+v", resp)
	}
	txIHaveStopWorker(t, rs)

	mux.Lock()
	defer mux.Unlock()
	if tries[*ids[0]] != 2 {
		t.Errorf("deferred-once article offered %d times, want 2", tries[*ids[0]])
	}
	if tries[*ids[1]] != 2 {
		t.Errorf("always-deferred article offered %d times, want 2 (one retry only)", tries[*ids[1]])
	}
	if tries[*ids[2]] != 1 {
		t.Errorf("rejected article offered %d times, want 1", tries[*ids[2]])
	}
	progress.Mux.RLock()
	defer progress.Mux.RUnlock()
	if progress.Transferred != 1 || progress.Rejected != 1 || progress.Retry != 1 {
		t.Errorf("transferred=%d rejected=%d retry=%d, want 1/1/1", progress.Transferred, progress.Rejected, progress.Retry)
	}
	// Only the first offer of an article is booked: the three first offers, not the
	// two of the 436 retry pass (the deferred articles are already counted).
	if progress.TTSentCount != 3 {
		t.Errorf("TTSentCount = %d, want 3 (the 436 retry pass is not counted again)", progress.TTSentCount)
	}
	if progress.TXBytes != 50 {
		t.Errorf("TXBytes = %d, want 50 (only the first offers carry bytes here)", progress.TXBytes)
	}
}

// TestTxIHaveWorkerUnsupported: a peer that refuses IHAVE stops the worker; the job
// stays in rs.jobs (BootConnWorkers requeues it) and the run is marked as stopping.
func TestTxIHaveWorkerUnsupported(t *testing.T) {
	txIHaveMode(t, "always")
	txIHaveQueues(t, 1)
	db := txIHaveOpenDB(t)
	group := "tx.ihave.unsupported"
	ids := txIHaveSeed(t, db, group, 3)
	s := txIHavePeer(t, &fakeserver.PeerOptions{NoStream: true})
	pool := txIHavePool(t, s)
	conn, _, err := transferGetConn(pool, s.Host())
	if err != nil {
		t.Fatalf("transferGetConn: %v", err)
	}

	var mux sync.Mutex
	offered := 0
	send := func(article *models.Article, newsgroup string) (int, int, error) {
		mux.Lock()
		defer mux.Unlock()
		offered++
		return 0, 0, fmt.Errorf("IHAVE: %w: 500 unknown command", nntp.ErrIHaveUnsupported)
	}
	rs := txIHaveStartWorker(t, db, conn, send)
	progress, offsetQ := txIHaveProgress(&group)
	job := txIHaveJob(&group, ids, progress, offsetQ)

	QueuesMutex.RLock()
	jobsChan := CheckQueues[0]
	QueuesMutex.RUnlock()
	select {
	case jobsChan <- job:
	case <-time.After(txIHaveWait):
		t.Fatal("worker did not take the job")
	}
	select {
	case <-rs.ExitChan:
	case <-time.After(txIHaveWait):
		t.Fatal("IHaveWorker did not exit after the peer refused IHAVE")
	}

	mux.Lock()
	defer mux.Unlock()
	if offered != 1 {
		t.Errorf("offered %d articles, want 1 (the worker stops on the first refusal)", offered)
	}
	if !ihavePeerRefused() {
		t.Error("ihavePeerRefused() = false, want true: BootConnWorkers must stop the run")
	}
	rs.Mux.Lock()
	queued := len(rs.jobs)
	rs.Mux.Unlock()
	if queued != 1 {
		t.Errorf("rs.jobs = %d, want the unfinished job left for the requeue", queued)
	}
	progress.Mux.RLock()
	defer progress.Mux.RUnlock()
	if progress.TxErrors != 1 {
		t.Errorf("txErrors = %d, want 1", progress.TxErrors)
	}
	if progress.Transferred != 0 {
		t.Errorf("transferred = %d, want 0", progress.Transferred)
	}
	if progress.TTSentCount != 0 {
		t.Errorf("TTSentCount = %d, want 0: a failed offer is not a sent article", progress.TTSentCount)
	}
}

// TestTxIHaveWorkerConnError: a connection error mid-job stops the worker and leaves
// the job for the requeue, without marking the peer as refusing IHAVE.
func TestTxIHaveWorkerConnError(t *testing.T) {
	txIHaveMode(t, "always")
	txIHaveQueues(t, 1)
	db := txIHaveOpenDB(t)
	group := "tx.ihave.connerror"
	ids := txIHaveSeed(t, db, group, 3)
	s := txIHavePeer(t, &fakeserver.PeerOptions{NoStream: true})
	pool := txIHavePool(t, s)
	conn, _, err := transferGetConn(pool, s.Host())
	if err != nil {
		t.Fatalf("transferGetConn: %v", err)
	}

	send := func(article *models.Article, newsgroup string) (int, int, error) {
		return 0, 0, errors.New("write: broken pipe")
	}
	rs := txIHaveStartWorker(t, db, conn, send)
	progress, offsetQ := txIHaveProgress(&group)
	job := txIHaveJob(&group, ids, progress, offsetQ)

	QueuesMutex.RLock()
	jobsChan := CheckQueues[0]
	QueuesMutex.RUnlock()
	select {
	case jobsChan <- job:
	case <-time.After(txIHaveWait):
		t.Fatal("worker did not take the job")
	}
	select {
	case <-rs.ExitChan:
	case <-time.After(txIHaveWait):
		t.Fatal("IHaveWorker did not exit after a connection error")
	}
	if ihavePeerRefused() {
		t.Error("a connection error marked the peer as refusing IHAVE")
	}
	rs.Mux.Lock()
	queued := len(rs.jobs)
	rs.Mux.Unlock()
	if queued != 1 {
		t.Errorf("rs.jobs = %d, want the unfinished job left for the requeue", queued)
	}
	progress.Mux.RLock()
	defer progress.Mux.RUnlock()
	if progress.ConnErrors != 1 {
		t.Errorf("connErrors = %d, want 1", progress.ConnErrors)
	}
	if progress.TTSentCount != 0 {
		t.Errorf("TTSentCount = %d, want 0: a failed offer is not a sent article", progress.TTSentCount)
	}
}

// TestTxIHaveWorkerPeer drives IHaveWorker through the real client command against a
// kit peer without streaming: every article of the job ends up in the peer's Have.
func TestTxIHaveWorkerPeer(t *testing.T) {
	txIHaveMode(t, "always")
	txIHaveQueues(t, 1)
	db := txIHaveOpenDB(t)
	group := "tx.ihave.peer"
	ids := txIHaveSeed(t, db, group, 3)

	var mux sync.Mutex
	var got []string
	s := txIHavePeer(t, &fakeserver.PeerOptions{
		NoStream: true,
		OnArticle: func(msgID string, wire []string) {
			mux.Lock()
			got = append(got, msgID)
			mux.Unlock()
		},
	})
	pool := txIHavePool(t, s)
	conn, _, err := transferGetConn(pool, s.Host())
	if err != nil {
		t.Fatalf("transferGetConn: %v", err)
	}
	rs := txIHaveStartWorker(t, db, conn, nil) // nil: the real conn.IHave
	progress, offsetQ := txIHaveProgress(&group)
	job := txIHaveJob(&group, ids, progress, offsetQ)
	if resp := txIHaveSendJob(t, job); resp == nil || resp.Err != nil {
		t.Fatalf("job response: %+v", resp)
	}
	txIHaveStopWorker(t, rs)

	mux.Lock()
	defer mux.Unlock()
	if len(got) != len(ids) {
		t.Fatalf("peer took %q, want all %d articles", got, len(ids))
	}
	if n := s.Count("IHAVE"); n != len(ids) {
		t.Errorf("peer saw %d IHAVE commands, want %d; lines: %q", n, len(ids), s.Lines())
	}
	progress.Mux.RLock()
	defer progress.Mux.RUnlock()
	if progress.Transferred != uint64(len(ids)) {
		t.Errorf("transferred = %d, want %d", progress.Transferred, len(ids))
	}
}
