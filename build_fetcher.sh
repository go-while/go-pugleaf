#!/bin/bash
echo "$0"
go build -race -o build/pugleaf-fetcher -ldflags "-X config.AppVersion=$(cat appVersion.txt)" ./cmd/nntp-fetcher
exit $?
