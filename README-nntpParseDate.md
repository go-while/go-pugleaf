# NNTP Date Parsing Implementation

## Overview

The NNTP date parsing system in go-pugleaf is a multi-layered implementation designed to handle the wide variety of date formats found in NNTP/Usenet articles. This document explains how the parsing system works, its integration with the codebase, and recent fixes for 2-digit year handling.

## Architecture

### Core Components

All parser code is in `internal/processor/proc-utils.go`:

1. **`ParseNNTPDate`** - Main (lenient) entry point; returns `time.Time`
2. **`ParseNNTPDateStrict`** - Same pipeline without the rslight epoch fallback; returns `(time.Time, bool)`,
   `ok` only for a layout match or a brute-force result whose year and month were both found in the string
3. **`NNTPDateLayouts`** - Comprehensive array of date format patterns
4. **`bruteForceDateParse`** / **`bruteForceDateParseOK`** - Fallback parser for malformed dates (the `OK` variant
   also reports whether year and month were found)
5. **`parseUnixEpochToRFC1123`** - Last resort for the rslight import (Unix epoch strings)
6. **`database.GlobalDateParser`** - Adapter (`internal/database/queries.go`) for system-wide integration

### Integration Pattern

The date parser uses a global adapter pattern to integrate with the database layer:

```go
// In cmd/web, cmd/nntp-server, cmd/nntp-fetcher and cmd/rslight-importer main.go
database.GlobalDateParser = processor.ParseNNTPDate

// In internal/database/queries.go
func parseDateString(dateStr string) time.Time {
    if GlobalDateParser != nil {
        if parsed := GlobalDateParser(dateStr); !parsed.IsZero() {
            return parsed
        }
        // Fall through to basic parsing if adapter fails
    }
    // Basic fallback parsing: testLayouts (RFC3339 and SQLite-style timestamps)
}
```

## Implementation Details

### 1. Main Parsing Function

Located in `internal/processor/proc-utils.go`:

```go
func ParseNNTPDate(dateStr string) time.Time {
    t, ok := ParseNNTPDateStrict(dateStr)
    if ok || !t.IsZero() {
        return t
    }
    // Last resort for RSLIGHT import: Unix epoch strings
    if RunRSLIGHTImport && dateStr != "" {
        return parseUnixEpochToRFC1123(strings.TrimSpace(parenRe.ReplaceAllString(dateStr, "")))
    }
    return time.Time{}
}
```

`ParseNNTPDateStrict` does the actual work, in this order:

1. Empty string → `time.Time{}, false`
2. Normalization: strip a trailing parenthesized zone (`" (NZDT)"`), trim, fix the rslight `"Sum, "` typo (only
   with `RunRSLIGHTImport`), replace US zone abbreviations EDT/EST/CDT/CST/MDT/MST/PDT/PST with numeric offsets,
   and widen 3-digit offsets (`+200` → `+0200`)
3. Try every layout in `NNTPDateLayouts` with `time.Parse` (first match wins, `ok=true`)
4. Otherwise return `bruteForceDateParseOK(dateStr)`

### 2. Date Layout Array

The `NNTPDateLayouts` array contains over 300 different date format patterns:

- **Standard RFC formats**: RFC822, RFC822Z, RFC850, RFC1123, etc.
- **ISO 8601 variants**: Various timezone and precision formats
- **European formats**: US vs European date ordering (MM/DD vs DD/MM)
- **2-digit year formats**: With proper century detection
- **Timezone variants**: Including European timezones (MESZ, MEZ, CET, CEST)
- **Malformed formats**: Common parsing issues found in real NNTP data

### 3. Brute Force Parser

When standard layouts fail, `bruteForceDateParse` uses regex patterns to extract:

- **Year**: 4-digit (1970-2099) or 2-digit with century heuristic
- **Month**: Name-based parsing (case insensitive, `jan`/`january` … `dec`/`december`)
- **Day**: Flexible day detection (1-31)
- **Time**: HH:MM:SS or HH:MM formats (default 12:00:00 when none is found)
- **Numeric dates**: `YYYY/MM/DD`, `DD/MM/YYYY`, `MM/DD/YYYY`, `DD/MM/YY`, `MM/DD/YY` (separators `/`, `-`, `.`)
  when year, month or day is still missing

Missing parts are defaulted: year 1990, month January, day 1 (a day past the end of a 30-day month or February
is clamped). The result is always UTC. A result more than 25 hours in the future returns `time.Time{}`. `ok` is true only when
year and month were both found in the string.

#### 2-Digit Year Heuristic

**Fixed in August 2025**: The century detection now matches Go's standard behavior. Two-digit candidates are
tried in three passes: values 60-99 first, then 32-59 (always 20xx), then 00-31 (skipping numbers that are the day,
the month or part of the time). The century rule used in the first and third pass and for numeric dates is:

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

