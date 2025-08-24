#!/bin/bash
SERVERS=$(grep -vE "^#" ../config/go-pugleaf_ssh-server.txt)
CDN=$(grep -vE "^#" ../config/go-pugleaf_cdn-server.txt|head -1)
echo "rsync update.tar.gz to $CDN"
rsync -va --progress update.tar.gz "$CDN"
while IFS="" read SERVER; do
 echo "rsync to $SERVER"
 #rsync -advz --delete-before build/ copynew.sh run_web*.sh "$SERVER":~/new/ &
 #sleep 0.3
 rsync -advz --progress web preload .update getUpdate.sh "$SERVER":~/ 1>/dev/null &
done< <(echo "$SERVERS")
