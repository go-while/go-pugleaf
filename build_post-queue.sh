#!/bin/bash
echo "$0"
GOEXPERIMENT=greenteagc go build -race -o build/post-queue -ldflags "-X main.appVersion=$(cat appVersion.txt)" ./cmd/post-queue
exit $?
