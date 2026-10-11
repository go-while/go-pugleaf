# Known Bugs in go-pugleaf

# expire-news
 - needs testing

# pugleaf-fetcher
 - does not respect max article size because we're sucking via XHDR message-id
 - eats memory when sucking with few hundreds of connections
 - NoCem processing does not exist yet

# webserver
 - eats memory over time

# nntp-server (audited 2026-10, see docs/audit/AUDIT-2026-10.md and docs/audit/audit-nntp-*.md)
 - **Must be fixed before any public deployment:** the XHDR range loop spins a core forever and blocks shutdown
   (SEC-1), LISTGROUP creates a group DB for any name (SEC-2), and a pipelined `QUIT` followed by another command
   crashes the whole process (A-nntp-sec-1). None of these needs authentication.
 - Open critical: SEC-1, A-nntp-sec-1. Open high (server): SEC-3 (no byte limits, MaxArtSize unused), SEC-7
   (LISTGROUP loads every row), A-nntp-rfc-2 (unstuffed `.` header lines / LF-joined headers desync readers;
   intake root cause A-intake-2).
 - Open high elsewhere: A-db-1 (a post to an unknown group writes a NULL-hierarchy row that breaks GROUP and every
   `$all` tool), INT-5, INT-6, A-db-2 (article cache grows unbounded); CLI-1…4, OPS-3 (client/transfer; pprof
   exposes `-password`); WEB-1, WEB-2 (cron create/toggle without `-edit-cronjobs`; stored XSS via the preview);
   A-tools-1, A-tools-2 (fetcher goroutines unbounded; `-fetch-newsgroups-from-remote` ignores `-data`); A-ops-1
   (`recover-db` operates on `./data` while its usage text names `-db`).
 - The test suite in `internal/nntp/nntp-server-*_test.go` and `cmd/nntp-server/server_e2e_test.go` marks every open
   item with `knownBug`; `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` lists them.
 - Fix plans: `.claude/plans/queued/{nntp-server-hardening,nntp-intake-injection,nntp-client-transfer,nntp-rfc-commands,audit-delta-fixes}.md`.
 - peering does not work: CHECK and outgoing feeds are unfinished code (CLI-6)
 - the history index must be (re)built with cmd/history-rebuild for articles imported before it was enabled
