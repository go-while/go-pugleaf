package processor

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/nntp"
)

// GroupAnalysis contains analysis results for a newsgroup
type GroupAnalysis struct {
	GroupName      string
	ProviderName   string
	TotalArticles  int64
	TotalBytes     int64
	FirstArticle   int64
	LastArticle    int64
	OldestDate     time.Time
	NewestDate     time.Time
	AnalyzedAt     time.Time
	CacheExists    bool
	CachedArticles int64
	SizeStats      *ArticleSizeStats // Article size distribution statistics
}

// DateRangeResult contains information about articles in a specific date range
type DateRangeResult struct {
	StartDate       time.Time
	EndDate         time.Time
	FirstArticleNum int64
	LastArticleNum  int64
	ArticleCount    int64
	TotalBytes      int64
}

// ArticleSizeStats tracks distribution of article sizes
type ArticleSizeStats struct {
	Under4K       int64 // < 4KB
	Size4to16K    int64 // 4KB - 16KB
	Size16to32K   int64 // 16KB - 32KB
	Size32to64K   int64 // 32KB - 64KB
	Size64to128K  int64 // 64KB - 128KB
	Size128to256K int64 // 128KB - 256KB
	Size256to512K int64 // 256KB - 512KB
	Over512K      int64 // > 512KB
	TotalBytes    int64 // Total bytes across all articles
	TotalCount    int64 // Total article count
}

// AddArticleSize adds an article size to the statistics
func (stats *ArticleSizeStats) AddArticleSize(bytes int64) {
	stats.TotalBytes += bytes
	stats.TotalCount++

	switch {
	case bytes < 4*1024:
		stats.Under4K++
	case bytes < 16*1024:
		stats.Size4to16K++
	case bytes < 32*1024:
		stats.Size16to32K++
	case bytes < 64*1024:
		stats.Size32to64K++
	case bytes < 128*1024:
		stats.Size64to128K++
	case bytes < 256*1024:
		stats.Size128to256K++
	case bytes < 512*1024:
		stats.Size256to512K++
	default:
		stats.Over512K++
	}
}

// PrintSizeDistribution prints a detailed breakdown of article sizes
func (stats *ArticleSizeStats) PrintSizeDistribution() {
	if stats.TotalCount == 0 {
		fmt.Println("No articles analyzed for size distribution")
		return
	}

	fmt.Println("\n=== Article Size Distribution ===")
	fmt.Printf("Total Articles: %s\n", formatNumberWithCommas(stats.TotalCount))
	fmt.Printf("Total Size: %s\n", formatBytes(stats.TotalBytes))
	fmt.Printf("Average Size: %s\n", formatBytes(stats.TotalBytes/stats.TotalCount))
	fmt.Println()

	printSizeRange := func(label string, count int64) {
		percentage := float64(count) * 100.0 / float64(stats.TotalCount)
		fmt.Printf("%-15s: %8s (%5.1f%%)\n", label, formatNumberWithCommas(count), percentage)
	}

	printSizeRange("< 4K", stats.Under4K)
	printSizeRange("4K - 16K", stats.Size4to16K)
	printSizeRange("16K - 32K", stats.Size16to32K)
	printSizeRange("32K - 64K", stats.Size32to64K)
	printSizeRange("64K - 128K", stats.Size64to128K)
	printSizeRange("128K - 256K", stats.Size128to256K)
	printSizeRange("256K - 512K", stats.Size256to512K)
	printSizeRange("> 512K", stats.Over512K)
}

const (
	AnalyzeBatchSize = 10000 // Process overview in batches of 10k articles
)

// AnalyzeGroup performs comprehensive analysis of a remote newsgroup
func (proc *Processor) AnalyzeGroup(groupName string, options *AnalyzeOptions) (*GroupAnalysis, error) {
	if proc.Pool == nil {
		return nil, fmt.Errorf("error in AnalyzeGroup proc.Pool is nil")
	}
	if proc.Pool.Backend == nil {
		return nil, fmt.Errorf("error in AnalyzeGroup proc.Pool.Backend is nil")
	}
	var providerName string
	if strings.Contains(proc.Pool.Backend.Host, ":") {
		providerName = filepath.Base(strings.Split(proc.Pool.Backend.Host, ":")[0])
	} else {
		providerName = filepath.Base(proc.Pool.Backend.Host)
	}

	log.Printf("Analyzing group '%s' on provider '%s'", groupName, providerName)

	// Select the group to get basic info
	groupInfo, err := proc.Pool.SelectGroup(groupName)
	if err != nil {
		return nil, fmt.Errorf("failed to select group '%s': %w", groupName, err)
	}

	analysis := &GroupAnalysis{
		GroupName:     groupName,
		ProviderName:  providerName,
		FirstArticle:  groupInfo.First,
		LastArticle:   groupInfo.Last,
		TotalArticles: groupInfo.Last - groupInfo.First + 1,
		AnalyzedAt:    time.Now(),
		SizeStats:     &ArticleSizeStats{}, // Initialize size statistics
	}

	log.Printf("Group '%s' has articles from %d to %d (%d total)", groupName, groupInfo.First, groupInfo.Last, analysis.TotalArticles)

	// Check if we have cached data
	cacheFile := proc.getCacheFilePath(providerName, groupName)
	analysis.CacheExists = proc.cacheFileExists(cacheFile)

	if analysis.CacheExists && !options.ForceRefresh {
		log.Printf("Using cached overview data for group '%s'", groupName)
		return proc.analyzeFromCache(cacheFile, analysis, options)
	}

	// No cache or forced refresh - analyze from remote
	return proc.analyzeFromRemote(groupInfo, analysis, options)
}

