sqlite3 data/cfg/pugleaf.sq3 "UPDATE config SET value = 'false' WHERE key = 'BlockBadBots'"
sqlite3 data/cfg/pugleaf.sq3 "SELECT key,value FROM config WHERE key = 'BadBots' or key = 'BlockBadBots'";
echo "restart webserver now"
