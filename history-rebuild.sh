#!/bin/bash
#echo "$0: deletes all in data/history. disable this echo to run it" && exit 1

rm -rfv data/history
sqlite3 data/cfg/pugleaf.sq3 "update config set value = 7 WHERE key = 'history_use_short_hash_len';" || exit 2
./history-rebuild -nntphostname pugleaf.net

