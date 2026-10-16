# NNTP Analyze Tool

A command-line tool that fetches the overview (XOVER) data of newsgroups from one NNTP server,
caches it locally and prints statistics about article counts, sizes and dates.

## Features

- **Group Analysis**: one newsgroup, every group in the local database (`$all`), a name prefix
  (`prefix.*`), or the groups listed in an active file
- **Article Size Distribution**: breakdown of article sizes across fixed ranges
- **Date Range Filtering**: restrict the statistics to a date range
- **Overview Cache**: later runs read the cached XOVER data instead of fetching it again
- **Export Formats**: JSON (one file per group) or CSV (one row per group)

## Installation

```bash
./build_analyze.sh            # writes build/nntp-analyze, with the version from appVersion.txt
# or
go build -o build/nntp-analyze ./cmd/nntp-analyze
```

`./nntp-analyze -help` prints usage examples, `./nntp-analyze -h` lists every flag with its default.

## Usage

### Basic Analysis

Analyze a single newsgroup on the default server (see [Server and defaults](#server-and-defaults)):
```bash
./nntp-analyze -group alt.test
```

On another server, plain text on port 119 (TLS is on by default, so it must be switched off):
```bash
./nntp-analyze -host news.example.com -port 119 -ssl=false -group alt.binaries.movies
```

On another server with TLS on port 563 and an account:
```bash
./nntp-analyze -host news.example.com -username user -password pass -group alt.test
```

### Analyze All Groups

`-group '$all'` analyzes every newsgroup in the go-pugleaf database below `-data`;
`-group 'prefix.*'` only those whose name starts with `prefix.`. Quote both in the shell.
```bash
./nntp-analyze -data ./data -group '$all'
./nntp-analyze -data ./data -group 'comp.*'
```

### Analyze Groups from an Active File

`-groups-file` is used instead of `-group`. Each line is `groupname high low status`. Empty lines
and `#` comments are skipped; lines with fewer than 4 fields and invalid group names are skipped
with a warning.
```bash
./nntp-analyze -groups-file /path/to/active
```
A groups-file run also writes a summary to `<out>/<active file path with / replaced by _>.summary.txt`.

### All Options

```
Group selection (one of the two is required):
  -group string        Newsgroup name, '$all' or 'prefix.*' ('$all' and 'prefix.*' read the
                       group list from the databases below -data)
  -groups-file string  Active file with the newsgroups to analyze

Server:
  -host string         NNTP server hostname (default "81-171-22-215.pugleaf.net")
  -port int            NNTP server port (default 563)
  -ssl                 Use SSL/TLS (default true; use -ssl=false for a plain-text port)
  -username string     NNTP username (default "read")
  -password string     NNTP password (default "only")
  -timeout int         Connect timeout in seconds (default 30), in every mode

Analysis:
  -force-refresh       Ignore the overview cache and fetch from the server again
  -max-analyze int     Stop after N articles (default 0 = no limit); a limited run writes no cache
  -start-date string   Only count articles from this date on (YYYY-MM-DD)
  -end-date string     Only count articles up to this date (YYYY-MM-DD)

Cache management (no analysis is run):
  -cache-stats         Show statistics from the cache file only (no XOVER fetch)
  -validate-cache      Check the format of the cache file
  -clear-cache         Delete the cache file

Files and output:
  -data string         Data root: overview cache and, for '$all'/'prefix.*', the databases (default "./data")
  -out string          Directory for -export files and the groups-file summary (default <data>/analyze-out)
  -export string       Export format: 'json' or 'csv'

  -help                Show usage examples and exit
```

### Examples

```bash
# Analyze with date filtering
./nntp-analyze -group alt.test -start-date 2024-01-01 -end-date 2024-12-31

# Fetch again and stop after 10,000 articles (this run writes no cache)
./nntp-analyze -group alt.test -force-refresh -max-analyze 10000

# Export results to JSON below a scratch data root
./nntp-analyze -data ./data-test -group alt.test -export json
```

## Server and defaults

Without `-host`, the tool connects to `81-171-22-215.pugleaf.net:563` with TLS. This is the
go-pugleaf project's public server, and the default `-username read` / `-password only` are its public
read-only credentials. Pass `-host` (and the account of that server) to analyze another server.
The default port 563 is the TLS port: for a plain-text server on port 119 add `-ssl=false`.

## Output Format

### Basic Statistics
- Group name and provider (the `-host` name without the port)
- Group size (article count reported by the server) and article number range
- Number of analyzed articles, with a warning when it is lower than the group size
- Total bytes
- Date range (oldest to newest article), time span and articles per day
- Cache status and analysis time

### Article Size Distribution
Articles are counted in these size ranges (by the XOVER byte count):
`< 4K`, `4K - 16K`, `16K - 32K`, `32K - 64K`, `64K - 128K`, `128K - 256K`, `256K - 512K`, `> 512K`.
Each range shows the count and the percentage of all analyzed articles.

### Example Output

```
=== Analysis Results ===
Group: alt.binaries.movies
Provider: news.example.com
Group Size: 15,432 articles (range 1001 - 16432)
Analyzed Articles: 15,432
Total Bytes: 2.3 GB
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

< 4K           :    1,245 (  8.1%)
4K - 16K       :    2,891 ( 18.7%)
16K - 32K      :    3,567 ( 23.1%)
32K - 64K      :    4,123 ( 26.7%)
64K - 128K     :    2,234 ( 14.5%)
128K - 256K    :      892 (  5.8%)
256K - 512K    :      345 (  2.2%)
> 512K         :      135 (  0.9%)
```

`$all`, `prefix.*` and `-groups-file` runs print this per group, then a summary with aggregate
statistics and the top 10 groups by article count and by total size.

## Caching System

- **Cache Location**: `<data>/cache/<host>/<sha256 of the group name>.overview`, where `<data>` is
  `-data` (default `./data`) and `<host>` is the `-host` name without a port (an IPv6 literal such as
  `[2001:db8::1]` becomes `2001_db8__1`).
- **Cache Format**: one tab-separated XOVER line per article
  (number, subject, from, date, message-id, references, bytes, lines).
- **Creation**: the first full analysis of a group fetches the whole group in batches of 10,000
  articles and writes the cache to a temporary file, which is renamed into place only after every
  batch succeeded. A failed batch fails the analysis of that group and leaves an existing cache as it was.
- **Reuse**: later runs read the cache as long as it exists; it is not updated with new articles.
  Use `-force-refresh` to fetch the group again (this replaces the cache).
- **Limited runs**: a run with `-max-analyze` writes no cache; an existing cache stays.
- **Date filters**: the cache always holds the whole group; `-start-date`/`-end-date` only filter
  the statistics.
- **Permissions**: cache and export directories are created with mode 0750, files with 0600.

### Cache Management Commands

```bash
# Delete the cache file of a group
./nntp-analyze -group alt.test -clear-cache

# Show statistics from the cache file without contacting the server for XOVER data
./nntp-analyze -group alt.test -cache-stats

# Check that the cache file has valid XOVER lines
./nntp-analyze -group alt.test -validate-cache
```

The cache file name depends on `-host`: pass the same `-host` (and `-data`) as in the run that
created the cache.

## Date Filtering

```bash
# Articles from a specific date range
./nntp-analyze -group alt.test -start-date 2024-01-01 -end-date 2024-12-31

# Articles from a specific date onwards
./nntp-analyze -group alt.test -start-date 2024-06-01

# Articles up to a specific date
./nntp-analyze -group alt.test -end-date 2024-12-31
```

Articles whose Date header cannot be parsed are counted without the date filter. They are logged,
and `$all`, `prefix.*` and `-groups-file` runs list them again at the end.

## Export Formats

Exports are written to `-out` (default `<data>/analyze-out`), which is created when needed.
A group name that is not a valid newsgroup name is not exported (a warning is logged; the results
are still printed).

### JSON Export
```bash
./nntp-analyze -group alt.test -export json
```
Writes `<out>/alt.test.json` (replacing an existing file) with the fields `group_name`,
`provider_name`, `total_articles`, `total_bytes`, `first_article`, `last_article`, `oldest_date`,
`newest_date`, `analyzed_at`, `cache_exists` and `cached_articles`.

### CSV Export
```bash
./nntp-analyze -group alt.test -export csv
```
Appends one row to `<out>/<hierarchy>.csv`, where `<hierarchy>` is the group name up to the first
dot (`alt.test` goes to `alt.csv`). A new or empty file first gets this header, which a `-export csv`
run also prints at its start:
```
group_name,provider_name,total_articles,total_bytes,first_article,last_article,oldest_date,newest_date,analyzed_at,cache_exists,cached_articles
```

## Performance Considerations

- **Large Groups**: there is no article limit by default (`-max-analyze 0`); set `-max-analyze`
  to stop early.
- **Batch Processing**: XOVER data is fetched in batches of 10,000 articles.
- **Connection Pooling**: one pool (up to 5 connections) is shared by all groups of a run.
- **Caching**: repeated analyses of a group read the local cache instead of the server.

## Error Handling

- **Network Issues**: there is no retry; a failed XOVER batch fails the analysis of that group,
  and `$all`/`-groups-file` runs continue with the next group and list the failures at the end.
- **Invalid Dates**: malformed Date headers are logged but don't stop the analysis.
- **Missing Articles**: a batch without articles (NNTP 423) is skipped.
- **Cache Corruption**: there is no automatic rebuild; check with `-validate-cache`, then use
  `-force-refresh` or `-clear-cache`.
- **Exit status**: a single-group run exits with 1 when the analysis fails; `$all`, `prefix.*` and
  `-groups-file` runs exit with 0 and list the failed groups at the end. A failed `-validate-cache`
  check is printed but does not count as a failure.

## Troubleshooting

1. **Connection Refused**: check host, port, `-ssl` (TLS is on by default) and firewall settings
2. **Authentication Failed**: verify username and password (the defaults belong to the default `-host`)
3. **Group Not Found**: ensure the newsgroup exists on the server and is accessible
4. **Stale or broken cache**: use `-force-refresh` or `-clear-cache`

## License

This tool is part of the go-pugleaf project. See the main project license for details.
