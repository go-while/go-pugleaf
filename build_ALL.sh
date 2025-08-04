#!/bin/bash
echo "running: $0"
rm -v build/*

#./build_merge-active.sh
#./build_merge-descriptions.sh
#./build_TestMsgIdItemCache.sh
#./build_history-rebuild.sh
#./build_nntp-server.sh
#./build_fix-references.sh
#./build_fix-thread-activity.sh

./build_rslight_importer.sh
./build_analyze.sh
./build_fetcher.sh
./build_webserver.sh
./build_recover-db.sh
./build_expire-news.sh
