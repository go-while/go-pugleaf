package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
)

func main() {
	var groupName string
	flag.StringVar(&groupName, "group", "", "Newsgroup name to fix (empty = fix all groups)")
	flag.Parse()

	db, err := database.OpenDatabase(nil)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Shutdown()

	if groupName != "" {
		log.Printf("Fixing thread activity for group: %s", groupName)
		if err := fixGroupThreadActivity(db, groupName); err != nil {
			log.Fatalf("Failed to fix group %s: %v", groupName, err)
		}
	} else {
		log.Printf("Fixing thread activity for all groups...")
		groups, err := db.MainDBGetAllNewsgroups()
		if err != nil {
			log.Fatalf("Failed to get newsgroups: %v", err)
		}

		fixed := 0
		errors := 0
		for _, group := range groups {
			if group.Name == "" {
				continue
			}
			log.Printf("Fixing group: %s", group.Name)
			if err := fixGroupThreadActivity(db, group.Name); err != nil {
				log.Printf("Error fixing group %s: %v", group.Name, err)
				errors++
			} else {
				fixed++
			}
		}
		log.Printf("Completed: %d groups fixed, %d errors", fixed, errors)
	}
}

func fixGroupThreadActivity(db *database.Database, groupName string) error {
	groupDBs, err := db.GetGroupDBs(groupName)
	if err != nil {
		return fmt.Errorf("failed to get group DB: %w", err)
	}
	defer groupDBs.Return(db)

	// Get only thread cache entries that have future last_activity timestamps
	rows, err := groupDBs.DB.Query(`
		SELECT thread_root, child_articles, last_activity
		FROM thread_cache
		WHERE last_activity > datetime('now', '+25 hour')
		ORDER BY thread_root`)
	if err != nil {
		return fmt.Errorf("failed to query thread cache: %w", err)
	}
	defer rows.Close()

	type threadInfo struct {
		root          int64
		childArticles string
		lastActivity  time.Time
	}

	var threads []threadInfo
	for rows.Next() {
		var t threadInfo
		var lastActivityStr sql.NullString
		if err := rows.Scan(&t.root, &t.childArticles, &lastActivityStr); err != nil {
			return fmt.Errorf("failed to scan thread: %w", err)
		}
		if lastActivityStr.Valid {
			if parsed, err := time.Parse("2006-01-02 15:04:05", lastActivityStr.String); err == nil {
				t.lastActivity = parsed
			}
		}
		threads = append(threads, t)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating threads: %w", err)
	}

	log.Printf("Found %d threads with future last_activity in group %s", len(threads), groupName)

	// Fix each thread
	updatedCount := 0
	for _, thread := range threads {
		// Build list of all articles in this thread
		articleNums := []int64{thread.root}
		if thread.childArticles != "" {
			parts := strings.Split(thread.childArticles, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" {
					var articleNum int64
					if _, err := fmt.Sscanf(part, "%d", &articleNum); err == nil {
						articleNums = append(articleNums, articleNum)
					}
				}
			}
		}

		// Find the most recent non-hidden article in this thread
		var maxDate time.Time
		var found bool

		for _, articleNum := range articleNums {
			var dateSent time.Time
			var hide int
			err := groupDBs.DB.QueryRow(`
				SELECT date_sent, hide
				FROM articles
				WHERE article_num = ?`, articleNum).Scan(&dateSent, &hide)

			if err != nil {
				continue // Article not found, skip
			}

			// Skip hidden articles and obvious future posts
			if hide != 0 || dateSent.After(time.Now().Add(2*time.Hour)) {
				continue
			}

			if !found || dateSent.After(maxDate) {
				maxDate = dateSent
				found = true
			}
		}

		// Update thread cache if we found a valid date
		if found && !maxDate.Equal(thread.lastActivity) {
			_, err := groupDBs.DB.Exec(`
				UPDATE thread_cache
				SET last_activity = ?
				WHERE thread_root = ?`, maxDate, thread.root)

			if err != nil {
				log.Printf("Failed to update thread %d: %v", thread.root, err)
				continue
			}

			updatedCount++
			if maxDate.After(time.Now()) {
				log.Printf("Thread %d: updated %v -> %v (still future!)",
					thread.root, thread.lastActivity.Format("2006-01-02 15:04:05"), maxDate.Format("2006-01-02 15:04:05"))
			}
		}
	}

	log.Printf("Group %s: updated %d/%d threads", groupName, updatedCount, len(threads))
	return nil
}
