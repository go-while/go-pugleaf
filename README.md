# 🐶 go-pugleaf

**A modern NNTP server and web gateway for Usenet/NetNews built in Go**

[![Go Version](https://img.shields.io/badge/Go-1.24+-blue.svg)](https://golang.org)
[![Development Status](https://img.shields.io/badge/Status-Testing-green.svg)](#development-status)
[![License](https://img.shields.io/badge/License-GPL%20v2-blue.svg)](LICENSE)


go-pugleaf provides a complete newsgroup platform with:
- Full NNTP server implementation (RFC 3977 compliant)
- Modern web interface for browsing (and posting *TODO*)
- Efficient article fetching and threading
- SQLite-based storage with per-group databases
- Spam flagging and moderation tools

## 🚀 Quick Start

Read [BUGS.md](https://github.com/go-while/go-pugleaf/blob/main/BUGS.md) first!

### Prerequisites
- Go 1.24.3+ (for building from source)
- Linux/Unix system (Windows support not tested)
- At least 1-256GB RAM, 1-10000GB+ disk space

### Installation

**Option 1: Download pre-built binaries**

Download the latest release for your platform from the [releases page](https://github.com/go-while/go-pugleaf/releases).

**Option 2: Build from source**

```bash
# Clone repository
useradd -m -s /bin/bash pugleaf
su pugleaf
cd /home/pugleaf
git clone https://github.com/go-while/go-pugleaf.git
cd go-pugleaf
git checkout testing-001

# Build all binaries (outputs to ./build)
./build_ALL.sh && cp build/* .

# Start web server
./webserver -nntphostname your.domain.com
# needs the "web" folder with templates

# Open browser to http://localhost:11980
```

### Initial Setup

1. **Create admin account** - Choose one of two methods:
  - **Web registration**: First registered user becomes administrator
  - **Command line**: Use the usermgr tool to create admin users directly
```bash
./build_usermgr.sh
mv build/usermgr .
./usermgr -create -username admin -email admin@example.com -display "Administrator" -admin
```
2. **Secure your instance** - Login → Statistics → Disable registrations
3. **Add newsgroups** - Admin → Add groups you want to follow
  - Or bulk import newsgroups:
```bash
./webserver -import-active preload/active.txt -nntphostname your.domain.com
./webserver -update-desc preload/newsgroups.descriptions -nntphostname your.domain.com
./rslight-importer -data data -etc etc -spool spool -nntphostname your.domain.com
```
  - rslight section import: see etc/menu.conf and creating sections aka folders in etc/ containing a groups.txt (e.g., etc/section/groups.txt)
  - spool folder can be empty if you don't want to import rocksolid backups.
4. **Configure provider** - Admin → Providers (defaults works)

5. **Limit Connections**
 - Please limit to max 50 connections at 81-171-22-215.pugleaf.net
 - this is a free service and we want to keep it running for everyone.
 - blueworldhosting and eternal-september have hardcoded limit of 3 conns!
 - There is NO NEED to download all usenet from archives again!
 - Unfiltered databases will be shared via torrent soon!

6. **Fetch articles** - Use `pugleaf-fetcher` to download articles from subscribed groups
7. **Runtime Mode** - Set connections to max 10 when your back filling is finished.

### Fetching Articles

```bash
# estimate size
./nntp-analyze -group news.admin.peering -start-date 2023-01-01 -end-date 2024-12-31
./nntp-analyze -group alt.* -start-date 1990-01-01
# beware: nntp-analyze writes all overview to calculate size into data/cache !


# Initial fetch from a specific date
# This will fetch N articles (max-batch) per group and quit
# When first run is done: remove the flag '-download-start-date ...' and run again and again.
./pugleaf-fetcher -nntphostname your.domain.com \
  -download-start-date 2024-12-31 \
  -group news.admin.*

# Continuous fetching (run after initial fetch)
./pugleaf-fetcher -nntphostname your.domain.com \
  -group news.admin.*

# Fetch all subscribed groups in a loop
while true; do
  ./pugleaf-fetcher -nntphostname your.domain.com
  sleep 5m
done

# Fetch specific subscribed groups in a loop
while true; do
  ./pugleaf-fetcher -nntphostname your.domain.com \
    -group news.admin.*
  sleep 5m
done

# You can run multiple fetchers concurrently, each with another group.*
# but provider conns are not tracked via database, only inside the running fetcher.
# You need to make sure you don't exceed the max connections from your provider.
# If you set providers max conn to 10 and run 4 fetchers you'll use up to 40 conns.
# The fetcher scales connections automatically based on the number of articles to fetch.
# Every fetcher will use all available connections if there are as many articles to fetch.
```

⚠️ **Important**: Stop the fetcher before adding new groups, or it will download all from scratch.

## 👥 User Management

go-pugleaf provides both web-based and command-line user management tools.

### Web Interface
- User registration and login via the web interface
- Admin panel for user management (accessible to administrators)
- First registered user automatically becomes administrator

### Command Line Tool (usermgr)
The `usermgr` tool provides complete command-line user management:

```bash
# Build the usermgr tool
go build -o build/usermgr ./cmd/usermgr
mv build/usermgr .

# Create a new user
./usermgr -create -username john -email john@example.com -display "John Doe"

# Create a new admin user
./usermgr -create -username admin -email admin@example.com -display "Administrator" -admin

# List all users (shows admin status)
./usermgr -list

# Delete a user
./usermgr -delete -username john

# Update a user's password
./usermgr -update -username john
```

The usermgr tool is particularly useful for:
- Creating the initial administrator account before first web access
- Batch user management and automation
- Managing users when web interface is unavailable

📖 **For complete documentation of all 19 available binaries and their flags, see [Binary Documentation](#binary-documentation) below.**

### Building individual tools

Each command in `cmd/*` has a matching build script in the repo root that produces a binary in `./build`:

- Core: `build_webserver.sh`, `build_nntp-server.sh`
- NNTP: `build_fetcher.sh`, `build_analyze.sh`, `build_test-nntp.sh`
- Users: `build_usermgr.sh`, `build_nntpmgr.sh`
- Import: `build_rslight_importer.sh`, `build_import_flat-files.sh`, `build_merge-active.sh`, `build_merge-descriptions.sh`
- Database: `build_expire-news.sh`, `build_recover-db.sh`, `build_fix-references.sh`, `build_fix-thread-activity.sh`, `build_history-rebuild.sh`

Or build a single tool manually, e.g.:
```bash
go build -o build/usermgr ./cmd/usermgr
```

### Building and Release

**Build all binaries:**
```bash
# Build all binaries (automatically generates checksums)
./build_ALL.sh
```

**Generate checksums manually:**
```bash
# Generate SHA256 checksums for all executables in build/
./createChecksums.sh
```

**Build and create release package:**
```bash
# Build all binaries and create release package with checksums
./build_ALL.sh update
```

This creates:
- `checksums.sha256` - SHA256 hashes for all individual executables (with build/ paths)
- `checksums.sha256.archive` - SHA256 hashes with relative paths for archive inclusion
- `update.tar.gz` - Compressed archive of all binaries including checksums.sha256
- `.update` - SHA256 hash of the tar.gz file

**Verify checksums:**
```bash
# Verify all executable checksums (from repository root)
sha256sum -c checksums.sha256

# Verify checksums after extracting release archive
tar -xzf update.tar.gz
cd extracted-directory/
sha256sum -c checksums.sha256  # Verify all executables in release
```

## 📚 Binary Documentation

go-pugleaf includes command-line applications for various newsgroup management tasks.

- Below is comprehensive documentation for all available binaries and their flags.

### Core Applications

#### `webserver` (cmd/web)
**Main web interface**
```bash
./webserver -nntphostname your.domain.com
```

**Required Flags:**
- `-nntphostname string` - Your hostname must be set

**SSL/TLS Configuration:**
- `-webssl` - Enable SSL for web interface
- `-websslcert string` - SSL certificate file (/path/to/fullchain.pem)
- `-websslkey string` - SSL key file (/path/to/privkey.pem)
- `-nntpcertfile string` - NNTP TLS certificate file (/path/to/fullchain.pem)
- `-nntpkeyfile string` - NNTP TLS key file (/path/to/privkey.pem)

**Server Configuration:**
- `-webport int` - Web server port (default: 11980 (no ssl) or 19443 (webssl))
- `-nntptcpport int` - NNTP TCP port
- `-nntptlsport int` - NNTP TLS port

**Cache Configuration:**
- `-maxsanartcache int` - Maximum number of cached sanitized articles (default: 10000)
- `-maxsanartcacheexpiry int` - Expiry of cached sanitized articles in minutes (default: 30)
- `-maxngpagecache int` - Maximum number of cached newsgroup pages (default: 4096)
- `-maxngpagecacheexpiry int` - Expiry of cached newsgroup pages in minutes (default: 5)
- `-maxarticlecache int` - Maximum number of cached articles (default: 10000)
- `-maxarticlecacheexpiry int` - Expiry of cached articles in minutes (default: 60)

**Data Management:**
- `-import-active string` - Import newsgroups from NNTP active file
- `-import-desc string` - Import newsgroups from descriptions file
- `-import-create` - Create missing newsgroups when importing
- `-write-active-file string` - Write NNTP active file to specified path
- `-write-active-only` - Use with -write-active-file (false writes only non active groups!) (default: true)
- `-update-descr` - Update newsgroup descriptions from file
- `-repair-watermarks` - Repair corrupted newsgroup watermarks
- `-update-newsgroup-activity` - Update newsgroup activity timestamps
- `-update-newsgroups-hide-futureposts` - Hide articles posted > 48h in future
- `-compare-active string` - Compare active file with database and show missing groups (format: groupname highwater lowwater status)
- `-compare-active-min-articles int64` - Use with -compare-active: only show groups with more than N articles (calculated as high-low)
- `-rsync-inactive-groups string` - Path to new data dir, uses rsync to copy all inactive group databases to new data folder
- `-rsync-remove-source` - Use with -rsync-inactive-groups. If set, removes source files after moving inactive groups (default: false)

**Bridge Features: (NOT WORKING!)**
- `-enable-fediverse` - Enable Fediverse bridge
- `-fediverse-domain string` - Fediverse domain (e.g. example.com)
- `-fediverse-baseurl string` - Fediverse base URL (e.g. https://example.com)
- `-enable-matrix` - Enable Matrix bridge
- `-matrix-homeserver string` - Matrix homeserver URL
- `-matrix-accesstoken string` - Matrix access token
- `-matrix-userid string` - Matrix user ID

**Advanced Options:**
- `-useshorthashlen int` - Short hash length for history storage (2-7, default: 7) - NOTE: cannot be changed once set!

**Disabled Options (commented out in code):**
- `# -withnntp` - Start NNTP server with default ports 1119/1563
- `# -withfetch` - Enable internal Cronjob to fetch new articles  
- `# -isleep int` - Sleeps in fetch routines (default: 300 seconds = 5min)
- `# -ignore-initial-tiny-groups int` - Ignore tiny groups with fewer articles (default: 0)

#### `pugleaf-fetcher` (cmd/nntp-fetcher)
**Article fetcher from NNTP providers**
```bash
./pugleaf-fetcher -nntphostname your.domain.com -group news.admin.*
```

**Required Flags:**
- `-nntphostname string` - Your hostname must be set!

**Connection Configuration:**
- `-host string` - NNTP hostname (default: "81-171-22-215.pugleaf.net")
- `-port int` - NNTP port (default: 563)
- `-username string` - NNTP username (default: "read")
- `-password string` - NNTP password (default: "only")
- `-ssl` - Use SSL/TLS connection (default: true)
- `-timeout int` - Connection timeout in seconds (default: 30)
- `-message-id string` - Test specific message ID

**Fetching Options:**
- `-group string` - Newsgroup to fetch (empty = all groups or wildcard like rocksolid.*)
- `-download-start-date string` - Start downloading from date (YYYY-MM-DD format)
- `-fetch-active-only` - Fetch only active newsgroups (default: true)

**Performance Configuration:**
- `-max-batch int` - Maximum articles per batch (default: 128, recommended: 100)
- `-max-batch-threads int` - Concurrent newsgroup batches (default: 16)
- `-max-loops int` - Loop a group this many times (default: 1)
- `-max-queue int` - Limit db_batch to have max N articles queued over all newsgroups (default: 16384)
- `-download-max-par int` - Groups in parallel (default: 1, can consume memory!)
- `-useshorthashlen int` - Short hash length for history (2-7, default: 7)

**Advanced Options:**
- `-ignore-initial-tiny-groups int64` - Ignore tiny groups with fewer articles during initial fetch (default: 0)
- `-update-newsgroups-from-remote string` - Fetch remote newsgroup list and add new groups (empty = disabled, use "group.*" or "$all")
- `-xover-copy` - Copy xover data from remote server (default: false)
- `-test-conn` - Test connection and exit (default: false)
- `-help` - Show usage examples and exit

#### `pugleaf-nntp-server` (cmd/nntp-server)
**Standalone NNTP server**
```bash
./pugleaf-nntp-server -nntphostname your.domain.com -nntptcpport 1119
```

**Required Flags:**
- `-nntphostname string` - Your hostname must be set!

**Server Configuration:**
- `-nntptcpport int` - NNTP TCP port
- `-nntptlsport int` - NNTP TLS port
- `-nntpcertfile string` - NNTP TLS certificate file
- `-nntpkeyfile string` - NNTP TLS key file
- `-maxconnections int` - Maximum authenticated connections (default: 500)
- `-useshorthashlen int` - Short hash length for history (2-7, default: 7)

### Analysis and Monitoring

#### `nntp-analyze` (cmd/nntp-analyze)
**Analyze newsgroup content and estimate storage requirements**
```bash
./nntp-analyze -group alt.test -start-date 2024-01-01 -end-date 2024-12-31
```

**Connection Configuration:**
- `-host string` - NNTP hostname (default: "81-171-22-215.pugleaf.net")
- `-port int` - NNTP port (default: 563)
- `-username string` - NNTP username (default: "read")
- `-password string` - NNTP password (default: "only")
- `-ssl` - Use SSL/TLS connection (default: true)
- `-timeout int` - Connection timeout in seconds (default: 30)

**Analysis Options:**
- `-group string` - Newsgroup to analyze (required, supports $all or alt.*)
- `-groups-file string` - Path to active file containing newsgroups
- `-start-date string` - Start date for analysis (YYYY-MM-DD)
- `-end-date string` - End date for analysis (YYYY-MM-DD)
- `-export string` - Export results (json|csv)

**Cache Management:**
- `-force-refresh` - Force refresh cached analysis data
- `-validate-cache` - Validate cache integrity
- `-clear-cache` - Clear cached data
- `-cache-stats` - Show cache statistics
- `-help` - Show usage examples and exit

### Database Management

#### `recover-db` (cmd/recover-db)
**Database consistency checking and repair**
```bash
./recover-db -db data -group alt.test -repair
```

**Required Flags:**
- `-db string` - Data path to main data directory (default: "data")
- `-group string` - Newsgroup name (default: "$all" for all groups)

**Operation Modes:**
- `-repair` - Attempt to repair detected inconsistencies
- `-parsedates` - Check date parsing differences
- `-rewritedates` - Rewrite incorrect dates (requires -parsedates)
- `-v` - Verbose output (default: true)

#### `fix-references` (cmd/fix-references)
**Fix broken References headers in articles**
```bash
./fix-references -db data -group alt.test -dry-run
```

**Required Flags:**
- `-db string` - Data path to main data directory (default: "data")
- `-group string` - Newsgroup name (default: "$all" for all groups)

**Operation Options:**
- `-dry-run` - Show what would be fixed without making changes
- `-limit int` - Limit number of articles to process (0 = no limit)
- `-batch-size int` - Articles per batch (default: 10000)
- `-rebuild-threads` - Rebuild thread relationships after fixing
- `-v` - Verbose output

#### `fix-thread-activity` (cmd/fix-thread-activity)
**Fix thread activity timestamps**
```bash
./fix-thread-activity -group alt.test
```

**Options:**
- `-group string` - Newsgroup name to fix (empty = fix all groups)

#### `history-rebuild` (cmd/history-rebuild)
**Rebuild article history databases**
```bash
./history-rebuild -nntphostname your.domain.com
```

**Required Flags:**
- `-nntphostname string` - NNTP hostname for proper article processing

**Operation Modes:**
- `-validate-only` - Only validate existing history
- `-clear-first` - Clear existing history before rebuild
- `-analyze-only` - Only analyze existing history databases

**Configuration:**
- `-batch-size int` - Articles per batch (default: 5000, deprecated)
- `-progress int` - Show progress every N articles (default: 2500)
- `-useshorthashlen int` - Short hash length (2-7, default: 7)
- `-verbose` - Enable verbose logging
- `-show-collisions` - Show collision details (use with -analyze-only)
- `-pprof string` - Enable pprof HTTP server (e.g., ':6060')

### User and Security Management

#### `nntpmgr` (cmd/nntpmgr)
**NNTP user management**
```bash
./nntpmgr -create -username reader1 -password secret123 -maxconns 3
```

**User Operations:**
- `-create` - Create a new NNTP user
- `-list` - List all NNTP users
- `-delete` - Delete an NNTP user
- `-update` - Update an NNTP user

**User Configuration:**
- `-username string` - Username for operations (default: "random" for 10-20 chars)
- `-password string` - Password (default: "random" for 10-20 chars, bcrypt hashed)
- `-maxconns int` - Maximum concurrent connections (default: 1)
- `-posting` - Allow posting (default: read-only)

**Tools:**
- `-rescan-db string` - Rescan database for newsgroup (default: alt.test)

#### `usermgr` (cmd/usermgr)
**Web user management**
```bash
./usermgr -create -username admin -email admin@example.com -admin
```

**User Operations:**
- `-create` - Create a new user
- `-list` - List all users
- `-delete` - Delete a user
- `-update` - Update a user's password

**User Configuration:**
- `-username string` - Username for operations
- `-email string` - Email for user creation
- `-display string` - Display name for user creation
- `-admin` - Grant admin permissions to user

### Data Import and Export

#### `rslight-importer` (cmd/rslight-importer)
**Import from legacy RockSolid Light installations**
```bash
./rslight-importer -data ./data -etc /etc/rslight -spool /old/spool -nntphostname your.domain.com
```

**Required Flags:**
- `-nntphostname string` - Your hostname must be set

**Path Configuration:**
- `-data string` - Directory for NEW database files (default: "./data")
- `-etc string` - Path to legacy RockSolid Light configs
- `-spool string` - Path to legacy spool directory with *.db3 files

**Operation Options:**
- `-threads int` - Parallel import threads (default: 1)
- `-useshorthashlen int` - Short hash length (2-7, default: 7)
- `-YESresetallgroupsYES` - **DANGER**: Reset ALL newsgroups and articles

#### `import-flat-files` (cmd/import-flat-files)
**Import articles from flat file format**
```bash
./import-flat-files -head /mnt/xfshead -body /mnt/xfsbody -db ./imported_articles
```

**Path Configuration:**
- `-head string` - Path to head files directory (default: "/mnt/xfshead")
- `-body string` - Path to body files directory (default: "/mnt/xfsbody")
- `-db string` - Path for SQLite database files (default: "./imported_articles")

**Operation Options:**
- `-resume` - Resume from where we left off
- `-dry-run` - Don't write to database, just scan files
- `-verbose` - Verbose logging
- `-update` - Update mode: only import missing articles

#### `merge-active` (cmd/merge-active)
**Merge NNTP active files**
```bash
./merge-active -filter input.active
./merge-active -overview
```

**Operation Modes:**
- `-filter` - Filter malformed group names and write to active.file.new
- `-overview` - Process overview files for groups with <100 messages

#### `merge-descriptions` (cmd/merge-descriptions)
**Merge newsgroup description files**
```bash
./merge-descriptions
```

### Maintenance and Cleanup

#### `expire-news` (cmd/expire-news)
**Article expiration and cleanup**
```bash
./expire-news -nntphostname your.domain.com -group alt.test -days 30 -force
```

**Required Flags:**
- `-nntphostname string` - Your hostname (required)

**Target Selection:**
- `-group string` - Newsgroup ('$all', specific group, or wildcard like news.*)

**Expiration Options:**
- `-days int` - Delete articles older than N days (0 = use per-group settings)
- `-respect-expiry` - Use per-group expiry_days settings from database
- `-prune` - Remove oldest articles to respect max_articles limit per group

**Operation Control:**
- `-dry-run` - Show what would be deleted without deleting
- `-force` - Actually perform deletions (required for non-dry-run)
- `-batch-size int` - Articles per batch (default: 1000)
- `-help` - Show usage examples and exit

### Testing and Development

#### `test-MsgIdItemCache` (cmd/test-MsgIdItemCache)
**Test message ID cache performance**
```bash
./test-MsgIdItemCache -items 100000 -workers 4 -concurrent
```

**Test Configuration:**
- `-items int` - Number of message IDs to test (default: 100000)
- `-workers int` - Number of concurrent workers (default: 1)
- `-concurrent` - Run concurrent stress test

#### Additional Tools

Several other binaries are available but don't have extracted flags (likely no command-line options):
- `benchmark_hash` - Hash function benchmarking
- `extract_hierarchies` - Extract newsgroup hierarchies
- `history-demo` - History system demonstration
- `parsedates` - Date parsing utilities
- `test-nntp` - NNTP protocol testing

### Common Flag Patterns

**Hostname Configuration:**
Most network-related tools require `-nntphostname your.domain.com` to identify your server.

**Database Paths:**
Database tools typically use `-db data` to specify the data directory.

**Batch Processing:**
Many tools support `-batch-size` for processing large datasets efficiently.

**Dry Run Mode:**
Tools that modify data support `-dry-run` to preview changes without applying them.

**Verbose Output:**
Most tools support `-v` or `-verbose` for detailed logging.

**History Configuration:**
Tools that work with article history use `-useshorthashlen` (2-7, default: 7) - this cannot be changed once set!

## 🤝 Contributing

This project is in active development.
We welcome contributions!
Areas of focus:
- RFC compliance improvements
- Web UI enhancements
- Performance optimization
- Documentation
- Testing

## 📄 License

GPL v2 - see [LICENSE](LICENSE)

## 🙏 Acknowledgements

This project is inspired by the work of Thomas "Retro Guy" Miller and the original RockSolid Light project.

-- the pugleaf.net development team -
```
