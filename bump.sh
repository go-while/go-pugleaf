#!/bin/sh
#./appVersion.sh 

if [ "$1" = "ALL" ]; then
    ./build_ALL.sh
else
    ./Build_DEV.sh 
fi

./createUpdate.sh
./rsync.sh 
