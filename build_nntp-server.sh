#!/bin/bash
echo "$0"
go build -o build/pugleaf-nntp-server -ldflags "-X config.AppVersion=$(cat appVersion.txt)" ./cmd/nntp-server
