#!/bin/bash
echo "$0"
GOEXPERIMENT=greenteagc go build -o build/audit-web-posts -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/audit-web-posts/
exit $?
