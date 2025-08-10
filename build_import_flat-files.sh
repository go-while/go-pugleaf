#!/bin/bash
echo "$0"
go build -race -o build/import-flat-files -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/import-flat-files
exit $?
