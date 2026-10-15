#!/bin/bash
echo "Building expire-news..."
go build -o build/expire-news -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/expire-news && echo "expire-news built OK" && exit 0
echo "error building expire-news"
exit 1
