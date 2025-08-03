#!/bin/bash
while true; do
./copynew.sh
./webserver -useshorthashlen 7 -import-desc preload/newsgroups.descriptions
echo "sleep 3s before restart"
sleep 3
done