// AnalyzeOptions contains options for group analysis
type AnalyzeOptions struct {
	ForceRefresh   bool      // Force refresh even if cache exists
	StartDate      time.Time // Only analyze articles after this date
	EndDate        time.Time // Only analyze articles before this date
	MaxArticles    int64     // Maximum number of articles to analyze
	IncludeHeaders bool      // Include subject/from in cache for detailed analysis
}

var InvalidDates = make(map[string]string)
var InvalidMutex = &sync.Mutex{}

// analyzeFromRemote fetches overview data from remote server and analyzes it
func (proc *Processor) analyzeFromRemote(groupInfo *nntp.GroupInfo, analysis *GroupAnalysis, options *AnalyzeOptions) (*GroupAnalysis, error) {
	cacheFile := proc.getCacheFilePath(analysis.ProviderName, analysis.GroupName)

	// Ensure cache directory exists
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Open cache file for writing
	file, err := os.Create(cacheFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	start := groupInfo.First
	end := groupInfo.Last

	var totalBytes int64
	var oldestDate, newestDate time.Time
	var cachedCount int64

	log.Printf("Fetching overview data for group '%s' from %d to %d", analysis.GroupName, start, end)

	// Process in batches
	for batchStart := start; batchStart <= end; batchStart += AnalyzeBatchSize {
		batchEnd := batchStart + AnalyzeBatchSize - 1
		if batchEnd > end {
			batchEnd = end
		}

		log.Printf("Processing batch %d-%d for group '%s'", batchStart, batchEnd, analysis.GroupName)

		// Get overview data for this batch
		enforceLimit := false
		overviews, err := proc.Pool.XOver(analysis.GroupName, batchStart, batchEnd, enforceLimit)
		if err != nil {
			log.Printf("Failed to get overview for batch %d-%d: %v", batchStart, batchEnd, err)
			continue
		}

		// Process each overview entry
		for _, ov := range overviews {

			date := ParseNNTPDate(ov.Date)
			if date.IsZero() {

				InvalidMutex.Lock()
				InvalidDates[ov.MessageID] = ov.Date
				InvalidMutex.Unlock()
				log.Printf("[WARN]: Could not parse date:'%s' msgId='%s' ng='%s'", ov.Date, ov.MessageID, analysis.GroupName)

			} else {

				// Apply date filtering if specified
				if !options.StartDate.IsZero() && date.Before(options.StartDate) {
					continue
				}
				if !options.EndDate.IsZero() && date.After(options.EndDate) {
					continue
				}

				// Track date range
				if oldestDate.IsZero() || date.Before(oldestDate) {
					oldestDate = date
				}
				if newestDate.IsZero() || date.After(newestDate) {
					newestDate = date
				}
			}

			// Write raw XOVER line to cache
			line := fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t%s\t%d\t%d",
				ov.ArticleNum,
				ov.Subject,
				ov.From,
				ov.Date,
				ov.MessageID,
				ov.References,
				ov.Bytes,
				ov.Lines)

			writer.WriteString(line + "\n")

			totalBytes += int64(ov.Bytes)
			analysis.SizeStats.AddArticleSize(int64(ov.Bytes)) // Track article size distribution
			cachedCount++

			// Apply max articles limit
			if options.MaxArticles > 0 && cachedCount >= options.MaxArticles {
				log.Printf("Reached maximum article limit (%d), stopping analysis", options.MaxArticles)
				goto analysisComplete
			}
		}
	}

analysisComplete:
	// Update analysis with collected data
	analysis.TotalBytes = totalBytes
	analysis.OldestDate = oldestDate
	analysis.NewestDate = newestDate
	analysis.CachedArticles = cachedCount
	analysis.CacheExists = true

	log.Printf("Analysis complete for group '%s': %d articles, %d bytes, dates %s to %s",
		analysis.GroupName, cachedCount, totalBytes, oldestDate.Format("2006-01-02"), newestDate.Format("2006-01-02"))

	return analysis, nil
}

