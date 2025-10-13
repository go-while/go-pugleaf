package nntp

// Package nntp provides NNTP command implementations for go-pugleaf.

import (
	"bufio"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
	"github.com/go-while/go-pugleaf/internal/models"
	"github.com/go-while/go-pugleaf/internal/utils"
	"github.com/redis/go-redis/v9"
)

// Constants for maximum lines to read in various commands

// MaxReadLinesArticle Maximum lines for ARTICLE command, including headers and body
const MaxReadLinesArticle = 256 * 1024

// MaxReadLinesHeaders Maximum lines for HEAD command, which only retrieves headers
const MaxReadLinesHeaders = 1024

// MaxReadLinesXover Maximum lines for XOVER command, which retrieves overview lines
var MaxReadLinesXover int64 = 100 // XOVER command typically retrieves overview lines MaxBatch REFERENCES this in processor!!!

// MaxReadLinesBody Maximum lines for BODY command, which retrieves the body of an article
const MaxReadLinesBody = MaxReadLinesArticle - MaxReadLinesHeaders

var NNTPTransferThreads int = 1

// var TakeThisQueue = make(chan *CHTTJob, NNTPTransferThreads)
//var CheckQueue = make(chan *CHTTJob, NNTPTransferThreads)

var JobIDCounter uint64 // Atomic counter for unique job IDs

// ResponseType indicates which handler should process a response
type ResponseType int

const (
	TYPE_CHECK ResponseType = iota
	TYPE_TAKETHIS
)

// ResponseData holds a pre-read response from the connection
type ResponseData struct {
	CmdID uint
	Code  int
	Line  string
	Err   error
}

type CmdIDinfo struct {
	CmdID    uint
	RespType ResponseType
}

// ResponseDemuxer reads all responses from a connection in ONE goroutine
// and dispatches them to the appropriate handler channel (CHECK or TAKETHIS)
// This eliminates race conditions in concurrent ReadCodeLine calls
type ResponseDemuxer struct {
	conn              *BackendConn
	cmdIDQ            []*CmdIDinfo
	signalChan        chan struct{}
	cmdIDQMux         sync.RWMutex
	LastID            uint
	checkResponseChan chan *ResponseData
	ttResponseChan    chan *ResponseData
	errChan           chan struct{}
	started           bool
	startedMux        sync.Mutex
}

// NewResponseDemuxer creates a new response demultiplexer
func NewResponseDemuxer(conn *BackendConn, errChan chan struct{}, BatchCheck int) *ResponseDemuxer {
	return &ResponseDemuxer{
		conn:              conn,
		signalChan:        make(chan struct{}, 1),
		checkResponseChan: make(chan *ResponseData, 128000), // Buffer for CHECK responses
		ttResponseChan:    make(chan *ResponseData, 128000), // Buffer for TAKETHIS responses
		errChan:           errChan,
		started:           false,
	}
}

// RegisterCommand registers a command ID with its type (CHECK or TAKETHIS)
func (d *ResponseDemuxer) RegisterCommand(cmdID uint, cmdType ResponseType) {
	d.cmdIDQMux.Lock()
	d.cmdIDQ = append(d.cmdIDQ, &CmdIDinfo{CmdID: cmdID, RespType: cmdType})
	d.cmdIDQMux.Unlock()
	select {
	case d.signalChan <- struct{}{}:
	default:
	}
}

// PopCommand removes a command ID from the queue
func (d *ResponseDemuxer) PopCommand() *CmdIDinfo {
	d.cmdIDQMux.Lock()
	defer d.cmdIDQMux.Unlock()

	if len(d.cmdIDQ) == 0 {
		return nil
	}

	cmdIDInfo := d.cmdIDQ[0]
	d.cmdIDQ = d.cmdIDQ[1:]
	return cmdIDInfo
}

// GetCheckResponseChan returns the channel for CHECK responses
func (d *ResponseDemuxer) GetCheckResponseChan() chan *ResponseData {
	return d.checkResponseChan
}

// GetTakeThisResponseChan returns the channel for TAKETHIS responses
func (d *ResponseDemuxer) GetTakeThisResponseChan() chan *ResponseData {
	return d.ttResponseChan
}

// Start launches the central response reader goroutine (call once)
func (d *ResponseDemuxer) Start() {
	d.startedMux.Lock()
	defer d.startedMux.Unlock()

	if d.started {
		return // Already started
	}
	d.started = true

	go d.readAndDispatch()
}

// readAndDispatch is the SINGLE goroutine that reads ALL responses from the shared connection
func (d *ResponseDemuxer) readAndDispatch() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ResponseDemuxer: panic in readAndDispatch: %v", r)
		}
		select {
		case d.errChan <- struct{}{}:
		default:
		}
	}()
	outoforderBacklog := make(map[uint]*CmdIDinfo, 1024)
	for {
		select {
		case <-d.errChan:
			log.Printf("ResponseDemuxer: got errChan signal, exiting")
			return
		default:
		}

		if !d.conn.IsConnected() {
			log.Printf("ResponseDemuxer: connection lost, exiting")
			return
		}

		var cmdInfo *CmdIDinfo
		if len(outoforderBacklog) > 0 {
			if cmdInfoBacklog, exists := outoforderBacklog[d.LastID+1]; exists {
				log.Printf("ResponseDemuxer: processing out-of-order backlog cmdID=%d d.LastID=%d", cmdInfoBacklog.CmdID, d.LastID)
				cmdInfo = cmdInfoBacklog
				outoforderBacklog[d.LastID+1] = nil
				delete(outoforderBacklog, d.LastID+1)
			} else {
				log.Printf("ResponseDemuxer: no backlog with cmdID=%d found. try PopCommand", d.LastID+1)
				cmdInfo = d.PopCommand()
			}
		} else {
			cmdInfo = d.PopCommand()
		}
		if cmdInfo == nil {
			if len(outoforderBacklog) > 0 {
				log.Printf("ResponseDemuxer: got no cmdInfo but have outoforderBacklog: %d [%v]", len(outoforderBacklog), outoforderBacklog)
				if _, exists := outoforderBacklog[d.LastID+1]; exists {
					log.Printf("ResponseDemuxer: pre-processing out-of-order backlog cmdID=%d d.LastID=%d", d.LastID+1, d.LastID)
					continue
				}
			}
			//log.Printf("ResponseDemuxer: nothing to process, waiting on signalChan")
			<-d.signalChan
			continue
		}
		if d.LastID+1 != cmdInfo.CmdID {
			log.Printf("ResponseDemuxer: WARNING - out-of-order cmdID received, expected %d got %d", d.LastID+1, cmdInfo.CmdID)
			outoforderBacklog[cmdInfo.CmdID] = cmdInfo
			continue
		} else {
			d.LastID = cmdInfo.CmdID
		}

		//log.Printf("ResponseDemuxer: waiting for response cmdID=%d respType=%d", cmdInfo.CmdID, cmdInfo.RespType)
		start := time.Now()
		d.conn.TextConn.StartResponse(cmdInfo.CmdID)
		code, line, err := d.conn.TextConn.ReadCodeLine(0) // Read any code
		d.conn.TextConn.EndResponse(cmdInfo.CmdID)
		if time.Since(start) > time.Second {
			log.Printf("LongWait ResponseDemuxer: received response cmdID=%d: code=%d line='%s' err='%v' respType=%d (waited %v)", cmdInfo.CmdID, code, line, err, cmdInfo.RespType, time.Since(start))
		}
		if err != nil && code == 0 {
			d.errChan <- struct{}{}
			log.Printf("ResponseDemuxer: error reading response for cmdID=%d: %v", cmdInfo.CmdID, err)
			return
		}
		respData := &ResponseData{
			CmdID: cmdInfo.CmdID,
			Code:  code,
			Line:  line,
			Err:   err,
		}
		// Dispatch based on registered type

		switch cmdInfo.RespType {
		case TYPE_CHECK:
			select {
			case d.checkResponseChan <- respData:
				// Dispatched successfully
				//log.Printf("ResponseDemuxer: dispatched CHECK response cmdID=%d d.checkResponseChan=%d", cmdInfo.CmdID, len(d.checkResponseChan))
			case <-d.errChan:
				log.Printf("ResponseDemuxer: got errChan while dispatching CHECK response, exiting")
				d.errChan <- struct{}{}
				return
			}

		case TYPE_TAKETHIS:
			select {
			case d.ttResponseChan <- respData:
				// Dispatched successfully
				//log.Printf("ResponseDemuxer: dispatched TAKETHIS response cmdID=%d d.ttResponseChan=%d", cmdInfo.CmdID, len(d.ttResponseChan))
			case <-d.errChan:
				d.errChan <- struct{}{}
				log.Printf("ResponseDemuxer: got errChan while dispatching TAKETHIS response, exiting")
				return
			}

		default:
			log.Printf("ResponseDemuxer: WARNING - unknown command type for cmdID=%d, signaling ERROR", cmdInfo.CmdID)
			select {
			case d.errChan <- struct{}{}:
			default:
			}
		}
	}
}

