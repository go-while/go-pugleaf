#!/bin/bash
echo "$0"
GOEXPERIMENT=greenteagc go build -o build/recover-db -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/recover-db
