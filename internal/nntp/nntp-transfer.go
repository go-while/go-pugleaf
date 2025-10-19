package nntp

import (
	"log"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
)

var NNTPTransferThreads int = 1

var JobIDCounter uint64 // Atomic counter for unique job IDs

var ReturnDelay = time.Millisecond * 16

// ResponseType indicates which handler should process a response
type ResponseType int

const (
	TYPE_CHECK ResponseType = iota
	TYPE_TAKETHIS
)

// Pool of ResponseData structs to reduce allocations
var ResponseDataPool = make(chan *ResponseData, 1024*1024)

// GetResponseData returns a recycled ResponseData struct or makes a new one if none are available
func GetResponseData(cmdID uint, code int, line string, err error) *ResponseData {
	select {
	case rd := <-ResponseDataPool:
		rd.CmdID = cmdID
		rd.Code = code
		rd.Line = line
		rd.Err = err
		return rd
	default:
		return &ResponseData{
			CmdID: cmdID,
			Code:  code,
			Line:  line,
			Err:   err,
		}
	}
}

// RecycleResponseData resets a ResponseData struct and recycles it back into the pool
func RecycleResponseData(rd *ResponseData) {
	rd.CmdID = 0
	rd.Code = 0
	rd.Line = ""
	rd.Err = nil
	select {
	case ResponseDataPool <- rd:
	default:
		// pool is full, discard
	}
}

// ResponseData holds a read response from the connection
type ResponseData struct {
	CmdID uint
	Code  int
	Line  string
	Err   error
}

// CmdIDinfo holds information about a command sent to the remote server
type CmdIDinfo struct {
	CmdID    uint
	RespType ResponseType
}

// used in nntp-transfer/main.go
type TakeThisMode struct {
	mux             sync.Mutex
	Newsgroup       *string
	TmpSuccessCount uint64
	TmpTTotalsCount uint64
	CheckMode       bool
}

// TTSetup holds the response channel for a batched TAKETHIS job
type TTSetup struct {
	ResponseChan chan *TTResponse
}

// Pool of TTSetup structs to reduce allocations
var TTSetupPool = make(chan *TTSetup, 1024*1024)

// GetTTSetup returns a recycled TTSetup struct or makes a new one if none are available
// the responseChan parameter is received from processBatch() and is mandatory and will be set on the returned struct
func GetTTSetup(responseChan chan *TTResponse) *TTSetup {
	select {
	case ch := <-TTSetupPool:
		ch.ResponseChan = responseChan
		return ch
	default:
		return &TTSetup{
			ResponseChan: responseChan,
		}
	}
}

// RecycleTTSetup recycles a TTSetup struct back into the pool
func RecycleTTSetup(tts *TTSetup) {
	tts.ResponseChan = nil
	select {
	case TTSetupPool <- tts:
	default:
		// pool is full, discard
	}
}

// OffsetQueue manages the number of concurrent batches being processed for a newsgroup
type OffsetQueue struct {
	Newsgroup     *string
	MaxQueuedJobs int
	mux           sync.RWMutex
	queued        int
	waiter        []chan struct{}
}

// Wait waits until the number of queued batches is less than n
func (o *OffsetQueue) Wait(n int) {
	start := time.Now()
	lastPrint := start
	setWaiting := false
	waitChan := common.GetStructChanCap1()
	defer common.RecycleStructChanCap1(waitChan)
	for {
		if common.WantShutdown() {
			return
		}
		o.mux.Lock()
		// log.Printf("OffsetQueue: currently queued: %d, waiting for %d batches to finish", o.queued, n)
		if o.queued < n {
			// enough batches have finished
			if time.Since(start).Milliseconds() > 1000 {
				log.Printf("Newsgroup: '%s' | OffsetQueue: waited (%d ms) for %d batches. queued: %d", *o.Newsgroup, time.Since(start).Milliseconds(), n, o.queued)
			}
			o.mux.Unlock()
			return
		}
		if time.Since(lastPrint) > time.Second*5 {
			log.Printf("Newsgroup: '%s' | OffsetQueue: waiting for queued batches: %d", *o.Newsgroup, o.queued)
			lastPrint = time.Now()
		}
		if !setWaiting {
			o.waiter = append(o.waiter, waitChan)
			setWaiting = true
		}
		o.mux.Unlock()

		// wait for signal or timeout to retry
		if setWaiting {
		wait:
			for {
				select {
				case <-waitChan:
					// got signal, recheck condition
					break wait
				case <-time.After(time.Second * 6):
					// timeout, recheck condition
					break wait
				}
			}
		}
	}
}

