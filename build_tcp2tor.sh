#!/bin/bash
echo "$0"
GOEXPERIMENT=greenteagc go build -race -o build/tcp2tor -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/tcp2tor/
exit $?
