# NNTP Date Parsing Implementation

## Overview

The NNTP date parsing system in go-pugleaf is a multi-layered implementation designed to handle the wide variety of date formats found in NNTP/Usenet articles. This document explains how the parsing system works, its integration with the codebase, and recent fixes for 2-digit year handling.

## Architecture

### Core Components

1. **`ParseNNTPDate`** - Main entry point function in `internal/processor/proc-utils.go`
2. **`NNTPDateLayouts`** - Comprehensive array of date format patterns
3. **`bruteForceDateParse`** - Fallback parser for malformed dates
4. **`GlobalDateParser`** - Adapter pattern for system-wide integration

### Integration Pattern

The date parser uses a global adapter pattern to integrate with the database layer:

```go
// In cmd/*/main.go files
database.GlobalDateParser = processor.ParseNNTPDate

// In internal/database/queries.go
func parseDateString(dateStr string) time.Time {
    if GlobalDateParser != nil {
        return GlobalDateParser(dateStr)
    }
    // Fallback to basic parsing
}
```

## Implementation Details

### 1. Main Parsing Function

Located in `internal/processor/proc-utils.go`:

```go
func ParseNNTPDate(dateStr string) time.Time {
    // Try standard Go layouts first
    for _, layout := range NNTPDateLayouts {
        if t, err := time.Parse(layout, dateStr); err == nil {
            return t
        }
    }

    // Fallback to brute force parsing
    return bruteForceDateParse(dateStr)
}
```

### 2. Date Layout Array

The `NNTPDateLayouts` array contains over 100 different date format patterns:

- **Standard RFC formats**: RFC822, RFC822Z, RFC850, RFC1123, etc.
- **ISO 8601 variants**: Various timezone and precision formats
- **European formats**: US vs European date ordering (MM/DD vs DD/MM)
- **2-digit year formats**: With proper century detection
- **Timezone variants**: Including European timezones (MESZ, MEZ, CET, CEST)
- **Malformed formats**: Common parsing issues found in real NNTP data

### 3. Brute Force Parser

When standard layouts fail, `bruteForceDateParse` uses regex patterns to extract:

- **Year**: 4-digit (1970-2099) or 2-digit with century heuristic
- **Month**: Name-based parsing (case insensitive)
- **Day**: Flexible day detection (1-31)
- **Time**: HH:MM:SS or HH:MM formats

#### 2-Digit Year Heuristic

**Fixed in August 2025**: The century detection now matches Go's standard behavior:

```go
// Heuristic: match Go's behavior - 69-99 = 1969-1999, 00-68 = 2000-2068
if y >= 69 {
    year = 1900 + y
} else {
    year = 2000 + y
}
```

This ensures dates like `"Wed, 30 Jun 93 21:04:13 MESZ"` parse correctly as `1993-06-30` instead of `2030-06-30`.

## Codebase Integration

### Where ParseNNTPDate is Used

The date parser is integrated throughout the codebase via the `GlobalDateParser` adapter:

#### 1. Main Applications
- **`cmd/web/main.go`** - Web server for article display
- **`cmd/nntp-server/main.go`** - NNTP server for incoming articles
- **`cmd/nntp-fetcher/main.go`** - Article fetching from remote servers
- **`cmd/history-rebuild/main.go`** - History database rebuilding
- **`cmd/recover-db/main.go`** - Database recovery and validation

#### 2. Core Processing
- **`internal/processor/threading.go`** - Article processing pipeline
- **`internal/processor/proc_ImportOV.go`** - Overview import
- **`internal/processor/proc_DLArt.go`** - Article downloading
- **`internal/processor/analyze.go`** - Newsgroup analysis

#### 3. Database Layer
- **`internal/database/queries.go`** - Date parsing adapter integration

### Initialization Pattern

Each application follows this pattern:

```go
func main() {
    // Initialize database
    db, err := database.OpenDatabase(nil)
    if err != nil {
        log.Fatalf("Failed to initialize database: %v", err)
    }
    defer db.Shutdown()

    // Set up the date parser adapter
    database.GlobalDateParser = processor.ParseNNTPDate
    log.Printf("Date parser adapter initialized")

    // Continue with application-specific logic...
}
```

## Common Use Cases

### 1. Article Processing

