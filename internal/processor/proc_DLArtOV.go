package processor

import (
	"fmt"
	"log"
	"time"

	"github.com/go-while/go-pugleaf/internal/history"
)

// DownloadArticlesViaOverview fetches full articles and stores them in the articles DB.
func (proc *Processor) DownloadArticlesViaOverview(groupName string) error {

	if err := proc.ImportOverview(groupName); err != nil {
		return err
	}

	groupDBs, err := proc.DB.GetGroupDBs(groupName)
	if err != nil {
		log.Printf("DownloadArticlesViaOverview: Failed to get group DBs for %s: %v", groupName, err)
		return err
	}
	defer groupDBs.Return(proc.DB)
	// Only fetch undownloaded overviews, in batches
	undl, err := proc.DB.GetUndownloadedOverviews(groupDBs, int(MaxBatch))
	if err != nil {
		return err
	}
	if len(undl) == 0 {
		log.Printf("DownloadArticlesViaOverview: No undownloaded articles for group '%s'", groupName)
		return nil
	}
	_, err = proc.Pool.SelectGroup(groupName) // Ensure remote has the group
	if err != nil {
		return fmt.Errorf("DownloadArticlesVO: Failed to select group '%s': %v", groupName, err)
	}
	log.Printf("proc.Pool.Backend=%#v", proc.Pool.Backend)
	batchQueue := make(chan *batchItem, len(undl))
	returnChan := make(chan *batchItem, len(undl))
	// launch goroutines to fetch articles in parallel
	runthis := int(float32(proc.Pool.Backend.MaxConns) * 0.75) // Use 75% of max connections for fetching articles
	if runthis < 1 {
		runthis = 1 // Ensure at least one goroutine runs
	}
	log.Printf("DownloadArticlesViaOverview: Fetching %d articles for group '%s' using %d goroutines", len(undl), groupName, runthis)
	for i := 1; i <= runthis; i++ {
		// Fetch article in a goroutines
		go func(worker int) {
			log.Printf("DownloadArticlesViaOverview: Worker %d group '%s' start", worker, groupName)
			defer func() {
				log.Printf("DownloadArticlesViaOverview: Worker %d group '%s' quit", worker, groupName)
			}()
			var processed int64
			for item := range batchQueue {
				//log.Printf("DownloadArticlesViaOverview: Worker %d processing group '%s' article %d (%s)", worker, *item.GroupName, *item.ArticleNum, *item.MessageID)
				art, err := proc.Pool.GetArticle(item.MessageID)
				if err != nil {
					log.Printf("DownloadArticlesViaOverview: group '%s' Failed to fetch article %s: %v", groupName, item.MessageID, err)
					item.Error = err   // Set error on item
					returnChan <- item // Send failed item back
					return
				}
				processed++
				item.Article = art // set pointer
				returnChan <- item // Send back the successfully imported article
			} // end for item
			log.Printf("DownloadArticlesViaOverview: Worker %d group '%s' processed %d articles", worker, groupName, processed)
		}(i)
	} // end for runthis anonymous go routines

	// for every undownloaded overview entry, create a batch item
	//batchMap := make(map[int64]*batchItem, len(undl))
	var batchList []*batchItem
	for _, ov := range undl {
		msgID := ov.MessageID
		num := ov.ArticleNum
		/*
			row := groupDBs.DB.QueryRow("SELECT 1 FROM articles WHERE message_id = ? LIMIT 1", msgID)
			var exists int
			if err := row.Scan(&exists); err == nil {
				// Already exists, mark as downloaded if not alread
				if ov.Downloaded == 0 {
					//err = proc.DB.SetOverviewDownloaded(groupDBs, num, 1)
					if err != nil {
						log.Printf("DownloadArticlesViaOverview: group '%s' Failed to mark article %d (%s) as downloaded: %v", groupName, num, msgID, err)
						continue
					}
					log.Printf("DownloadArticlesViaOverview: group '%s' Marked article %d (%s) as downloaded", groupName, num, msgID)

				}
				log.Printf("DownloadArticlesViaOverview:  group '%s' Article %d (%s) already exists in articles DB, skipping import", groupName, num, msgID)
				continue
			}
		*/
		item := &batchItem{
			MessageID:  msgID,
			ArticleNum: num,
			GroupName:  &groupName,
			// Article: nil, // will be set by the goroutine
		}
		batchQueue <- item                  // send to batch queue
		batchList = append(batchList, item) // also add to batchList for later processing
		//log.Printf("DownloadArticlesViaOverview: Queued article %d (%s) for group '%s'", num, msgID, groupName)
	} // end for undl

	gots := 0
	errs := 0
	ticker := time.NewTicker(50 * time.Millisecond)
	start := time.Now()
	nextCheck := start.Add(5 * time.Second)
	lastGots := 0
	lastErrs := 0
	deathCounter := 11 // Counter to track if we are stuck
	// Start processing loop
forProcessing:
	for {
		select {
		case <-ticker.C:
			//log.Printf("DownloadArticlesViaOverview: group '%s' %d, gots: %d, errs: %d, batchQueue len: %d", groupName, deathCounter, gots, errs, len(batchQueue))
			// Periodically check if we have may be stuck
			if gots+errs >= len(undl) {
				log.Printf("DownloadArticlesViaOverview: group '%s' All %d (gots: %d, errs: %d) articles processed, closing batch channel", groupName, gots+errs, gots, errs)
				close(batchQueue)   // Close channel to stop goroutines
				break forProcessing // Exit processing loop if all items are processed
			}
			if gots > lastGots || errs > lastErrs {
				nextCheck = time.Now().Add(5 * time.Second) // Reset last check time
				lastGots = gots
				lastErrs = errs
				deathCounter = 11 // Reset death counter on progress
			}
			if nextCheck.Before(time.Now()) {
				// If we haven't made progress in 5 seconds, log a warning
				log.Printf("DownloadArticlesViaOverview: group '%s' Stuck? %d articles processed (%d got, %d errs) in last 5 seconds", groupName, gots+errs, gots, errs)
				nextCheck = time.Now().Add(5 * time.Second) // Reset last check time
				deathCounter--
			}
			if deathCounter <= 0 {
				log.Printf("DownloadArticlesViaOverview: group '%s' Timeout... stopping import deathCounter=%d", groupName, deathCounter)
				close(batchQueue) // Close channel to stop goroutines
				return fmt.Errorf("DownloadArticlesViaOverview: group '%s' Timeout... %d articles processed (%d got, %d errs)", groupName, gots+errs, gots, errs)
			}

		case item := <-returnChan:
			if item.Error != nil {
				log.Printf("DownloadArticlesViaOverview: group '%s' Error fetching article %s: %v", groupName, item.MessageID, item.Error)
				errs++
			} else {
				//log.Printf("DownloadArticlesViaOverview: group '%s' fetched article %d (%s)", groupName, *item.ArticleNum, *item.MessageID)
				gots++
			}
		}
	}

	// now loop over the batchList and insert articles
	for _, item := range batchList {
		if item == nil {
			continue // Skip nil items (not fetched)
		}
		if item.Article == nil {
			log.Printf("DownloadArticlesViaOverview: group '%s' Article %d (%s) was not fetched successfully, breaking import", groupName, item.ArticleNum, item.MessageID)
			return fmt.Errorf("internal/processor: DownloadArticlesViaOverview group '%s' article %d (%s) was not fetched successfully", groupName, item.ArticleNum, item.MessageID)
		}
		bulkmode := true
		response, err := proc.processArticle(item.Article, groupName, bulkmode)
		if err != nil {
			log.Printf("DownloadArticlesViaOverview:  group '%s' Failed to process article %d (%s): %v", groupName, item.ArticleNum, item.MessageID, err)
			continue // Skip this item on error
		}
		if response == history.CasePass {
			log.Printf("DownloadArticlesViaOverview:  group '%s' imported article %d (%s)", groupName, item.ArticleNum, item.MessageID)
		}

	}

	return nil
} // end func DownloadArticlesViaOverview