// analyzeFromCache analyzes cached overview data
func (proc *Processor) analyzeFromCache(cacheFile string, analysis *GroupAnalysis, options *AnalyzeOptions) (*GroupAnalysis, error) {
	file, err := os.Open(cacheFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open cache file: %w", err)
	}
	defer file.Close()

	// Initialize size stats if not already done
	if analysis.SizeStats == nil {
		analysis.SizeStats = &ArticleSizeStats{}
	}

	scanner := bufio.NewScanner(file)
	var totalBytes int64
	var oldestDate, newestDate time.Time
	var cachedCount int64

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse XOVER line: articleNum\tsubject\tfrom\tdate\tmessageID\treferences\tbytes\tlines
		parts := strings.Split(line, "\t")
		if len(parts) < 8 {
			log.Printf("Invalid cached overview line: %s", line)
			continue
		}

		// Parse article number
		articleNum, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			log.Printf("Invalid article number in cache: %s", parts[0])
			continue
		}

		// Parse date
		date := ParseNNTPDate(parts[3])
		if date.IsZero() {
			InvalidMutex.Lock()
			InvalidDates[parts[4]] = parts[3]
			InvalidMutex.Unlock()
			log.Printf("[WARN]: Could not parse cached date:'%s' msgId='%s' n=%d ng='%s'", parts[3], parts[4], articleNum, analysis.GroupName)
		} else {

			// Apply date filtering if specified
			if !options.StartDate.IsZero() && date.Before(options.StartDate) {
				continue
			}
			if !options.EndDate.IsZero() && date.After(options.EndDate) {
				continue
			}

			// Track date range
			if oldestDate.IsZero() || date.Before(oldestDate) {
				oldestDate = date
			}
			if newestDate.IsZero() || date.After(newestDate) {
				newestDate = date
			}
		}

		// Parse bytes
		bytes, err := strconv.ParseInt(parts[6], 10, 64)
		if err != nil {
			log.Printf("Invalid bytes in cache: %s", parts[6])
			bytes = 0
		}

		totalBytes += bytes
		analysis.SizeStats.AddArticleSize(bytes) // Track article size distribution
		cachedCount++

		// Apply max articles limit
		if options.MaxArticles > 0 && cachedCount >= options.MaxArticles {
			break
		}
	}

	// Update analysis with cached data
	analysis.TotalBytes = totalBytes
	analysis.OldestDate = oldestDate
	analysis.NewestDate = newestDate
	analysis.CachedArticles = cachedCount

	return analysis, nil
}

// FindArticlesByDateRange finds articles within a specific date range
func (proc *Processor) FindArticlesByDateRange(groupName string, startDate, endDate time.Time) (*DateRangeResult, error) {
	if proc.Pool == nil {
		return nil, fmt.Errorf("NNTP pool is nil")
	}

	providerName := "unknown"
	if proc.Pool.Backend.Provider != nil {
		providerName = proc.Pool.Backend.Provider.Name
	}

	cacheFile := proc.getCacheFilePath(providerName, groupName)

	if !proc.cacheFileExists(cacheFile) {
		return nil, fmt.Errorf("no cached overview data for group '%s', run analyze first", groupName)
	}

	return proc.findDateRangeInCache(cacheFile, startDate, endDate)
}

// findDateRangeInCache searches cached data for articles in date range
func (proc *Processor) findDateRangeInCache(cacheFile string, startDate, endDate time.Time) (*DateRangeResult, error) {
	file, err := os.Open(cacheFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open cache file: %w", err)
	}
	var result = &DateRangeResult{}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	var firstArticleNum, lastArticleNum int64 = 0, 0
	var articleCount, totalBytes int64

	// Load all articles in date range
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse XOVER line: articleNum\tsubject\tfrom\tdate\tmessageID\treferences\tbytes\tlines
		parts := strings.Split(line, "\t")
		if len(parts) < 8 {
			continue
		}

		// Parse bytes
		bytes, err := strconv.ParseInt(parts[6], 10, 64)
		if err != nil {
			log.Printf("Invalid bytes in cache: %s", parts[6])
			bytes = 0
		}

		// Parse article number
		articleNum, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}

		// Parse date
		date := ParseNNTPDate(parts[3])
		if date.IsZero() {
			continue
		}

		// Check if article is in date range
		if date.Before(startDate) || date.After(endDate) {
			continue
		}

		// Track article range
		if firstArticleNum == 0 || articleNum < firstArticleNum {
			firstArticleNum = articleNum
		}
		if lastArticleNum == 0 || articleNum > lastArticleNum {
			lastArticleNum = articleNum
		}

		articleCount++
		totalBytes += bytes
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading cache file: %w", err)
	}

	result.StartDate = startDate
	result.EndDate = endDate
	result.FirstArticleNum = firstArticleNum
	result.LastArticleNum = lastArticleNum
	result.ArticleCount = articleCount
	result.TotalBytes = totalBytes

	return result, nil
}

