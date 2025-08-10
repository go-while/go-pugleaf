#!/bin/bash -e
# config.settings contains subd|fqdn|type|upstream combinations
# example: novabbs|pugleaf.net|http|localhost:11980
# example: rocksolid-us|pugleaf.net|https|remote-node-host.pugleaf.net:11980
#
while read line; do
 SUBD=$(echo "$line"|cut -d"|" -f1)
 FQDN=$(echo "$line"|cut -d"|" -f2)
 TYPE=$(echo "$line"|cut -d"|" -f3)
 UPST=$(echo "$line"|cut -d"|" -f4)
 (test -z "$SUBD" || test -z "$FQDN" || test -z "$TYPE" || test -z "$UPST") && echo "error in config.settings" && exit 1
 FILE="/etc/nginx/sites-available/${SUBD}.${FQDN}"
 cp /etc/nginx/sites-available/pugleaf.nginx.template "$FILE"
 sed "s/XXSUBDXX/$SUBD/g" -i "$FILE"
 sed "s/YYFQDNYY/$FQDN/g" -i "$FILE"
 sed "s/ZZTYPEZZ/$TYPE/g" -i "$FILE"
 sed "s/XXUPSTREAMXX/$UPST/g" -i "$FILE"
done< <(cat config.settings)
