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
| `nntp-cmd-auth.go` | AUTHINFO USER/PASS |
| `nntp-cmd-group.go` | GROUP, LISTGROUP |
| `nntp-cmd-list.go` | LIST [ACTIVE \| NEWSGROUPS] |
| `nntp-article-common.go` | ARTICLE, HEAD, BODY, STAT (shared lookup by number or message-id) |
| `nntp-cmd-xover.go`, `nntp-cmd-xhdr.go` | XOVER, XHDR |
| `nntp-cmd-posting.go` | POST, IHAVE, TAKETHIS |
| `nntp-cmd-helpers.go` | `rateLimitOnError` and header helpers |

Commands outside this list (DATE, NEXT, LAST, NEWGROUPS, NEWNEWS, OVER, HDR, CHECK, STARTTLS)
answer `500`, MODE STREAM answers `501`. Tests: `nntp-server-*_test.go` (harness in `nntp-server-harness_test.go`),
end-to-end tests in `cmd/nntp-server/`, and `scripts/test-nntp-server.sh` against a built binary.

## Server options

Fields on `NNTPServer` (set them after `NewNNTPServer` and before `Start`/`Serve`) and the
matching `cmd/nntp-server` flags:

| Field / config | Flag | Default | Meaning |
|----------------|------|---------|---------|
| `Config.NNTP.Port` | `-nntptcpport` | 0 | plain TCP port |
| `Config.NNTP.TLSPort`, `TLSCert`, `TLSKey` | `-nntptlsport`, `-nntpcertfile`, `-nntpkeyfile` | 0 | implicit TLS port (TLS 1.2 minimum) |
| `Config.NNTP.MaxConns` | `-maxconnections` | 500 | concurrent client connections, all clients together |
| `Config.NNTP.MaxArtSize` | `-maxartsize` | 32768 (0 = config default) | article byte limit (`MaxArticleBytes`; 1 MiB when <= 0) |
| `Config.Hostname` | `-nntphostname` | database fallback | hostname in the greeting |
| `IdleTimeout` | `-idletimeout` | 3m | idle timeout per command and write timeout per flush |
| `ArticleCmdDelay` | `-articledelay` | 25ms | sleep before ARTICLE/HEAD/BODY/STAT |
| `GroupCmdDelay` | `-groupdelay` | 25ms | sleep before GROUP |
| `ErrorDelay` | `-errordelay` | 1s | sleep before error replies (see below) |
| `PlaintextAuth` | `-plaintextauth` | `accept` | AUTHINFO without TLS: `accept`, `advertise` or `refuse` |
| `ReadRequiresAuth` | `-readauth` | false | reader commands need AUTHINFO first |
| `EnforceUserMaxConns` | `-enforce-user-maxconns` | false | per-user `maxconns` limit |
| `ShutdownTimeout` | - | 10s | how long `Stop` waits for client handlers |

`Start` fails when no port is configured, or when a TLS port is set without a usable certificate and
key; nothing is left listening then. `NewNNTPServer` starts no goroutine; `Start`/`Serve` start the
accept loops and the local430 cleanup, `Stop` ends all of them (the caller's WaitGroup covers them).

## Behaviour

- **Reads without auth.** The server is a public reader by default. With `ReadRequiresAuth`, GROUP,
  LISTGROUP, LIST, STAT, HEAD, BODY, ARTICLE, XOVER, XHDR (and the future NEXT, LAST, OVER, HDR,
  NEWGROUPS, NEWNEWS) answer `480` until a successful AUTHINFO.
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
- **Error delay.** `ErrorDelay` is slept before every 412, 420, 423, 430, 481, 482, 500 and 501
  reply, never before 2xx/3xx, 205, 400, 403, 411, 440, 480, 483 or 502.
- **Connection limit.** Counted at accept: over `MaxConns` the client gets
  `400 Too many connections, try again later` and is closed. With `EnforceUserMaxConns` a user with
  `maxconns > 0` gets at most that many authenticated connections on this server (`maxconns <= 0`:
  unlimited); over the limit the login answers `481 Too many connections for this user`. The count
  is kept in memory per server process.
- **local430.** A negative cache of message-ids that answered 430, so repeated lookups of a missing
  article by many readers or peers do not hit the processor each time: TTL 60 s (enforced on every
  check), at most 65536 entries, message-ids over 250 octets are never cached. A just-arrived
  article can therefore still answer 430 for up to 60 s unless intake calls `local430.Remove`
  (articles stored by another process, e.g. the fetcher, are not removed).
- **Statistics.** Only known command words are counted by name; every other word is counted
  under `UNKNOWN`, so junk input cannot grow the map.
- **Shutdown and faults.** `Stop` closes the listeners and every open client connection and waits
  up to `ShutdownTimeout`. A panic in one connection is logged with its stack and ends only that
  connection. QUIT, client EOF and closes by `Stop` are not logged as connection errors.
