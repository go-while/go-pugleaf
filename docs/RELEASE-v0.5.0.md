# go-pugleaf v0.5.0

The first release since v0.4.7-1 (2025-09-18): 1,108 commits. It carries a security audit of the whole code base with
every critical and high finding fixed, a reworked message-id history index, a bulk mbox importer, a rewritten
nntp-transfer, a web server without gin, and COMPRESS DEFLATE for provider connections.

**Upgrade if you expose `nntp-server` to the internet.** v0.4.7-1 ships the server code paths of the two critical
findings: a pipelined `QUIT` followed by any command crashed the whole process, and an unclamped `XHDR` range pinned a
CPU core and blocked shutdown. Both need no login.

## Upgrading

1. **Build with Go 1.27.1 or newer** (`go.mod` pins toolchain go1.27.3). go-sqlite3 still needs cgo and a C compiler.
2. **Stop the services, back up `data/`, install the new binaries, start again.** 18 database migrations
   (0009-0033) run automatically on the first start of any tool; there is no manual step.
3. **History index:** the index was reworked (message-id → group and article number). New articles are indexed on
   arrival. For articles stored before the upgrade, run `history-rebuild -data <dir>` once if you use duplicate
   checks across groups, crosspost reuse in the fetcher or the `Xref:` header of nntp-server. `history-rebuild
   -validate-only` shows how many entries lack a number.
4. **Check your scripts against the changed flags below.** Removed flags make a tool exit with a usage error.

### Changed defaults

| Tool | Flag | Was | Now |
|---|---|---|---|
| nntpmgr | `-maxconns` (new users) | 1 | 3 (allowed 1-25) |
| nntp-server | article size limit | unlimited | 32 KiB (`-maxartsize`) |
| nntp-transfer | `-ssl` | true | false (pass `-ssl -port 563` for TLS peers) |
| nntp-transfer | `-batch-check` | 25 | 100 |
| history-rebuild | `-batch-size` | 5000 | 10000 (now a number range per query) |
| tcp2tor | `-listen-host` / `-listen-port` | `""` / 1119 | `127.2.3.4` / 0 (set it, or use `-config`) |
| recover-db | `-db` | `data` | deprecated alias of `-data` |

### Removed or renamed flags

- **web:** `-import-desc` and `-import-create` → `-update-newsgroups-descriptions-file` and
  `-update-newsgroups-descriptions-import-create-newsgroups`; `-update-descr` →
  `-update-overwrite-newsgroups-descriptions`; `-update-newsgroup-activity` → `-update-newsgroups-activity`. The
  never-working embedded NNTP server and fetcher of the web binary (`-withnntp`, `-withfetch`, `-isleep`) are gone:
  run `nntp-server` and `nntp-fetcher` as their own services.
- **history-rebuild:** `-clear-first`, `-nntphostname`, `-read-offset`, `-show-collisions` removed; new `-restart` and
  `-trim`.
- **fix-references:** `-db` → `-data`.
- **test-nntp** and the `TestMsgIdItemCache` helper are no longer built.
- Every tool takes `-data <dir>` and honours it everywhere (several tools used to open `./data` regardless).

## Security

An audit in October 2026 reviewed every package (RFC conformity, security, DoS, concurrency, intake, web, tools,
operations) and added an NNTP server test suite. It found 2 critical, 19 high, 59 medium and 68 low issues. All
critical and high findings are fixed; the remaining open items are deliberate decisions listed in `BUGS.md`. The full
report is `docs/audit/AUDIT-2026-10.md`. The most important fixes:

- **NNTP server:** no crash on pipelined `QUIT`; ranges clamped on `XHDR`, `OVER`, `LISTGROUP`; unknown groups are
  never created by `LISTGROUP` or by a posted `Newsgroups:` header; byte limits on command and article lines; `TAKETHIS`
  checks authentication before reading the article; optional per-user connection limits (`-enforce-user-maxconns`);
  idle timeouts; a negative cache for repeated misses.
- **Article intake (RFC 5536/5537):** the header block is validated, dot-stuffing is preserved, repeated single
  headers are rejected, control messages are rejected, article numbers are never reused after deletes.
