#!/bin/bash -e
FILE=".update.todo"
test ! -e "$FILE" && echo "update file not found" && exit 1
mkdir -p tmp
rm -f tmp/*
test -e update.tar.gz && rm -v update.tar.gz
wget https://pugleaf.net/cdn/go-pugleaf/update.tar.gz -O tmp/update.tar.gz && cd tmp && \
 sha256sum update.tar.gz && du -b update.tar.gz && \
 sha256sum -c ../$FILE && tar xfvz update.tar.gz && mv * ../new/
rm -v ../"$FILE"
