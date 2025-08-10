#!/bin/bash
echo "$0"
go build -race -o build/merge-descriptions -ldflags "-X config.AppVersion=$(cat appVersion.txt)" ./cmd/merge-descriptions/
