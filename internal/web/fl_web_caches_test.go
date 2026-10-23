package web

// Test for slice fl-web of plan followup-leftovers: the stop guard of the global caches is per
// cache instance, so caches re-initialised after a Shutdown are stopped by the next one and no
// instance is stopped twice (NewsgroupCache.Stop is a bare close).

import (
	"testing"
	"time"

	"github.com/go-while/go-pugleaf/internal/cache"
)

// flWebStopped reports whether nc's cleanup channel is closed: a second Stop then panics.
func flWebStopped(nc *cache.NewsgroupCache) (stopped bool) {
	defer func() {
		if recover() != nil {
			stopped = true
		}
	}()
	nc.Stop()
	return false
}

func TestFlWebStopCachesPerInstance(t *testing.T) {
	sc1 := cache.NewSanitizedCache(10, time.Minute)
	nc1 := cache.NewNewsgroupCache(10, time.Minute)
	stopCaches(sc1, nc1)
	stopCaches(sc1, nc1) // repeated Shutdown: no second close
	stopCaches(nil, nil)

	// Re-initialised caches (new instances) are stopped by the next call.
	sc2 := cache.NewSanitizedCache(10, time.Minute)
	nc2 := cache.NewNewsgroupCache(10, time.Minute)
	stopCaches(sc2, nc2)
	if !flWebStopped(nc2) {
		t.Fatal("the re-initialised newsgroup cache was not stopped")
	}
	// Going back to an instance stopped earlier does not stop it again (it would panic).
	stopCaches(sc1, nc1)
	stopCaches(sc2, nil)
	if !flWebStopped(nc1) {
		t.Fatal("the first newsgroup cache was not stopped")
	}
	sc2.Stop() // SanitizedCache.Stop is idempotent per instance
}
