# 🐶 go-pugleaf

**A modern NNTP server and web gateway for Usenet/NetNews built in Go**

[![Go Version](https://img.shields.io/badge/Go-1.26+-blue.svg)](https://golang.org)
[![Development Status](https://img.shields.io/badge/Status-Testing-green.svg)](#development-status)
[![License](https://img.shields.io/badge/License-GPL%20v2-blue.svg)](LICENSE)


go-pugleaf provides a complete newsgroup platform with:
- NNTP server (RFC 3977 partial, see docs/audit)
- Modern web interface for browsing and posting
- Efficient article fetching and threading
- SQLite-based storage with per-group databases
- Spam flagging and moderation tools

## 🚀 Quick Start

Read [BUGS.md](https://github.com/go-while/go-pugleaf/blob/main/BUGS.md) first!

### Prerequisites
- Go 1.26.0+ (for building from source; `go.mod` pins `toolchain go1.27.2`, downloaded automatically with GOTOOLCHAIN=auto)
- Linux/Unix system (Windows support not tested)
- At least 1-256GB RAM, 1-10000GB+ disk space

### Installation & Setup

**Step 1: Get the binaries**

Choose one option:
- **Download release**: Get pre-built binaries from [releases page](https://github.com/go-while/go-pugleaf/releases)
- **Build from source**:
```bash
git clone https://github.com/go-while/go-pugleaf.git
cd go-pugleaf
./build_ALL.sh && cp build/* .
```

**Step 2: Initial configuration**

```bash
# First run - store the hostname in the database (only needed once)
./webserver -nntphostname my.host.name

# Stop server (Ctrl+C), then start normally
./webserver
```
#### `webserver` (cmd/web)
**Main web interface**
```bash
./webserver -nntphostname your.domain.com
# Flags (run ./webserver -h for the full list)
-nntphostname string - your hostname; required on the first run, later runs reuse the stored value
-data string - path to the database and config directory (default: ./data)
-webport int - web server port, 1024-65535 (default: 11980, also with -webssl)

# SSL/TLS Configuration
-webssl - enable SSL for the web interface
-websslcert string - SSL certificate file (/path/to/fullchain.pem)
-websslkey string - SSL key file (/path/to/privkey.pem)

# Maintenance (run once, then exit): -import-active, -update-newsgroups-descriptions-file,
# -write-active-file, -compare-active, -disable-active, -repair-watermarks, -find-orphan-dbs, ...
# Cron jobs: -no-cronjobs disables them; -edit-cronjobs temporarily allows editing them in the admin UI.
```
The webserver has no embedded NNTP server: its `-nntptcpport`, `-nntptlsport`, `-nntpcertfile` and
`-nntpkeyfile` flags are accepted but ignored. Run `pugleaf-nntp-server` instead (see below).

**Step 3: Create admin account**

- Open browser: http://localhost:11980
- Register first user account (automatically becomes admin)
- Login → Admin → CFG → Disable registrations (secure your instance)

### Command Line Tool (usermgr)
The `usermgr` tool manages web users from the command line. It is not part of `build_ALL.sh`:
build it with `./build_usermgr.sh` (output: `build/usermgr`). It uses the same `-data` directory
as the webserver (default: `./data`). Passwords are asked for on the terminal (12-72 characters).

```bash
# List all users (shows admin status)
usermgr -list

# Delete a user
usermgr -delete -username john

# Update a user's password
usermgr -update -username john

# Create a new user
usermgr -create -username john -email john@example.com -display "John Doe"

# Create a new admin user
usermgr -create -username admin -email admin@example.com -display "Administrator" -admin
```

NNTP users (for `pugleaf-nntp-server`) are managed with `pugleaf-nntpmgr` (`./build_nntpmgr.sh`):
`-create`, `-list`, `-update`, `-delete`, `-activate`, `-deactivate`, `-setpassword`, with
`-username`, `-password`, `-maxconns` and `-posting`; see `pugleaf-nntpmgr -h`.

**Step 4: Add newsgroups**

Choose one method:
- **Web interface**: Admin → Add groups manually
- **Command line**: Import from active file or descriptions file
- Files are in `preload/` or `active_files/` directory.
- The defaults may contain a large number of binary groups; modify as needed.
```bash
./webserver -import-active preload/active.2025-06
./webserver -update-newsgroups-descriptions-file preload/newsgroups.descriptions \
 -update-overwrite-newsgroups-descriptions=false \
 -update-newsgroups-descriptions-import-create-newsgroups=false

# import rocksolid light backups (-etc is required, -spool is optional)
./rslight-importer -data data/ -etc etc/ -spool spool/
```

**Optional: Import RockSolid Light backups**
- See `etc/menu.conf` for section configuration
- Create sections (folders) in `etc/`
- Add a `groups.txt` per folder in `etc/$section/` (e.g., `etc/rocksolid/groups.txt`)
- Spool folder can be empty if you don't want to import db backups

**Step 5: Start fetching news**

- Web interface:
- Admin → Server → Configure
- Admin → Cron Jobs
- Click play button ▶ next to `pugleaf-fetcher`
- News should arrive within 5 minutes

### Advanced: Manual Fetching

For backfilling disable the cron job first and run `pugleaf-fetcher` directly. Each run fetches
at most `-max-batch` articles (10-1000, default 1000) per group and then exits, so repeat it until
the backfill is done. Like the webserver it needs `-nntphostname` on its first run and reads the
databases below `-data` (default `./data`). `./pugleaf-fetcher -help` prints usage examples.

```bash
# Initial backfill from specific date
./pugleaf-fetcher -group "news.*" -download-start-date 1980-01-01   # there is no end-date flag; use -max-batch to pace

# Continuous fetching (run after initial fetch)
./pugleaf-fetcher -group "news.*"

# All active groups (same as -group '$all'); -fetch-active-only=false fetches ONLY the inactive groups
./pugleaf-fetcher

# Add groups from the provider's list (dry run without -fetch-newsgroups-force; new groups are inactive)
./pugleaf-fetcher -fetch-newsgroups-from-remote 'comp.*' -exclude-prefix 'alt.binaries.*' -fetch-newsgroups-force
```

⚠️ **Important**: Stop the fetcher before adding new groups, or it will download all from scratch.

### NNTP server (`pugleaf-nntp-server`, cmd/nntp-server)

```bash
./pugleaf-nntp-server -nntphostname your.domain.com -nntptcpport 1119 \
    -nntptlsport 1563 -nntpcertfile /path/to/fullchain.pem -nntpkeyfile /path/to/privkey.pem
# -data string          database directory (default: ./data)
# -nntptcpport int      NNTP TCP port (0 = no plain listener; at least one of the two ports is required)
# -nntptlsport int      NNTP TLS port (0 = no TLS listener); the cert/key also serve STARTTLS on the TCP port
# -maxconnections int   maximum concurrent client connections (default: 500)
# -readauth             require AUTHINFO before reader commands
# -plaintextauth string AUTHINFO without TLS: accept (default), advertise or refuse
# -maxartsize int       maximum article size in bytes (0 = config default 32768)
# -idletimeout, -articledelay, -groupdelay, -errordelay, -local430ttl: see -h
```

### Available Tools

The following binaries are built by `build_ALL.sh` (names as in `build/`):

- **rslight-importer** - Import from legacy RockSolid Light
- **nntp-analyze** - Analyze newsgroup content and storage requirements
- **expire-news** - Article expiration and cleanup
- **pugleaf-fetcher** - Article fetcher from NNTP providers
- **webserver** - Web interface (no embedded NNTP server)
- **pugleaf-nntp-server** - Standalone NNTP server (RFC 3977 partial, see docs/audit)
- **nntp-transfer** - Transfer newsgroups via NNTP CHECK/TAKETHIS
- **post-queue** - Process outgoing article post queue
- **tcp2tor** - TCP to Tor proxy

Built with their own `build_<tool>.sh` script only:

- **usermgr** - Web user management
- **pugleaf-nntpmgr** - NNTP user management
- **history-rebuild** - Rebuild the message-id history index from the group databases
- **recover-db**, **fix-references**, **fix-thread-activity**, **merge-active**, **merge-descriptions**,
  **audit-web-posts**, **import-flat-files** - repair, merge and import tools (see `cmd/<tool>`)

📖 **For command-line options, run any binary with the `-h` flag to see available arguments and usage examples.**

### Building and Release

**Build all binaries:**
```bash
# Build all binaries (automatically generates checksums)
./build_ALL.sh
```

**Verify checksums:**
```bash
# Verify all executable checksums (from repository root)
sha256sum -c checksums.sha256
```

## 🤝 Contributing

- This project is in active development.
- We welcome contributions!

### Areas of focus:
- RFC compliance improvements
- Web UI enhancements
- Performance optimization
- Documentation
- Testing

## 📄 License

GPL v2 - see [LICENSE](LICENSE)

## 🙏 Acknowledgements

This project is inspired by the work of Thomas "Retro Guy" Miller and the original RockSolid Light project.

-- the pugleaf.net development team --

