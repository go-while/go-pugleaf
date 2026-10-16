# internal/nntp

This package holds the NNTP **server** (reader side plus IHAVE/TAKETHIS/POST intake, used by
`cmd/nntp-server`) and the NNTP **client** side (backend pool, fetching, transfer, peering, used by
`cmd/nntp-fetcher`, `cmd/nntp-transfer` and others). This README describes the server.

## Server file map

| File | Contents |
|------|----------|
| `nntp-server.go` | `NNTPServer`, `NewNNTPServer`, `Start`/`Serve`/`Stop`, accept loop, connection limit, per-user slots, `PlaintextAuthPolicy` |
| `nntp-server-cliconns.go` | `ClientConnection`, the command loop (`Handle`), greeting, dispatch (`handleCommand`), `getServerCapabilities` |
| `nntp-server-commands.go` | `commandMaxArgs`, `readerCommands`, the gates `argCountOK`, `readGateOK`, `authinfoAdvertised`, `postingAllowed` |
| `nntp-server-lineio.go` | bounded command-line reader `readCommandLine`, bounded article reader `readArticleBlock`, `MaxArticleBytes` |
| `nntp-server-dataio.go` | `dataBlockWriter` (one buffered writer per connection, dot-stuffing, flush every 256 lines), `sendDataBlock` |
| `nntp-server-statistics.go` | `ServerStats` (connections, per-command counts, auth successes/failures) |
| `nntp-cache-local.go` | `Local430`, the negative cache of message-ids that answered 430 |
| `nntp-auth-manager.go` | `AuthManager.AuthenticateUser` (bcrypt check through `database.AuthenticateNNTPUser`) |
| `nntp-cmd-basic.go` | CAPABILITIES, MODE READER, HELP, QUIT |
| `nntp-cmd-date.go`, `nntp-cmd-nextlast.go` | DATE, NEXT, LAST |
| `nntp-cmd-newgroups.go`, `nntp-cmd-list-wildmat.go`, `nntp-wildmat.go` | NEWGROUPS, LIST ACTIVE/NEWSGROUPS with a wildmat (RFC 3977 §4) |
| `nntp-cmd-over-hdr.go` | OVER, HDR, LIST OVERVIEW.FMT, LIST HEADERS |
| `nntp-cmd-stream.go` | MODE STREAM, CHECK (RFC 4644) |
| `nntp-cmd-starttls.go` | STARTTLS (RFC 4642) |
| `nntp-cmd-auth.go` | AUTHINFO USER/PASS |
| `nntp-cmd-group.go` | GROUP, LISTGROUP |
| `nntp-cmd-list.go` | LIST [ACTIVE \| NEWSGROUPS] without a wildmat |
| `nntp-article-common.go` | ARTICLE, HEAD, BODY, STAT (shared lookup by number or message-id) |
| `nntp-cmd-xover.go`, `nntp-cmd-xhdr.go` | XOVER, XHDR (`overviewLine`, `sanitizeOverviewField`) |
| `nntp-cmd-range.go` | `parseRangeArg`, `requireGroup` (412 without a selected group), current-group helpers |
| `nntp-xref.go` | `XrefMode` (`SetXrefMode`), `XrefLocation`, `xrefLine`/`xrefFor`: the local Xref: header |
| `nntp-cmd-posting.go` | POST, IHAVE, TAKETHIS, POST injection (`injectProtoArticle`), feed Path (`relayPath`), reply mapping (`intakeReply`) |
| `nntp-cmd-helpers.go` | `rateLimitOnError` (the error delay) |

Command list: CAPABILITIES, MODE READER, MODE STREAM, STARTTLS (only with a certificate), AUTHINFO
USER/PASS, DATE, LIST ACTIVE|NEWSGROUPS [wildmat], LIST OVERVIEW.FMT, LIST HEADERS, NEWGROUPS date time
[GMT], GROUP, LISTGROUP, NEXT, LAST, STAT, HEAD, BODY, ARTICLE, OVER [range|<message-id>], HDR field
[range|<message-id>], XOVER, XHDR, POST, IHAVE, CHECK <message-id>, TAKETHIS, HELP, QUIT. NEWNEWS is not
implemented and not advertised (plan nntp-rfc-commands, decision D3): it answers `500`. Tests: `nntp-server-*_test.go` (harness in `nntp-server-harness_test.go`),
end-to-end tests in `cmd/nntp-server/`, and `scripts/test-nntp-server.sh` against a built binary.

