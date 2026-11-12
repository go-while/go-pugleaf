package database

import (
	"testing"
	"time"
)

// TestFlushDue: the timer flush comes sooner the more articles are queued, from
// BatchInterval down to minFlushInterval, and never later than BatchInterval.
func TestFlushDue(t *testing.T) {
	const batch = 1000
	iv := 3 * time.Second
	for _, tc := range []struct {
		queued int
		want   time.Duration
	}{
		{0, iv},
		{250, iv},
		{500, 1500 * time.Millisecond},
		{1000, 750 * time.Millisecond},
		{3000, 250 * time.Millisecond},
		{6000, minFlushInterval},
		{100000, minFlushInterval},
	} {
		if got := flushDue(tc.queued, batch, iv); got != tc.want {
			t.Errorf("flushDue(%d) = %v, want %v", tc.queued, got, tc.want)
		}
	}
	// a tiny batch size must not divide by zero or exceed the interval
	if got := flushDue(5, 1, iv); got < minFlushInterval || got > iv {
		t.Errorf("flushDue(5, 1) = %v", got)
	}
}
