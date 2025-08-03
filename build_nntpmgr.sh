#!/bin/bash
echo "$0"
go build -race -o build/pugleaf-nntpmgr -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/nntpmgr
exit $?
