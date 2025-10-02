# 🐶 go-pugleaf

**A modern NNTP server and web gateway for Usenet/NetNews built in Go**

[![Go Version](https://img.shields.io/badge/Go-1.25+-blue.svg)](https://golang.org)
[![Development Status](https://img.shields.io/badge/Status-Testing-green.svg)](#development-status)
[![License](https://img.shields.io/badge/License-GPL%20v2-blue.svg)](LICENSE)


go-pugleaf provides a complete newsgroup platform with:
- Full NNTP server implementation (RFC 3977 compliant) *TODO*
- Modern web interface for browsing and posting
- Efficient article fetching and threading
- SQLite-based storage with per-group databases
- Spam flagging and moderation tools

## 🚀 Quick Start

Read [BUGS.md](https://github.com/go-while/go-pugleaf/blob/main/BUGS.md) first!

### Prerequisites
- Go 1.25.1+ (for building from source)
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
# First run - configure hostname (only needed once)
./webserver -nntphostname my.host.name

# Stop server (Ctrl+C), then start normally
./webserver
```
#### `webserver` (cmd/web)
**Main web interface**
```bash
./webserver -nntphostname your.domain.com
# Required Flags
-nntphostname string - Your hostname must be set first time
-webport int - Web server port (default: 11980 (no ssl) or 19443 (ssl))

# SSL/TLS Configuration
-webssl - Enable SSL for web interface
-websslcert string - SSL certificate file (/path/to/fullchain.pem)
-websslkey string - SSL key file (/path/to/privkey.pem)
# integrated NNTP server not availabled yet
#-nntptcpport int - NNTP TCP port
#-nntptlsport int - NNTP TLS port
#-nntpcertfile string - NNTP TLS certificate file (/path/to/fullchain.pem)
#-nntpkeyfile string - NNTP TLS key file (/path/to/privkey.pem)
```

**Step 3: Create admin account**

- Open browser: http://localhost:11980
- Register first user account (automatically becomes admin)
- Login → Admin → CFG → Disable registrations (secure your instance)

### Command Line Tool (usermgr)
The `usermgr` tool provides user management via the command-line:

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

**Step 4: Add newsgroups**

Choose one method:
- **Web interface**: Admin → Add groups manually
- **Command line**: Import from active file
```bash
./webserver -import-active preload/active.txt
./webserver -update-descr preload/newsgroups.descriptions

# import rocksolid light backups
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

For backfilling disable cron job before and run `pugleaf-fetcher` directly:

```bash
# Initial backfill from specific date
./pugleaf-fetcher -group "news.*" -download-start-date 1980-01-01 -download-end-date 1981-01-01

# Continuous fetching (run after initial fetch)
./pugleaf-fetcher -group "news.*"
```

⚠️ **Important**: Stop the fetcher before adding new groups, or it will download all from scratch.

### Available Tools

The following binaries are built by `build_ALL.sh`:

- **rslight-importer** - Import from legacy RockSolid Light
- **nntp-analyze** - Analyze newsgroup content and storage requirements
- **expire-news** - Article expiration and cleanup
- **pugleaf-fetcher** - Article fetcher from NNTP providers
- **webserver** - Web interface (and embedded NNTP server *TODO*)
- **nntp-server** - Standalone NNTP server (reading works partially, posting not yet)
- **nntp-transfer** - Transfer newsgroups via NNTP CHECK/TAKETHIS
- **post-queue** - Process outgoing article post queue
- **tcp2tor** - TCP to Tor proxy

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

