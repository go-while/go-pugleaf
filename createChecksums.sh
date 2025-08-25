#!/bin/bash -e

# Generate SHA256 checksums for all executables in build directory

BUILD_DIR="build"
CHECKSUMS_FILE="checksums.sha256"

echo "Generating SHA256 checksums for executables in $BUILD_DIR/"

# Check if build directory exists
if [ ! -d "$BUILD_DIR" ]; then
    echo "Error: Build directory '$BUILD_DIR' not found"
    echo "Please run ./build_ALL.sh first to build all executables"
    exit 1
fi

# Check if there are any files in build directory
if [ ! "$(ls -A $BUILD_DIR)" ]; then
    echo "Error: Build directory '$BUILD_DIR' is empty"
    echo "Please run ./build_ALL.sh first to build all executables"
    exit 1
fi

# Remove existing checksums file
rm -f "$CHECKSUMS_FILE"

# Generate checksums for all files in build directory
echo "Creating $CHECKSUMS_FILE with SHA256 hashes..."
sha256sum "$BUILD_DIR"/* > "$CHECKSUMS_FILE"

# Also create a version with relative paths for inclusion in the release archive
cd "$BUILD_DIR"
sha256sum * > "../${CHECKSUMS_FILE}.archive"
cd ..

# Display the checksums file
echo ""
echo "Checksums generated successfully:"
echo "================================="
cat "$CHECKSUMS_FILE"

echo ""
echo "Checksums file created: $CHECKSUMS_FILE"
echo "Archive checksums file created: ${CHECKSUMS_FILE}.archive (for inclusion in release)"
echo "Number of executables: $(wc -l < $CHECKSUMS_FILE)"