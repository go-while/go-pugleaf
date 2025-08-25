#!/bin/sh -e
# Generate checksums for individual executables
echo "Generating checksums for individual executables..."
./createChecksums.sh

rm -f update.tar.gz
tar -f update.tar.gz -C build -c -v -z .
sha256sum update.tar.gz > .update
HASH=$(cat .update|cut -d" " -f1)
du -b update.tar.gz
cat .update
echo ""
echo "Individual executable checksums available in: checksums.sha256"
exit 0