func sha256hashFromString(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// getCacheFilePath returns the cache file path for a provider/group combination
func (proc *Processor) getCacheFilePath(providerName string, groupName string) string {
	// Sanitize group name for filesystem
	safeName := strings.ReplaceAll(groupName, "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")

	return filepath.Join("data", "cache", providerName, sha256hashFromString(safeName)+".overview")
}

// cacheFileExists checks if a cache file exists and is not empty
func (proc *Processor) cacheFileExists(cacheFile string) bool {
	info, err := os.Stat(cacheFile)
	return err == nil && info.Size() > 0
}

// GetCachedMessageIDs returns cached message IDs for download optimization
func (proc *Processor) GetCachedMessageIDs(groupName string, startArticle, endArticle int64) ([]nntp.HeaderLine, error) {
	providerName := "unknown"
	if proc.Pool.Backend != nil {
		providerName = proc.Pool.Backend.Provider.Name
	}

	cacheFile := proc.getCacheFilePath(providerName, groupName)

	if !proc.cacheFileExists(cacheFile) {
		// No cache available, fall back to XHDR
		log.Printf("No cache available for group '%s', using XHDR", groupName)
		return proc.Pool.XHdr(groupName, "message-id", startArticle, endArticle)
	}

	// Load from cache
	file, err := os.Open(cacheFile)
	if err != nil {
		log.Printf("Failed to open cache file, falling back to XHDR: %v", err)
		return proc.Pool.XHdr(groupName, "message-id", startArticle, endArticle)
	}
	defer file.Close()

	var results []nntp.HeaderLine
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse XOVER line: articleNum\tsubject\tfrom\tdate\tmessageID\treferences\tbytes\tlines
		parts := strings.Split(line, "\t")
		if len(parts) < 8 {
			continue
		}

		// Parse article number
		articleNum, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}

		// Check if article is in requested range
		if articleNum < startArticle || articleNum > endArticle {
			continue
		}

		// Extract message ID (5th field, index 4)
		messageID := parts[4]
		if messageID == "" {
			continue
		}

		results = append(results, nntp.HeaderLine{
			ArticleNum: articleNum,
			Value:      messageID,
		})
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading cache file, falling back to XHDR: %v", err)
		return proc.Pool.XHdr(groupName, "message-id", startArticle, endArticle)
	}

	// Sort by article number
	sort.Slice(results, func(i, j int) bool {
		return results[i].ArticleNum < results[j].ArticleNum
	})

	log.Printf("Retrieved %d message IDs from cache for group '%s' (range %d-%d)",
		len(results), groupName, startArticle, endArticle)

	return results, nil
}

// ClearCache removes cached overview data for a group
func (proc *Processor) ClearCache(groupName string) error {
	providerName := "unknown"
	if proc.Pool.Backend != nil {
		providerName = proc.Pool.Backend.Provider.Name
	}

	cacheFile := proc.getCacheFilePath(providerName, groupName)

	if proc.cacheFileExists(cacheFile) {
		err := os.Remove(cacheFile)
		if err != nil {
			return fmt.Errorf("failed to remove cache file: %w", err)
		}
		log.Printf("Cleared cache for group '%s'", groupName)
	}

	return nil
}

// GetCacheStats returns statistics about cached overview data
func (proc *Processor) GetCacheStats(groupName string) (*GroupAnalysis, error) {
	providerName := "unknown"
	if proc.Pool != nil && proc.Pool.Backend.Provider != nil {
		providerName = proc.Pool.Backend.Provider.Name
	}

	cacheFile := proc.getCacheFilePath(providerName, groupName)

	if !proc.cacheFileExists(cacheFile) {
		return nil, fmt.Errorf("no cached data found for group '%s'", groupName)
	}

	analysis := &GroupAnalysis{
		GroupName:    groupName,
		ProviderName: providerName,
		CacheExists:  true,
		AnalyzedAt:   time.Now(),
		SizeStats:    &ArticleSizeStats{}, // Initialize size statistics
	}

	options := &AnalyzeOptions{}
	return proc.analyzeFromCache(cacheFile, analysis, options)
}

// ValidateCacheIntegrity checks if the cache file is valid and consistent
func (proc *Processor) ValidateCacheIntegrity(groupName string) error {
	providerName := "unknown"
	if proc.Pool != nil && proc.Pool.Backend.Provider != nil {
		providerName = proc.Pool.Backend.Provider.Name
	}

	cacheFile := proc.getCacheFilePath(providerName, groupName)

	if !proc.cacheFileExists(cacheFile) {
		return fmt.Errorf("cache file does not exist for group '%s'", groupName)
	}

	file, err := os.Open(cacheFile)
	if err != nil {
		return fmt.Errorf("failed to open cache file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	validLines := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Validate XOVER line format
		parts := strings.Split(line, "\t")
		if len(parts) < 8 {
			log.Printf("Warning: Invalid cache line %d in group '%s': insufficient fields", lineNum, groupName)
			continue
		}

		// Validate article number
		if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
			continue
		}

		validLines++
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading cache file: %w", err)
	}

	log.Printf("Cache validation for group '%s': %d total lines, %d valid lines", groupName, lineNum, validLines)

	if validLines == 0 {
		return fmt.Errorf("cache file contains no valid data")
	}

	return nil
}

// GetArticleCountByDateRange returns the number of articles in a specific date range without full analysis
func (proc *Processor) GetArticleCountByDateRange(groupName string, startDate, endDate time.Time) (int64, error) {
	providerName := "unknown"
	if proc.Pool != nil && proc.Pool.Backend.Provider != nil {
		providerName = proc.Pool.Backend.Provider.Name
	}

	cacheFile := proc.getCacheFilePath(providerName, groupName)

	if !proc.cacheFileExists(cacheFile) {
		return 0, fmt.Errorf("no cached data found for group '%s'", groupName)
	}

	file, err := os.Open(cacheFile)
	if err != nil {
		return 0, fmt.Errorf("failed to open cache file: %w", err)
	}
	defer file.Close()

	var count int64
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		parts := strings.Split(line, "\t")
		if len(parts) < 8 {
			continue
		}

		date := ParseNNTPDate(parts[3])
		if date.IsZero() {
			continue
		}

		if !date.Before(startDate) && !date.After(endDate) {
			count++
		}
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("error reading cache file: %w", err)
	}

	return count, nil
}

