# NNTP Analyze Tool

A powerful command-line tool for analyzing NNTP newsgroups, providing detailed statistics about article distribution, sizes, dates, and caching performance.

## Features

- **Comprehensive Group Analysis**: Analyze individual newsgroups or all groups at once
- **Article Size Distribution**: Detailed breakdown of article sizes across predefined ranges
- **Date Range Analysis**: Filter and analyze articles by date ranges
- **Caching System**: Intelligent caching for improved performance on subsequent analyses
- **Multiple Export Formats**: Export results in JSON or CSV format
- **Batch Processing**: Efficient processing of large newsgroups with configurable limits

## Installation

```bash
go build -o nntp-analyze ./cmd/nntp-analyze
```

## Usage

### Basic Analysis

Analyze a single newsgroup:
```bash
./nntp-analyze -host news.example.com -port 119 -group alt.binaries.movies
```

### Analyze All Groups

Use the special `$all` flag to analyze all groups in the database:
```bash
./nntp-analyze -host news.example.com -port 119 -group '$all'
```

### Advanced Options

```bash
./nntp-analyze [OPTIONS]

Required:
  -host string       NNTP server hostname
  -port int         NNTP server port (default: 119)
  -group string     Newsgroup name to analyze (use '$all' for all groups)

Authentication:
  -username string  NNTP username (if required)
  -password string  NNTP password (if required)
  -ssl             Use SSL/TLS connection

Analysis Options:
  -force           Force refresh of cached data
  -max-articles int Maximum number of articles to analyze (0 = unlimited)
  -start-date string Start date for analysis (YYYY-MM-DD format)
  -end-date string   End date for analysis (YYYY-MM-DD format)
  -timeout int      Connection timeout in seconds (default: 30)

Cache Management:
  -clear-cache     Clear cached data for the specified group
  -cache-stats     Show statistics for cached data only
  -validate-cache  Validate cache file integrity

Export Options:
  -export string   Export format: 'json' or 'csv'

Examples:
  # Analyze with date filtering
  ./nntp-analyze -host news.server.com -group alt.test -start-date 2024-01-01 -end-date 2024-12-31

  # Force refresh and limit analysis
  ./nntp-analyze -host news.server.com -group alt.test -force -max-articles 10000

  # Export results to JSON
  ./nntp-analyze -host news.server.com -group alt.test -export json

  # Analyze with SSL
  ./nntp-analyze -host ssl.news.server.com -port 563 -ssl -group alt.test
```

## Output Format

The analyzer provides detailed statistics including:

### Basic Statistics
- Group name and provider information
- Total article count and size
- Article number range (first to last)
- Date range (oldest to newest articles)
- Time span and articles per day
- Cache status and performance

### Article Size Distribution
Articles are categorized into the following size ranges:
- **< 4K**: Small text posts, short messages
- **4K - 16K**: Medium text posts, small attachments
- **16K - 32K**: Large text posts, small binaries
- **32K - 64K**: Medium binaries, multi-part text
- **64K - 128K**: Large binaries, compressed files
- **128K - 256K**: Very large binaries
- **256K - 512K**: Huge binaries, large media files
- **> 512K**: Extremely large binaries

Each category shows:
- Absolute count with comma-separated formatting
- Percentage of total articles
- Human-readable size information

### Example Output

```
=== Analysis Results ===
Group: alt.binaries.movies
Provider: news.example.com
Total Articles: 15,432
Total Bytes: 2.3 GB
Article Range: 1001 - 16432
Date Range: 2024-01-15 to 2024-12-28
Time Span: 348.0 days
Articles per Day: 44.3
Cached Articles: 15,432
Cache Exists: true
Analyzed At: 2024-12-28 14:30:25

=== Article Size Distribution ===
Total Articles: 15,432
Total Size: 2.3 GB
Average Size: 156.2 KB

< 4K           :    1,245 ( 8.1%)
4K - 16K       :    2,891 (18.7%)
16K - 32K      :    3,567 (23.1%)
32K - 64K      :    4,123 (26.7%)
64K - 128K     :    2,234 (14.5%)
128K - 256K    :      892 ( 5.8%)
256K - 512K    :      345 ( 2.2%)
> 512K         :      135 ( 0.9%)
```

## Caching System

The analyzer uses an intelligent caching system to improve performance:

- **Cache Location**: `data/cache/{provider}/{sanitized_group_name}.overview`
- **Cache Format**: Tab-separated XOVER data for fast parsing
- **Automatic Creation**: Cache is created during first analysis
- **Incremental Updates**: Future analyses can reuse cached data
- **Cache Validation**: Built-in integrity checking

### Cache Management Commands

```bash
# Clear cache for a specific group
./nntp-analyze -host news.server.com -group alt.test -clear-cache

# Show cache statistics without re-analyzing
./nntp-analyze -host news.server.com -group alt.test -cache-stats

# Validate cache file integrity
./nntp-analyze -host news.server.com -group alt.test -validate-cache
```

## Date Filtering

The analyzer supports flexible date filtering:

```bash
# Analyze articles from a specific date range
./nntp-analyze -host news.server.com -group alt.test \
  -start-date 2024-01-01 -end-date 2024-12-31

# Analyze articles from a specific date onwards
./nntp-analyze -host news.server.com -group alt.test \
  -start-date 2024-06-01

# Analyze articles up to a specific date
./nntp-analyze -host news.server.com -group alt.test \
  -end-date 2024-12-31
```

## Export Formats

### JSON Export
```bash
./nntp-analyze -host news.server.com -group alt.test -export json
```

Produces structured JSON output suitable for integration with other tools.

### CSV Export
```bash
./nntp-analyze -host news.server.com -group alt.test -export csv
```

Produces comma-separated values suitable for spreadsheet applications.

## Performance Considerations

- **Large Groups**: Analysis is limited to 10,000 articles by default for performance
- **Batch Processing**: Articles are processed in 10,000-article batches
- **Connection Pooling**: Uses efficient connection pooling for NNTP operations
- **Memory Usage**: Streaming processing keeps memory usage low
- **Caching**: Significantly reduces analysis time for repeated operations

## Error Handling

The analyzer handles various error conditions gracefully:

- **Network Issues**: Automatic retry and timeout handling
- **Invalid Dates**: Malformed date headers are logged but don't stop analysis
- **Missing Articles**: Gaps in article sequences are handled transparently
- **Cache Corruption**: Automatic cache validation and rebuilding

## Troubleshooting

### Common Issues

1. **Connection Refused**: Check host, port, and firewall settings
2. **Authentication Failed**: Verify username and password
3. **Group Not Found**: Ensure the newsgroup exists and is accessible
4. **Cache Issues**: Use `-clear-cache` to rebuild corrupted cache files

### Debug Information

The analyzer provides detailed logging during operation:
- Connection status and authentication
- Batch processing progress
- Date parsing warnings for malformed headers
- Cache operations and statistics

## Integration

The analyzer can be integrated into larger workflows:

- **Monitoring**: Regular analysis of newsgroup activity
- **Reporting**: Automated generation of usage statistics
- **Data Pipeline**: CSV/JSON export for further processing
- **Alerting**: Detect unusual patterns in newsgroup activity

## Contributing

When contributing to the analyzer:

1. Test with various newsgroup types (text, binary, mixed)
2. Ensure backward compatibility with existing cache files
3. Add appropriate error handling for new features
4. Update this README for new functionality

## License

This tool is part of the go-pugleaf project. See the main project license for details.
