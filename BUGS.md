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
 - **Fixed by plan `nntp-server-hardening` (2026-10-11, `.claude/plans/done/nntp-server-hardening.md`):** the two
   unauthenticated criticals (SEC-1 XHDR range spin, A-nntp-sec-1 pipelined `QUIT` crash), SEC-2 server side and
   A-nntp-sec-4 (LISTGROUP/GROUP no longer create group DBs for unknown names), SEC-3 server side (512-octet command
   lines, 4 KiB hard cap, bounded article reader `readArticleBlock`), SEC-4, SEC-5, SEC-6 (auth cache bound to the
   stored hash), SEC-7, A-nntp-sec-2 (`hide = 0` on every NNTP read), A-nntp-sec-3, A-nntp-sec-5, A-nntp-sec-6,
   A-nntp-rfc-2 (served side), A-nntp-rfc-3/4/5/6/9/10/11/12, BUG-1…6, BUG-7 (server part), BUG-9, RFC-2/3/4/6,
   RFC-5 (MODE/AUTHINFO part), T-nntp-group-1, T-nntp-auth-1, OPS-2 (nntpmgr part), A-ops-9, A-web-5 (DB part), FN-1.
 - Still open on the server, owned by the next plans: SEC-2 intake side and SEC-3 article-limit wiring, BUG-7 "too
   large → reply after close", RFC-5 POST/IHAVE codes, INT-1, INT-2, A-intake-* (`nntp-intake-injection`); RFC-1
   (DATE/NEXT/LAST/NEWGROUPS/NEWNEWS/OVER/HDR/LIST OVERVIEW.FMT/MODE STREAM/CHECK/STARTTLS, `nntp-rfc-commands`).
 - Accepted limitation (user decision 2026-10-11): the local430 negative cache keeps a 60 s TTL, so an article that
   arrives while its id is cached answers 430 for up to 60 s. BUG-8 (same-process arrival) is closed by
   `nntp-intake-injection` calling `Local430.Remove`; T-nntp-article-1 (arrival through another process) stays.
 - Open high elsewhere: A-db-1 (a post to an unknown group writes a NULL-hierarchy row that breaks GROUP and every
   `$all` tool), INT-5, INT-6, A-db-2 (article cache grows unbounded); CLI-1…4, OPS-3 (client/transfer; pprof
   exposes `-password`); WEB-1, WEB-2 (cron create/toggle without `-edit-cronjobs`; stored XSS via the preview);
   A-tools-1, A-tools-2 (fetcher goroutines unbounded; `-fetch-newsgroups-from-remote` ignores `-data`); A-ops-1
   (`recover-db` operates on `./data` while its usage text names `-db`).
 - The test suite in `internal/nntp/nntp-server-*_test.go` and `cmd/nntp-server/server_e2e_test.go` marks every open
   item with `knownBug`; `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` lists them (9 ids:
   BUG-7, BUG-8, INT-1, INT-2, RFC-1, RFC-5, SEC-2, SEC-3, T-nntp-article-1).
 - Fix plans: `.claude/plans/queued/{nntp-intake-injection,nntp-client-transfer,nntp-rfc-commands,audit-delta-fixes}.md`.
 - peering does not work: CHECK and outgoing feeds are unfinished code (CLI-6)
 - the history index must be (re)built with cmd/history-rebuild for articles imported before it was enabled
