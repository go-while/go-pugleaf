package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/history"
)

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	log.Printf("Starting go-pugleaf MsgIdItemCache Test (version: %s)", config.AppVersion)
	// Command line flags
	numItems := flag.Int("items", 100000, "Number of message IDs to test")
	numWorkers := flag.Int("workers", 1, "Number of concurrent workers")
	testDuration := flag.Duration("duration", 10*time.Second, "Duration for concurrent stress test")
	concurrentTest := flag.Bool("concurrent", false, "Run concurrent stress test")
	flag.Parse()

	fmt.Printf("Testing MsgIdItemCache with %d unique message IDs...\n", *numItems)
	if *concurrentTest {
		fmt.Printf("Will run concurrent stress test with %d workers for %v\n", *numWorkers, *testDuration)
	}

	// Get baseline memory stats
	baselineMem := getMemStats()
	printMemStats("Baseline Memory", baselineMem)

	// Initialize the cache
	cache := history.NewMsgIdItemCache()

	afterCacheMem := getMemStats()
	fmt.Printf("\nCache initialization overhead: %.2f MB\n",
		float64(afterCacheMem.HeapAlloc-baselineMem.HeapAlloc)/(1024*1024))

	// Generate and insert message IDs
	start := time.Now()
	messageIds := make([]string, *numItems)

	fmt.Printf("\nGenerating %d unique message IDs...\n", *numItems)
	for i := 0; i < *numItems; i++ {
		// Generate unique message ID in a realistic format
		messageIds[i] = fmt.Sprintf("<%d.%d.%d@example.com>",
			time.Now().UnixNano(),
			rand.Int63(),
			i)
	}
	genTime := time.Since(start)
	fmt.Printf("Generated %d message IDs in %v\n", *numItems, genTime)

	afterGenMem := getMemStats()
	fmt.Printf("Memory after string generation: %.2f MB (+ %.2f MB)\n",
		float64(afterGenMem.HeapAlloc)/(1024*1024),
		float64(afterGenMem.HeapAlloc-afterCacheMem.HeapAlloc)/(1024*1024))

	// Insert all message IDs into cache
	start = time.Now()
	fmt.Println("\nInserting into cache...")
	progressStep := *numItems / 4 // Show 4 progress updates
	if progressStep == 0 {
		progressStep = 1
	}

	for i, msgId := range messageIds {
		item := cache.GetORCreate(msgId)
		if item == nil {
			fmt.Printf("ERROR: Failed to create item for message ID %d: %s\n", i, msgId)
			return
		}
		if item.MessageId != msgId {
			fmt.Printf("ERROR: Item mismatch for message ID %d: expected %s, got %v\n", i, msgId, item.MessageId)
			return
		}

		// Progress indicator with memory stats
		if (i+1)%progressStep == 0 {
			currentMem := getMemStats()
			fmt.Printf("  Inserted %d items... (HeapAlloc: %.2f MB)\n", i+1, float64(currentMem.HeapAlloc)/(1024*1024))
		}
	}
	insertTime := time.Since(start)
	fmt.Printf("Inserted %d items in %v\n", *numItems, insertTime)

	afterInsertMem := getMemStats()
	printMemStats("\nMemory After Cache Population", afterInsertMem)

	cacheMemoryUsage := float64(afterInsertMem.HeapAlloc-afterGenMem.HeapAlloc) / (1024 * 1024)
	fmt.Printf("Cache memory usage: %.2f MB\n", cacheMemoryUsage)
	fmt.Printf("Memory per item: %.2f bytes\n", float64(afterInsertMem.HeapAlloc-afterGenMem.HeapAlloc)/float64(*numItems))

	// Get cache statistics and estimate memory
	fmt.Println("\n=== Cache Statistics ===")
	buckets, items, maxChainLength := cache.Stats()
	bucketCount, _, loadFactor, isResizing := cache.GetResizeInfo()
	fmt.Printf("Buckets used: %d / %d (%.2f%%)\n", buckets, bucketCount, float64(buckets)/float64(bucketCount)*100)
	fmt.Printf("Total items: %d\n", items)
	fmt.Printf("Max chain length: %d\n", maxChainLength)
	fmt.Printf("Average items per bucket: %.2f\n", float64(items)/float64(buckets))
	fmt.Printf("Load factor: %.2f\n", loadFactor)
	fmt.Printf("Currently resizing: %t\n", isResizing)

	// Memory usage estimation
	estimateMemoryUsage(cache, messageIds)

	if *concurrentTest {
		runConcurrentStressTest(cache, messageIds, *numWorkers, *testDuration)
	} else {
		runSequentialTests(cache, messageIds)
	}

	// Final cache statistics
	fmt.Println("\n=== Final Cache Statistics ===")
	buckets, items, maxChainLength = cache.Stats()
	bucketCount, _, loadFactor, isResizing = cache.GetResizeInfo()
	fmt.Printf("Buckets used: %d / %d (%.2f%%)\n", buckets, bucketCount, float64(buckets)/float64(bucketCount)*100)
	fmt.Printf("Total items: %d\n", items)
	fmt.Printf("Max chain length: %d\n", maxChainLength)
	fmt.Printf("Average items per bucket: %.2f\n", float64(items)/float64(buckets))
	fmt.Printf("Load factor: %.2f\n", loadFactor)
	fmt.Printf("Currently resizing: %t\n", isResizing)

	if maxChainLength > 20 {
		fmt.Printf("\nWARNING: Max chain length (%d) is quite high. Consider increasing cache size or improving hash function.\n", maxChainLength)
	}

	fmt.Println("\nTest completed successfully!")

	// Final memory stats
	memStats := getMemStats()
	printMemStats("After test completion", memStats)
}

