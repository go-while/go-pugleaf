# Deploying the go-pugleaf web server

Operational notes for `cmd/web`. Everything here reflects the `web-sqlite-hardening` and
`web-sqlite-leftovers` plans; behaviour that changed is called out so an upgrade is not a surprise.

## Reverse proxy

`cmd/web` speaks plain HTTP and expects a proxy in front of it.

**Trusted proxies.** `ReverseProxyAddr` (admin settings) lists the peers whose forwarded headers are
believed. When it is empty the defaults are used:

```
127.0.0.0/8   ::1   10.0.0.0/8   172.16.0.0/12   192.168.0.0/16   fc00::/7
```

A proxy that is not covered makes every visitor appear to come from the proxy's own address. The
effective list and the header in use are logged once at startup:

```
[WEB]: Trusted reverse proxies: [127.0.0.0/8 ::1/128 10.0.0.0/8 ...] | client IP header: [X-Forwarded-For]
```

**Which header.** Only **one** header is consulted, chosen by the `ReverseProxyIPHeader` setting:
`X-Forwarded-For` (default) or `X-Real-IP`. Trusting both is unsafe — a proxy that sets only one of
them lets a client supply the other, and the spoofed address would reach the BadIPs block and
`last_login_ip`. Set it to whatever your proxy actually writes. **The change takes effect after a
restart.** An unsupported value is logged and falls back to `X-Forwarded-For`:

```
[WEB]: Unknown ReverseProxyIPHeader 'X-Client-Ip', using X-Forwarded-For (valid: X-Forwarded-For, X-Real-IP)
```

nginx, for the default setting:

```nginx
proxy_set_header Host              $http_host;
proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

If you prefer `X-Real-IP`, set `ReverseProxyIPHeader=X-Real-IP` and send
`proxy_set_header X-Real-IP $remote_addr;`. Do not rely on both.

`Host` is passed through as sent (`$http_host`, not `$host`), so canonical URLs and cookies match
the name the visitor typed.

**Untrusted forwarders.** When a loopback or private peer that is *not* in the trusted list sends
`X-Forwarded-For` or `X-Real-IP`, its headers are ignored and logged once per IP:

```
[WEB]: ignoring forwarded client IP headers from untrusted proxy 172.17.0.1: add it to ReverseProxyAddr
```

That is the line to look for when every visitor shares one IP, or when cookies unexpectedly lose
`Secure`. At most 256 addresses are remembered; past that a single suppression line is logged and
further notices stop.

## Sessions

- Tokens are **hashed at rest**: `users.session_id` holds `hex(sha256(token))` and the raw token
  only exists in the cookie, so a database copy does not yield working sessions.
- **Migration 0028 clears every session**, so all users log in once after the upgrade. Expected.
- The idle timeout is **55–60 minutes**. The server-side expiry slides at most once every 5 minutes
  (one write per user per 5 minutes rather than one per page view), and the cookie's `Max-Age`
  follows the server-side expiry, so a browser no longer holds a valid-looking cookie for a session
  the server has already dropped.
- **Lockout**: `MaxLoginAttempts` within `LoginLockoutTime`, tracked in `users.login_attempt_at`.
  Logging out and the 15-minute session cleanup no longer restart a running lockout window.

## CSRF

State-changing requests are rejected when `Sec-Fetch-Site` or `Origin` says the request is
cross-site; same-origin and direct navigations are allowed. A block is logged with `[WEB]`. Browsers
that send neither header are not blocked by this check.

## SQLite

- **Foreign keys are enforced.** Before deploying onto an existing data directory, run
  `PRAGMA foreign_key_check;` on a **copy** of `data/cfg/pugleaf.sq3` and the group DBs; rows that
  violate a constraint will start failing writes rather than being silently accepted.
- Group databases use WAL. **Migration 0009 drops indexes on first open of each group DB**, which is
  a one-time cost proportional to group size — expect a slow first open on large groups after an
  upgrade, not a hang.
- Busy errors are retried; the retry budget measures **time spent busy**, not total elapsed time, so
  a long-running batch that hits contention at the end is still retried.
- The main DB connection pool (`MaxOpenConns 100`, `MaxIdleConns 25`) was measured and left
  unchanged — see [`perf/main-db-pool.md`](perf/main-db-pool.md). The memory ceiling is
  `open connections × CacheSize` (16 MiB per connection), so `CacheSize` is the lever if that
  matters, not the pool size.

## Templates

Templates are parsed once and cached for the process lifetime. Set `PUGLEAF_DEV_TEMPLATES=1` to
re-parse on every request while editing `web/templates/`.

## Data directory

Both `cmd/web` and `cmd/nntp-fetcher` honour `-data`; nothing falls back to a `./data` relative to
the working directory any more. The fetcher's progress database lives at
`<data>/progress.db/progress.db`, provider list caches at `<data>/cache/`, and group databases at
`<data>/db/<md5(group)>/<sanitized-group>.db`.

The fetcher exits with a clear error when no provider is **enabled**, instead of panicking:

```
[FETCHER]: No enabled provider backend available (23 providers, none enabled)
```

## Known limitations

- Group and section pages link pages by number only up to page 100. Older articles exist but are
  not reachable from the pagination links (cursor navigation is designed but not implemented).
- The bad-bots and blocked-IP lists both apply immediately when saved from the admin settings form.
  The `ReverseProxyIPHeader` and `ReverseProxyAddr` settings need a web server restart.

## Auditing old web posts

`build/audit-web-posts` is read-only and reports web posts whose stored headers contain injected
lines (CR/LF smuggled into a display name, subject or reply message-id) — possible for posts
accepted before the header validation landed.

```
./build_audit-web-posts.sh
./build/audit-web-posts -data ./data          # add -all to scan every article of those groups
```

Exit codes: `0` clean, `1` something flagged, `2` error. Output is one TSV line per finding plus a
`checked=/flagged=/missing=` summary. **Remediation is manual** — the tool never writes.

It opens every database with `immutable=1` where it safely can, so it creates nothing next to your
data. If a database has a pending `-wal` (a writer is running, or one exited without checkpointing)
it warns and falls back to a plain read-only open, which *can* let SQLite create `-shm`/`-wal` files.
Use `-strict` to refuse those databases instead, or audit a copy. For a guaranteed-zero-touch run,
stop the writers first.
