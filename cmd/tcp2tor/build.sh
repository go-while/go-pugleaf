#!/bin/bash
# Build script for tcp2tor

set -e

echo "Building tcp2tor..."

# Get version info
if [ -f "../../VERSION" ]; then
    VERSION=$(cat ../../VERSION)
else
    VERSION="dev-$(date +%Y%m%d)"
fi

# Build for current platform
go build -trimpath -ldflags "-w -s -X main.appVersion=$VERSION" -o tcp2tor . || exit 1
sha256sum tcp2tor > tcp2tor.sha256
echo "✓ Built tcp2tor (version $VERSION)"
echo ""
echo "Usage examples:"
echo "  ./tcp2tor -help"
echo ""
