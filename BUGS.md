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
 - requesting articles via message-id uses the history index (message-id -> newsgroups), needs testing
 - posting / IHAVE / TAKETHIS check the history index for duplicates, needs testing
 - peering does not work: CHECK and outgoing feeds are unfinished code
 - the history index must be (re)built with cmd/history-rebuild for articles imported before it was enabled
