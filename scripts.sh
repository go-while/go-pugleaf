rm data/history -r
sqlite3 data/cfg/pugleaf.sq3 "update config set value = 7 WHERE key = 'history_use_short_hash_len';"
./history-rebuild -nntphostname pugleaf.net

./recover-db -parsedates -rewritedates
./webserver -update-newsgroups-hide-futureposts -update-newsgroup-activity;
./webserver -update-newsgroup-activity;

./copynew.sh; ./recover-db -parsedates -rewritedates; ./webserver -update-newsgroups-hide-futureposts -update-newsgroup-activity;



./fix-references -dry-run -group de.alt.talk.unmut
./fix-references -rebuild-threads -group de.alt.talk.unmut
./fix-thread-activity
