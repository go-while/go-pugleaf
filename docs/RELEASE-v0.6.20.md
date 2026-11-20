# go-pugleaf v0.6.20

A small release after v0.6.19: the news server can log why it rejects articles from its peers, and the repository
has a security policy. No database migrations.

## Upgrading

1. **Build with Go 1.27.4 or newer**, as for v0.6.19. go-sqlite3 still needs cgo and a C compiler.
2. **Stop the services, install the new binaries, start again.** The new log stays off unless you set its flag.

## Changes since v0.6.19

### News server: log of rejected feed articles
- New `pugleaf-nntp-server -inn-artlog FILE`: one line per article that a peer sent in full by IHAVE or TAKETHIS
  and the server rejected (437/439, including duplicates found after the article was sent), in the format of INN's
  `news` log, for example `Nov 20 15:17:19.123 - news-ams.pugleaf.net <id@example.invalid> 439 In progress`.
- The reason is the reply text without the leading TAKETHIS message-id; control characters (C0 and DEL) become
  spaces, surrounding spaces are trimmed, and the reason is cut at 256 bytes. An invalid message-id is written as
  `(null)`.
- Not logged: accepted articles, deferrals (436, 431, 400), and refusals before the article is sent (IHAVE 435,
  CHECK 438), so most duplicates a streaming peer offers do not appear.
- The directory of FILE must exist and be writable by the service user; if the file cannot be opened at startup,
  the server does not start.
- Lines are queued in memory (at most 8192; more are dropped with a `[PEERSTATS]` message) and normally written
  once a second and on a clean shutdown. The file is reopened after logrotate moves it, and copytruncate works too;
  the file itself is not size-limited, so add it to your logrotate configuration. Logging is best effort: if a write
  fails while the server runs (directory removed, disk full), the lines not yet written are dropped without retry
  and a `[PEERSTATS]` error is logged.
- The `-inn-log` statistics format (innd/innfeed lines for innreport) is unchanged; the reject lines go to their
  own file because innreport would not recognize them. The server refuses to start when `-inn-log` and
  `-inn-artlog` name the same file.

### Security policy
- `SECURITY.md`: report vulnerabilities privately through GitHub Security Advisories (the web form or `gh api`),
  not in public issues, pull requests or newsgroups.
