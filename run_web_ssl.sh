#!/bin/bash
while true; do
./webserver -useshorthashlen 7 -webssl -websslcert fullchain.pem -websslkey privkey.pem -webport 11981
echo sleeping
sleep 3s
done
