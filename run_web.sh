#!/bin/bash
while true; do
./copynew.sh
./webserver
echo "sleep 3s before restart"
sleep 3
done
