package web

import (
	"log"
	"time"
)

// StartSessionCleanup starts a background goroutine to clean up expired sessions.
// The goroutine stops on Shutdown or when db.StopChan is closed, and holds a db.WG slot
// so the database is not shut down while a cleanup query runs.
func (s *WebServer) StartSessionCleanup() {
	s.DB.WG.Add(1)
	go func() {
		defer s.DB.WG.Done()
		for {
			select {
			case <-s.stopCh:
				log.Printf("[WEB]: Session cleanup stopped")
				return
			case <-s.DB.StopChan:
				log.Printf("[WEB]: Session cleanup stopped (database shutdown)")
				return
			case <-time.After(15 * time.Minute):
			}
			select {
			case <-s.stopCh:
				log.Printf("[WEB]: Session cleanup stopped")
				return
			case <-s.DB.StopChan:
				log.Printf("[WEB]: Session cleanup stopped (database shutdown)")
				return
			default:
			}
			if err := s.DB.CleanupExpiredSessions(); err != nil {
				log.Printf("Error cleaning up expired sessions: %v", err)
			}
			log.Printf("Session cleanup completed at %s", time.Now().Format(time.RFC3339))
		}
	}()

	log.Println("Started session cleanup background task")
}
