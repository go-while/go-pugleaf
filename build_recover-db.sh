#!/bin/bash
echo "$0"
go build -o build/recover-db -ldflags "-X config.AppVersion=$(cat appVersion.txt)" ./cmd/recover-db
