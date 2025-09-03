#!/bin/sh -e
# Generate checksums for individual executables
echo "Generating checksums for individual executables..."
./createChecksums.sh

# Copy archive checksums file into build directory for inclusion in release archive
echo "Including checksums in release archive..."
cp checksums.sha256.archive build/checksums.sha256

rm -f update.tar.gz
tar -f update.tar.gz -C build -c -v -z .
sha256sum update.tar.gz > .update
HASH=$(cat .update|cut -d" " -f1)
du -b update.tar.gz
cat .update
echo ""
echo "Release archive created with individual executable checksums included"
echo "Individual executable checksums available in: checksums.sha256"
echo "Checksums are also included in the release archive as checksums.sha256"
exit 0
