package processor

import (
	"log"
	"sync"
)

var GroupCounter = &Counter{
	Map: make(map[string]int64),
}

type Counter struct {
	mux sync.Mutex       // Mutex to protect map access
	Map map[string]int64 // Map to count articles per group
}

func NewCounter() *Counter {
	return &Counter{
		Map: make(map[string]int64, 256), // Initialize with a reasonable size
	}
}

func (*Counter) GetReset(group string) int64 {
	GroupCounter.mux.Lock()
	defer GroupCounter.mux.Unlock()
	if count, ok := GroupCounter.Map[group]; ok {
		delete(GroupCounter.Map, group) // Reset the count for the group
		log.Printf("Resetting counter for group '%s', previous count was %d", group, count)
		return count
	}
	log.Printf("No counter found for group '%s', nothing to reset", group)
	return 0
}

func (*Counter) GetResetAll() map[string]int64 {
	GroupCounter.mux.Lock()
	defer GroupCounter.mux.Unlock()
	retmap := make(map[string]int64, len(GroupCounter.Map))
	for group, count := range GroupCounter.Map {
		retmap[group] = count // Copy current counts to return map
	}
	GroupCounter.Map = make(map[string]int64) // Reset the map
	log.Printf("Resetting all group counters, returning %d counters", len(retmap))
	return retmap
}

func (*Counter) Add(group string, value int64) {
	GroupCounter.mux.Lock()
	defer GroupCounter.mux.Unlock()
	if _, ok := GroupCounter.Map[group]; !ok {
		GroupCounter.Map[group] = 0 // Initialize if not present
	}
	GroupCounter.Map[group] += value
	//log.Printf("Incremented counter for group '%s', new count is %d", group, GroupCounter.Map[group])
}

func (*Counter) Increment(group string) {
	GroupCounter.mux.Lock()
	defer GroupCounter.mux.Unlock()
	if _, ok := GroupCounter.Map[group]; !ok {
		GroupCounter.Map[group] = 0 // Initialize if not present
	}
	GroupCounter.Map[group]++
}
