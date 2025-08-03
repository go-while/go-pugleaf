package web

import (
	"log"
	"time"
)

// StartSessionCleanup starts a background goroutine to clean up expired sessions
func (s *WebServer) StartSessionCleanup() {
	go func() {
		for {
			time.Sleep(15 * time.Minute)
			if err := s.DB.CleanupExpiredSessions(); err != nil {
				log.Printf("Error cleaning up expired sessions: %v", err)
			}
			log.Printf("Session cleanup completed at %s", time.Now().Format(time.RFC3339))
		}
	}()

	log.Println("Started session cleanup background task")
}