func runSequentialTests(cache *history.MsgIdItemCache, messageIds []string) {
	numItems := len(messageIds)

	// Test retrieval performance
	fmt.Println("\n=== Testing Retrieval Performance ===")
	start := time.Now()
	found := 0
	notFound := 0

	// Test retrieving all existing items
	progressStep := numItems / 5 // Show 5 progress updates
	if progressStep == 0 {
		progressStep = 1
	}

	for i, msgId := range messageIds {
		item := cache.GetORCreate(msgId)
		if item != nil && item.MessageId == msgId {
			found++
		} else {
			notFound++
			fmt.Printf("ERROR: Failed to retrieve item %d: %s\n", i, msgId)
		}

		// Progress indicator
		if (i+1)%progressStep == 0 {
			fmt.Printf("  Tested %d retrievals...\n", i+1)
		}
	}
	retrievalTime := time.Since(start)
	fmt.Printf("Retrieved %d items in %v\n", numItems, retrievalTime)
	fmt.Printf("Found: %d, Not found: %d\n", found, notFound)

	// Additional verification - test single item timing
	fmt.Println("\n=== Single Item Timing Analysis ===")
	if numItems > 0 {
		testMsgId := messageIds[numItems/2] // Pick one from the middle

		// Warm up (eliminate any cold cache effects)
		for i := 0; i < 1000; i++ {
			cache.GetORCreate(testMsgId)
		}

		// Time single operations
		singleTimes := make([]time.Duration, 1000)
		for i := 0; i < 1000; i++ {
			singleStart := time.Now()
			item := cache.GetORCreate(testMsgId)
			singleTimes[i] = time.Since(singleStart)
			if item == nil {
				fmt.Printf("ERROR: Failed single retrieval test\n")
			}
		}

		// Calculate statistics
		var totalTime time.Duration
		minTime := singleTimes[0]
		maxTime := singleTimes[0]
		for _, t := range singleTimes {
			totalTime += t
			if t < minTime {
				minTime = t
			}
			if t > maxTime {
				maxTime = t
			}
		}
		avgTime := totalTime / 1000

		fmt.Printf("Single operation timing (1000 samples):\n")
		fmt.Printf("  Average: %v (%d ns)\n", avgTime, avgTime.Nanoseconds())
		fmt.Printf("  Min: %v (%d ns)\n", minTime, minTime.Nanoseconds())
		fmt.Printf("  Max: %v (%d ns)\n", maxTime, maxTime.Nanoseconds())
		fmt.Printf("  Theoretical max rate: %.0f ops/sec\n", float64(time.Second)/float64(avgTime))
	}

	// Test with randomized access pattern
	fmt.Println("\n=== Randomized Access Pattern Test ===")
	testSize := 10000
	if numItems < testSize {
		testSize = numItems
	}

	rand.Seed(time.Now().UnixNano())
	randomIndices := make([]int, testSize)
	for i := range randomIndices {
		randomIndices[i] = rand.Intn(len(messageIds))
	}

	start = time.Now()
	randomFound := 0
	for _, idx := range randomIndices {
		item := cache.GetORCreate(messageIds[idx])
		if item != nil && item.MessageId == messageIds[idx] {
			randomFound++
		}
	}
	randomTime := time.Since(start)
	fmt.Printf("Random access: %d items in %v\n", testSize, randomTime)
	fmt.Printf("Random access rate: %.0f items/sec\n", float64(testSize)/randomTime.Seconds())
	fmt.Printf("Random found: %d/%d\n", randomFound, testSize)

	// Test retrieval of non-existent items
	fmt.Println("\n=== Testing Non-existent Items ===")
	start = time.Now()
	nonExistentFound := 0
	for i := 0; i < 1000; i++ {
		fakeId := fmt.Sprintf("<fake.%d.%d@nowhere.com>", time.Now().UnixNano(), i)
		item := cache.GetORCreate(fakeId)
		if item != nil {
			nonExistentFound++
		}
	}
	nonExistentTime := time.Since(start)
	fmt.Printf("Tested 1k non-existent items in %v\n", nonExistentTime)
	fmt.Printf("Non-existent items created: %d\n", nonExistentFound)

	// Performance summary
	fmt.Println("\n=== Performance Summary ===")
	fmt.Printf("Retrieval rate: %.0f items/sec\n", float64(numItems)/retrievalTime.Seconds())

	// Test threading functionality
	fmt.Println("\n=== Testing Threading Functionality ===")

	// Test AddMsgIdToCache
	testGroup := "test.group"
	testMsgId := "<threading-test@example.com>"
	//testArtNum := int64(12345)

	//added := cache.AddMsgIdToCache(testGroup, testMsgId, testArtNum) // DEPRECATED; CAN MUTATE PTR DIRECTLY!
	//fmt.Printf("Added message to cache: %t\n", added)

	// Convert testGroup to pointer for the new API
	testGroupPtr := &testGroup
	nonexistentGroup := "nonexistent.group"
	nonexistentGroupPtr := &nonexistentGroup

	// Test GetMsgIdFromCache
	artNum, rootArticle, isRoot := cache.GetMsgIdFromCache(testGroupPtr, testMsgId)
	fmt.Printf("Retrieved from cache: ArtNum=%d, RootArticle=%d, IsRoot=%t\n", artNum, rootArticle, isRoot)

	// First, ensure the message has a group context
	item := cache.GetOrCreateForGroup(testMsgId, testGroupPtr)
	if item != nil {
		fmt.Printf("Created/got item for group context\n")
	}

	// Test SetThreadingInfoForGroup (group-aware method)
	rootArtNum := int64(12340)
	threadingSet := cache.SetThreadingInfoForGroup(testGroupPtr, testMsgId, rootArtNum, rootArtNum, false)
	fmt.Printf("Set threading info for group: %t\n", threadingSet)

	// Test retrieval after setting threading info
	artNum2, rootArticle2, isRoot2 := cache.GetMsgIdFromCache(testGroupPtr, testMsgId)
	fmt.Printf("After threading update: ArtNum=%d, RootArticle=%d, IsRoot=%t\n", artNum2, rootArticle2, isRoot2)
	// Test HasMessageIDInGroup
	hasMsg := cache.HasMessageIDInGroup(testMsgId, testGroupPtr)
	fmt.Printf("Message exists in group: %t\n", hasMsg)

	hasMsg2 := cache.HasMessageIDInGroup(testMsgId, nonexistentGroupPtr)
	fmt.Printf("Message exists in nonexistent group: %t\n", hasMsg2)

	// Test GetMsgIdFromCache directly
	directArtNum, directRoot, directIsRoot := cache.GetMsgIdFromCache(testGroupPtr, testMsgId)
	fmt.Printf("Direct GetMsgIdFromCache: ArtNum=%d, Root=%d, IsRoot=%t\n",
		directArtNum, directRoot, directIsRoot)
}

