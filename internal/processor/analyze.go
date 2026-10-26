package processor

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-while/go-pugleaf/internal/common"
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
	if options == nil {
		options = &AnalyzeOptions{}
	}
	providerName, cacheFile, err := proc.analyzeCacheFile(groupName)
	if err != nil {
		return nil, err
	}

	log.Printf("Analyzing group '%s' on provider '%s'", groupName, providerName)

	// Select the group to get basic info
	groupInfo, err := proc.Pool.SelectGroup(groupName)
	if err != nil {
		return nil, fmt.Errorf("failed to select group '%s': %w", groupName, err)
	}
	if groupInfo == nil {
		return nil, fmt.Errorf("failed to select group '%s': no group info", groupName)
	}
	if err := validateAnalyzeRange(groupInfo.First, groupInfo.Last); err != nil {
		return nil, fmt.Errorf("group '%s': %w", groupName, err)
	}
	var totalArticles int64
	if groupInfo.Last >= groupInfo.First {
		totalArticles = groupInfo.Last - groupInfo.First + 1 // bounded by validateAnalyzeRange
	}

	analysis := &GroupAnalysis{
		GroupName:     groupName,
		ProviderName:  providerName,
		FirstArticle:  groupInfo.First,
		LastArticle:   groupInfo.Last,
		TotalArticles: totalArticles,
		AnalyzedAt:    time.Now(),
		SizeStats:     &ArticleSizeStats{}, // Initialize size statistics
	}

	log.Printf("Group '%s' has articles from %d to %d (%d total)", groupName, groupInfo.First, groupInfo.Last, analysis.TotalArticles)

	// Check if we have cached data
	analysis.CacheExists = proc.cacheFileExists(cacheFile)

	if analysis.CacheExists && !options.ForceRefresh {
		log.Printf("Using cached overview data for group '%s'", groupName)
		return proc.analyzeFromCache(cacheFile, analysis, options)
	}

	// No cache or forced refresh - analyze from remote
	return proc.analyzeFromRemote(groupInfo, cacheFile, analysis, options)
}

// maxAnalyzeRange bounds Last-First of a GROUP reply. A broken or hostile reply
// (Last near MaxInt64) would otherwise make the analysis issue XOVER batches forever.
var maxAnalyzeRange int64 = 10_000_000_000

// validateAnalyzeRange checks the article range of a GROUP reply before it is used.
// Last < First is an empty group and is accepted.
func validateAnalyzeRange(first, last int64) error {
	if first < 0 || last < 0 {
		return fmt.Errorf("invalid article range %d-%d: negative article number", first, last)
	}
	if last >= first && last-first >= maxAnalyzeRange {
		return fmt.Errorf("invalid article range %d-%d: more than %d article numbers", first, last, maxAnalyzeRange)
	}
	return nil
}

// analyzeXOver fetches one overview batch; a variable so tests can stub the server.
var analyzeXOver = func(pool *nntp.Pool, group string, start, end int64, enforceLimit bool) ([]nntp.OverviewLine, error) {
	return pool.XOver(group, start, end, enforceLimit)
}

// errAnalyzeStop ends forEachAnalyzeBatch early without an error (e.g. -max-analyze reached).
var errAnalyzeStop = errors.New("analyze: stop")

// forEachAnalyzeBatch calls fn for consecutive batches [start,end] of at most size
// article numbers covering first..last. It never computes a value above last, so it
// terminates for any last up to MaxInt64. fn returning errAnalyzeStop ends the loop
// with a nil error; any other error is returned.
func forEachAnalyzeBatch(first, last, size int64, fn func(start, end int64) error) error {
	if size <= 0 {
		return fmt.Errorf("invalid batch size %d", size)
	}
	for batchStart := first; batchStart <= last; {
		batchEnd := last
		if last-batchStart >= size {
			batchEnd = batchStart + size - 1
		}
		if err := fn(batchStart, batchEnd); err != nil {
			if errors.Is(err, errAnalyzeStop) {
				return nil
			}
			return err
		}
		if batchEnd >= last {
			break
		}
		batchStart = batchEnd + 1
	}
	return nil
}

