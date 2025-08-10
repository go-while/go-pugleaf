#!/bin/bash
echo "$0"
go build -race -o build/merge-active -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/merge-active
