#!/bin/bash
echo "$0"
go build -o build/merge-active -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/merge-active
