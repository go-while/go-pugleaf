# Known Bugs in go-pugleaf

# expire-news
 - needs testing

# pugleaf-fetcher
 - does not respect max article size because we're sucking via XHDR message-id
 - eats memory when sucking with few hundreds of connections
 - NoCem processing does not exist yet

# webserver
 - eats memory over time

# nntp-server (low priority)
 - reading should work but needs testing
 - requesting articles via message-id does not work
 - posting does not work because history and hashdb is disabled
 - history + hashdb (sqlite3_sharded) eats memory and has IO issues
 - peering does not work. unfinished code.