// used in nntp-transfer/main.go
type TakeThisMode struct {
	mux             sync.Mutex
	Newsgroup       *string
	TmpSuccessCount uint64
	TmpTTotalsCount uint64
	CheckMode       bool // Start with TAKETHIS mode (false)
}

type TTSetup struct {
	ResponseChan chan *TTResponse
	OffsetQ      *OffsetQueue
}

type OffsetQueue struct {
	Newsgroup     *string
	MaxQueuedJobs int
	mux           sync.RWMutex
	isleep        time.Duration
	queued        int
}

var ReturnDelay = time.Millisecond * 16

func (o *OffsetQueue) Wait(n int) {
	start := time.Now()
	lastPrint := start
	for {
		o.mux.RLock()
		if o.queued < n {
			o.mux.RUnlock()

			o.mux.Lock()
			if time.Since(start).Milliseconds() > 1000 {
				log.Printf("Newsgroup: '%s' | OffsetQueue: waited (%d ms) for %d batches to finish, currently queued: %d", *o.Newsgroup, time.Since(start).Milliseconds(), n, o.queued)
			}
			o.isleep = o.isleep / 2
			if o.isleep < time.Millisecond {
				o.isleep = 0
			}
			o.mux.Unlock()
			return
		}
		if time.Since(lastPrint) > time.Second*5 {
			if common.WantShutdown() {
				return
			}
			log.Printf("Newsgroup: '%s' | OffsetQueue: waiting for batches to finish, currently queued: %d", *o.Newsgroup, o.queued)
			lastPrint = time.Now()
		}
		o.mux.RUnlock()
		o.mux.Lock()
		o.isleep += time.Millisecond
		if o.isleep > ReturnDelay {
			o.isleep = ReturnDelay
		}
		time.Sleep(o.isleep)
		o.mux.Unlock()
	}
}

func (o *OffsetQueue) OffsetBatchDone() {
	o.mux.Lock()
	defer o.mux.Unlock()
	o.queued--
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

type TTResponse struct {
	Job          *CHTTJob
	ForceCleanUp bool
	Err          error
}

type CheckResponse struct { // deprecated
	CmdId   uint
	Article *models.Article
}

type ReadRequest struct {
	CmdID uint
	Job   *CHTTJob
	N     int
	Reqs  int
	MsgID *string
}

func (rr *ReadRequest) ClearReadRequest() {
	rr.Job = nil
	rr.MsgID = nil
	rr = nil
}

func (rr *ReadRequest) ReturnReadRequest(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
	rr.ClearReadRequest()
}

// TakeThisTracker tracks metadata for pending TAKETHIS responses
type TakeThisTracker struct {
	CmdID    uint
	Job      *CHTTJob
	Article  *models.Article
	RedisCli *redis.Client // Will be *redis.Client in practice
}

// batched CHECK/TAKETHIS Job
type CHTTJob struct {
	JobID        uint64 // Unique job ID for tracing
	Newsgroup    *string
	Mux          sync.RWMutex
	TTMode       *TakeThisMode
	ResponseChan chan *TTResponse
	responseSent bool // Track if response already sent (prevents double send)
	Articles     []*models.Article
	ArticleMap   map[*string]*models.Article
	MessageIDs   []*string
	WantedIDs    []*string
	checked      uint64
	wanted       uint64
	unwanted     uint64
	rejected     uint64
	retry        uint64
	transferred  uint64
	redisCached  uint64
	TxErrors     uint64
	TmpTxBytes   uint64
	TTxBytes     uint64
	ConnErrors   uint64
	OffsetStart  int64
	BatchStart   int64
	BatchEnd     int64
	OffsetQ      *OffsetQueue
	NGTProgress  *NewsgroupTransferProgress
}

func (job *CHTTJob) ReturnResponseChan() chan *TTResponse {
	job.Mux.RLock()
	defer job.Mux.RUnlock()
	if job.ResponseChan != nil {
		return job.ResponseChan
	}
	return nil
}

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

	job.ResponseChan <- &TTResponse{Job: job, ForceCleanUp: ForceCleanUp, Err: Err}
	close(job.ResponseChan)
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

	OffsetStart   int64
	BatchStart    int64
	BatchEnd      int64
	TotalArticles int64
	RedisCached   uint64
	ArticlesTT    uint64
	ArticlesCH    uint64
	Finished      bool
	TXBytes       uint64
	TXBytesTMP    uint64
	LastCronTX    time.Time
	LastSpeedKB   uint64
	LastArtPerfC  uint64 // check articles per second
	LastArtPerfT  uint64 // takethis articles per second
}

