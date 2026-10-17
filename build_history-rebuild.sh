#!/bin/bash
echo "$0"
go build -o build/history-rebuild -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/history-rebuild
