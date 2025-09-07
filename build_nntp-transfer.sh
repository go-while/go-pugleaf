#!/bin/bash
echo "$0"
GOEXPERIMENT=greenteagc go build -race -o build/nntp-transfer -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/nntp-transfer