type WorkerStats struct {
	Gets     int64
	Inserts  int64
	Deletes  int64
	Errors   int64
	Duration time.Duration
}

func runConcurrentStressTest(cache *history.MsgIdItemCache, messageIds []string, numWorkers int, duration time.Duration) {
	fmt.Printf("\n=== Concurrent Stress Test (%d workers, %v) ===\n", numWorkers, duration)

	var wg sync.WaitGroup
	workerStats := make([]WorkerStats, numWorkers)
	startTime := time.Now()

	// Generate additional random message IDs for stress testing
	extraIds := make([]string, 50000)
	for i := range extraIds {
		extraIds[i] = fmt.Sprintf("<stress.%d.%d@test.com>", time.Now().UnixNano(), i)
	}

	for workerID := 0; workerID < numWorkers; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			stats := &workerStats[id]
			localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			workerStart := time.Now()

			for time.Since(startTime) < duration {
				// Random operation: 50% get, 30% insert, 20% delete
				op := localRand.Intn(100)

				switch {
				case op < 50: // Get operation
					var msgId string
					if localRand.Intn(2) == 0 && len(messageIds) > 0 {
						// Get existing item
						msgId = messageIds[localRand.Intn(len(messageIds))]
					} else {
						// Get random item (might not exist)
						msgId = extraIds[localRand.Intn(len(extraIds))]
					}

					item := cache.GetORCreate(msgId)
					if item != nil {
						atomic.AddInt64(&stats.Gets, 1)
					} else {
						atomic.AddInt64(&stats.Errors, 1)
					}

				case op < 80: // Insert operation
					msgId := fmt.Sprintf("<worker.%d.%d.%d@stress.com>",
						id, time.Now().UnixNano(), localRand.Int63())

					item := cache.GetORCreate(msgId)
					if item != nil {
						atomic.AddInt64(&stats.Inserts, 1)
					} else {
						atomic.AddInt64(&stats.Errors, 1)
					}

				default: // Delete operation (if delete function exists)
					if len(messageIds) > 0 {
						msgId := messageIds[localRand.Intn(len(messageIds))]
						// Note: We would need to implement and test cache.Delete(msgId) here
						// For now, just do another get to test concurrent access
						item := cache.GetORCreate(msgId)
						if item != nil {
							atomic.AddInt64(&stats.Deletes, 1)
						} else {
							atomic.AddInt64(&stats.Errors, 1)
						}
					}
				}

				// Small random delay to create realistic access patterns
				if localRand.Intn(1000) == 0 {
					time.Sleep(time.Microsecond * time.Duration(localRand.Intn(100)))
				}
			}

			stats.Duration = time.Since(workerStart)
		}(workerID)
	}

	// Progress reporting
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for range ticker.C {
			if time.Since(startTime) >= duration {
				return
			}
			// Print current cache stats
			buckets, items, maxChain := cache.Stats()
			fmt.Printf("  [%v] Items: %d, Buckets: %d, MaxChain: %d\n",
				time.Since(startTime).Round(time.Second), items, buckets, maxChain)
		}
	}()

	wg.Wait()
	totalTime := time.Since(startTime)

	// Aggregate and print results
	fmt.Printf("\n=== Concurrent Test Results ===\n")
	fmt.Printf("Total test time: %v\n", totalTime)

	var totalGets, totalInserts, totalDeletes, totalErrors int64
	for i, stats := range workerStats {
		fmt.Printf("Worker %d: Gets=%d, Inserts=%d, Deletes=%d, Errors=%d, Duration=%v\n",
			i, stats.Gets, stats.Inserts, stats.Deletes, stats.Errors, stats.Duration)

		totalGets += stats.Gets
		totalInserts += stats.Inserts
		totalDeletes += stats.Deletes
		totalErrors += stats.Errors
	}

	totalOps := totalGets + totalInserts + totalDeletes
	fmt.Printf("\nTotals: Gets=%d, Inserts=%d, Deletes=%d, Errors=%d\n",
		totalGets, totalInserts, totalDeletes, totalErrors)
	fmt.Printf("Total operations: %d\n", totalOps)
	fmt.Printf("Operations per second: %.0f\n", float64(totalOps)/totalTime.Seconds())
	fmt.Printf("Error rate: %.2f%%\n", float64(totalErrors)/float64(totalOps+totalErrors)*100)

	// Final cache state
	buckets, items, maxChain := cache.Stats()
	fmt.Printf("\nFinal cache state: Items=%d, Buckets=%d, MaxChain=%d\n", items, buckets, maxChain)
}

