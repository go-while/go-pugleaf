#!/bin/bash
test -z "$1" && echo "usage: $0 \"news\\.admin\\.\" (espace dots with \\. or dot acts like a wildcard!)" && exit 1
out=$(echo $1 | sed 's/\\//g' )
grep -E "^$1" active.* | sort -u > "out/${out}_.txt"
echo "written $(wc -l out/${out}_.txt|cut -d" " -f1) lines to out/${out}_.txt"
