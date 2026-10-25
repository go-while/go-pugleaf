# COMPRESS DEFLATE (RFC 8054) on provider connections

go-pugleaf's NNTP *client* (the fetcher, nntp-transfer, post-queue and the web gateway's
provider connections) can compress a provider connection with `COMPRESS DEFLATE` (RFC 8054).
It is off by default and switched on per provider.

## The provider flag

Admin → Providers → the provider's **Compress (DEFLATE)** checkbox (next to *Posting*, in the
add form and in the edit row); in the database it is `providers.compress` (migration
`0033_main_providers_compress.sql`), in a config file `"compress": true`. A provider row with
the flag set gives every connection of that provider's pool `BackendConfig.Compress`.

When it is on, the client sends `COMPRESS DEFLATE` once per connection, after the greeting and
after `AUTHINFO` (RFC 8054 §2.2.2 wants compression after authentication). On the server's
`206` everything from that CRLF on is deflated in both directions:

```
[NNTP-CONN] provider 'news.example.org': COMPRESS DEFLATE accepted
```

A refusal is not a connection failure: the connection stays plain and keeps working. It is
logged **once per provider**, not once per connection, so a provider without the command costs
one line per fetcher run:

```
[NNTP-CONN] provider 'news.example.org': COMPRESS DEFLATE refused (500 command not recognized), continuing uncompressed
```

A `480` has its own wording and *is* logged every time, because it is a misconfiguration
(the provider wants credentials this connection did not send), not a provider without COMPRESS:

```
[NNTP-CONN] provider 'news.example.org': COMPRESS DEFLATE needs authentication first (480 ...), continuing uncompressed
```

## What the numbers mean

The fetcher prints one summary line per provider at the end of a run, next to the other
`[FETCHER]` provider lines:

```
[FETCHER] provider 'news.example.org': rx 184.2 MiB (wire 42.9 MiB, compressed 76.7%) tx 311.4 KiB
```

`rx` is what the NNTP layer read (the inflated stream), `wire` what crossed the socket, and
`compressed` the share of `rx` that the socket did not carry. Without the flag the line is
just `rx … tx …`: raw and wire are the same stream. The same totals are in `PoolStats`
(`RxBytesRaw`, `RxBytesWire`, `TxBytesRaw`, `TxBytesWire`), summed over the pool's live and
already closed connections.

## Limits and memory

Every hostile-peer bound of the client measures the **inflated** stream: the response line cap
(`MaxResponseLineBytes`), the `MaxReadLines*` limits and the per-response octet budgets
(`MaxArticleResponseBytes`, `MaxListResponseBytes`), and the read deadlines too. A small
compressed bomb is therefore cut by the same caps as an uncompressed one — the compression
ratio buys a peer nothing.

Each compressed connection costs **up to ~1 MiB of heap** on top of a plain one (the pending buffer grows only while the reader falls behind): the inflater
holds up to 1 MiB of decoded octets for the reader (it blocks while that much is unread) plus
the deflate window and buffers of both directions. With `MaxConns: 16` that is ~16 MiB per
provider pool; a provider with many connections and little traffic is not worth the flag.

## Which servers offer it

- **INN's nnrpd 2.6+** offers `COMPRESS DEFLATE` (it flushes with zlib's `Z_PARTIAL_FLUSH`,
  which `compress/flate`'s reader alone cannot follow — hence `internal/nntp/inflate`).
  Several text providers running INN therefore have it, usually only after `AUTHINFO`.
- Anything else answers `500`/`501` and the connection stays plain, which is harmless: tick the
  box, look for the `accepted` or `refused` line once, and leave it as it is.
- **go-pugleaf's own NNTP server does not offer COMPRESS** and advertises no such capability
  (decision 2026-10-25): the flag is for *outgoing* connections to other people's servers only.

The byte totals count from a successful connect on: the greeting, AUTHINFO and COMPRESS
octets of a connection whose connect failed are not aggregated.