## Server options

Fields on `NNTPServer` (set them after `NewNNTPServer` and before `Start`/`Serve`) and the
matching `cmd/nntp-server` flags:

| Field / config | Flag | Default | Meaning |
|----------------|------|---------|---------|
| `Config.NNTP.Port` | `-nntptcpport` | 0 | plain TCP port |
| `Config.NNTP.TLSPort`, `TLSCert`, `TLSKey` | `-nntptlsport`, `-nntpcertfile`, `-nntpkeyfile` | 0 | implicit TLS port (TLS 1.2 minimum) |
| `Config.NNTP.MaxConns` | `-maxconnections` | 500 | concurrent client connections, all clients together |
| `Config.NNTP.MaxArtSize` | `-maxartsize` | 0 = config default 32768 | article byte limit (`MaxArticleBytes`; 1 MiB when the config value is <= 0) |
| `Config.Hostname` | `-nntphostname` | database fallback | hostname in the greeting and the Xref: header (`go-pugleaf` when empty) |
| `IdleTimeout` | `-idletimeout` | 3m | idle timeout per command and write timeout per flush |
| `ArticleCmdDelay` | `-articledelay` | 25ms | sleep before ARTICLE/HEAD/BODY/STAT |
| `GroupCmdDelay` | `-groupdelay` | 25ms | sleep before GROUP |
| `ErrorDelay` | `-errordelay` | 1s | sleep before error replies (see below) |
| `PlaintextAuth` | `-plaintextauth` | `accept` | AUTHINFO without TLS: `accept`, `advertise` or `refuse` |
| `ReadRequiresAuth` | `-readauth` | false | reader commands need AUTHINFO first |
| `EnforceUserMaxConns` | `-enforce-user-maxconns` | false | per-user `maxconns` limit |
| `SetLocal430TTL` | `-local430ttl` | 60s | TTL of the local430 negative cache (see below) |
| `SetXrefMode` | `-xref` | `partial` | local Xref: header: `off`, `partial` or `full` (see below); may be changed while serving |
| `ShutdownTimeout` | - | 10s | how long `Stop` waits for client handlers |

