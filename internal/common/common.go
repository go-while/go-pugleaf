package common

import "sync"

var shutdownMutex sync.Mutex
var closedShutdownChan bool
var ShutdownChan = make(chan struct{})

func ForceShutdown() {
	shutdownMutex.Lock()
	defer shutdownMutex.Unlock()
	if !closedShutdownChan {
		close(ShutdownChan)
		closedShutdownChan = true
	}
}

func WantShutdown() bool {
	select {
	case _, ok := <-ShutdownChan:
		if !ok {
			// channel is closed
			return true
		}
	default:
	}
	return false
}

func IsClosedChannel(ch chan struct{}) bool {
	select {
	case _, ok := <-ch:
		if !ok {
			// channel is closed
			return true
		}
	default:
	}
	return false
}

func ChanLock(lockChan chan struct{}) {
	// try aquire lock
	lockChan <- struct{}{}
}

func ChanRelease(lockChan chan struct{}) {
	// release lock
	<-lockChan
}
