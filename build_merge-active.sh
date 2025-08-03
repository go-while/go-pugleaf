#!/bin/bash
echo "$0"
go build -race -o build/merge-active -ldflags "-X config.AppVersion=$(cat appVersion.txt)" ./cmd/merge-active
