#!/bin/bash
echo "$0"
go build -o build/pugleaf-nntp-server -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/nntp-server
