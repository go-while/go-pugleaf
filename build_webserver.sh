#!/bin/bash
echo "$0"
GOEXPERIMENT=greenteagc go build -race -o build/webserver -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/web/
exit $?