// ExportAnalysisToJSON exports group analysis results to JSON format
func (analysis *GroupAnalysis) ExportAnalysisToJSON() ([]byte, error) {
	// Simple JSON export without external dependencies
	jsonData := fmt.Sprintf(`{
	"group_name": "%s",
	"provider_name": "%s",
	"total_articles": %d,
	"total_bytes": %d,
	"first_article": %d,
	"last_article": %d,
	"oldest_date": "%s",
	"newest_date": "%s",
	"analyzed_at": "%s",
	"cache_exists": %t,
	"cached_articles": %d,
}`,
		analysis.GroupName,
		analysis.ProviderName,
		analysis.TotalArticles,
		analysis.TotalBytes,
		analysis.FirstArticle,
		analysis.LastArticle,
		analysis.OldestDate.Format(time.RFC3339),
		analysis.NewestDate.Format(time.RFC3339),
		analysis.AnalyzedAt.Format(time.RFC3339),
		analysis.CacheExists,
		analysis.CachedArticles,
	)

	return []byte(jsonData), nil
}

// ExportAnalysisToCSV exports group analysis results to CSV format
func (analysis *GroupAnalysis) ExportAnalysisToCSV() string {
	return fmt.Sprintf("%s,%s,%d,%d,%d,%d,%s,%s,%s,%t,%d\n",
		analysis.GroupName,
		analysis.ProviderName,
		analysis.TotalArticles,
		analysis.TotalBytes,
		analysis.FirstArticle,
		analysis.LastArticle,
		analysis.OldestDate.Format("2006-01-02"),
		analysis.NewestDate.Format("2006-01-02"),
		analysis.AnalyzedAt.Format("2006-01-02 15:04:05"),
		analysis.CacheExists,
		analysis.CachedArticles,
	)
}

// GetCSVHeader returns the CSV header for analysis exports
func GetAnalysisCSVHeader() string {
	return "group_name,provider_name,total_articles,total_bytes,first_article,last_article,oldest_date,newest_date,analyzed_at,cache_exists,cached_articles\n"
}

// AnalyzeMode performs newsgroup analysis operations using an existing processor instance
func (proc *Processor) AnalyzeMode(testGrp string, forceRefresh bool, maxAnalyzeArticles int64,
	startDate string, endDate string, exportFormat string, validateCache bool,
	clearCache bool, cacheStats bool) (*GroupAnalysis, error) {

	if testGrp == "" {
		return nil, fmt.Errorf("group name is required for analysis")
	}

	// Validate export format
	if exportFormat != "" && exportFormat != "json" && exportFormat != "csv" {
		return nil, fmt.Errorf("export format must be 'json' or 'csv'")
	}

	// Handle cache operations first
	if clearCache {
		fmt.Printf("Clearing cache for group '%s'...\n", testGrp)
		err := proc.ClearCache(testGrp)
		if err != nil {
			return nil, fmt.Errorf("failed to clear cache: %w", err)
		}
		fmt.Println("✓ Cache cleared successfully")
		return nil, nil
	}

	if validateCache {
		fmt.Printf("Validating cache integrity for group '%s'...\n", testGrp)
		err := proc.ValidateCacheIntegrity(testGrp)
		if err != nil {
			fmt.Printf("⚠ Cache validation failed: %v\n", err)
		} else {
			fmt.Println("✓ Cache validation passed")
		}
		return nil, nil
	}

	if cacheStats {
		fmt.Printf("Getting cache statistics for group '%s'...\n", testGrp)
		stats, err := proc.GetCacheStats(testGrp)
		if err != nil {
			return nil, fmt.Errorf("failed to get cache stats: %w", err)
		}
		printAnalysisResults(stats, exportFormat)
		return stats, nil
	}

	// Parse date filters if provided
	var startDateParsed, endDateParsed time.Time
	var err error

	if startDate != "" {
		startDateParsed, err = time.Parse("2006-01-02", startDate)
		if err != nil {
			return nil, fmt.Errorf("invalid start date format (use YYYY-MM-DD): %w", err)
		}
	}

	if endDate != "" {
		endDateParsed, err = time.Parse("2006-01-02", endDate)
		if err != nil {
			return nil, fmt.Errorf("invalid end date format (use YYYY-MM-DD): %w", err)
		}
	}

	// Create analysis options
	options := &AnalyzeOptions{
		ForceRefresh:   forceRefresh,
		StartDate:      startDateParsed,
		EndDate:        endDateParsed,
		MaxArticles:    maxAnalyzeArticles,
		IncludeHeaders: true,
	}

	// Perform analysis
	fmt.Printf("Performing full analysis...\n")
	analysis, err := proc.AnalyzeGroup(testGrp, options)
	if err != nil {
		return nil, fmt.Errorf("analysis failed: %w", err)
	}

	// Print results
	printAnalysisResults(analysis, exportFormat)

	return analysis, nil
}

