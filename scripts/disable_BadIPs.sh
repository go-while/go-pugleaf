sqlite3 data/cfg/pugleaf.sq3 "UPDATE config SET value = 'false' WHERE key = 'BlockBadIPs'"
sqlite3 data/cfg/pugleaf.sq3 "SELECT key,value FROM config WHERE key = 'BadIPs' or key = 'BlockBadIPs'";
echo "restart webserver now"
