package common

import (
	"log"
	"sync"
)

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
	// try acquire lock
	lockChan <- struct{}{}
}

func ChanRelease(lockChan chan struct{}) {
	// release lock
	<-lockChan
}

var StructChansCap1 = make(chan chan struct{}, 16384)

// GetStructChanCap1 returns a recycled chan struct{} or makes a new one with capacity of 1 if none are available
func GetStructChanCap1() chan struct{} {
	select {
	case ch := <-StructChansCap1:
		return ch
	default:
		return make(chan struct{}, 1)
	}
}

// RecycleStructChan recycles a chan struct{} for later use
func RecycleStructChanCap1(ch chan struct{}) {
	if cap(ch) != 1 {
		log.Printf("Warning: Attempt to recycle chan struct{} with wrong capacity: %d", cap(ch))
		return
	}
	// empty out the channel
	select {
	case <-ch:
		// successfully emptied
	default:
		// is already empty
	}
	// recycle it
	select {
	case StructChansCap1 <- ch:
		// successfully recycled
	default:
		log.Printf("Warning: RecycleStructChan buffer full: %d", len(StructChansCap1))
		// recycle buffer full, let it go
	}
}