func (ngp *NewsgroupTransferProgress) GetSpeed() uint64 {
	ngp.Mux.RLock()
	defer ngp.Mux.RUnlock()
	return ngp.LastSpeedKB
}

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

func (job *CHTTJob) Increment(counter int, n uint64) {
	job.Mux.Lock()
	defer job.Mux.Unlock()
	switch counter {
	case IncrFLAG_CHECKED:
		job.checked += n
	case IncrFLAG_WANTED:
		job.wanted += n
	case IncrFLAG_UNWANTED:
		job.unwanted += n
	case IncrFLAG_REJECTED:
		job.rejected += n
	case IncrFLAG_RETRY:
		job.retry += n
	case IncrFLAG_TRANSFERRED:
		job.transferred += n
	case IncrFLAG_REDIS_CACHED:
		job.redisCached += n
	case IncrFLAG_TX_ERRORS:
		job.TxErrors += n
	case IncrFLAG_CONN_ERRORS:
		job.ConnErrors += n
	}
}

func (job *CHTTJob) AppendWantedMessageID(msgID *string) {
	job.Mux.Lock()
	job.WantedIDs = append(job.WantedIDs, msgID)
	job.Mux.Unlock()
	job.Increment(IncrFLAG_WANTED, 1)
}

func (job *CHTTJob) GetUpdateCounters(transferred, unwanted, rejected, checked, redisCached, txErrors, connErrors *uint64) {
	job.Mux.Lock()
	*transferred += job.transferred
	*unwanted += job.unwanted
	*rejected += job.rejected
	*checked += job.checked
	*redisCached += job.redisCached
	*txErrors += job.TxErrors
	*connErrors += job.ConnErrors
	job.Mux.Unlock()
}

func (ttMode *TakeThisMode) UseCHECK() bool {
	ttMode.mux.Lock()
	defer ttMode.mux.Unlock()
	if ttMode.CheckMode {
		return true
	}
	return false
}

func (ttMode *TakeThisMode) SetForceCHECK() {
	ttMode.mux.Lock()
	ttMode.CheckMode = true
	ttMode.mux.Unlock()
}

func (ttMode *TakeThisMode) IncrementSuccess() {
	ttMode.mux.Lock()
	ttMode.TmpSuccessCount++
	ttMode.mux.Unlock()
}

func (ttMode *TakeThisMode) IncrementTmp() {
	ttMode.mux.Lock()
	ttMode.TmpTTotalsCount++
	ttMode.mux.Unlock()
}

func (ttMode *TakeThisMode) SetNoCHECK() {
	ttMode.mux.Lock()
	ttMode.CheckMode = false
	ttMode.mux.Unlock()
}

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

