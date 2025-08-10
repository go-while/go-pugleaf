#!/bin/bash
echo "Building expire-news..."
go build -o build/expire-news cmd/expire-news/main.go && echo "expire-news built OK" && exit 0
echo "error building expire-news"
exit 1
