#!/bin/sh -e
tar -f update.tar.gz -C build -c -v -z .
sha256sum update.tar.gz > .update
du -hs update.tar.gz
cat .update
exit 0
