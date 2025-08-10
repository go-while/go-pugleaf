#!/bin/bash
echo "$0"
go build -o build/rslight-importer -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/rslight-importer
exit $?
