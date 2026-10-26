package main

// Tests of the followup-leftovers slice fl-ops-tools (fix-thread-activity): the first
// SIGINT/SIGTERM asks main to stop after the current newsgroup, a second one exits at
// once (through ftaExit in the tool; a counter here). Synchronous: the signals are
// queued in a buffered channel, no real signal is sent.

import (
	"os"
	"syscall"
	"testing"
)

func TestFlOpsFtaSignalLoop(t *testing.T) {
	cases := []struct {
		name               string
		sigs               []os.Signal
		wantStop, wantExit int
	}{
		{"none", nil, 0, 0},
		{"one", []os.Signal{syscall.SIGTERM}, 1, 0},
		{"two", []os.Signal{os.Interrupt, os.Interrupt}, 1, 1},
	}
	for _, c := range cases {
		ch := make(chan os.Signal, len(c.sigs))
		for _, s := range c.sigs {
			ch <- s
		}
		close(ch) // the loop returns at the close when fewer than two signals came
		var stops, exits int
		ftaSignalLoop(ch, func() { stops++ }, func() { exits++ })
		if stops != c.wantStop || exits != c.wantExit {
			t.Errorf("%s: requestStop %d, exitNow %d; want %d, %d", c.name, stops, exits, c.wantStop, c.wantExit)
		}
	}
}
