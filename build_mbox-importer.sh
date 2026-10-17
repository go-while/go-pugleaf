#!/bin/bash
echo "$0"
go build -o build/mbox-importer -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/mbox-importer
