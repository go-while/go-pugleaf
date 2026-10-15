#!/bin/sh
echo "scripts.sh is a notes file, not a script; copy the commands you need" >&2; exit 1
rm data/history -r
sqlite3 data/cfg/pugleaf.sq3 "update config set value = 7 WHERE key = 'history_use_short_hash_len';"
./history-rebuild

./recover-db -parsedates -rewritedates
./webserver -update-newsgroups-hide-futureposts -update-newsgroups-activity 1;
./webserver -update-newsgroups-activity 1;

./copynew.sh; ./recover-db -parsedates -rewritedates; ./webserver -update-newsgroups-hide-futureposts -update-newsgroups-activity 1;



./fix-references -dry-run -group de.alt.talk.unmut
# fix-references cannot start until A-ops-7 is fixed (AppVersion unset); -dry-run defaults to true, so writes need -dry-run=false
./fix-references -rebuild-threads -dry-run=false -group de.alt.talk.unmut
./fix-thread-activity