The date parser is integrated throughout the codebase via the `GlobalDateParser` adapter and direct calls:

#### 1. Main Applications
These set `database.GlobalDateParser = processor.ParseNNTPDate` after opening the database:
- **`cmd/web/main.go`** - Web server for article display
- **`cmd/nntp-server/main.go`** - NNTP server for incoming articles
- **`cmd/nntp-fetcher/main.go`** - Article fetching from remote servers
- **`cmd/rslight-importer/main.go`** - RockSolid Light import

Direct callers (no adapter):
- **`cmd/recover-db/main.go`** - Date re-check uses `processor.ParseNNTPDateStrict`; a string it rejects is
  reported and never rewritten
- **`cmd/parsedates/main.go`** - CLI that prints the `ParseNNTPDate` result for one date string

#### 2. Core Processing
- **`internal/processor/threading.go`** - Article processing pipeline, bulk mode (fetcher/importers): lenient
  `ParseNNTPDate`
- **`internal/processor/intake.go`** - Non-bulk intake (POST/IHAVE/TAKETHIS): `checkIntakeDates` parses Date (and
  Injection-Date) with `ParseNNTPDateStrict` and rejects `!ok` dates and dates more than `MaxFutureDate` (24 h)
  in the future
- **`internal/processor/proc_ImportOV.go`** - Overview import
- **`internal/processor/proc_DLArt.go`** - Article downloading
- **`internal/processor/analyze.go`** - Newsgroup analysis

#### 3. Database Layer
- **`internal/database/queries.go`** - Date parsing adapter integration

### Initialization Pattern

Each application follows this pattern:

```go
func main() {
    // ... flags, dbConfig ...
    db, err := database.OpenDatabase(dbConfig)
    if err != nil {
        log.Fatalf("[WEB]: Failed to initialize database: %v", err)
    }

    // Set up the date parser adapter to use processor's ParseNNTPDate
    database.GlobalDateParser = processor.ParseNNTPDate

    // Continue with application-specific logic...
}
```

## Common Use Cases

### 1. Article Processing

When processing incoming NNTP articles:

```go
// In threading.go (bulk mode; non-bulk intake uses checkIntakeDates in intake.go)
article.DateSent = ParseNNTPDate(article.DateString)
if article.DateSent.IsZero() {
    log.Printf("[ERROR-HDR] Article '%s' no valid date... headerDate='%v' dateString='%s'", ...)
    return history.CaseError, rejected("invalid date")
}
if article.DateSent.After(time.Now().Add(MaxFutureDate)) {
    return history.CaseError, rejected("date in the future")
}
```

### 2. Overview Import

When importing article overviews:

```go
// In proc_ImportOV.go
var date time.Time
if ov.Date != "" {
    date = ParseNNTPDate(ov.Date)
}
o := &models.Overview{
    DateSent:   date,
    DateString: ov.Date, // Keep original for debugging
    // ... other fields
}
```

### 3. Analysis and Recovery

For database analysis and recovery operations:

```go
// In analyze.go (recover-db uses ParseNNTPDateStrict instead)
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
2. **Brute force parsing** - Handles malformed dates; missing year/month/day are defaulted, so even garbage
   gives a non-zero date (`"garbage"` → 1990-01-01 12:00 UTC) with `ok=false` from `ParseNNTPDateStrict`
3. **Zero time** - `ParseNNTPDate` returns `time.Time{}` only for an empty string or a brute-force result more
   than 25 h in the future (after the rslight epoch fallback, when enabled)
4. **Strict callers** - Non-bulk intake and `recover-db` reject `!ok` dates instead of storing the default
5. **Original string preservation** - Stores `date_string` alongside parsed `date_sent`

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

Tests in `internal/processor/intake_test.go`:
- `TestIntakeProcParseNNTPDateStrict` - RFC formats with numeric offsets, a trailing `(CEST)` comment, `EST`
  normalization, a brute-force match (`"sometime 7 March 1999 at 10:20"`), the 1990-01-01 fallback for
  `"garbage"`, `"Tuesday 7pm"` and `"March"` (`ok=false`), the empty string, and the 25 h future bound of
  `bruteForceDateParseOK`; it also checks that `ParseNNTPDate` results stay unchanged (K1)
- `TestIntakeProcFutureMargin` - `checkIntakeDates`: the 24 h `MaxFutureDate` boundary, invalid dates and the
  Injection-Date rules

There is no automated test for the 2-digit year boundary cases; this example shows the expected results (the
`MESZ` layouts match them, so `time.Parse`'s `06` rule applies):
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

To check a single string by hand, build and run `cmd/parsedates` (`parsedates "27 May 09 1:01:53 PM"`).
