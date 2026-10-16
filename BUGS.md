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
 - **Fixed by plan `nntp-rfc-commands` (2026-10-15, `.claude/plans/done/nntp-rfc-commands.md`):** RFC-1: DATE, NEXT,
   LAST, NEWGROUPS (from `newsgroups.created_at`, LIST ACTIVE format), LIST ACTIVE/NEWSGROUPS with an RFC 3977 §4
   wildmat, LIST OVERVIEW.FMT, LIST HEADERS, OVER and HDR (any header; same value sources as XHDR), MODE STREAM and
   CHECK (RFC 4644; STREAMING advertised iff MODE STREAM gives 203), STARTTLS (RFC 4642; advertised only on a
   plaintext, unauthenticated connection when `-nntpcertfile/-nntpkeyfile` are set, which now works without
   `-nntptlsport`). NEWNEWS stays unimplemented and unadvertised by decision (no index on `articles.imported_at`;
   a `NEWNEWS *` would open every group DB). Also fixed on the way: a STARTTLS-upgraded session now sends TLS
   close_notify at QUIT (T-rfc-commands-2). Not implemented by design: cancel/Supersedes processing, `control.*`
   filing, moderator forwarding, NEWNEWS, LIST ACTIVE.TIMES/DISTRIB.PATS, AUTHINFO SASL (local `Xref:` generation
   was on this list until plan `xref-history`, see below).
 - **Fixed by plan `xref-history` (2026-10-16, `.claude/plans/done/xref-history.md`):** local `Xref:` generation
   (BUG-5, INT-4 Xref part). `pugleaf-nntp-server -xref off|partial|full` (default `partial`) serves `Xref:` as the
   last header line of HEAD/ARTICLE, as the 9th OVER/XOVER field (LIST OVERVIEW.FMT then ends with `Xref:full`) and
   in HDR/XHDR. `partial` lists only the serving group (no lookups; no line for a message-id read with no group
   selected); `full` adds the other groups from the history index, which now stores `groupID:articleNum` tokens:
   one history lookup per served article (one per line for OVER/XOVER and HDR/XHDR ranges). `off` is the old output. Numbers for existing
   data appear only after a full `history-rebuild` run (plain `groupID` tokens count as "Without number" in
   `-validate-only`); new articles get them on arrival. Foreign Xref headers are still stripped on intake and
   dropped on relay. Binary e2e test: `TestE2EXrefBinary` in `cmd/nntp-server/e2e_xref_test.go`.
 - Accepted limitation (user decision 2026-10-11): the local430 negative cache keeps a 60 s TTL, so an article stored
   by another process (fetcher) answers 430 for up to 60 s (T-nntp-article-1). Same-process arrivals (plan
   `post-430-window`): POST answers 240 only after the batch writer committed the article (`-post-commit-wait`,
   default 5 s; 0 = no wait), and every same-process arrival (POST/IHAVE/TAKETHIS) gets a 10 s grace in which a
   by-message-id lookup tries the arrival's groups directly and a miss is not cached in local430.
 - **Fixed by plan `nntp-client-transfer` (2026-10-15, `.claude/plans/done/nntp-client-transfer.md`):** CLI-1 (one
   Path line, headers relayed verbatim, dot-stuffed byte-exact bodies), CLI-2 (411 no longer panics the fetcher),
   CLI-3 (client socket deadlines, TLS handshake timeout, no leaked sockets), CLI-4 (authenticated streaming
   transfers no longer stall at the first CHECK), CLI-5, CLI-6 (dead peering manager and `cmd/test-nntp` removed),
   OPS-3 (pprof only on `-pprof-port`/localhost without `cmdline`; UI on `-web-bind`, default 127.0.0.1), INT-5 `bytes`
   half, A-nntp-client-1/2/3/4/6/7/9/10/11, A-intake-6 sender half, A-ops-3d/f (SIGTERM in history-rebuild and
   nntp-transfer), A-tools-3 client guard, NC-1…3 (LF in outbound headers, groups re-offered after 431/errors, Redis
   keys scoped per target). Also fixed on the way: a shutdown hang in nntp-transfer when a peer never answers.
 - **Fixed by plan `audit-delta-fixes` (2026-10-15, `.claude/plans/done/audit-delta-fixes.md`):** WEB-1 (`-edit-cronjobs`
   gates cron create and enable), WEB-2 (preview rendered as text, Content-Security-Policy on every response), A-web-1
   (hidden articles are 404 for non-admins on every read path, tree nodes redacted), A-web-2/3/4 (sanitized cache
   bound, race, no write-back), A-web-5 web half (NNTP user mapping in the admin form), A-web-11, A-web-13, A-web-15,
   A-web-17, A-web-20, INT-7 (web posts store the full RFC 5536 header block with Injection-Info/Injection-Date; the
   POST path to a provider drops both), FU:E12–E17; A-db-1 (NULL-safe readers + repair migration 0029), A-db-2,
   A-db-5, A-db-6, A-db-9, A-db-11, A-db-12, A-db-13, INT-5 (monotonic article numbers via `article_num_seq`,
   migration 0010; expire-news removes derived rows), OPS-1 (library code returns errors, `INIT` reset on Shutdown,
   every background loop stops, web/recover-db/fetcher tear the DB down on exit), OPS-2a (embedded NNTP server
   removed from `cmd/web`), OPS-2b, OPS-4a/b/c/d/f, A-tools-2, A-tools-3 fetcher half, A-tools-5a, A-ops-1
   (`recover-db` `-db` is an alias of `-data`, write modes need `-force`), A-ops-2, A-ops-3a/b/c/e, A-ops-4
   (`-disable-active` refuses an empty keep set, `-disable-active-dry-run`), A-ops-5, A-ops-15, SEC-7 residual.
   A-tools-1, A-tools-4, A-ops-3d/f were closed by the sibling NNTP plans. The optional waves O1/O2 (run 2026-10-15)
   closed A-web-6…10/12/14/16/18/19, A-db-7/8/10/14, A-tools-6…21, A-ops-6…8/10/11/12/17, A-intake-10 (the
   fetcher's eternal-september/blueworldhosting caps stay by user decision; the 441 attempt counter is in `issues.md`);
   A-db-3 and A-db-4 were closed by `nntp-intake-injection` (d27c46d, 2b13adf; one A-db-3 remnant, the
   `ExistsMsgIdInArticlesDB` caller in `proc_DLArt.go`, closed by `followup-leftovers`). Deferred with a hand-off
   note and a user decision (2026-10-15: stay deferred): history fail-stop exits (Q3), A-ops-13/14/16, nonce-based CSP
   (Q5).
 - Plan `followup-leftovers` (2026-10-15): A-tools-5b closed (the fetcher records progress only after the batch writer
   committed the group's articles: `SQ3batch.WaitCommitted`); Q4 closed (`toolchain go1.27.2`, `go 1.26.0`, x/net
   v0.60.0, quic-go v0.59.1: govulncheck reports 0 reachable vulnerabilities, 1 module-only finding GO-2026-5932 for
   the unused x/crypto/openpgp); `tests.yml` runs vet and the race suite on manual dispatch (user decision: not on push/PR); post-queue rows park after
   `PostQueueMaxAttempts` (default 5) upstream 441 rejections (migration 0030, admin Retry); one ACTIVE NNTP account per
   web user (migration 0031, partial unique index); plus the small web/ops/nntp leftovers listed in the plan's Outcome.
 - Plan `test-timing-hardening` (2026-10-15): tests no longer assert on wall-clock upper bounds under 10x; hang guards
   come from `PUGLEAF_TEST_DEADLINE` (default 60 s); the NNTP server counts delay sites (`ErrorDelayCount`,
   `ArticleDelayCount`) for structural checks; the auth timing-enumeration test was removed on purpose. Its stress
   pass found and fixed a POST data race (message-id read after the batch hand-off, 4956980).
 - The test suite in `internal/nntp/nntp-server-*_test.go` and `cmd/nntp-server/server_e2e_test.go` marks every open
   item with `knownBug`; `PUGLEAF_KNOWN_BUGS=1 go test ./internal/nntp/ ./cmd/nntp-server/` lists them (1 id:
   T-nntp-article-1, an accepted limitation).
 - Leftovers of the optional waves are listed in the Outcome of `.claude/plans/done/audit-delta-fixes.md`; the
   wall-clock-sensitive tests are the subject of the plan `.claude/plans/done/test-timing-hardening.md`.
 - peering: the unfinished `PeeringManager` was removed (CLI-6); incoming feeds work via IHAVE/TAKETHIS, outgoing
   feeds via `cmd/nntp-transfer` (CHECK/TAKETHIS streaming); there is no automatic peer-to-peer feed configuration.
 - the history index must be (re)built with cmd/history-rebuild for articles imported before it was enabled