`Start` fails when no port is configured, or when a TLS port is set without a usable certificate and
key; nothing is left listening then. `NewNNTPServer` starts no goroutine; `Start`/`Serve` start the
accept loops and the local430 cleanup, `Stop` ends all of them (the caller's WaitGroup covers them).

## Behaviour

- **Reads without auth.** The server is a public reader by default. With `ReadRequiresAuth`, GROUP,
  LISTGROUP, LIST, STAT, HEAD, BODY, ARTICLE, XOVER, XHDR, NEXT, LAST, OVER, HDR, NEWGROUPS and
  NEWNEWS answer `480` until a successful AUTHINFO.
- **Plaintext AUTHINFO** (RFC 4643). `accept`: accepted on plain TCP but not advertised in
  CAPABILITIES; `advertise`: accepted and advertised; `refuse`: `483 Encryption required`. TLS
  connections always advertise `AUTHINFO USER` before login. After `281` AUTHINFO is no longer
  advertised and any further AUTHINFO answers `502`. The password is the raw rest of the line, so
  passwords with spaces work. Replies never echo the username or other client input.
- **Greeting and MODE READER.** `200` when this client may post (server has a processor and the
  user, if logged in, has posting rights), else `201`. Other MODE variants answer `501`.
- **Command lines** (RFC 3977 §3.1). At most 512 octets including CRLF: a longer line that ends
  within 4096 bytes gets `501 Command line too long` and the session continues; 4096 bytes without
  a line end get `400 Command line too long` and the connection is closed. Surplus arguments get
  `501` (`commandMaxArgs`); QUIT ignores them, AUTHINFO and TAKETHIS are not checked there.
- **Article reader.** `readArticleBlock(maxBytes)` counts every byte, keeps the 1024-head-line and
  16384-line caps, drains an oversized article up to its terminator (`ErrArticleTooLarge`, the
  connection stays usable) and gives up past max(4 x limit, 1 MiB), at most 64 MiB
  (`ErrArticleDrainLimit`, the caller closes). The read deadline is refreshed per article line.
- **Error delay.** `ErrorDelay` is slept before every 412, 420, 423, 430, 481 and 482 reply, before
  most 501 replies (not the IHAVE/TAKETHIS argument checks), before 500 for unknown and empty
  commands, before the intake rejections for "too large" and "invalid" (441, IHAVE 437, TAKETHIS
  439 for too large or a bad message-id) and the IHAVE 436 history error, and before the 480/502 of
  IHAVE and TAKETHIS without login or rights. Never before 2xx/3xx, 205, 400, 403, 411, 435, 440,
  483, or the 480 of the read gate and POST.
- **Intake.** POST, IHAVE and TAKETHIS need a processor (else POST `440`, IHAVE/TAKETHIS `500`), a
  successful AUTHINFO (`480`) and posting rights. Articles go through `readArticleBlock` with
  `MaxArticleBytes`: too large gives 441/437/439 with the stream in sync, past the drain bound
  `400` and close. POST is injected (`injectProtoArticle`, RFC 5537 §3.5): Message-ID and Date are
  generated when missing (then also Injection-Date), Injection-Info is always added, the Path gets
  `<hostname>!.POSTED!`; a proto-article with Injection-Info, Xref or `.POSTED` in its Path is refused,
  and so is a Date older than `PostDateMaxAge` (72 h) or more than 24 h in the future.
  IHAVE/TAKETHIS prepend `<hostname>!.SEEN.<ip>!` (`relayPath`) and need a Message-ID header equal
  to the command argument.
- **Connection limit.** Counted at accept: over `MaxConns` the client gets
  `400 Too many connections, try again later` and is closed. With `EnforceUserMaxConns` a user with
  `maxconns > 0` gets at most that many authenticated connections on this server (`maxconns <= 0`:
  unlimited); over the limit the login answers `481 Too many connections for this user`. The count
  is kept in memory per server process.
- **local430.** A negative cache of message-ids that answered 430, so repeated lookups of a missing
  article by many readers or peers do not hit the processor each time: TTL 60 s by default (`-local430ttl`; enforced on every
  check), at most 65536 entries, message-ids over 250 octets are never cached. A just-arrived
  article can therefore still answer 430 for up to 60 s unless intake calls `local430.Remove`
  (articles stored by another process, e.g. the fetcher, are not removed).
- **Xref** (RFC 5536 §3.2.14). `Xref: <host> <group>:<num> <g2>:<n2> ...` with `Config.Hostname`, the
  serving group first. `off`: no Xref line, overview lines have 8 fields, no `Xref:full` in
  LIST OVERVIEW.FMT, HDR/XHDR Xref answer an empty value (the pre-Xref output). `partial` (default):
  the serving group only, no lookups. `full`: `partial` plus the other groups from
  `Processor.XrefLocations` (the history index; one lookup per served article or overview line);
  without a processor or on a lookup error it behaves like `partial` (the first error is logged once
  per server). HEAD/ARTICLE append the line as the last header line and drop any stored `Xref:`
  line; OVER/XOVER carry it as the 9th field (`Xref: ...`, empty when there is no value) and
  LIST OVERVIEW.FMT lists `Xref:full`; HDR/XHDR Xref give the value without the `Xref: ` prefix.
  An article found by message-id outside the current group (number 0) has no serving group:
  `partial` sends no Xref line (empty field/value), `full` lists all known locations.
- **Statistics.** Only known command words are counted by name; every other word is counted
  under `UNKNOWN`, so junk input cannot grow the map.
- **Shutdown and faults.** `Stop` closes the listeners and every open client connection and waits
  up to `ShutdownTimeout`. A panic in one connection is logged with its stack and ends only that
  connection. QUIT, client EOF and closes by `Stop` are not logged as connection errors.
