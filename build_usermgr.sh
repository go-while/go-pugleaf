#!/bin/bash
echo "$0"
go build -o build/usermgr -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/usermgr
