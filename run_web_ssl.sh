#!/bin/bash
while true; do
./webserver -webssl -websslcert fullchain.pem -websslkey privkey.pem -webport 11981
echo sleeping
sleep 3s
done
