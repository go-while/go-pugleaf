#!/bin/bash
echo "$0"
go build -o build/nntp-analyze -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/nntp-analyze