// AnalyzeModeStandalone is a wrapper for backward compatibility that creates its own pool
func AnalyzeModeStandalone(host *string, port *int, username *string, password *string, ssl *bool, timeout *int,
	testGrp *string, forceRefresh *bool, maxAnalyzeArticles *int64,
	startDate *string, endDate *string, exportFormat *string, validateCache *bool,
	clearCache *bool, cacheStats *bool) error {

	if *testGrp == "" {
		return fmt.Errorf("group name is required for analysis (use -group flag)")
	}

	// Create NNTP connection configuration
	backendConfig := &nntp.BackendConfig{
		Host:           *host,
		Port:           *port,
		SSL:            *ssl,
		Username:       *username,
		Password:       *password,
		MaxConns:       5,
		ConnectTimeout: time.Duration(*timeout) * time.Second,
		//ReadTimeout:    60 * time.Second,
		//WriteTimeout:   30 * time.Second,
	}

	fmt.Printf("Analyzing newsgroup '%s' on %s:%d (SSL: %v)\n", *testGrp, *host, *port, *ssl)

	// Create connection pool
	pool := nntp.NewPool(backendConfig)
	defer pool.ClosePool()
	pool.StartCleanupWorker(30 * time.Second)

	// Create processor (minimal setup for analysis only)
	proc := &Processor{
		Pool: pool,
	}

	// Use the proper method
	_, err := proc.AnalyzeMode(*testGrp, *forceRefresh, *maxAnalyzeArticles,
		*startDate, *endDate, *exportFormat, *validateCache, *clearCache, *cacheStats)

	return err
}

// printAnalysisResults formats and displays analysis results
func printAnalysisResults(analysis *GroupAnalysis, exportFormat string) {
	// If export format is specified, write to file instead of stdout

	//GetAnalysisCSVHeader()

	// cut groupname by first dot to get primary hierarchie
	// e.g. "de.rec.film.kritiken" -> "de"
	hier := strings.Split(analysis.GroupName, ".")[0]
	//test if out/ dir exists
	if _, err := os.Stat("out"); os.IsNotExist(err) {
		err := os.Mkdir("out", 0755)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to create output directory: %v\n", err)
			os.Exit(1)
		}
	}
	switch exportFormat {
	case "json":
		filename := fmt.Sprintf("out/%s.json", analysis.GroupName)
		jsonData, err := analysis.ExportAnalysisToJSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to export JSON: %v\n", err)
			os.Exit(1)
		}
		err = os.WriteFile(filename, jsonData, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to write JSON file %s: %v\n", filename, err)
			os.Exit(1)
		}
		fmt.Printf("JSON exported to: %s\n", filename)
		//return
	case "csv":
		filename := fmt.Sprintf("out/%s.csv", hier)
		csvData := analysis.ExportAnalysisToCSV()
		f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			_, err = f.WriteString(csvData)
			f.Close()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to write CSV file %s: %v\n", filename, err)
			os.Exit(1)
		}
		fmt.Printf("CSV exported to: %s\n", filename)
		//return
	}

	// Default: Human-readable analysis output
	fmt.Println("\n=== Analysis Results ===")
	fmt.Printf("Group: %s\n", analysis.GroupName)
	fmt.Printf("Provider: %s\n", analysis.ProviderName)
	fmt.Printf("Group Size: %s articles (range %d - %d)\n", formatNumberWithCommas(analysis.TotalArticles), analysis.FirstArticle, analysis.LastArticle)
	fmt.Printf("Analyzed Articles: %s\n", formatNumberWithCommas(analysis.CachedArticles))

	// Show warning if analysis was limited
	if analysis.CachedArticles < analysis.TotalArticles {
		missing := analysis.TotalArticles - analysis.CachedArticles
		fmt.Printf("⚠ Analysis processed %s of %s articles (%s missing/unavailable)\n",
			formatNumberWithCommas(analysis.CachedArticles),
			formatNumberWithCommas(analysis.TotalArticles),
			formatNumberWithCommas(missing))
	}

	fmt.Printf("Total Bytes: %s\n", formatBytes(analysis.TotalBytes))

	if !analysis.OldestDate.IsZero() && !analysis.NewestDate.IsZero() {
		fmt.Printf("Date Range: %s to %s\n",
			analysis.OldestDate.Format("2006-01-02"),
			analysis.NewestDate.Format("2006-01-02"))

		days := analysis.NewestDate.Sub(analysis.OldestDate).Hours() / 24
		if days > 0 {
			fmt.Printf("Time Span: %.1f days\n", days)
			fmt.Printf("Articles per Day: %.1f\n", float64(analysis.CachedArticles)/days)
		}
	}

	fmt.Printf("Cached Articles: %s\n", formatNumberWithCommas(analysis.CachedArticles))
	fmt.Printf("Cache Exists: %v\n", analysis.CacheExists)
	fmt.Printf("Analyzed At: %s\n", analysis.AnalyzedAt.Format("2006-01-02 15:04:05"))

	// Print detailed size distribution if we have size statistics
	if analysis.SizeStats != nil && analysis.SizeStats.TotalCount > 0 {
		analysis.SizeStats.PrintSizeDistribution()
	}
}

// formatNumber formats large numbers with thousand separators
func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	if n < 1000000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	return fmt.Sprintf("%.1fB", float64(n)/1000000000)
}

// formatBytes formats byte counts in human readable format
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	if n < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}

