package web

import (
	"log"
	"time"
)

// StartSessionCleanup starts a background goroutine to clean up expired sessions.
// The goroutine stops on Shutdown.
func (s *WebServer) StartSessionCleanup() {
	go func() {
		for {
			select {
			case <-s.stopCh:
				log.Printf("[WEB]: Session cleanup stopped")
				return
			case <-time.After(15 * time.Minute):
			}
			if err := s.DB.CleanupExpiredSessions(); err != nil {
				log.Printf("Error cleaning up expired sessions: %v", err)
			}
			log.Printf("Session cleanup completed at %s", time.Now().Format(time.RFC3339))
		}
	}()

	log.Println("Started session cleanup background task")
}