// OffsetBatchDone signals that a batch has finished processing
func (o *OffsetQueue) OffsetBatchDone() {
	o.mux.Lock()
	defer o.mux.Unlock()
	o.queued--
	if len(o.waiter) > 0 {
		// notify one waiter
		waitChan := o.waiter[0]
		o.waiter = o.waiter[1:]
		select {
		case waitChan <- struct{}{}:
		default:
			// if the channel is full, skip sending
		}
	}
	//log.Printf("OffsetQueue: a batch is done, still queued: %d", o.queued)
}

func (o *OffsetQueue) Add(n int) {
	o.mux.Lock()
	defer o.mux.Unlock()
	o.queued += n
	if o.MaxQueuedJobs > 10 && o.queued > o.MaxQueuedJobs/100*90 {
		// prints only if occupancy is over 90%
		log.Printf("Newsgroup: '%s' | OffsetQueue: added %d batches, now queued: %d/%d", *o.Newsgroup, n, o.queued, o.MaxQueuedJobs)
	}
}

// TTResponse holds the response for a batched TAKETHIS job
type TTResponse struct {
	Job          *CHTTJob
	ForceCleanUp bool
	Err          error
}

// Pool of TTResponse structs to reduce allocations
var TTResponsePool = make(chan *TTResponse, 1024*1024)

// GetTTResponse returns a recycled TTResponse struct or makes a new one if none are available
func GetTTResponse(job *CHTTJob, forceCleanup bool, err error) *TTResponse {
	select {
	case resp := <-TTResponsePool:
		resp.Job = job
		resp.ForceCleanUp = forceCleanup
		resp.Err = err
		return resp
	default:
		return &TTResponse{
			Job:          job,
			ForceCleanUp: forceCleanup,
			Err:          err,
		}
	}
}

// RecycleTTResponse resets a TTResponse struct and recycles it back into the pool
func RecycleTTResponse(resp *TTResponse) {
	resp.Job = nil
	resp.ForceCleanUp = false
	resp.Err = nil
	select {
	case TTResponsePool <- resp:
	default:
		// pool is full, discard
	}
}

// Pool of TTResponse chans to reduce allocations
var TTResponseChans = make(chan chan *TTResponse, 1024*1024)

// GetTTResponseChan returns a recycled chan *TTResponse or makes a new one with capacity of 1 if none are available
func GetTTResponseChan() chan *TTResponse {
	select {
	case ch := <-TTResponseChans:
		return ch
	default:
		return make(chan *TTResponse, 1)
	}
}

func RecycleTTResponseChan(ch chan *TTResponse) {
	if cap(ch) != 1 {
		log.Printf("Warning: Attempt to recycle chan *TTResponse with wrong capacity: %d", cap(ch))
		return
	}
	// empty out the channel
	select {
	case <-ch:
		// successfully emptied
	default:
		// is already empty
	}
	// park
	select {
	case TTResponseChans <- ch:
		// successfully recycled
	default:
		// channel pool is full, discard
	}
}

type CheckResponse struct { // deprecated
	CmdId   uint
	Article *models.Article
}

type ReadRequest struct {
	CmdID uint
	Job   *CHTTJob
	MsgID *string
	N     int
	Reqs  int
}

// Pool of ReadRequest structs to reduce allocations
var ReadRequestsPool = make(chan *ReadRequest, 1024*1024)

// ClearReadRequest resets a ReadRequest struct and recycles it back into the pool
func (rr *ReadRequest) ClearReadRequest(respData *ResponseData) {
	rr.CmdID = 0
	rr.Job = nil
	rr.MsgID = nil
	rr.N = 0
	rr.Reqs = 0
	RecycleReadRequest(rr)
	if respData != nil {
		RecycleResponseData(respData)
	}
}