// analyzeCacheWriter writes an overview cache to a temp file in the cache
// directory and renames it over the cache file only on Commit, so an aborted or
// failed run never leaves a partial cache that later runs would trust.
type analyzeCacheWriter struct {
	final string
	tmp   *os.File
	w     *bufio.Writer
	done  bool
}

func newAnalyzeCacheWriter(final string) (*analyzeCacheWriter, error) {
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}
	// Remove temp files a killed run left behind for this cache file. A concurrent
	// run on the same group loses its temp file and fails its Commit, which is safe.
	prefix := filepath.Base(final) + "."
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tmp") {
				continue
			}
			if rmErr := os.Remove(filepath.Join(dir, name)); rmErr != nil && !os.IsNotExist(rmErr) {
				log.Printf("[ANALYZE] failed to remove stale cache temp file %s: %v", name, rmErr)
			}
		}
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(final)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("failed to create cache file: %w", err)
	}
	return &analyzeCacheWriter{final: final, tmp: tmp, w: bufio.NewWriter(tmp)}, nil
}

// WriteLine appends one overview line (without the newline).
func (cw *analyzeCacheWriter) WriteLine(line string) error {
	if _, err := cw.w.WriteString(line); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}
	if err := cw.w.WriteByte('\n'); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}
	return nil
}

// Commit flushes, syncs and closes the temp file and renames it over the cache file.
// On any error the temp file is removed and the old cache (if any) stays.
func (cw *analyzeCacheWriter) Commit() error {
	if cw.done {
		return errors.New("analyze cache writer already finished")
	}
	cw.done = true
	tmpName := cw.tmp.Name()
	fail := func(what string, err error) error {
		_ = cw.tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to %s cache file: %w", what, err)
	}
	if err := cw.w.Flush(); err != nil {
		return fail("flush", err)
	}
	if err := cw.tmp.Sync(); err != nil {
		return fail("sync", err)
	}
	if err := cw.tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to close cache file: %w", err)
	}
	if err := os.Rename(tmpName, cw.final); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to rename cache file: %w", err)
	}
	return nil
}

// Abort discards the temp file. It is a no-op after Commit.
func (cw *analyzeCacheWriter) Abort() {
	if cw.done {
		return
	}
	cw.done = true
	_ = cw.tmp.Close()
	_ = os.Remove(cw.tmp.Name())
}

// analyzeCacheMaxLine bounds one cached overview line. The NNTP client caps a
// response line at 1 MiB by default (nntp.DefaultClientMaxLineBytes).
const analyzeCacheMaxLine = 4 << 20

// newAnalyzeCacheScanner returns a line scanner for overview cache files whose
// buffer takes lines up to analyzeCacheMaxLine; callers must check Err().
func newAnalyzeCacheScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), analyzeCacheMaxLine)
	return scanner
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