// StatArticle checks if an article exists on the server
func (c *BackendConn) StatArticle(messageID string) (bool, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return false, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.TextConn.Cmd("STAT %s", messageID)
	if err != nil {
		return false, fmt.Errorf("failed to send STAT command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, _, err := c.TextConn.ReadCodeLine(223)
	if err != nil {
		return false, fmt.Errorf("failed to read STAT response: %w", err)
	}

	switch code {
	case ArticleExists:
		return true, nil
	case NoSuchArticle, DMCA:
		return false, nil
	default:
		return false, fmt.Errorf("unexpected STAT response: %d", code)
	}
}

// GetArticle retrieves a complete article from the server
func (c *BackendConn) GetArticle(messageID *string, bulkmode bool) (*models.Article, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()
	/*
		// Set a per-operation timeout (10 seconds for article retrieval)
		if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return nil, fmt.Errorf("failed to set read deadline: %w", err)
		}
		defer func() {
			// Clear the deadline when operation completes
			if c.conn != nil {
				c.conn.SetReadDeadline(time.Time{})
			}
		}()
	*/
	id, err := c.TextConn.Cmd("ARTICLE %s", *messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send ARTICLE '%s' command: %w", *messageID, err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(ArticleFollows)
	if err != nil && code == 0 {
		log.Printf("[ERROR] failed to read ARTICLE '%s' code=%d message='%s' err: %v", *messageID, code, message, err)
		return nil, fmt.Errorf("failed to read ARTICLE '%s' code=%d message='%s' err: %v", *messageID, code, message, err)
	}

	if code != ArticleFollows {
		switch code {
		case NoSuchArticle:
			log.Printf("[BECONN] GetArticle: not found: '%s' code=%d message='%s' err='%v'", *messageID, code, message, err)
			return nil, ErrArticleNotFound
		case DMCA:
			log.Printf("[BECONN] GetArticle: removed (DMCA): '%s' code=%d message='%s' err='%v'", *messageID, code, message, err)
			return nil, ErrArticleRemoved
		default:
			return nil, fmt.Errorf("unexpected ARTICLE '%s' code=%d message='%s' err='%v'", *messageID, code, message, err)
		}
	}

	// Read the article content
	lines, err := c.readMultilineResponse("article")
	if err != nil {
		return nil, fmt.Errorf("failed to read article '%s' content: %w", *messageID, err)
	}

	// Parse article into headers and body
	article, err := ParseLegacyArticleLines(*messageID, lines, bulkmode)
	if err != nil {
		return nil, fmt.Errorf("failed to parse article '%s': %w", *messageID, err)
	}

	return article, nil
}

// GetHead retrieves only the headers of an article
func (c *BackendConn) GetHead(messageID string) (*models.Article, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.TextConn.Cmd("HEAD %s", messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send HEAD command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(HeadFollows)
	if err != nil {
		return nil, fmt.Errorf("failed to read HEAD response: %w", err)
	}

	if code != HeadFollows {
		switch code {
		case NoSuchArticle:
			log.Printf("[INFO] head not found: %s", messageID)
			return nil, ErrArticleNotFound
		case DMCA:
			log.Printf("[INFO] head removed (DMCA): %s", messageID)
			return nil, ErrArticleRemoved
		default:
			return nil, fmt.Errorf("unexpected HEAD response: %d %s", code, message)
		}
	}

	// Read the headers
	lines, err := c.readMultilineResponse("headers")
	if err != nil {
		return nil, fmt.Errorf("failed to read headers: %w", err)
	}

	// Parse headers only
	article := &models.Article{
		MessageID: messageID,
		Headers:   make(map[string][]string),
	}

	if err := ParseHeaders(article, lines); err != nil {
		return nil, fmt.Errorf("failed to parse headers: %w", err)
	}

	return article, nil
}

// GetBody retrieves only the body of an article
func (c *BackendConn) GetBody(messageID string) ([]byte, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.TextConn.Cmd("BODY %s", messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to send BODY command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(BodyFollows)
	if err != nil {
		return nil, fmt.Errorf("failed to read BODY response: %w", err)
	}

	if code != BodyFollows {
		switch code {
		case NoSuchArticle:
			log.Printf("[INFO] body not found: %s", messageID)
			return nil, ErrArticleNotFound
		case DMCA:
			log.Printf("[INFO] body removed (DMCA): %s", messageID)
			return nil, ErrArticleRemoved
		default:
			return nil, fmt.Errorf("unexpected BODY response: %d %s", code, message)
		}
	}

	// Read the body
	lines, err := c.readMultilineResponse("body")
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	// Join lines with CRLF
	body := strings.Join(lines, "\r\n")
	return []byte(body), nil
}

// ListGroups retrieves a list of available newsgroups
func (c *BackendConn) ListGroups() ([]GroupInfo, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.TextConn.Cmd("LIST")
	if err != nil {
		return nil, fmt.Errorf("failed to send LIST command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(215)
	if err != nil {
		return nil, fmt.Errorf("failed to read LIST response: %w", err)
	}

	if code != 215 {
		return nil, fmt.Errorf("unexpected LIST response: %d %s", code, message)
	}

	// Read the group list
	lines, err := c.readMultilineResponse("list")
	if err != nil {
		return nil, fmt.Errorf("failed to read group list: %w", err)
	}

	// Parse group information
	var groups = make([]GroupInfo, 0, len(lines))
	for _, line := range lines {
		group, err := c.parseGroupLine(line)
		if err != nil {
			continue // Skip malformed lines
		}
		groups = append(groups, group)
	}

	return groups, nil
}

// ListGroupsLimited retrieves a limited number of newsgroups for testing
func (c *BackendConn) ListGroupsLimited(maxGroups int) ([]GroupInfo, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.TextConn.Cmd("LIST")
	if err != nil {
		return nil, fmt.Errorf("failed to send LIST command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(215)
	if err != nil {
		return nil, fmt.Errorf("failed to read LIST response: %w", err)
	}

	if code != 215 {
		return nil, fmt.Errorf("unexpected LIST response: %d %s", code, message)
	}

	// Read the group list with limit
	var groups []GroupInfo
	lineCount := 0

	for {
		if lineCount >= maxGroups {
			c.conn.Close() // Close connection on limit reached
			c.forceClose = true
			log.Printf("Connection reached maximum group limit: %d", maxGroups)
			break
		}

		line, err := c.TextConn.ReadLine()
		if err != nil {
			return nil, fmt.Errorf("failed to read group list: %w", err)
		}

		// Check for end marker
		if line == "." {
			break
		}

		// Handle dot-stuffing
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}

		// Parse group information
		group, err := c.parseGroupLine(line)
		if err != nil {
			continue // Skip malformed lines
		}

		groups = append(groups, group)
		lineCount++
	}

	// Read remaining lines until end marker if we hit the limit
	if lineCount >= maxGroups {
		for {
			line, err := c.TextConn.ReadLine()
			if err != nil {
				break
			}
			if line == DOT {
				break
			}
		}
	}

	return groups, nil
}

// SelectGroup selects a newsgroup for operation
func (c *BackendConn) SelectGroup(groupName string) (*GroupInfo, int, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, 0, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	id, err := c.TextConn.Cmd("GROUP %s", groupName)
	if err != nil {
		return nil, 0, err
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(211)
	if err != nil {
		if code != 411 {
			log.Printf("[ERROR] failed to read GROUP '%s' code=%d message='%s' err: %v", groupName, code, message, err)
		}
		return nil, code, err
	}

	// Parse group information from response
	// RFC 3977: Response code is 211
	// message format is "count first last group"
	parts := strings.Fields(message)
	if len(parts) < 4 {
		return nil, code, fmt.Errorf(
			"malformed GROUP response (expected 'count first last group'): %s group %s",
			message, groupName,
		)
	}

	count, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, code, fmt.Errorf("failed to parse count in GROUP '%s' response: %w", groupName, err)
	}
	first, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, code, fmt.Errorf("failed to parse first in GROUP '%s' response: %w", groupName, err)
	}
	last, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, code, fmt.Errorf("failed to parse last in GROUP '%s' response: %w", groupName, err)
	}

	//log.Printf("Selected group '%s' with %d articles (range: %d-%d)", groupName, count, first, last)

	return &GroupInfo{
		Name:  groupName,
		Count: count,
		First: first,
		Last:  last,
		//PostingOK: true, // Assume posting is OK unless we know otherwise
	}, code, nil
}

// XOver retrieves overview data for a range of articles
// This is essential for efficiently building newsgroup databases
// enforceLimit controls whether to limit to max 1000 articles to prevent SQLite overload
func (c *BackendConn) XOver(groupName string, start, end int64, enforceLimit bool) ([]OverviewLine, error) {
	if groupName == "" {
		return nil, fmt.Errorf("error XOver: group name is required")
	}
	//log.Printf("XOver group '%s' start=%d end=%d", groupName, start, end)
	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil && code != 411 {
		return nil, fmt.Errorf("failed to select group '%s': cdeo=%d err=%w", groupName, code, err)
	}
	_ = groupInfo // groupInfo is not used further, but we keep it for clarity
	c.lastUsed = time.Now()

	// Limit to 1000 articles maximum to prevent SQLite overload (only if enforceLimit is true)
	if enforceLimit && end > 0 && (end-start+1) > MaxReadLinesXover {
		end = start + MaxReadLinesXover - 1
	}

	var id uint
	if end > 0 {
		id, err = c.TextConn.Cmd("XOVER %d-%d", start, end)
	} else {
		id, err = c.TextConn.Cmd("XOVER %d", start)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send XOVER command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(224)
	if err != nil {
		return nil, fmt.Errorf("failed to read XOVER response: %w", err)
	}

	if code != 224 {
		return nil, fmt.Errorf("XOVER failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("xover")
	if err != nil {
		return nil, fmt.Errorf("failed to read XOVER data: %w", err)
	}

	// Parse overview lines
	added := 0
	// nolint
	var overviews []OverviewLine
	for _, line := range lines {
		overview, err := c.parseOverviewLine(line)
		if err != nil {
			continue // Skip malformed lines
		}
		overviews = append(overviews, overview)
		added++
	}
	//log.Printf("XOver found %d articles in group '%s' read lines=%d", added, groupName, len(lines))
	return overviews, nil
}

// XHdr retrieves specific header field for a range of articles
// Automatically limits to max 1000 articles to prevent SQLite overload
func (c *BackendConn) XHdr(groupName, field string, start, end int64) ([]*HeaderLine, error) {
	c.mux.Lock()
	if !c.IsConnected() {
		c.mux.Unlock()
		return nil, fmt.Errorf("not connected")
	}
	c.mux.Unlock()
	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil && code != 411 {
		return nil, fmt.Errorf("failed to select group '%s': code=%d err=%w", groupName, code, err)
	}
	_ = groupInfo // groupInfo is not used further, but we keep it for clarity
	c.lastUsed = time.Now()

	// Limit to 1000 articles maximum to prevent SQLite overload
	if end > 0 && (end-start+1) > MaxReadLinesXover {
		end = start + MaxReadLinesXover - 1
	}
	log.Printf("XHdr group '%s' field '%s' start=%d end=%d", groupName, field, start, end)
	var id uint
	if end > 0 {
		id, err = c.TextConn.Cmd("XHDR %s %d-%d", field, start, end)
	} else {
		id, err = c.TextConn.Cmd("XHDR %s %d", field, start)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send XHDR command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(221)
	if err != nil {
		return nil, fmt.Errorf("failed to read XHDR response: %w", err)
	}

	if code != 221 {
		return nil, fmt.Errorf("XHDR failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("xhdr")
	if err != nil {
		return nil, fmt.Errorf("failed to read XHDR data: %w", err)
	}

	// Parse header lines
	var headers = make([]*HeaderLine, 0, len(lines))
	for _, line := range lines {
		header, err := c.parseHeaderLine(line)
		if err != nil {
			continue // Skip malformed lines
		}
		headers = append(headers, header)
	}

	return headers, nil
}

var ErrOutOfRange error = fmt.Errorf("end range exceeds group last article number")

func (c *BackendConn) WantShutdown(shutdownChan <-chan struct{}) bool {
	select {
	case _, ok := <-shutdownChan:
		if !ok {
			// channel is closed
			return true
		}
	default:
	}
	return false
}

// XHdrStreamed performs XHDR command and streams results line by line through a channel
// Fetches max 1000 hdrs and starts a new fetch if the channel is less than 10% capacity
func (c *BackendConn) XHdrStreamed(groupName, field string, start, end int64, xhdrChan chan<- *HeaderLine, shutdownChan <-chan struct{}) error {
	channelCap := cap(xhdrChan)
	lowWaterMark := channelCap / 10 // 10% threshold
	if lowWaterMark < 1 {
		lowWaterMark = 1
	}

	currentStart := start
	var isleep int64 = 10
	for currentStart <= end {
		// Check for shutdown signal
		if c.WantShutdown(shutdownChan) {
			close(xhdrChan)
			log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
			return fmt.Errorf("shutdown requested")
		}

		// Wait if channel is not empty
		for len(xhdrChan) > lowWaterMark {
			time.Sleep(time.Duration(isleep) * time.Millisecond)
			if c.WantShutdown(shutdownChan) {
				close(xhdrChan)
				log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
				return fmt.Errorf("shutdown requested")
			}
		}

		// Calculate batch end (max 1000 articles)
		batchEnd := currentStart + 999 // 1000 articles max
		if batchEnd > end {
			batchEnd = end
		}

		// Fetch this batch
		startStream := time.Now()
		err := c.XHdrStreamedBatch(groupName, field, currentStart, batchEnd, xhdrChan, shutdownChan)
		if err != nil {
			close(xhdrChan) // Close on error
			return fmt.Errorf("XHdrStreamedBatch failed for range %d-%d: %w", currentStart, batchEnd, err)
		}
		isleep = time.Since(startStream).Milliseconds() / 2
		if isleep < 10 {
			isleep = 10
		}

		// Move to next batch
		currentStart = batchEnd + 1

	}

	// Close channel when all batches are complete
	close(xhdrChan)
	return nil
}

// XHdrStreamedBatch performs XHDR command and streams results line by line through a channel
func (c *BackendConn) XHdrStreamedBatch(groupName, field string, start, end int64, xhdrChan chan<- *HeaderLine, shutdownChan <-chan struct{}) error {
	c.mux.Lock()
	if !c.IsConnected() {
		c.mux.Unlock()
		return fmt.Errorf("not connected")
	}
	c.mux.Unlock()

	groupInfo, code, err := c.SelectGroup(groupName)
	if err != nil && code != 411 {
		return fmt.Errorf("failed to select group '%s': code=%d err=%w", groupName, code, err)
	}
	if end > groupInfo.Last {
		return ErrOutOfRange
	}
	c.lastUsed = time.Now()

	// Limit to 1000 articles maximum to prevent SQLite overload
	const maxFetchLimit = 1000
	if end > 0 && (end-start+1) > maxFetchLimit {
		end = start + maxFetchLimit - 1
	}
	//log.Printf("XHdrStreamed group '%s' field '%s' start=%d end=%d", groupName, field, start, end)

	var id uint
	if end > 0 {
		id, err = c.TextConn.Cmd("XHDR %s %d-%d", field, start, end)
	} else {
		id, err = c.TextConn.Cmd("XHDR %s %d-%d", field, start, start)
	}
	if err != nil {
		return fmt.Errorf("failed to send XHDR command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	// Check for shutdown before reading initial response
	if c.WantShutdown(shutdownChan) {
		log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
		return fmt.Errorf("shutdown requested")
	}

	code, message, err := c.TextConn.ReadCodeLine(221)
	if err != nil {
		return fmt.Errorf("failed to read XHDR response: %w", err)
	}

	if code != 221 {
		return fmt.Errorf("XHDR failed: ng: '%s' %d %s", groupName, code, message)
	}

	// Read multiline response line by line and send to channel immediately
	for {
		// Check for shutdown signal between reads
		if c.WantShutdown(shutdownChan) {
			go c.ForceCloseConn()
			log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
			return fmt.Errorf("shutdown requested")
		}

		line, err := c.TextConn.ReadLine()
		if err != nil {
			log.Printf("[ERROR] XHdrStreamed read error ng: '%s' err='%v'", groupName, err)
			// EOF or error, finish streaming
			break
		}

		// Check for end marker
		if line == DOT {
			break
		}

		// Parse the header line
		header, parseErr := c.parseHeaderLine(line)
		if parseErr != nil {
			log.Printf("[ERROR] XHdrStreamed parse error ng: '%s' err='%v'", groupName, parseErr)
			continue // Skip malformed lines
		}

		if c.WantShutdown(shutdownChan) {
			c.conn.Close() // Close connection on limit reached
			c.forceClose = true
			log.Printf("XHdrStreamed: Worker received shutdown signal, stopping")
			return fmt.Errorf("shutdown requested")
		}

		xhdrChan <- header
	}

	// Don't close channel here - let the main function handle it
	return nil
}

// ListGroup retrieves article numbers for a specific group
func (c *BackendConn) ListGroup(groupName string, start, end int64) ([]int64, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	c.lastUsed = time.Now()

	var id uint
	var err error
	if start > 0 && end > 0 {
		id, err = c.TextConn.Cmd("LISTGROUP %s %d-%d", groupName, start, end)
	} else {
		id, err = c.TextConn.Cmd("LISTGROUP %s", groupName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to send LISTGROUP command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id) // Always clean up response state

	code, message, err := c.TextConn.ReadCodeLine(211)
	if err != nil {
		return nil, fmt.Errorf("failed to read LISTGROUP response: %w", err)
	}

	if code != 211 {
		return nil, fmt.Errorf("LISTGROUP failed: %d %s", code, message)
	}

	// Use your existing readMultilineResponse function!
	lines, err := c.readMultilineResponse("listgroup")
	if err != nil {
		return nil, fmt.Errorf("failed to read article numbers: %w", err)
	}

	// Parse article numbers
	var articleNums []int64
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		num, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			continue // Skip invalid numbers
		}
		articleNums = append(articleNums, num)
	}

	return articleNums, nil
}

// readMultilineResponse reads a multi-line response ending with "."
func (c *BackendConn) readMultilineResponse(src string) ([]string, error) {
	var lines []string
	lineCount := 0
	maxReadLines := MaxReadLines // Use the constant defined in your package

	switch src {

	case "article":
		// For ARTICLE, we expect headers and body
		maxReadLines = MaxReadLinesArticle

	case "headers":
		// For HEAD, we expect only headers
		maxReadLines = MaxReadLinesHeaders

	case "body":
		maxReadLines = MaxReadLinesBody // BODY can be large, but we limit it

	default:
		// pass
		/*
			case "list":
				// For LIST, we expect group names and info
				maxReadLines = MaxReadLinesList

			case "overview":
				// For XOVER, we expect overview lines
				maxReadLines = MaxReadLinesOverview

			case "xover":
				maxReadLines = MaxReadLinesOverview

			case "xhdr":
				maxReadLines = MaxReadLinesXHDR

			case "listgroup":
				maxReadLines = MaxReadLinesListGroup
		*/

	}
	for {
		if lineCount >= maxReadLines {
			c.conn.Close() // Close connection on limit reached
			c.forceClose = true
			return nil, fmt.Errorf("too many lines in response (limit: %d)", maxReadLines)
		}

		line, err := c.TextConn.ReadLine()
		if err != nil {
			return nil, err
		}

		// Check for end marker
		if line == "." {
			break
		}

		// Handle dot-stuffing (lines starting with .. become .)
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}

		lines = append(lines, line)
		lineCount++
	}

	return lines, nil
}

// ParseArticleLines parses article lines into headers and body
func ParseLegacyArticleLines(messageID string, lines []string, bulkmode bool) (*models.Article, error) {
	article := &models.Article{
		MessageID: messageID,
		Headers:   make(map[string][]string),
	}

	// Find the separator between headers and body
	bodyStart := -1
	for i, line := range lines {
		if line == "" {
			bodyStart = i + 1
			break
		}
	}

	if bodyStart == -1 {
		return nil, fmt.Errorf("malformed article: no header-body separator found in msgId='%s'", messageID)
	}

	// Parse headers
	if err := ParseHeaders(article, lines[:bodyStart-1]); err != nil {
		return nil, err
	}

	// Parse article
	if bodyStart < len(lines) {
		article.BodyText = strings.Join(lines[bodyStart:], "\n")
		article.Bytes = len(article.BodyText)
		article.Lines = len(lines) - bodyStart
		if !bulkmode {
			// original body lines for peering
			article.NNTPhead = lines[:bodyStart-1]
			article.NNTPbody = lines[bodyStart:]
		}
	}
	article.Subject = common.GetHeaderFirst(article.Headers, "subject")
	article.FromHeader = common.GetHeaderFirst(article.Headers, "from")
	article.Path = common.GetHeaderFirst(article.Headers, "path")
	article.References = common.GetHeaderFirst(article.Headers, "references")
	article.RefSlice = utils.ParseReferences(article.References) // capture all references for thread chain analysis
	article.DateString = common.GetHeaderFirst(article.Headers, "date")
	return article, nil
}

func MultiLineHeaderToMergedString(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) == 1 {
		return vals[0] // Fast path for single-line headers (most common case)
	}
	return strings.Join(vals, "\n") // Ultra fast for multi-line
}

// parseHeaders parses header lines into the article headers map
func ParseHeaders(article *models.Article, headerLines []string) error {
	var currentHeader string

	for _, line := range headerLines {
		if line == "" {
			break
		}

		// Check for header continuation (line starts with space or tab)
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if currentHeader != "" {
				// Append to previous header
				existing := article.Headers[currentHeader]
				if len(existing) > 0 {
					existing[len(existing)-1] += " " + strings.TrimSpace(line)
					article.Headers[currentHeader] = existing
				}
			}
			continue
		}

		// Parse new header
		colonPos := strings.Index(line, ":")
		if colonPos == -1 {
			continue // Skip malformed headers
		}

		headerName := strings.TrimSpace(line[:colonPos])
		headerValue := strings.TrimSpace(line[colonPos+1:])

		currentHeader = strings.ToLower(headerName)
		if strings.ToLower(headerName) == "xref" {
			currentHeader = ""
			continue
		}
		switch strings.ToLower(headerName) {
		case "newsgroups", "date", "references", "subject", "from", "path":
			//pass
		default:
			// not needed
			currentHeader = ""
			continue
		}
		article.Headers[currentHeader] = append(article.Headers[currentHeader], headerValue)
	}
	article.HeadersJSON = MultiLineHeaderToMergedString(headerLines)
	return nil
}

// parseGroupLine parses a single line from LIST command response
func (c *BackendConn) parseGroupLine(line string) (GroupInfo, error) {
	// Format: "group last first posting"
	parts := strings.Fields(line)
	if len(parts) < 4 {
		return GroupInfo{}, fmt.Errorf("malformed group line: %s", line)
	}

	last, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return GroupInfo{}, fmt.Errorf("invalid last article number in group line: %s", line)
	}
	first, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return GroupInfo{}, fmt.Errorf("invalid first article number in group line: %s", line)
	}
	postingOK := parts[3] == "y"

	count := int64(0)
	if last >= first {
		count = last - first + 1
	}

	return GroupInfo{
		Name:      parts[0],
		Count:     count,
		First:     first,
		Last:      last,
		PostingOK: postingOK,
	}, nil
}

// parseOverviewLine parses a single XOVER response line
// Format: articlenum<tab>subject<tab>from<tab>date<tab>message-id<tab>references<tab>bytes<tab>lines
func (c *BackendConn) parseOverviewLine(line string) (OverviewLine, error) {
	parts := strings.Split(line, "\t")
	if len(parts) < 7 {
		return OverviewLine{}, fmt.Errorf("malformed XOVER line: %s", line)
	}

	articleNum, _ := strconv.ParseInt(parts[0], 10, 64)
	bytes, _ := strconv.ParseInt(parts[6], 10, 64)
	lines := int64(0)
	if len(parts) > 7 {
		lines, _ = strconv.ParseInt(parts[7], 10, 64)
	}

	return OverviewLine{
		ArticleNum: articleNum,
		Subject:    parts[1],
		From:       parts[2],
		Date:       parts[3],
		MessageID:  parts[4],
		References: parts[5],
		Bytes:      bytes,
		Lines:      lines,
	}, nil
}

// parseHeaderLine parses a single XHDR response line
// Format: articlenum<space>header-value
func (c *BackendConn) parseHeaderLine(line string) (*HeaderLine, error) {
	parts := strings.SplitN(line, " ", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("malformed XHDR line: %s", line)
	}

	articleNum, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		log.Printf("Invalid article number in XHDR line: %q", parts[0])
		return nil, fmt.Errorf("invalid article number in XHDR line: %q", parts[0])
	}

	return &HeaderLine{
		ArticleNum: articleNum,
		Value:      parts[1],
	}, nil
}

// SendCheckMultiple sends CHECK commands for multiple message IDs without returning responses!
// Registers each command ID with the demuxer for proper response routing
func (c *BackendConn) SendCheckMultiple(messageIDs []*string, readCHECKResponsesChan chan *ReadRequest, job *CHTTJob, demuxer *ResponseDemuxer) error {
	c.mux.Lock()

	if !c.IsConnected() {
		c.mux.Unlock()
		return fmt.Errorf("not connected")
	}

	if c.ModeReader {
		c.mux.Unlock()
		return fmt.Errorf("cannot check article in reader mode")
	}
	c.lastUsed = time.Now()
	c.mux.Unlock()

	if len(messageIDs) == 0 {
		return fmt.Errorf("no message IDs provided")
	}

	//writer := bufio.NewWriter(c.conn)
	//defer writer.Flush()
	//log.Printf("Newsgroup: '%s' | SendCheckMultiple commands for %d message IDs", *job.Newsgroup, len(messageIDs))

	for n, msgID := range messageIDs {
		if msgID == nil || *msgID == "" {
			log.Printf("Newsgroup: '%s' | Skipping empty message ID in CHECK command", *job.Newsgroup)
			continue
		}
		//log.Printf("Newsgroup: '%s' | CHECK '%s' acquire c.mux.Lock() (%d/%d)", *job.Newsgroup, *msgID, n+1, len(messageIDs))
		c.mux.Lock()
		cmdID, err := c.TextConn.Cmd("CHECK %s", *msgID)
		c.mux.Unlock()
		if err != nil {
			return fmt.Errorf("failed to send CHECK '%s': %w", *msgID, err)
		}

		// Register command ID with demuxer as TYPE_CHECK
		demuxer.RegisterCommand(cmdID, TYPE_CHECK)

		//log.Printf("Newsgroup: '%s' | CHECK sent '%s' (CmdID=%d) pass notify to readResponsesChan=%d", *job.Newsgroup, *msgID, cmdID, len(readCHECKResponsesChan))
		readCHECKResponsesChan <- &ReadRequest{CmdID: cmdID, Job: job, MsgID: msgID, N: n + 1, Reqs: len(messageIDs)}
		//log.Printf("Newsgroup: '%s' | CHECK notified response reader '%s' (CmdID=%d) readCHECKResponsesChan=%d", *job.Newsgroup, *msgID, cmdID, len(readCHECKResponsesChan))
	}
	return nil
}

// SendTakeThisArticleStreaming IS UNSAFE! MUST BE LOCKED AND UNLOCKED OUTSIDE FOR THE WHOLE BATCH!!!
// sends TAKETHIS command and article content without waiting for response
// Returns command ID for later response reading - used for streaming mode
// Registers the command ID with the demuxer for proper response routing
func (c *BackendConn) SendTakeThisArticleStreaming(article *models.Article, nntphostname *string, newsgroup string, demuxer *ResponseDemuxer, readTAKETHISResponsesChan chan *ReadRequest, job *CHTTJob) (cmdID uint, txBytes int, err error) {
	//start := time.Now()
	//c.mux.Lock()
	//defer c.mux.Unlock()

	if !c.IsConnected() {
		//c.mux.Unlock()
		return 0, 0, fmt.Errorf("not connected")
	}

	if c.ModeReader {
		//c.mux.Unlock()
		return 0, 0, fmt.Errorf("cannot send article in reader mode")
	}
	c.lastUsed = time.Now()
	//c.mux.Unlock()

	// Prepare article for transfer
	headers, err := common.ReconstructHeaders(article, true, nntphostname, newsgroup)
	if err != nil {
		return 0, 0, err
	}
	//writer := bufio.NewWriterSize(c.conn, c.GetBufSize(article.Bytes)) // Slightly larger buffer than article size for headers
	writer := bufio.NewWriter(c.conn) // Slightly larger buffer than article size for headers

	//c.mux.Lock()
	//defer c.mux.Unlock()

	//startSend := time.Now()
	// Send TAKETHIS command
	cmdID, err = c.TextConn.Cmd("TAKETHIS %s", article.MessageID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed SendTakeThisArticleStreaming command: %w", err)
	}

	// Send headers
	for _, headerLine := range headers {
		if tx, err := writer.WriteString(headerLine + CRLF); err != nil {
			return 0, txBytes, fmt.Errorf("failed to write header SendTakeThisArticleStreaming: %w", err)
		} else {
			txBytes += tx
		}
	}

	// Send empty line between headers and body
	if tx, err := writer.WriteString(CRLF); err != nil {
		return 0, txBytes, fmt.Errorf("failed to write header/body separator SendTakeThisArticleStreaming: %w", err)
	} else {
		txBytes += tx
	}

	// Send body with proper dot-stuffing
	// Split body preserving line endings
	bodyLines := strings.Split(article.BodyText, "\n")
	for i, line := range bodyLines {
		// Skip empty last element from trailing \n
		if i == len(bodyLines)-1 && line == "" {
			break
		}

		// Remove trailing \r if present (will add CRLF)
		line = strings.TrimSuffix(line, "\r")

		// Dot-stuff lines that start with a dot (RFC 977)
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}

		if tx, err := writer.WriteString(line + CRLF); err != nil {
			return 0, txBytes, fmt.Errorf("failed to write body line SendTakeThisArticleStreaming: %w", err)
		} else {
			txBytes += tx
		}
	}

	// Send termination line (single dot)
	if tx, err := writer.WriteString(DOT + CRLF); err != nil {
		return 0, txBytes, fmt.Errorf("failed to send article terminator SendTakeThisArticleStreaming: %w", err)
	} else {
		txBytes += tx
	}
	//log.Printf("Newsgroup: '%s' | TAKETHIS sent CmdID=%d '%s' txBytes: %d in %v (sending took: %v) readTAKETHISResponsesChanLen=%d/%d", newsgroup, cmdID, article.MessageID, txBytes, time.Since(start), time.Since(startSend), len(readTAKETHISResponsesChan), cap(readTAKETHISResponsesChan))

	//startFlush := time.Now()
	if err := writer.Flush(); err != nil {
		return 0, txBytes, fmt.Errorf("failed to flush article data SendTakeThisArticleStreaming: %w", err)
	}

	//chanStart := time.Now()
	// Register command ID with demuxer as TYPE_TAKETHIS (CRITICAL: must match CHECK pattern)
	demuxer.RegisterCommand(cmdID, TYPE_TAKETHIS)

	//log.Printf("Newsgroup: '%s' | TAKETHIS flushed CmdID=%d '%s' (flushing took: %v) total time: %v readTAKETHISResponsesChan=%d/%d", newsgroup, cmdID, article.MessageID, time.Since(startFlush), time.Since(start), len(readTAKETHISResponsesChan), cap(readTAKETHISResponsesChan))
	// Queue ReadRequest IMMEDIATELY after command (like SendCheckMultiple does at line 1608)
	readTAKETHISResponsesChan <- &ReadRequest{CmdID: cmdID, Job: job, MsgID: &article.MessageID, N: 1, Reqs: 1}
	//log.Printf("Newsgroup: '%s' | TAKETHIS notified response reader CmdID=%d '%s' waited %v readTAKETHISResponsesChan=%d/%d", newsgroup, cmdID, article.MessageID, time.Since(chanStart), len(readTAKETHISResponsesChan), cap(readTAKETHISResponsesChan))
	// Return command ID without reading response (streaming mode)
	return cmdID, txBytes, nil
}

// PostArticle posts an article using the POST command
func (c *BackendConn) PostArticle(article *models.Article) (int, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if !c.IsConnected() {
		return 0, fmt.Errorf("not connected")
	}
	// Prepare article for posting
	headers, err := common.ReconstructHeaders(article, false, nil, "")
	if err != nil {
		return 0, fmt.Errorf("failed to reconstruct headers: %v", err)
	}
	c.lastUsed = time.Now()

	// Send POST command
	id, err := c.TextConn.Cmd("POST")
	if err != nil {
		return 0, fmt.Errorf("failed to send POST command: %w", err)
	}

	c.TextConn.StartResponse(id)
	// Read response to POST command
	code, line, err := c.TextConn.ReadCodeLine(340)
	c.TextConn.EndResponse(id)
	if err != nil && code == 0 {
		return code, fmt.Errorf("POST command failed: %s", line)
	}
	writer := bufio.NewWriter(c.conn)
	defer writer.Flush()
	switch code {
	case 340:
		// pass, posted

	case 401:
		if strings.ToLower(line) == "mode reader" {
			if err := c.SwitchMode(MODE_READER_MV); err != nil {
				return code, fmt.Errorf("POST '%s' failed. switching to reader mode failed: %w", article.MessageID, err)
			}

			// Send POST command again
			id, err := c.TextConn.Cmd("POST")
			if err != nil {
				return 0, fmt.Errorf("failed to send POST command: %w", err)
			}
			c.TextConn.StartResponse(id)
			defer c.TextConn.EndResponse(id)
			// Read response to POST command
			code, line, err = c.TextConn.ReadCodeLine(340)
			if err != nil {
				return code, fmt.Errorf("POST command failed: %s", line)
			}
			c.ModeReader = true
		}
	}

	if code != 340 {
		return code, fmt.Errorf("POST command rejected (code %d): %s", code, line)
	}

	// Send headers using writer (not DotWriter)
	for _, headerLine := range headers {
		if _, err := writer.WriteString(headerLine + CRLF); err != nil {
			return 0, fmt.Errorf("failed to write header: %w", err)
		}
	}

	// Send empty line between headers and body
	if _, err := writer.WriteString(CRLF); err != nil {
		return 0, fmt.Errorf("failed to write header/body separator: %w", err)
	}

	// Send body with proper dot-stuffing (like TakeThisArticle)
	// Split body preserving line endings
	bodyLines := strings.Split(article.BodyText, "\n")
	for i, line := range bodyLines {
		// Skip empty last element from trailing \n
		if i == len(bodyLines)-1 && line == "" {
			break
		}

		// Remove trailing \r if present (will add CRLF)
		line = strings.TrimSuffix(line, "\r")

		// Dot-stuff lines that start with a dot (RFC 977)
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}

		if _, err := writer.WriteString(line + CRLF); err != nil {
			return 0, fmt.Errorf("failed to write body line: %w", err)
		}
	}

	// Send termination line (single dot)
	if _, err := writer.WriteString(DOT + CRLF); err != nil {
		return 0, fmt.Errorf("failed to send article terminator: %w", err)
	}

	// Flush the writer to ensure all data is sent
	if err := writer.Flush(); err != nil {
		return 0, fmt.Errorf("failed to flush article data: %w", err)
	}

	// Read final response
	code, _, err = c.TextConn.ReadCodeLine(240)
	if err != nil {
		return code, fmt.Errorf("failed to read POST response: %w", err)
	}

	// Parse response codes
	// 240 - article posted successfully
	// 441 - posting failed
	return code, nil
}

// SwitchMode switches the NNTP connection to a specific mode
// Supported modes: "reader", "stream"
func (c *BackendConn) SwitchMode(mode int) error {
	switch mode {
	case MODE_READER_MV:
		return c.SwitchToModeReader()
	case MODE_STREAM_MV:
		return c.SwitchToModeStream()
	default:
		return fmt.Errorf("unsupported mode: %d (supported: reader, stream)", mode)
	}
}

// SwitchToModeReader switches the connection to MODE READER
func (c *BackendConn) SwitchToModeReader() error {

	if c.ModeReader {
		// Already in reader mode
		return nil
	}

	c.lastUsed = time.Now()

	// Send MODE READER command
	id, err := c.TextConn.Cmd("MODE READER")
	if err != nil {
		return fmt.Errorf("failed to send MODE READER command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id)

	code, line, err := c.TextConn.ReadCodeLine(200)
	if err != nil {
		return fmt.Errorf("failed to read MODE READER response: %w", err)
	}

	if code != 200 {
		return fmt.Errorf("MODE READER failed (code %d): %s", code, line)
	}

	c.ModeReader = true
	return nil
}

// SwitchToModeStream switches the connection to MODE STREAM
func (c *BackendConn) SwitchToModeStream() error {

	if c.ModeStream {
		// Already in stream mode
		return nil
	}
	if c.ModeReader {
		return fmt.Errorf("cannot switch from MODE READER to MODE STREAM on same connection")
	}

	c.lastUsed = time.Now()

	// Send MODE STREAM command
	id, err := c.TextConn.Cmd("MODE STREAM")
	if err != nil {
		return fmt.Errorf("failed to send MODE STREAM command: %w", err)
	}

	c.TextConn.StartResponse(id)
	defer c.TextConn.EndResponse(id)

	code, line, err := c.TextConn.ReadCodeLine(203)
	if err != nil {
		return fmt.Errorf("failed to read MODE STREAM response: %w", err)
	}

	if code != 203 {
		return fmt.Errorf("MODE STREAM failed (code %d): %s", code, line)
	}

	c.ModeStream = true
	return nil
}
