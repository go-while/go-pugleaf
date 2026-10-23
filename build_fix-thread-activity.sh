#!/bin/bash
echo "$0"
go build -o build/fix-thread-activity -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/fix-thread-activity
exit $?