// GetReadRequest returns a recycled ReadRequest struct or makes a new one if none are available
func GetReadRequest(CmdID uint, Job *CHTTJob, MsgID *string, n int, reqs int) *ReadRequest {
	select {
	case rr := <-ReadRequestsPool:
		rr.CmdID = CmdID
		rr.Job = Job
		rr.MsgID = MsgID
		rr.N = n
		rr.Reqs = reqs
		return rr
	default:
		return &ReadRequest{
			CmdID: CmdID,
			Job:   Job,
			MsgID: MsgID,
			N:     n,
			Reqs:  reqs,
		}
	}
}

func RecycleReadRequest(rr *ReadRequest) {
	select {
	case ReadRequestsPool <- rr:
	default:
		// pool is full, discard
	}
}

// batched CHECK/TAKETHIS Job
type CHTTJob struct {
	JobID            uint64 // Unique job ID for tracing
	Newsgroup        *string
	Mux              sync.RWMutex
	TTMode           *TakeThisMode
	ResponseChan     chan *TTResponse
	responseSent     bool // Track if response already sent (prevents double send)
	Articles         []*models.Article
	ArticleMap       map[*string]*models.Article
	MessageIDs       []*string
	WantedIDs        []*string
	PendingResponses sync.WaitGroup // Track pending TAKETHIS responses
	CheckSentCount   uint64         // Track how many CHECK commands were sent
	//checked      uint64
	//wanted       uint64
	//unwanted     uint64
	//rejected     uint64
	//retry        uint64
	//transferred  uint64
	//redisCached  uint64
	//TxErrors     uint64
	//ConnErrors   uint64
	TmpTxBytes  uint64
	TTxBytes    uint64
	OffsetStart int64
	BatchStart  int64
	BatchEnd    int64
	OffsetQ     *OffsetQueue
	NGTProgress *NewsgroupTransferProgress
}

// GetResponseChan returns the ResponseChan for the job
func (job *CHTTJob) GetResponseChan() chan *TTResponse {
	job.Mux.RLock()
	defer job.Mux.RUnlock()
	if job.ResponseChan != nil {
		return job.ResponseChan
	}
	return nil
}

// QuitResponseChan signals that no more responses will be sent and returns the closed ResponseChan
func (job *CHTTJob) QuitResponseChan() chan *TTResponse {
	job.OffsetQ.OffsetBatchDone()
	job.Response(true, nil)
	job.Mux.RLock()
	defer job.Mux.RUnlock()
	if job.ResponseChan != nil {
		log.Printf("Newsgroup: '%s' | CHTTJob.QuitResponseChan(): returning closed ResponseChan for job #%d", *job.Newsgroup, job.JobID)
		return job.ResponseChan
	}
	return nil
}

// Response sends the response back to a go routine via the ResponseChan
func (job *CHTTJob) Response(ForceCleanUp bool, Err error) {
	if job.ResponseChan == nil {
		log.Printf("ERROR CHTTJob.Response(): ResponseChan is nil for job #%d", job.JobID)
		return
	}

	// Check if response already sent (prevents double send on connection loss)
	job.Mux.Lock()
	if job.responseSent {
		log.Printf("WARNING CHTTJob.Response(): Response already sent for job #%d, skipping", job.JobID)
		job.Mux.Unlock()
		return
	}
	job.responseSent = true
	job.Mux.Unlock()

	job.ResponseChan <- GetTTResponse(job, ForceCleanUp, Err)
	//close(job.ResponseChan)
}

// NewsgroupTransferProgressMap is protected by ResultsMutex, used in nntp-transfer/main.go
var ResultsMutex sync.RWMutex
var NewsgroupTransferProgressMap = make(map[string]*NewsgroupTransferProgress)

