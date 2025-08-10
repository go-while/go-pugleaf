#!/bin/bash
echo "$0"
go build -o build/recover-db -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/recover-db
