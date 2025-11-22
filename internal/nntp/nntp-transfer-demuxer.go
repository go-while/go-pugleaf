package nntp

import (
	"log"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
)

// ResponseDemuxer reads all responses from a connection in ONE goroutine
// and dispatches them to the appropriate handler channel (CHECK or TAKETHIS)
// This eliminates race conditions in concurrent ReadCodeLine calls
type ResponseDemuxer struct {
	conn      *BackendConn
	cmdIDChan chan *CmdIDinfo
	//signalChan chan struct{}
	//cmdIDQMux         sync.RWMutex
	LastID            uint
	checkResponseChan chan *ResponseData
	ttResponseChan    chan *ResponseData
	errChan           chan struct{}
	started           bool
	startedMux        sync.Mutex
	lastRequest       time.Time
	lastRequestMux    sync.RWMutex
}

// NewResponseDemuxer creates a new response demultiplexer
func NewResponseDemuxer(conn *BackendConn, errChan chan struct{}) *ResponseDemuxer {
	return &ResponseDemuxer{
		conn:              conn,
		cmdIDChan:         make(chan *CmdIDinfo, 64*1024),      // Buffer for command IDs
		checkResponseChan: make(chan *ResponseData, 1024*1024), // Buffer for CHECK responses
		ttResponseChan:    make(chan *ResponseData, 1024*1024), // Buffer for TAKETHIS responses
		//signalChan:        make(chan struct{}, 64*1024),
		errChan: errChan,
		started: false,
	}
}

// RegisterCommand registers a command ID with its type (CHECK or TAKETHIS)
func (d *ResponseDemuxer) RegisterCommand(cmdID uint, cmdType ResponseType) {
	d.lastRequestMux.Lock()
	d.lastRequest = time.Now()
	d.lastRequestMux.Unlock()
	select {
	case d.cmdIDChan <- &CmdIDinfo{CmdID: cmdID, RespType: cmdType}:
		// Registered successfully
	case <-d.errChan:
		log.Printf("ResponseDemuxer: got errChan while registering command, exiting")
		common.SignalErrChan(d.errChan)
		return
	}
	/*
		select {
		case d.signalChan <- struct{}{}:
			// sent signal
		case <-d.errChan:
			log.Printf("ResponseDemuxer: got errChan while signaling command, exiting")
			common.SignalErrChan(d.errChan)
			return
			/,* disabled
			default:
				// no-op
			*,/
		}
	*/
}

// PopCommand removes a command ID from the queue
func (d *ResponseDemuxer) PopCommand() *CmdIDinfo {

	//if len(d.cmdIDChan) == 0 {
	//	return nil
	//}

	select {
	case cmdIDInfo := <-d.cmdIDChan:
		return cmdIDInfo
	case <-d.errChan:
		log.Printf("ResponseDemuxer: got errChan while popping command, exiting")
		common.SignalErrChan(d.errChan)
		return nil
		//default:
		//	// no-op
	}

	//return nil
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

	/* disabled
	go func() {
		// keep alive
		for {
			<-time.After(time.Millisecond * 16)
			select {
			case d.signalChan <- struct{}{}:
			default:
			}
		}
	}()
	*/
}

// readAndDispatch is the SINGLE goroutine that reads ALL responses from the shared connection
func (d *ResponseDemuxer) readAndDispatch() {
	/* disabled
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ResponseDemuxer: panic in readAndDispatch: %v", r)
		}
		common.SignalErrChan(d.errChan)
	}()
	*/
	outoforderBacklog := make(map[uint]*CmdIDinfo, 1024)
loop:
	for {
		select {
		case <-d.errChan:
			log.Printf("ResponseDemuxer: got errChan signal, exiting")
			common.SignalErrChan(d.errChan)
			// exit
			return
		default:
			// pass
		}

		if !d.conn.IsConnected() {
			common.SignalErrChan(d.errChan)
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
			/* disabled
			if len(outoforderBacklog) > 0 {
				log.Printf("ResponseDemuxer: got no cmdInfo but have outoforderBacklog: %d [%v]", len(outoforderBacklog), outoforderBacklog)
				if _, exists := outoforderBacklog[d.LastID+1]; exists {
					log.Printf("ResponseDemuxer: pre-processing out-of-order backlog cmdID=%d d.LastID=%d", d.LastID+1, d.LastID)
					continue loop
				}
			}
			*/
			//log.Printf("ResponseDemuxer: nothing to process, waiting on signalChan")
			//<-d.signalChan
			continue loop
		}
		if d.LastID+1 != cmdInfo.CmdID {
			log.Printf("ResponseDemuxer: WARNING - out-of-order cmdID received, expected %d got %d", d.LastID+1, cmdInfo.CmdID)
			outoforderBacklog[cmdInfo.CmdID] = cmdInfo
			continue loop
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
			common.SignalErrChan(d.errChan)
			log.Printf("ResponseDemuxer: error reading response for cmdID=%d: %v", cmdInfo.CmdID, err)
			return
		}
		// Dispatch based on registered type
		switch cmdInfo.RespType {
		case TYPE_CHECK:
			select {
			case d.checkResponseChan <- GetResponseData(cmdInfo.CmdID, code, line, err):
				// Dispatched successfully
				//log.Printf("ResponseDemuxer: dispatched CHECK response cmdID=%d d.checkResponseChan=%d", cmdInfo.CmdID, len(d.checkResponseChan))
			case <-d.errChan:
				log.Printf("ResponseDemuxer: got errChan while dispatching CHECK response, exiting")
				common.SignalErrChan(d.errChan)
				return
			}

		case TYPE_TAKETHIS:
			select {
			case d.ttResponseChan <- GetResponseData(cmdInfo.CmdID, code, line, err):
				// Dispatched successfully
				//log.Printf("ResponseDemuxer: dispatched TAKETHIS response cmdID=%d d.ttResponseChan=%d", cmdInfo.CmdID, len(d.ttResponseChan))
			case <-d.errChan:
				common.SignalErrChan(d.errChan)
				log.Printf("ResponseDemuxer: got errChan while dispatching TAKETHIS response, exiting")
				return
			}

		default:
			log.Printf("ResponseDemuxer: WARNING - unknown command type for cmdID=%d, signaling ERROR", cmdInfo.CmdID)
			common.SignalErrChan(d.errChan)
		}
	}
}

// GetStatistics returns current demuxer statistics
func (d *ResponseDemuxer) GetDemuxerStats() (pendingCommands int64, checkResponsesQueued int64, ttResponsesQueued int64, lastRequest time.Time) {

	pendingCommands = int64(len(d.cmdIDChan))
	checkResponsesQueued = int64(len(d.checkResponseChan))
	ttResponsesQueued = int64(len(d.ttResponseChan))

	d.lastRequestMux.RLock()
	lastRequest = d.lastRequest
	d.lastRequestMux.RUnlock()

	return pendingCommands, checkResponsesQueued, ttResponsesQueued, lastRequest
}