// formatNumberWithCommas formats numbers with comma separators (no K/M abbreviations)
func formatNumberWithCommas(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}

	// Convert to string and add commas
	str := fmt.Sprintf("%d", n)
	result := ""

	for i, char := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result += ","
		}
		result += string(char)
	}

	return result
}

// DownloadArticlesWithDateFilter downloads articles starting from a specific date
func DownloadArticlesWithDateFilter(proc *Processor, groupName, startDateStr, endDateStr string, useAnalyzeCache bool, ignoreInitialTinyGroups int64, DLParChan chan struct{}) error {
	var startDate, endDate time.Time
	var err error

	// Parse start date if provided
	if startDateStr != "" {
		startDate, err = time.Parse("2006-01-02", startDateStr)
		if err != nil {
			return fmt.Errorf("invalid start date format '%s': %v (expected YYYY-MM-DD)", startDateStr, err)
		}
		log.Printf("Download start date: %s", startDate.Format("2006-01-02"))
	}

	// Parse end date if provided
	if endDateStr != "" {
		endDate, err = time.Parse("2006-01-02", endDateStr)
		if err != nil {
			return fmt.Errorf("invalid end date format '%s': %v (expected YYYY-MM-DD)", endDateStr, err)
		}
		log.Printf("Download end date: %s", endDate.Format("2006-01-02"))
	}

	// If using analyze cache and dates are specified, find article ranges
	if useAnalyzeCache && !startDate.IsZero() {
		log.Printf("Using analyze cache to determine article ranges for date-based download")

		// Check if cache exists, if not run analysis first
		analysis, err := proc.GetCacheStats(groupName)
		if err != nil {
			log.Printf("No cache found for group '%s', running analysis first...", groupName)

			// Run analysis to build cache
			options := &AnalyzeOptions{
				ForceRefresh: false,
				MaxArticles:  0, // No limit for complete analysis
			}

			analysis, err = proc.AnalyzeGroup(groupName, options)
			if err != nil {
				return fmt.Errorf("failed to analyze group '%s': %v", groupName, err)
			}
			log.Printf("Analysis complete for group '%s': %d articles cached", groupName, analysis.CachedArticles)
		} else {
			log.Printf("Using existing cache for group '%s': %d articles available", groupName, analysis.CachedArticles)
		}

		// Set end date to now if not specified
		if endDate.IsZero() {
			endDate = time.Now()
		}

		// Find articles in the specified date range
		dateRange, err := proc.FindArticlesByDateRange(groupName, startDate, endDate)
		if err != nil {
			return fmt.Errorf("failed to find articles in date range: %v", err)
		}

		if dateRange.ArticleCount == 0 {
			log.Printf("No articles found in date range %s to %s for group '%s'",
				startDate.Format("2006-01-02"), endDate.Format("2006-01-02"), groupName)
			return nil
		}

		log.Printf("Found %d articles in date range %s to %s (articles %d-%d)",
			dateRange.ArticleCount, startDate.Format("2006-01-02"), endDate.Format("2006-01-02"),
			dateRange.FirstArticleNum, dateRange.LastArticleNum)

		// Download articles in the found range using custom download function
		return DownloadArticlesInRange(proc, groupName, dateRange.FirstArticleNum, dateRange.LastArticleNum, ignoreInitialTinyGroups)
	}

	// If no analyze cache is used but dates are specified, we need to do analysis first
	if !startDate.IsZero() {
		log.Printf("Date filtering requested but analyze cache not enabled")
		log.Printf("Running analysis to enable date-based filtering...")

		options := &AnalyzeOptions{
			StartDate:    startDate,
			EndDate:      endDate,
			ForceRefresh: false,
		}

		analysis, err := proc.AnalyzeGroup(groupName, options)
		if err != nil {
			return fmt.Errorf("failed to analyze group for date filtering: %v", err)
		}

		log.Printf("Analysis complete. Found %d articles in date range", analysis.CachedArticles)

		// Now use the cache-based approach
		return DownloadArticlesWithDateFilter(proc, groupName, startDateStr, endDateStr, true, ignoreInitialTinyGroups, DLParChan)
	}

	// Fall back to normal download if no date filtering
	log.Printf("No date filtering specified, using normal download")
	return proc.DownloadArticles(groupName, ignoreInitialTinyGroups, DLParChan)
}

