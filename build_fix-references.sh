#!/bin/bash
echo "$0"
go build -o build/fix-references -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/fix-references
exit $?
