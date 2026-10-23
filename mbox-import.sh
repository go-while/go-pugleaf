#!/bin/bash
# Imports every mbox / UAT archive of a directory into the newsgroup named by its file name
# (de.comm.foo.mbox.gz -> de.comm.foo), several files at a time, and renames each imported file
# to NAME.done, so the script can be re-run on the same directory.
# Run it from the go-pugleaf directory: the data directory defaults to ./data.
#
# Usage: ./mbox-import.sh [DIR] [more mbox-importer flags...]
#   PARALLEL=N   files imported at the same time (default 4)
DIR=${1:-/home/mbox2nntp/src/mbox2nntp/contrib/uat-mbox/}
[ $# -gt 0 ] && shift
exec ./mbox-importer -group-from-name -parallel "${PARALLEL:-4}" "$@" "$DIR"
