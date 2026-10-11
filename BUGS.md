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
 - **Fixed by plan `nntp-intake-injection` (2026-10-11, `.claude/plans/done/nntp-intake-injection.md`):** SEC-2 intake
   side and A-intake-8 (intake never creates groups; unknown, inactive, reserved, status-`x` groups are dropped,
   moderated groups need `Approved:`), SEC-3 intake side and BUG-7 "too large" (POST/IHAVE/TAKETHIS on the bounded
   reader: 441/437/439 with the stream in sync, 400 + clean close past the drain bound), A-intake-2 (strict header-block
   check, fetch-mode normalization), A-intake-3 (Message-ID syntax, header vs id), INT-1 (POST injection: Message-ID,
   Date, Injection-Date, Injection-Info, `!.POSTED!` Path; feeds get `!.SEEN.<ip>!`), INT-2 and A-nntp-rfc-8 (dedupe
   and failure codes per RFC 3977/4644; in-flight TAKETHIS answers 439 like INN), INT-3, INT-4 (Control rejected,
   Xref stripped), A-intake-4, A-intake-5, A-intake-6 intake half, A-intake-7, A-intake-9, A-intake-11, RFC-5
   (POST/IHAVE/TAKETHIS codes), A-nntp-rfc-7, BUG-8, A-db-1 upsert part (the batch writer never creates a
   `newsgroups` row), INT-6, A-db-3, A-db-4, A-tools-1 (capture limiter, 2048 in flight), A-tools-4.
 - Still open on the server: RFC-1 (DATE/NEXT/LAST/NEWGROUPS/NEWNEWS/OVER/HDR/LIST OVERVIEW.FMT/MODE STREAM/CHECK/
   STARTTLS, `nntp-rfc-commands`). Not implemented by design: cancel/Supersedes processing, `control.*` filing,
   moderator forwarding, local `Xref:` generation.
 - Accepted limitation (user decision 2026-10-11): the local430 negative cache keeps a 60 s TTL, so an article stored
   by another process (fetcher) answers 430 for up to 60 s (T-nntp-article-1); same-process arrivals are removed at once.
 - Open high elsewhere: A-db-1 readers and the repair migration for existing NULL-hierarchy rows (the writer is
   fixed), INT-5, A-db-2 (article cache grows unbounded); CLI-1…4, OPS-3 (client/transfer; pprof
   exposes `-password`); WEB-1, WEB-2 (cron create/toggle without `-edit-cronjobs`; stored XSS via the preview);
   A-tools-1, A-tools-2 (fetcher goroutines unbounded; `-fetch-newsgroups-from-remote` ignores `-data`); A-ops-1
   (`recover-db` operates on `./data` while its usage text names `-db`).
 - The test suite in `internal/nntp/nntp-server-*_test.go` and `cmd/nntp-server/server_e2e_test.go` marks every open
   item with `knownBug`; `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` lists them (2 ids:
   RFC-1, T-nntp-article-1).
 - Fix plans: `.claude/plans/queued/{nntp-client-transfer,nntp-rfc-commands,audit-delta-fixes}.md`.
 - peering does not work: CHECK and outgoing feeds are unfinished code (CLI-6)
 - the history index must be (re)built with cmd/history-rebuild for articles imported before it was enabled
