package processor

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

// ImportOverview fetches XOVER data for a group and stores it in the overview DB.
func (proc *Processor) ImportOverview(groupName string) error {
	groupDBs, err := proc.DB.GetGroupDBs(groupName)
	if err != nil {
		return err
	}
	defer groupDBs.Return(proc.DB)
	/*
		defer func() {
			err := proc.DB.CloseGroupDBs()
			if err != nil {
				log.Printf("ImportOverview: Failed to close group DBs for %s: %v", groupName, err)
			}
		}()
	*/
	groupInfo, err := proc.Pool.SelectGroup(groupName) // Ensure remote has the group
	if err != nil {
		return fmt.Errorf("DownloadArticles: Failed to select group '%s': %v", groupName, err)
	}
	// Efficiently find the highest article number already in articles table
	var maxNum sql.NullInt64
	if err := database.RetryableQueryRowScan(groupDBs.DB, "SELECT MAX(article_num) FROM articles", nil, &maxNum); err != nil {
		return err
	}
	start := groupInfo.First           // Start from the first article in the remote group
	end := start + int64(MaxBatch) - 1 // End at the last article in the remote group
	if end > groupInfo.Last {
		end = groupInfo.Last
	}
	if maxNum.Valid && maxNum.Int64 >= start {
		start = maxNum.Int64 + 1
		end = start + int64(MaxBatch) - 1
		if end > groupInfo.Last {
			end = groupInfo.Last
		}
		log.Printf("ImportOverview: Adjusting start to %d based on existing articles for newsgroup '%s' last=%d", start, groupName, maxNum.Int64)
	}
	if start > end {
		log.Printf("ImportOverview: No new data to import for newsgroup '%s' (last=%d) [fetch start=%d end=%d] [remote first=%d last=%d]", groupName, maxNum.Int64, start, end, groupInfo.First, groupInfo.Last)
		return nil
	}
	toFetch := end - start + 1
	if toFetch <= 0 {
		log.Printf("ImportOverview: No data to fetch for newsgroup '%s' (start=%d, end=%d)", groupName, start, end)
		return nil
	}
	log.Printf("ImportOverview: Fetching XOVER for %s from %d to %d (last known: %d)", groupName, start, end, maxNum.Int64)
	enforceLimit := true // Enforce limit to avoid fetching too many articles at once
	overviewsNew, err := proc.Pool.XOver(groupName, start, end, enforceLimit)
	if err != nil {
		return err
	}

	importedCount := 0
	for _, ov := range overviewsNew {
		// Parse date string to time.Time
		var date time.Time
		if ov.Date != "" {
			date = ParseNNTPDate(ov.Date)
			// Handle parsing errors gracefully
		}
		o := &models.Overview{
			Subject:    ov.Subject, // Store raw subject as received from network
			FromHeader: ov.From,    // Store raw From header as received from network
			DateSent:   date,
			DateString: ov.Date, // Store raw date string for old/malformed dates
			MessageID:  ov.MessageID,
			References: ov.References,
			Bytes:      int(ov.Bytes),
			Lines:      int(ov.Lines),
			ReplyCount: 0, // Initialize to 0, will be updated when replies are found
		}
		if num, err := proc.DB.InsertOverview(groupDBs, o); err != nil || num == 0 {
			log.Printf("Failed to insert overview for article %d: %v", num, err)
		} else {
			importedCount++
		}
	}

	// In ImportOverview after inserting overviews
	log.Printf("ImportOverview: Inserted %d overviews to newsgroup '%s', forcing commit", importedCount, groupName)
	if tx, err := groupDBs.DB.Begin(); err == nil {
		tx.Commit() // Force any pending transactions to commit
		log.Printf("ImportOverview: Forced commit completed newsgroup '%s'", groupName)
	} else {
		log.Printf("ImportOverview: Could not begin transaction for commit newsgroup '%s': %v", groupName, err)
	}
	// After overview inserts, force WAL to sync
	_, err = database.RetryableExec(groupDBs.DB, "PRAGMA wal_checkpoint(FULL)")
	if err != nil {
		log.Printf("ImportOverview: WAL checkpoint failed newsgroup '%s': %v", groupName, err)
	} else {
		log.Printf("ImportOverview: WAL checkpoint completed newsgroup '%s'", groupName)
	}
	// After all overview inserts in ImportOverview
	_, err = database.RetryableExec(groupDBs.DB, "PRAGMA synchronous = FULL")
	if err != nil {
		log.Printf("Warning: Could not set synchronous mode: %v", err)
	}
	return nil
} // end func ImportOverview