// NewsgroupProgress tracks the progress of a newsgroup transfer
type NewsgroupTransferProgress struct {
	Mux         sync.RWMutex
	Newsgroup   *string
	Started     time.Time
	LastUpdated time.Time

	OffsetStart               int64
	BatchStart                int64
	BatchEnd                  int64
	TotalArticles             int64
	Checked                   uint64
	Wanted                    uint64
	Unwanted                  uint64
	Rejected                  uint64
	Retry                     uint64
	Transferred               uint64
	TTSentCount               uint64
	CheckSentCount            uint64
	RedisCached               uint64
	TxErrors                  uint64
	ConnErrors                uint64
	Skipped                   uint64
	RedisCachedBeforeCheck    uint64
	RedisCachedBeforeTakethis uint64
	ArticlesTT                uint64
	ArticlesCH                uint64
	Finished                  bool
	TXBytes                   uint64
	TXBytesTMP                uint64
	LastCronTX                time.Time
	LastSpeedKB               uint64
	LastArtPerfC              uint64 // check articles per second
	LastArtPerfT              uint64 // takethis articles per second
}

// GetSpeed returns the last calculated transfer speed in KB/s
func (ngp *NewsgroupTransferProgress) GetSpeed() uint64 {
	ngp.Mux.RLock()
	defer ngp.Mux.RUnlock()
	return ngp.LastSpeedKB
}

// CalcSpeed calculates the transfer speed and article performance
func (ngp *NewsgroupTransferProgress) CalcSpeed() {
	ngp.Mux.Lock()
	if time.Since(ngp.LastCronTX) >= time.Second*3 {
		since := uint64(time.Since(ngp.LastCronTX).Seconds())
		if ngp.TXBytesTMP > 0 {
			ngp.LastSpeedKB = ngp.TXBytesTMP / since / 1024
		} else {
			ngp.LastSpeedKB = 0
		}
		if ngp.ArticlesCH > 0 {
			ngp.LastArtPerfC = ngp.ArticlesCH / since
		} else {
			ngp.LastArtPerfC = 0
		}
		if ngp.ArticlesTT > 0 {
			ngp.LastArtPerfT = ngp.ArticlesTT / since
		} else {
			ngp.LastArtPerfT = 0
		}
		//log.Printf("Newsgroup: '%s' | Transfer Perf: %d KB/s (%d bytes in %v) did: CH=(%d|%d/s) TT=(%d|%d/s)", *ngp.Newsgroup, ngp.LastSpeedKB, ngp.TXBytesTMP, since, ngp.ArticlesCH, ngp.LastArtPerfC, ngp.ArticlesTT, ngp.LastArtPerfT)

		ngp.ArticlesCH = 0
		ngp.ArticlesTT = 0
		ngp.TXBytesTMP = 0
		ngp.LastCronTX = time.Now()
	}
	ngp.Mux.Unlock()
}

// AddNGTP adds to the NewsgroupTransferProgress temporary counters to calculate speed
func (ngp *NewsgroupTransferProgress) AddNGTP(articlesCH uint64, articlesTT uint64, txbytes uint64) {
	if articlesCH > 0 {
		ngp.Mux.Lock()
		ngp.ArticlesCH += articlesCH
		ngp.Mux.Unlock()
	}
	if articlesTT > 0 {
		ngp.Mux.Lock()
		ngp.ArticlesTT += articlesTT
		ngp.Mux.Unlock()
	}
	if txbytes > 0 {
		ngp.Mux.Lock()
		ngp.TXBytes += txbytes
		ngp.TXBytesTMP += txbytes
		ngp.Mux.Unlock()
	}
	if articlesCH > 0 || articlesTT > 0 || txbytes > 0 {
		ngp.Mux.Lock()
		ngp.LastUpdated = time.Now()
		ngp.Mux.Unlock()
	}
	ngp.CalcSpeed()
}

const IncrFLAG_CHECKED = 1
const IncrFLAG_WANTED = 2
const IncrFLAG_UNWANTED = 3
const IncrFLAG_REJECTED = 4
const IncrFLAG_RETRY = 5
const IncrFLAG_TRANSFERRED = 6
const IncrFLAG_REDIS_CACHED = 7
const IncrFLAG_TX_ERRORS = 8
const IncrFLAG_CONN_ERRORS = 9
const IncrFLAG_SKIPPED = 10
const IncrFLAG_REDIS_CACHED_BEFORE_CHECK = 11
const IncrFLAG_REDIS_CACHED_BEFORE_TAKETHIS = 12
const IncrFLAG_TTSentCount = 13

