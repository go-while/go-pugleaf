# GitHub Actions Release Workflow

This repository includes an automated release workflow that builds binaries for multiple platforms when version tags are pushed.

## How to Create a Release

1. **Tag a release version:**
   ```bash
   git tag v1.0.0
   git push origin v1.0.0
   ```

2. **The workflow will automatically:**
   - Build binaries for Linux (amd64, arm64), macOS (amd64, arm64), and Windows (amd64)
   - Create platform-specific archives (`.tar.gz` for Unix, `.zip` for Windows)
   - Create a GitHub release with all binaries attached
   - Use the tag version for binary version injection

## Built Applications

Each release includes the following binaries:

- `webserver` - Main web interface
- `pugleaf-fetcher` - Article fetcher from NNTP providers  
- `pugleaf-nntp-server` - NNTP server implementation
- `expire-news` - Article expiration tool
- `merge-active` - Active file merger
- `merge-descriptions` - Description file merger
- `test-MsgIdItemCache` - Cache testing tool
- `history-rebuild` - History rebuild utility
- `fix-references` - Reference fixing tool
- `fix-thread-activity` - Thread activity fixer
- `rslight-importer` - RSLight data importer
- `nntp-analyze` - NNTP analysis tool
- `recover-db` - Database recovery tool

## Platform Support

- **Linux**: amd64, arm64
- **macOS**: amd64, arm64 (Intel and Apple Silicon)
- **Windows**: amd64

## Release Archives

Archives are named: `go-pugleaf-v{VERSION}-{OS}-{ARCH}.{tar.gz|zip}`

Examples:
- `go-pugleaf-v1.0.0-linux-amd64.tar.gz`
- `go-pugleaf-v1.0.0-darwin-arm64.tar.gz`
- `go-pugleaf-v1.0.0-windows-amd64.zip`

## Workflow Configuration

The workflow is defined in `.github/workflows/release.yml` and:

- Triggers on tags matching `v*` pattern
- Uses Go 1.25.x for builds
- Removes `-race` flag for cross-compilation compatibility
- Uses `GOEXPERIMENT=greenteagc` for the fetcher binary
- Includes README.md and LICENSE in each archive

## Manual Testing

You can test the build logic locally using the existing build scripts:

```bash
# Test individual builds
./build_webserver.sh
./build_fetcher.sh

# Test full build
./build_ALL.sh
```

For cross-compilation testing:
```bash
GOOS=linux GOARCH=arm64 go build -o build/webserver-arm64 ./cmd/web
```