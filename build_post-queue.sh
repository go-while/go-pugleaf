#!/bin/bash
echo "$0"
GOEXPERIMENT=greenteagc go build -o build/post-queue -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/post-queue
exit $?