func estimateMemoryUsage(cache *history.MsgIdItemCache, messageIds []string) {
	fmt.Println("\n=== Memory Usage Estimation ===")

	// Size of basic types
	sizeOfPointer := unsafe.Sizeof(uintptr(0))
	sizeOfString := unsafe.Sizeof("")
	sizeOfInt := unsafe.Sizeof(int(0))
	sizeOfRWMutex := unsafe.Sizeof(sync.RWMutex{})

	fmt.Printf("Basic type sizes:\n")
	fmt.Printf("  Pointer: %d bytes\n", sizeOfPointer)
	fmt.Printf("  String header: %d bytes\n", sizeOfString)
	fmt.Printf("  Int: %d bytes\n", sizeOfInt)
	fmt.Printf("  RWMutex: %d bytes\n", sizeOfRWMutex)

	// Estimate cache structure overhead
	cacheOverhead := sizeOfRWMutex + unsafe.Sizeof(map[int]*history.MsgIdItemCachePage{})
	fmt.Printf("\nCache overhead: %d bytes\n", cacheOverhead)

	// Get actual sample data from cache to measure real sizes
	var sampleItem *history.MessageIdItem
	var actualMsgIdLen, actualHashLen int

	if len(messageIds) > 0 {
		sampleItem = cache.GetORCreate(messageIds[0])
		if sampleItem != nil {
			if sampleItem.MessageId != "" {
				actualMsgIdLen = len(sampleItem.MessageId)
			}
			actualHashLen = len(sampleItem.MessageIdHash)
		}
	}

	fmt.Printf("\nActual sample data:\n")
	fmt.Printf("  Sample MessageId length: %d bytes\n", actualMsgIdLen)
	fmt.Printf("  Sample MessageIdHash length: %d bytes\n", actualHashLen)
	fmt.Printf("  MessageIdHash type: %T\n", sampleItem.MessageIdHash)

	if actualHashLen > 0 {
		fmt.Printf("  Hash format: %q\n", sampleItem.MessageIdHash)
		switch actualHashLen {
		case 32:
			fmt.Printf("  -> Appears to be MD5 hash (32 hex chars)\n")
		case 40:
			fmt.Printf("  -> Appears to be SHA1 hash (40 hex chars)\n")
		case 64:
			fmt.Printf("  -> Appears to be SHA256 hash (64 hex chars)\n")
		case 128:
			fmt.Printf("  -> Appears to be SHA512 hash (128 hex chars)\n")
		default:
			fmt.Printf("  -> Unknown hash format length\n")
		}
	}

	// Calculate average string lengths from actual data
	avgMsgIdLen := actualMsgIdLen
	avgHashLen := actualHashLen

	// MessageIdItem contains: *string (MessageId) + string (MessageIdHash) + other fields
	msgIdItemSize := sizeOfPointer + sizeOfString // pointer to MessageId + MessageIdHash string header

	// MsgIdItemCachePage contains: *MessageIdItem + *Next + *Prev + RWMutex
	pageSize := sizeOfPointer*3 + sizeOfRWMutex + msgIdItemSize

	buckets, cacheItems, _ := cache.Stats()

	// Calculate memory for actual string data
	totalMsgIdStringBytes := cacheItems * avgMsgIdLen
	totalHashStringBytes := cacheItems * avgHashLen
	totalStringBytes := totalMsgIdStringBytes + totalHashStringBytes
	totalPageBytes := cacheItems * int(pageSize)
	totalMapOverhead := buckets * 16 // rough estimate for map bucket overhead

	totalEstimated := int(cacheOverhead) + totalStringBytes + totalPageBytes + totalMapOverhead

	fmt.Printf("\nPer-item breakdown:\n")
	fmt.Printf("  MsgIdItemCachePage: %d bytes\n", pageSize)
	fmt.Printf("  MessageIdItem: %d bytes\n", msgIdItemSize)
	fmt.Printf("  MessageId string data: %d bytes\n", avgMsgIdLen)
	fmt.Printf("  MessageIdHash string data: %d bytes\n", avgHashLen)
	fmt.Printf("  Total per item: %d bytes\n", pageSize+uintptr(avgMsgIdLen+avgHashLen))

	fmt.Printf("\nTotal estimated memory:\n")
	fmt.Printf("  Cache overhead: %d bytes\n", cacheOverhead)
	fmt.Printf("  MessageId string data: %d bytes (%.2f MB)\n", totalMsgIdStringBytes, float64(totalMsgIdStringBytes)/(1024*1024))
	fmt.Printf("  MessageIdHash string data: %d bytes (%.2f MB)\n", totalHashStringBytes, float64(totalHashStringBytes)/(1024*1024))
	fmt.Printf("  Page structures: %d bytes (%.2f MB)\n", totalPageBytes, float64(totalPageBytes)/(1024*1024))
	fmt.Printf("  Map overhead: %d bytes\n", totalMapOverhead)
	fmt.Printf("  Total estimated: %d bytes (%.2f MB)\n", totalEstimated, float64(totalEstimated)/(1024*1024))
}

func getMemStats() runtime.MemStats {
	runtime.GC() // Force garbage collection
	runtime.GC() // Run twice to be sure
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m
}

func printMemStats(label string, m runtime.MemStats) {
	fmt.Printf("%s:\n", label)
	fmt.Printf("  Alloc: %.2f MB\n", float64(m.Alloc)/(1024*1024))
	fmt.Printf("  TotalAlloc: %.2f MB\n", float64(m.TotalAlloc)/(1024*1024))
	fmt.Printf("  Sys: %.2f MB\n", float64(m.Sys)/(1024*1024))
	fmt.Printf("  HeapAlloc: %.2f MB\n", float64(m.HeapAlloc)/(1024*1024))
	fmt.Printf("  HeapSys: %.2f MB\n", float64(m.HeapSys)/(1024*1024))
	fmt.Printf("  NumGC: %d\n", m.NumGC)
}