When processing incoming NNTP articles:

```go
// In threading.go
dateSent := ParseNNTPDate(getHeaderFirst(art.Headers, "date"))
dateString := getHeaderFirst(art.Headers, "date")
if dateSent.IsZero() {
    log.Printf("[WARN:OLD] Article '%s' no valid date... dateString='%s'",
               art.MessageID, dateString)
}
```

### 2. Overview Import

When importing article overviews:

```go
// In proc_ImportOV.go
date := ParseNNTPDate(ov.Date)
o := &models.Overview{
    DateSent:   date,
    DateString: ov.Date, // Keep original for debugging
    // ... other fields
}
```

### 3. Analysis and Recovery

For database analysis and recovery operations:

```go
// In analyze.go and recover-db
date := ParseNNTPDate(ov.Date)
if date.IsZero() {
    InvalidMutex.Lock()
    InvalidDates[ov.MessageID] = ov.Date
    InvalidMutex.Unlock()
    log.Printf("[WARN]: Could not parse date:'%s'", ov.Date)
}
```

## Recent Improvements (August 2025)

### Problem Fixed
- **Issue**: 2-digit years were incorrectly parsed (e.g., `93` → `2030` instead of `1993`)
- **Root Cause**:
  1. European timezone `MESZ` not in standard layouts, causing fallback to `bruteForceDateParse`
  2. Different century heuristic than Go's standard behavior

### Solution Implemented
1. **Added European timezone layouts** to prevent fallback:
   ```go
   "Mon, _2 Jan 2006 15:04:05 MESZ",
   "Mon, _2 Jan 06 15:04:05 MESZ",
   "Mon, _2 Jan 2006 15:04:05 MEZ",
   "Mon, _2 Jan 06 15:04:05 CET",
   "Mon, _2 Jan 2006 15:04:05 CEST",
   ```

2. **Fixed 2-digit year heuristic** to match Go's behavior:
   ```go
   // OLD: 70-99 = 1970-1999, 00-69 = 2000-2069
   // NEW: 69-99 = 1969-1999, 00-68 = 2000-2068
   ```

### Verification
All boundary cases now work correctly:
- `68` → `2068` ✅
- `69` → `1969` ✅
- `93` → `1993` ✅ (was `2030` ❌)
- `00` → `2000` ✅

## Error Handling

### Graceful Degradation
1. **Standard layouts tried first** - Fast path for common formats
2. **Brute force parsing** - Handles malformed dates
3. **Zero time fallback** - Returns `time.Time{}` for unparseable dates
4. **Original string preservation** - Stores `date_string` alongside parsed `date_sent`

### Logging and Debugging
- Invalid dates are logged with message ID and newsgroup context
- Original date strings are preserved in the database
- Analysis tools track parsing failures for investigation

## Performance Considerations

### Optimizations
- **Layout ordering**: Most common formats first for fast matching
- **Single allocation**: No object pooling to avoid race conditions
- **Regex compilation**: Patterns compiled once at startup
- **Early returns**: Fast path for standard formats

### Memory Management
- No object pools (removed due to race conditions)
- Relies on Go's garbage collector
- Minimal allocations in hot paths

## Future Improvements

### Potential Enhancements
1. **Timezone database integration** - Handle more timezone abbreviations
2. **Locale-specific parsing** - Support for non-English month names
3. **Performance profiling** - Optimize layout ordering based on real data
4. **Validation metrics** - Track parsing success rates by format

### Monitoring
- Track parsing failures by newsgroup
- Monitor performance impact of fallback parsing
- Validate century heuristic accuracy over time

## Testing

The implementation includes comprehensive testing for:
- Standard RFC formats
- 2-digit year boundary cases
- European timezone handling
- Malformed date recovery
- Century detection accuracy

Example test cases verify the fix:
```go
testCases := []struct {
    date string
    expectedYear int
}{
    {"Wed, 30 Jun 68 21:04:13 MESZ", 2068},
    {"Wed, 30 Jun 69 21:04:13 MESZ", 1969},
    {"Wed, 30 Jun 93 21:04:13 MESZ", 1993}, // Fixed!
    {"Wed, 30 Jun 00 21:04:13 MESZ", 2000},
}
```

All test cases pass with the current implementation.