- **Web:** stored XSS in the article preview fixed, Content-Security-Policy on every response, CSRF protection on all
  forms, hashed session tokens at rest, login rate limiting, cron jobs can only be created with `-edit-cronjobs`,
  foreign keys enforced in SQLite.
- **Tools:** `nntp-transfer`'s pprof endpoint no longer leaks passwords and binds to loopback; `recover-db` asks before
  destructive modes and opens the directory it prints; the NNTP client has read, write and TLS handshake deadlines
  and caps on response size.

## New

- **mbox-importer** (new tool): imports Usenet mbox archives in bulk, also `.gz`, `.zip`, `.7z`, `.xz`, `.zst` and
  Usenet Archive Toolkit `.usenet` files; marks finished files as done; first-group rule for crossposts.
- **audit-web-posts** (new tool): checks posts made through the web front end.
- **nntp-server:** `Xref:` header from the history index (`-xref off|partial|full`), STARTTLS, DATE, NEXT/LAST,
  NEWGROUPS, LIST wildmats, OVER/HDR with message-ids, streaming (`MODE STREAM`, `CHECK`, `TAKETHIS`), `-nntpbind`,
  `-plaintextauth`, `-readauth`, `-post-commit-wait`; accept errors back off instead of flooding the log. `NEWNEWS`
  answers `500` by design.
- **nntp-fetcher:** COMPRESS DEFLATE (RFC 8054) per provider (admin checkbox, see `docs/COMPRESS.md`), crosspost reuse
  from other groups (`-reuse-crossposts`), `-reset-progress`, `-exclude-prefix`, a per-provider byte summary at shutdown;
  survives hostile or broken providers (expired logins, wrong message-ids, endless replies).
- **nntp-transfer** (rewritten): pipelined `CHECK`/`TAKETHIS` with Redis caching of sent message-ids, include and
  exclude files, date ranges, progress database, an optional status web page; IHAVE fallback for peers without
  streaming (`-ihave auto|never|always`, see `docs/TRANSFER.md`); `-compress`.
- **Web front end:** new admin pages for settings (hostname, registration, post size), cron jobs and the post queue
  (retry, delete); bad-bot and IP block lists; hierarchy pages kept up to date incrementally (also with 100k+ groups); a full-text
  search index; a memorial page; gin replaced by net/http (smaller binary, `405` with `Allow`, a 1 MiB body cap).
- **expire-news** `-trim-history`, **recover-db** `-force` / `-reorder-by-datesent`, **merge-active** overview
  options, **tcp2tor** config file, connection limits and `-allow-public-bind`, **nntpmgr** `-activate`,
  `-deactivate`, `-setpassword`.

## Reliability and performance

- Bulk intake accepts more real-world archives: lenient date parsing with fallbacks to `Posted:`/`Date-Received:`,
  dates before 1980 treated as unreadable.
- Many data races, goroutine leaks and shutdown hangs fixed; every tool stops cleanly on SIGTERM.
- Fewer and better SQLite indexes, retries on busy databases, transactions where rows must change together.

## Testing

- Tests went from a handful to suites in 30 packages, run under the race detector; `PUGLEAF_KNOWN_BUGS=1` lists the
  single accepted known bug (`nntp-article-430-cache-delay`: an article stored by the fetcher can answer 430 on
  nntp-server for up to 60 s).
- NNTP tests use the shared module `github.com/go-while/nntp-testkit` v0.8.0; go-pugleaf's server passes its
  41-check RFC 3977/4643/4644 conformance run.
- Built and tested on 18 distributions: AlmaLinux 9/10, Alpine 3.22-3.24, Arch, CentOS Stream 9/10, Debian 13,
  Gentoo, openEuler 25.03, openSUSE Leap 15.6/16.0, Rocky 9/10, Ubuntu 24.04/25.04/26.04.

## Known issues

- An article the fetcher stores can answer `430` on nntp-server for up to 60 s (local negative cache; accepted).
- go-pugleaf's own NNTP server does not offer COMPRESS.
- Deferred by decision: see `BUGS.md`.
