package nntp

import (
	"sync"
	"time"
)

// ServerStats tracks NNTP server statistics
type ServerStats struct {
	mux               sync.RWMutex
	startTime         time.Time
	activeConnections int64
	totalConnections  int64
	commandCounts     map[string]int64
	authSuccesses     int64
	authFailures      int64
}

// NewServerStats creates a new server statistics tracker
func NewServerStats() *ServerStats {
	return &ServerStats{
		startTime:     time.Now(),
		commandCounts: make(map[string]int64),
	}
}

// ConnectionStarted increments the connection counters
func (s *ServerStats) ConnectionStarted() {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.activeConnections++
	s.totalConnections++
}

// ConnectionEnded decrements the active connection counter
func (s *ServerStats) ConnectionEnded() {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.activeConnections--
}

// GetActiveConnections returns the current number of active connections
func (s *ServerStats) GetActiveConnections() int {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return int(s.activeConnections)
}

// GetTotalConnections returns the total number of connections since startup
func (s *ServerStats) GetTotalConnections() int64 {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.totalConnections
}

// knownStatCommands are the command words counted under their own name: the
// dispatched commands plus the RFC 3977/4644 words other plans add. Every other
// word is counted under "UNKNOWN", so junk input can't grow the map (SEC-4).
var knownStatCommands = map[string]bool{
	"CAPABILITIES": true, "MODE": true, "AUTHINFO": true, "QUIT": true, "HELP": true, "LIST": true,
	"GROUP": true, "LISTGROUP": true, "STAT": true, "HEAD": true, "BODY": true, "ARTICLE": true,
	"XOVER": true, "XHDR": true, "POST": true, "IHAVE": true, "TAKETHIS": true,
	"DATE": true, "NEXT": true, "LAST": true, "NEWGROUPS": true, "NEWNEWS": true, "OVER": true,
	"HDR": true, "CHECK": true, "STARTTLS": true,
}

// CommandExecuted increments the counter for a specific command
// (words not in knownStatCommands are counted as "UNKNOWN").
func (s *ServerStats) CommandExecuted(command string) {
	if !knownStatCommands[command] {
		command = "UNKNOWN"
	}
	s.mux.Lock()
	defer s.mux.Unlock()
	s.commandCounts[command]++
}

// GetCommandCount returns the execution count for a specific command
func (s *ServerStats) GetCommandCount(command string) int64 {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.commandCounts[command]
}

// GetAllCommandCounts returns a copy of all command counts
func (s *ServerStats) GetAllCommandCounts() map[string]int64 {
	s.mux.RLock()
	defer s.mux.RUnlock()

	counts := make(map[string]int64)
	for cmd, count := range s.commandCounts {
		counts[cmd] = count
	}
	return counts
}

// AuthSuccess increments the successful authentication counter
func (s *ServerStats) AuthSuccess() {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.authSuccesses++
}

// AuthFailure increments the failed authentication counter
func (s *ServerStats) AuthFailure() {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.authFailures++
}

// GetAuthStats returns authentication statistics
func (s *ServerStats) GetAuthStats() (successes, failures int64) {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.authSuccesses, s.authFailures
}

// GetUptime returns how long the server has been running
func (s *ServerStats) GetUptime() time.Duration {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return time.Since(s.startTime)
}

// Reset resets all statistics (except start time)
func (s *ServerStats) Reset() {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.totalConnections = 0
	s.commandCounts = make(map[string]int64)
	s.authSuccesses = 0
	s.authFailures = 0
}
