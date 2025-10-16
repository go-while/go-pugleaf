package nntp

import (
	"log"
	"sync"
	"time"
)

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
		checkResponseChan: make(chan *ResponseData, 64*1024), // Buffer for CHECK responses
		ttResponseChan:    make(chan *ResponseData, 64*1024), // Buffer for TAKETHIS responses
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

	go func() {
		// keep alive
		for {
			time.Sleep(1 * time.Second)
			select {
			case d.signalChan <- struct{}{}:
			default:
			}
		}
	}()
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
			select {
			case d.errChan <- struct{}{}:
			default:
			}
			// exit
			return
		default:
			// pass
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
		// Dispatch based on registered type
		switch cmdInfo.RespType {
		case TYPE_CHECK:
			select {
			case d.checkResponseChan <- GetResponseData(cmdInfo.CmdID, code, line, err):
				// Dispatched successfully
				//log.Printf("ResponseDemuxer: dispatched CHECK response cmdID=%d d.checkResponseChan=%d", cmdInfo.CmdID, len(d.checkResponseChan))
			case <-d.errChan:
				log.Printf("ResponseDemuxer: got errChan while dispatching CHECK response, exiting")
				d.errChan <- struct{}{}
				return
			}

		case TYPE_TAKETHIS:
			select {
			case d.ttResponseChan <- GetResponseData(cmdInfo.CmdID, code, line, err):
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

// GetStatistics returns current demuxer statistics
func (d *ResponseDemuxer) GetStatistics() (pendingCommands int, checkResponsesQueued int, ttResponsesQueued int) {
	d.cmdIDQMux.RLock()
	pendingCommands = len(d.cmdIDQ)
	d.cmdIDQMux.RUnlock()

	checkResponsesQueued = len(d.checkResponseChan)
	ttResponsesQueued = len(d.ttResponseChan)

	return pendingCommands, checkResponsesQueued, ttResponsesQueued
}
