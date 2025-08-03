# 🐶 go-pugleaf

**A modern NNTP server and web gateway for Usenet/NetNews built in Go**

[![Go Version](https://img.shields.io/badge/Go-1.24+-blue.svg)](https://golang.org)
[![Development Status](https://img.shields.io/badge/Status-Phase%203%20Complete-green.svg)](#development-status)
[![License](https://img.shields.io/badge/License-GPL%20v2-blue.svg)](LICENSE)



go-pugleaf provides a complete newsgroup platform with:
- Full NNTP server implementation (RFC 3977 compliant)
- Modern web interface for browsing and posting
- Efficient article fetching and threading
- SQLite-based storage with per-group databases
- Built-in spam filtering and moderation tools

## 🚀 Quick Start

### Prerequisites
- Go 1.21+ (for building from source)
- Linux/Unix system (Windows support planned)
- At least 2GB RAM, 10GB+ disk space

### Installation

```bash
# Clone repository
git clone https://github.com/go-while/go-pugleaf.git
cd go-pugleaf
git checkout testing-001

# Build all binaries
./build_ALL.sh

# Start web server
./webserver -nntphostname your.domain.com

# Open browser to http://localhost:11980
```

### Initial Setup

1. **Register admin account** - First registered user becomes administrator
2. **Secure your instance** - Login → Statistics → Disable registrations
3. **Add newsgroups** - Admin → Add groups you want to follow
- or bulk import newsgroups
```bash
./webserver -import-active preload/active.txt
./webserver -update-desc preload/newsgroups.descriptions
- rslight section import:
- see etc/menu.conf and creating sections aka folders in etc/ containing a groups.txt
- like etc/section/groups.txt
./rslight-importer -data data -etc etc -spool spool -nntphostname your.domain.com
- spool folder can be empty if you don't want to import rocksolid backups.
```
4. **Configure provider** - Admin → Providers (defaults works)

5. **Limit Connections**
 - Please limit to max 500 connections at lux-feed1.newsdeef.eu
 - this is a free service and we want to keep it running for everyone.
 - blueworldhosting and eternal-september have hardcoded limit of 3 conns!

6. **Fetch articles** - Use `pugleaf-fetcher` to download articles from subscribed groups
7. **Runtime Mode** - Set connections to max 10 when your back filling is finished.

### Fetching Articles

```bash
# estimate size
./nntp-analyze -group news.admin.peering -start-date 2023-01-01 -end-date 2024-12-31
./nntp-analyze -group alt.* -start-date 1990-01-01
# beware: nntp-analyze writes all overview to calculate size into data/cache !


# Initial fetch from a specific date
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
```

⚠️ **Important**: Stop the fetcher before adding new groups, or it will download all from scratch.


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