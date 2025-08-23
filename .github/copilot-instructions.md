# Go-Pugleaf Copilot Instructions

## Project Overview

**go-pugleaf** is a modern NNTP server and web gateway for Usenet/NetNews built in Go. It provides a complete newsgroup platform with full RFC 3977 compliant NNTP server implementation, modern web interface for browsing, efficient article fetching and threading, SQLite-based storage with per-group databases, and spam flagging/moderation tools.

### High-Level Repository Information
- **Type**: Production newsgroup server system (medium-to-large codebase)
- **Size**: 150 Go files across 13 internal packages, 21 command applications
- **Languages**: Go 1.24.3+ (primary), HTML/CSS/JavaScript (web frontend), Shell scripts (build system)
- **Frameworks**: Gin web framework, custom NNTP protocol implementation, SQLite3 database
- **Target Runtime**: Linux/Unix systems (Windows support not tested)
- **Dependencies**: Minimal external dependencies - SQLite3, Gin, Go crypto libraries

## Build, Test, and Development Commands

### Essential Prerequisites
```bash
# Always ensure Go 1.24.3+ is available
go version  # Must be 1.24.3 or higher

# Always run module commands before building
go mod tidy && go mod verify
```

### Build Commands (Always run in repository root)
```bash
# Build all applications (14+ binaries) - takes ~15-30 seconds
./build_ALL.sh

# Build individual applications - takes ~1-2 seconds each
./build_webserver.sh      # Main web interface
./build_fetcher.sh        # Article fetcher
./build_nntp-server.sh    # NNTP server
./build_expire-news.sh    # Article expiration tool

# Build output goes to build/ directory
ls -la build/  # List all built binaries
```

### Testing
```bash
# Run all available tests (very limited test coverage currently)
go test ./... -v

# Run specific package tests (currently only nntp package has tests)
go test ./internal/nntp/... -v

# Note: Most packages have no test files - this is expected
# Some nntp tests skip due to missing test data files - this is normal
```

### Linting (if golangci-lint is available)
```bash
# Project includes comprehensive .golangci.yml with security focus
golangci-lint run

# Security-focused linting config emphasizes:
# - gosec (security vulnerabilities)
# - errcheck (unchecked errors) 
# - sqlclosecheck (SQL connection handling)
```

### Running Applications
```bash
# Web server (main application)
./build/webserver -nntphostname your.domain.com
# Opens web interface on http://localhost:11980
# Requires "web" folder with templates to be present

# NNTP fetcher for downloading articles
./build/pugleaf-fetcher -group alt.test -test-conn

# NNTP server for serving articles
./build/pugleaf-nntp-server -port 1119

# Get help for any application
./build/webserver -help
./build/pugleaf-fetcher -help
```

### Important Build Notes
- **Always** run builds from repository root directory
- The `build/` directory is auto-created and contains all executables
- Build scripts use `-race` flag for race condition detection
- Version injection uses `appVersion.txt` file
- Clean builds remove `build/*` before building
- Build failures typically indicate missing dependencies

## Project Layout and Architecture

### Core Directory Structure
```
/cmd/                    # 21 command applications (main executables)
  ├── web/              # Main web server (primary app)
  ├── nntp-fetcher/     # Article fetching from NNTP providers  
  ├── nntp-server/      # NNTP server implementation
  ├── expire-news/      # Article expiration and cleanup
  └── [17 other tools]  # Import, analysis, migration utilities

/internal/              # 13 internal packages (core business logic)
  ├── nntp/            # NNTP protocol implementation (60+ files)
  ├── web/             # Web server handlers and templates  
  ├── database/        # SQLite abstraction and per-group databases
  ├── config/          # Configuration management
  ├── models/          # Data models and structures
  └── [8 other packages] # cache, embedded, history, processor, etc.

/web/                   # Frontend assets
  ├── templates/       # HTML templates (Gin templating)
  └── static files     # CSS, JS, favicon

/configs/               # Configuration samples and documentation
/active_files/          # Newsgroup hierarchy data
/build/                 # Built executables (gitignored)
```

### Key Configuration Files
- `.golangci.yml` - Comprehensive linter configuration (security-focused)
- `go.mod` - Go module definition with minimal dependencies
- `configs/sample.yaml` - Application configuration template
- `appVersion.txt` - Version injection for builds

### Major Architectural Components

**NNTP Server** (`internal/nntp/`): 
- Modular command handlers (article, auth, group, list, etc.)
- Connection pooling and client management
- Authentication and authorization framework
- Pattern matching for newsgroup filtering

**Web Interface** (`internal/web/`):
- Gin-based REST API and web pages
- User authentication and session management  
- Admin interface for newsgroup/provider management
- Multiple theme support (modern, classic, amber)

**Database Layer** (`internal/database/`):
- SQLite3 with per-newsgroup database strategy
- Article storage with threading logic
- Overview data caching for performance
- Migration system for schema updates

### Dependencies Not Obvious from Layout
- **Race Detection**: All builds use `-race` flag for concurrency safety
- **Version Injection**: Build scripts inject version from `appVersion.txt`
- **Template Dependencies**: Web server requires `web/` directory present
- **SSL/TLS Support**: Both web and NNTP servers support TLS with cert files

### Where to Make Changes

**Adding NNTP Commands**: Add to `internal/nntp/nntp-cmd-*.go` files
**Web Pages/API**: Add handlers to `internal/web/` and templates to `web/templates/`
**Database Schema**: Add migrations to `internal/database/migrations/`
**Configuration**: Update `internal/config/` and `configs/sample.yaml`
**Build Process**: Modify individual `build_*.sh` scripts
**New Applications**: Add to `cmd/` directory with corresponding build script

### Key Files by Importance
1. `cmd/web/main.go` - Main web application entry point
2. `internal/web/server_core.go` - Core web server setup and routing
3. `internal/nntp/nntp-server.go` - Main NNTP server implementation
4. `internal/database/database.go` - Database abstraction layer
5. `build_ALL.sh` - Master build script for all applications
6. `go.mod` - Dependencies and Go version requirements

### Validation and CI/CD
Currently **no GitHub Actions or automated CI/CD**. Validation is manual:
- Build all applications with `./build_ALL.sh`
- Run tests with `go test ./...`
- Lint with `golangci-lint run` (if available)
- Manual testing of web interface and NNTP functionality

### Important Patterns
- **Error Handling**: Extensive error checking required (see .golangci.yml)
- **Security Focus**: Authentication, SQL injection prevention, input validation
- **Memory Management**: Known memory consumption issues in fetcher/webserver
- **Modular Design**: Clean separation between NNTP, web, and database layers
- **Per-Group Databases**: Articles stored in separate SQLite files per newsgroup

### Troubleshooting Common Issues
- **Build Failures**: Check Go version (must be 1.24.3+), run `go mod tidy`
- **Missing Templates**: Ensure `web/` directory present for web server
- **Memory Issues**: Known issue with long-running fetcher and webserver processes
- **NNTP Testing**: Use telnet to localhost:1119 for manual NNTP server testing
- **Database Locks**: SQLite per-group strategy minimizes lock contention

## Agent Efficiency Tips

**Trust these instructions** - this information has been validated by running all documented commands. Only search for additional information if these instructions are incomplete or incorrect.

**Build Strategy**: Always start with `./build_ALL.sh` to ensure all dependencies are met, then use individual build scripts for iterative development.

**Testing Strategy**: Limited test coverage means manual validation is critical. Always test web interface and NNTP functionality after changes.

**Change Strategy**: Make minimal modifications due to complex interdependencies. The modular architecture allows focused changes within single components.