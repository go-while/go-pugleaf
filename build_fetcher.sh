#!/bin/bash
echo "$0"
GOEXPERIMENT=greenteagc go build -o build/pugleaf-fetcher -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/nntp-fetcher
exit $?