// DownloadArticlesInRange downloads articles in a specific article number range
func DownloadArticlesInRange(proc *Processor, groupName string, startArticle, endArticle int64, ignoreInitialTinyGroups int64) error {
	log.Printf("Downloading articles %d-%d for group '%s'", startArticle, endArticle, groupName)

	// Select the group
	groupInfo, err := proc.Pool.SelectGroup(groupName)
	if err != nil {
		return fmt.Errorf("failed to select group '%s': %v", groupName, err)
	}

	// Validate the range
	if startArticle < groupInfo.First {
		startArticle = groupInfo.First
		log.Printf("Adjusted start article to group minimum: %d", startArticle)
	}
	if endArticle > groupInfo.Last {
		endArticle = groupInfo.Last
		log.Printf("Adjusted end article to group maximum: %d", endArticle)
	}

	if startArticle > endArticle {
		return fmt.Errorf("invalid article range: start %d > end %d", startArticle, endArticle)
	}

	// Limit batch size for very large ranges
	maxBatchSize := int64(10000) // Process in chunks of 10000 articles

	for currentStart := startArticle; currentStart <= endArticle; currentStart += maxBatchSize {
		currentEnd := currentStart + maxBatchSize - 1
		if currentEnd > endArticle {
			currentEnd = endArticle
		}

		log.Printf("Downloading batch: articles %d-%d", currentStart, currentEnd)

		// Get message IDs for this batch
		messageIDs, err := proc.GetCachedMessageIDs(groupName, currentStart, currentEnd)
		if err != nil {
			log.Printf("Failed to get cached message IDs, trying XHDR: %v", err)
			messageIDs, err = proc.Pool.XHdr(groupName, "message-id", currentStart, currentEnd)
			if err != nil {
				log.Printf("Failed to get message IDs for range %d-%d: %v", currentStart, currentEnd, err)
				continue
			}
		}

		if len(messageIDs) == 0 {
			log.Printf("No message IDs found for range %d-%d", currentStart, currentEnd)
			continue
		}

		log.Printf("Found %d message IDs for range %d-%d", len(messageIDs), currentStart, currentEnd)

		// Download articles for this batch using existing infrastructure
		err = DownloadMessageIDs(proc, groupName, messageIDs)
		if err != nil {
			log.Printf("Failed to download articles in range %d-%d: %v", currentStart, currentEnd, err)
			continue
		}

		log.Printf("Successfully downloaded batch %d-%d", currentStart, currentEnd)
	}

	log.Printf("Completed downloading articles %d-%d for group '%s'", startArticle, endArticle, groupName)
	return nil
}

// DownloadMessageIDs downloads specific articles by their message IDs
func DownloadMessageIDs(proc *Processor, groupName string, messageIDs []nntp.HeaderLine) error {
	if len(messageIDs) == 0 {
		return nil
	}

	log.Printf("Downloading %d articles for group '%s'", len(messageIDs), groupName)

	// Create channels for batch processing
	batchQueue := make(chan *batchItem, len(messageIDs))
	returnChan := make(chan *batchItem, len(messageIDs))

	// Launch goroutines to fetch articles in parallel
	runthis := int(float32(proc.Pool.Backend.MaxConns) * 0.75) // Use 75% of max connections
	if runthis < 1 {
		runthis = 1
	}

	log.Printf("Using %d goroutines to download articles", runthis)

	for i := 1; i <= runthis; i++ {
		go func(worker int) {
			defer func() {
				log.Printf("Download worker %d finished", worker)
			}()
			for item := range batchQueue {
				art, err := proc.Pool.GetArticle(item.MessageID)
				if err != nil {
					log.Printf("Failed to fetch article %s: %v", item.MessageID, err)
					item.Error = err
					returnChan <- item
					continue
				}
				item.Article = art
				returnChan <- item
			}
		}(i)
	}

	// Create batch items for all message IDs
	var batchList []*batchItem
	for _, msgID := range messageIDs {
		item := &batchItem{
			MessageID: msgID.Value,
			GroupName: &groupName,
		}
		batchQueue <- item
		batchList = append(batchList, item)
	}

	// Process downloaded articles
	gots := 0
	errs := 0
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	startTime := time.Now()
	nextCheck := startTime.Add(5 * time.Second)
	lastGots := 0
	lastErrs := 0
	deathCounter := 0

forProcessing:
	for {
		select {
		case <-ticker.C:
			if gots+errs >= len(messageIDs) {
				log.Printf("All %d articles processed (got: %d, errors: %d)", gots+errs, gots, errs)
				close(batchQueue)
				break forProcessing
			}
			if gots > lastGots || errs > lastErrs {
				nextCheck = time.Now().Add(5 * time.Second)
				lastGots = gots
				lastErrs = errs
				deathCounter = 0
			}
			if nextCheck.Before(time.Now()) {
				log.Printf("Progress check: %d articles processed (%d got, %d errors) in last 5 seconds", gots+errs, gots, errs)
				nextCheck = time.Now().Add(5 * time.Second)
				deathCounter++
			}
			if deathCounter > 11 {
				log.Printf("Timeout downloading articles, stopping")
				close(batchQueue)
				return fmt.Errorf("timeout downloading articles: %d processed (%d got, %d errors)", gots+errs, gots, errs)
			}

		case item := <-returnChan:
			if item.Error != nil {
				log.Printf("Error fetching article %s: %v", item.MessageID, item.Error)
				errs++
			} else {
				gots++
			}
		}
	}

	// Process the successfully downloaded articles
	for _, item := range batchList {
		if item == nil || item.Article == nil {
			continue
		}

		// For now, just log that we got the article
		// TODO: Process article through proper channels
		log.Printf("Downloaded article %s (%d bytes)", item.MessageID, item.Article.Bytes)

		// Note: In a production system, you would process the article here
		// bulkmode := true
		// response, err := proc.processArticle(item.Article, groupName, bulkmode)
		// For now, we'll just mark it as a success
	}

	log.Printf("Successfully downloaded %d articles for group '%s'", gots, groupName)
	return nil
}
