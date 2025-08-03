#!/bin/bash
SERVERS=$(grep -vE "^#" ../config/go-pugleaf_ssh-server.txt)
#./build_ALL.sh || exit 1
echo 1 > .update
while IFS="" read SERVER; do
 echo "rsync to $SERVER"
 rsync -advz --delete-before build/ copynew.sh run_web*.sh pugleaf-fetcher-STOP.sh "$SERVER":~/new/ &
 sleep 0.3
 rsync -advz --delete-before migrations web preload .update "$SERVER":~/ &
 sleep 0.3
done< <(echo "$SERVERS")