// Increment increments a counter in NewsgroupTransferProgress
func (ntp *NewsgroupTransferProgress) Increment(counter int, n uint64) {
	ntp.Mux.Lock()
	defer ntp.Mux.Unlock()
	switch counter {
	case IncrFLAG_CHECKED:
		ntp.Checked += n
	case IncrFLAG_WANTED:
		ntp.Wanted += n
	case IncrFLAG_UNWANTED:
		ntp.Unwanted += n
	case IncrFLAG_REJECTED:
		ntp.Rejected += n
	case IncrFLAG_RETRY:
		ntp.Retry += n
	case IncrFLAG_TRANSFERRED:
		ntp.Transferred += n
	case IncrFLAG_REDIS_CACHED:
		ntp.RedisCached += n
	case IncrFLAG_TX_ERRORS:
		ntp.TxErrors += n
	case IncrFLAG_CONN_ERRORS:
		ntp.ConnErrors += n
	case IncrFLAG_SKIPPED:
		ntp.Skipped += n
	case IncrFLAG_TTSentCount:
		ntp.TTSentCount += n
	case IncrFLAG_REDIS_CACHED_BEFORE_CHECK:
		ntp.RedisCachedBeforeCheck += n
		ntp.RedisCached += n // also increment total
	case IncrFLAG_REDIS_CACHED_BEFORE_TAKETHIS:
		ntp.RedisCachedBeforeTakethis += n
		ntp.RedisCached += n // also increment total
	}
}

// AppendMessageID appends a message ID to the job
func (job *CHTTJob) AppendWantedMessageID(msgID *string) {
	job.Mux.Lock()
	job.WantedIDs = append(job.WantedIDs, msgID)
	job.Mux.Unlock()
}

// UseCHECK returns true if CHECK mode is active
func (ttMode *TakeThisMode) UseCHECK() bool {
	ttMode.mux.Lock()
	defer ttMode.mux.Unlock()
	if ttMode.CheckMode {
		return true
	}
	return false
}

// SetForceCHECK forces CHECK mode
func (ttMode *TakeThisMode) SetForceCHECK() {
	ttMode.mux.Lock()
	ttMode.CheckMode = true
	ttMode.mux.Unlock()
}

// IncrementSuccess increments the temporary TAKETHIS success count
func (ttMode *TakeThisMode) IncrementSuccess() {
	ttMode.mux.Lock()
	ttMode.TmpSuccessCount++
	ttMode.mux.Unlock()
}

// IncrementTmp increments the temporary TAKETHIS total count
func (ttMode *TakeThisMode) IncrementTmp() {
	ttMode.mux.Lock()
	ttMode.TmpTTotalsCount++
	ttMode.mux.Unlock()
}

// SetNoCHECK forces TAKETHIS mode
func (ttMode *TakeThisMode) SetNoCHECK() {
	ttMode.mux.Lock()
	ttMode.CheckMode = false
	ttMode.mux.Unlock()
}

// FlipMode checks the TAKETHIS success rate and flips between CHECK and TAKETHIS modes
func (ttMode *TakeThisMode) FlipMode(lowerLevel float64, upperLevel float64) bool {
	ttMode.mux.Lock()
	defer ttMode.mux.Unlock()
	if ttMode.TmpSuccessCount < 10 || ttMode.TmpTTotalsCount < 100 {
		return true // Force CHECK mode for this batch
	}
	successRate := float64(ttMode.TmpSuccessCount) / float64(ttMode.TmpTTotalsCount) * 100.0
	ttMode.TmpSuccessCount = 0
	ttMode.TmpTTotalsCount = 0
	switch ttMode.CheckMode {
	case false: // Currently in TAKETHIS mode
		if successRate < lowerLevel {
			ttMode.CheckMode = true
			log.Printf("Newsgroup: '%s' | TAKETHIS success rate %.1f%% < %f%%, switching to CHECK mode", *ttMode.Newsgroup, successRate, lowerLevel)
		}
	case true: // Currently in CHECK mode
		if successRate > upperLevel {
			ttMode.CheckMode = false
			log.Printf("Newsgroup: '%s' | TAKETHIS success rate %.1f%% >= %f%%, switching to TAKETHIS mode", *ttMode.Newsgroup, successRate, upperLevel)
		}
	}
	retval := ttMode.CheckMode
	return retval
}
