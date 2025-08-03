#!/bin/bash
# tool to search for strings in GO source files
find . -iname "*.go" -exec grep -in "$1" {} +