// analyzeFromRemote fetches overview data from remote server and analyzes it.
//
// The cache always holds every overview line of the group: the -start-date/-end-date
// filter applies only to the statistics. A run limited by -max-analyze stops before
// the end of the group, so it writes no cache at all (an existing cache stays). The
// cache is written to a temp file and renamed into place only after every XOVER batch
// succeeded; a failed batch fails the analysis.
func (proc *Processor) analyzeFromRemote(groupInfo *nntp.GroupInfo, cacheFile string, analysis *GroupAnalysis, options *AnalyzeOptions) (*GroupAnalysis, error) {
	var cw *analyzeCacheWriter
	if options.MaxArticles <= 0 {
		var err error
		cw, err = newAnalyzeCacheWriter(cacheFile)
		if err != nil {
			return nil, err
		}
		defer cw.Abort() // no-op after a successful Commit
	} else {
		log.Printf("[ANALYZE] -max-analyze %d set: group '%s' is analyzed without writing the overview cache", options.MaxArticles, analysis.GroupName)
	}

	start := groupInfo.First
	end := groupInfo.Last

	var totalBytes int64
	var oldestDate, newestDate time.Time
	var cachedCount int64

	log.Printf("Fetching overview data for group '%s' from %d to %d", analysis.GroupName, start, end)

	// Process in batches
	err := forEachAnalyzeBatch(start, end, AnalyzeBatchSize, func(batchStart, batchEnd int64) error {
		log.Printf("Processing batch %d-%d for group '%s'", batchStart, batchEnd, analysis.GroupName)

		// Get overview data for this batch
		enforceLimit := false
		overviews, err := analyzeXOver(proc.Pool, analysis.GroupName, batchStart, batchEnd, enforceLimit)
		if errors.Is(err, nntp.ErrNoArticlesInRange) {
			// 423: no articles in this batch (e.g. expired below a stale low-water mark)
			log.Printf("No articles in batch %d-%d for group '%s'", batchStart, batchEnd, analysis.GroupName)
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to get overview for batch %d-%d of group '%s': %w", batchStart, batchEnd, analysis.GroupName, err)
		}

		// Process each overview entry
		for _, ov := range overviews {
			// Write raw XOVER line to cache (every line, the date filter is for the stats only)
			if cw != nil {
				line := fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t%s\t%d\t%d",
					ov.ArticleNum,
					ov.Subject,
					ov.From,
					ov.Date,
					ov.MessageID,
					ov.References,
					ov.Bytes,
					ov.Lines)
				if err := cw.WriteLine(line); err != nil {
					return err
				}
			}

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

			totalBytes += int64(ov.Bytes)
			analysis.SizeStats.AddArticleSize(int64(ov.Bytes)) // Track article size distribution
			cachedCount++

			// Apply max articles limit
			if options.MaxArticles > 0 && cachedCount >= options.MaxArticles {
				log.Printf("Reached maximum article limit (%d), stopping analysis", options.MaxArticles)
				return errAnalyzeStop
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if cw != nil {
		if err := cw.Commit(); err != nil {
			return nil, err
		}
		analysis.CacheExists = true
	}

	// Update analysis with collected data
	analysis.TotalBytes = totalBytes
	analysis.OldestDate = oldestDate
	analysis.NewestDate = newestDate
	analysis.CachedArticles = cachedCount

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

	scanner := newAnalyzeCacheScanner(file)
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
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading cache file %s: %w", cacheFile, err)
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
	_, cacheFile, err := proc.analyzeCacheFile(groupName)
	if err != nil {
		return nil, err
	}

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

	scanner := newAnalyzeCacheScanner(file)

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

// analyzeCachePath returns the overview cache file path below dataDir.
// The file name is unchanged: sha256 of the sanitized group name.
func analyzeCachePath(dataDir string, providerName string, groupName string) string {
	// Sanitize group name for filesystem
	safeName := strings.ReplaceAll(groupName, "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")

	return filepath.Join(dataDir, "cache", providerName, sha256hashFromString(safeName)+".overview")
}

// getCacheFilePath returns the cache file path for a provider/group combination,
// below the configured -data root: proc.AnalyzeDataDir, else proc.DB.GetDataDir().
// With neither set it returns an error; there is no cwd-relative fallback.
func (proc *Processor) getCacheFilePath(providerName string, groupName string) (string, error) {
	dataDir := proc.AnalyzeDataDir
	if dataDir == "" && proc.DB != nil {
		dataDir = proc.DB.GetDataDir()
	}
	if dataDir == "" {
		return "", fmt.Errorf("analyze: no data directory set for the overview cache (-data)")
	}
	return analyzeCachePath(dataDir, providerName, groupName), nil
}

// analyzeProviderName returns the cache directory name of the backend: the base
// name of Backend.Host without a ":port" suffix. An IPv6 literal (bracketed, with or
// without a port, or bare) has its colons replaced by '_' ("[2001:db8::1]:119" →
// "2001_db8__1"); plain host names keep their historic directory name. Every cache
// function uses it, so AnalyzeGroup, the cache tools and the date-range helpers agree
// on one key.
func (proc *Processor) analyzeProviderName() (string, error) {
	if proc.Pool == nil {
		return "", fmt.Errorf("analyze: NNTP pool is nil")
	}
	if proc.Pool.Backend == nil {
		return "", fmt.Errorf("analyze: NNTP pool backend is nil")
	}
	host := proc.Pool.Backend.Host
	switch {
	case strings.HasPrefix(host, "["):
		// bracketed IPv6 literal, optionally followed by ":port"
		host = strings.TrimPrefix(host, "[")
		if i := strings.Index(host, "]"); i >= 0 {
			host = host[:i]
		}
		host = strings.ReplaceAll(host, ":", "_")
	case strings.Count(host, ":") > 1:
		// bare IPv6 literal (a port cannot be told apart, none is cut)
		host = strings.ReplaceAll(host, ":", "_")
	default:
		if i := strings.Index(host, ":"); i >= 0 {
			host = host[:i]
		}
	}
	name := filepath.Base(host)
	if host == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		return "", fmt.Errorf("analyze: backend host %q gives no usable cache directory name", proc.Pool.Backend.Host)
	}
	return name, nil
}

// analyzeCacheFile returns the provider name and the cache file path for groupName.
func (proc *Processor) analyzeCacheFile(groupName string) (providerName string, cacheFile string, err error) {
	providerName, err = proc.analyzeProviderName()
	if err != nil {
		return "", "", err
	}
	cacheFile, err = proc.getCacheFilePath(providerName, groupName)
	if err != nil {
		return "", "", err
	}
	return providerName, cacheFile, nil
}

// cacheFileExists checks if a cache file exists and is not empty
func (proc *Processor) cacheFileExists(cacheFile string) bool {
	info, err := os.Stat(cacheFile)
	return err == nil && info.Size() > 0
}

// GetCachedMessageIDs returns cached message IDs for download optimization
func (proc *Processor) GetCachedMessageIDs(groupName string, startArticle, endArticle int64) ([]nntp.HeaderLine, error) {
	_, cacheFile, err := proc.analyzeCacheFile(groupName)
	if err != nil {
		return nil, err
	}

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
	scanner := newAnalyzeCacheScanner(file)

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
	_, cacheFile, err := proc.analyzeCacheFile(groupName)
	if err != nil {
		return err
	}

	if err := os.Remove(cacheFile); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to remove cache file: %w", err)
	}
	log.Printf("Cleared cache for group '%s'", groupName)

	return nil
}

// GetCacheStats returns statistics about cached overview data
func (proc *Processor) GetCacheStats(groupName string) (*GroupAnalysis, error) {
	providerName, cacheFile, err := proc.analyzeCacheFile(groupName)
	if err != nil {
		return nil, err
	}

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
	_, cacheFile, err := proc.analyzeCacheFile(groupName)
	if err != nil {
		return err
	}

	if !proc.cacheFileExists(cacheFile) {
		return fmt.Errorf("cache file does not exist for group '%s'", groupName)
	}

	file, err := os.Open(cacheFile)
	if err != nil {
		return fmt.Errorf("failed to open cache file: %w", err)
	}
	defer file.Close()

	scanner := newAnalyzeCacheScanner(file)
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
	_, cacheFile, err := proc.analyzeCacheFile(groupName)
	if err != nil {
		return 0, err
	}

	if !proc.cacheFileExists(cacheFile) {
		return 0, fmt.Errorf("no cached data found for group '%s'", groupName)
	}

	file, err := os.Open(cacheFile)
	if err != nil {
		return 0, fmt.Errorf("failed to open cache file: %w", err)
	}
	defer file.Close()

	var count int64
	scanner := newAnalyzeCacheScanner(file)

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

// analysisExport is the JSON shape of a GroupAnalysis export (keys unchanged).
type analysisExport struct {
	GroupName      string `json:"group_name"`
	ProviderName   string `json:"provider_name"`
	TotalArticles  int64  `json:"total_articles"`
	TotalBytes     int64  `json:"total_bytes"`
	FirstArticle   int64  `json:"first_article"`
	LastArticle    int64  `json:"last_article"`
	OldestDate     string `json:"oldest_date"`
	NewestDate     string `json:"newest_date"`
	AnalyzedAt     string `json:"analyzed_at"`
	CacheExists    bool   `json:"cache_exists"`
	CachedArticles int64  `json:"cached_articles"`
}

// ExportAnalysisToJSON exports group analysis results to JSON format
func (analysis *GroupAnalysis) ExportAnalysisToJSON() ([]byte, error) {
	return json.MarshalIndent(analysisExport{
		GroupName:      analysis.GroupName,
		ProviderName:   analysis.ProviderName,
		TotalArticles:  analysis.TotalArticles,
		TotalBytes:     analysis.TotalBytes,
		FirstArticle:   analysis.FirstArticle,
		LastArticle:    analysis.LastArticle,
		OldestDate:     analysis.OldestDate.Format(time.RFC3339),
		NewestDate:     analysis.NewestDate.Format(time.RFC3339),
		AnalyzedAt:     analysis.AnalyzedAt.Format(time.RFC3339),
		CacheExists:    analysis.CacheExists,
		CachedArticles: analysis.CachedArticles,
	}, "", "\t")
}

// analysisCSVHeader is the column order of ExportAnalysisToCSV.
var analysisCSVHeader = []string{"group_name", "provider_name", "total_articles", "total_bytes",
	"first_article", "last_article", "oldest_date", "newest_date", "analyzed_at", "cache_exists", "cached_articles"}

// csvRecord encodes one CSV record (RFC 4180 quoting) terminated by a newline.
func csvRecord(fields []string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write(fields) // a strings.Builder never fails a write
	w.Flush()
	return b.String()
}

// ExportAnalysisToCSV exports group analysis results to CSV format
func (analysis *GroupAnalysis) ExportAnalysisToCSV() string {
	return csvRecord([]string{
		analysis.GroupName,
		analysis.ProviderName,
		strconv.FormatInt(analysis.TotalArticles, 10),
		strconv.FormatInt(analysis.TotalBytes, 10),
		strconv.FormatInt(analysis.FirstArticle, 10),
		strconv.FormatInt(analysis.LastArticle, 10),
		analysis.OldestDate.Format("2006-01-02"),
		analysis.NewestDate.Format("2006-01-02"),
		analysis.AnalyzedAt.Format("2006-01-02 15:04:05"),
		strconv.FormatBool(analysis.CacheExists),
		strconv.FormatInt(analysis.CachedArticles, 10),
	})
}

// GetCSVHeader returns the CSV header for analysis exports
func GetAnalysisCSVHeader() string {
	return csvRecord(analysisCSVHeader)
}

// AnalyzeMode performs newsgroup analysis operations using an existing processor instance
// outDir is the directory for -export files (see printAnalysisResults).
func (proc *Processor) AnalyzeMode(testGrp string, forceRefresh bool, maxAnalyzeArticles int64,
	startDate string, endDate string, exportFormat string, validateCache bool,
	clearCache bool, cacheStats bool, outDir string) (*GroupAnalysis, error) {

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
		if err := printAnalysisResults(stats, exportFormat, outDir); err != nil {
			return nil, fmt.Errorf("failed to print cache stats: %w", err)
		}
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
	if err := printAnalysisResults(analysis, exportFormat, outDir); err != nil {
		return nil, fmt.Errorf("failed to print analysis results: %w", err)
	}

	return analysis, nil
}

// AnalyzeModeStandalone is a wrapper for backward compatibility that creates its own pool.
// dataDir is the -data root (overview cache below <dataDir>/cache), outDir the export directory.
func AnalyzeModeStandalone(host *string, port *int, username *string, password *string, ssl *bool, timeout *int,
	testGrp *string, forceRefresh *bool, maxAnalyzeArticles *int64,
	startDate *string, endDate *string, exportFormat *string, validateCache *bool,
	clearCache *bool, cacheStats *bool, dataDir *string, outDir *string) error {

	if *testGrp == "" {
		return fmt.Errorf("group name is required for analysis (use -group flag)")
	}
	if dataDir == nil || *dataDir == "" {
		return fmt.Errorf("data directory is required for analysis (use -data flag)")
	}
	if outDir == nil {
		return fmt.Errorf("output directory is required for analysis")
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

	// Create processor (minimal setup for analysis only)
	proc := &Processor{
		Pool:           pool,
		AnalyzeDataDir: *dataDir,
	}

	// Use the proper method
	_, err := proc.AnalyzeMode(*testGrp, *forceRefresh, *maxAnalyzeArticles,
		*startDate, *endDate, *exportFormat, *validateCache, *clearCache, *cacheStats, *outDir)

	return err
}

// printAnalysisResults formats and displays analysis results.
// With exportFormat "json" or "csv" it also writes <outDir>/<group>.json or appends to
// <outDir>/<first label>.csv; outDir is created (0750) when needed. A group name that
// is not a valid newsgroup name (RFC 5536 syntax: no "/", no ".." component) is not
// used as a file name: the export is skipped with a warning and the result is still
// printed. It returns an error instead
// of exiting when the export directory or file cannot be written.
func printAnalysisResults(analysis *GroupAnalysis, exportFormat string, outDir string) error {
	if exportFormat != "" && !common.ValidGroupNameSyntax(analysis.GroupName) {
		// The name would become a file name: skip the export, keep the printed result.
		log.Printf("[ANALYZE] Warning: not exporting group %q to %s: not a valid newsgroup name", analysis.GroupName, exportFormat)
		exportFormat = ""
	}
	if exportFormat != "" {
		if outDir == "" {
			return fmt.Errorf("no output directory for the %s export", exportFormat)
		}
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}
	}
	switch exportFormat {
	case "json":
		filename := filepath.Join(outDir, analysis.GroupName+".json")
		jsonData, err := analysis.ExportAnalysisToJSON()
		if err != nil {
			return fmt.Errorf("failed to export JSON: %w", err)
		}
		err = os.WriteFile(filename, append(jsonData, '\n'), 0o600)
		if err != nil {
			return fmt.Errorf("failed to write JSON file %s: %w", filename, err)
		}
		fmt.Printf("JSON exported to: %s\n", filename)
	case "csv":
		// cut groupname by first dot to get primary hierarchie
		// e.g. "de.rec.film.kritiken" -> "de"
		hier := strings.Split(analysis.GroupName, ".")[0]
		filename := filepath.Join(outDir, hier+".csv")
		if err := appendAnalysisCSV(filename, analysis); err != nil {
			return fmt.Errorf("failed to write CSV file %s: %w", filename, err)
		}
		fmt.Printf("CSV exported to: %s\n", filename)
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
	return nil
}

// appendAnalysisCSV appends the CSV record of analysis to filename and writes the
// header first when the file is new or empty.
func appendAnalysisCSV(filename string, analysis *GroupAnalysis) error {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err == nil && info.Size() == 0 {
		_, err = f.WriteString(GetAnalysisCSVHeader())
	}
	if err == nil {
		_, err = f.WriteString(analysis.ExportAnalysisToCSV())
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
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
