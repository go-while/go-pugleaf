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
