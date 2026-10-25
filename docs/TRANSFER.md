# nntp-transfer: sending modes

`cmd/nntp-transfer` feeds articles from the local databases to a peer. It has two sending
modes: **streaming** (CHECK/TAKETHIS, RFC 4644) and **IHAVE** (RFC 3977 §6.3.2), the fallback
for peers without streaming. The mode is decided once per run, logged once, and named in the
end-of-run summary (`Summary: mode=streaming|ihave | total: ... `).

## Streaming (default)

The pool gets its connections with `MODE STREAM` (`nntp.MODE_STREAM_MV`). Per batch the tool
pipelines `CHECK` for every message-id (unless `-check-first=false`) and then `TAKETHIS` for the
wanted ones; the responses are routed back by the demuxer.

| reply | command | what the run does |
| --- | --- | --- |
| 238 | CHECK | wanted: the article is sent with TAKETHIS |
| 431 | CHECK | try again later: counted as `retry`, not sent in this run |
| 438 | CHECK | not wanted: counted as `unwanted`, cached in Redis |
| 239 | TAKETHIS | transferred: counted as `transferred`, cached in Redis |
| 439 | TAKETHIS | rejected: counted as `rejected`, cached in Redis (never offered again) |

## IHAVE

The pool gets its connections without any mode switch (`nntp.MODE_NONE_MV`): real innd hands a
`MODE READER` connection to nnrpd, which has no IHAVE, so a reader-mode connection is refused up
front (`nntp.ErrIHaveUnsupported`) instead of being used. Each article is offered synchronously —
`IHAVE <message-id>`, and on a 335 the same wire form TAKETHIS sends (one shared
`transferHeaders` + `writeArticleLines`, pinned by `TestCliIHaveWireEqualsTakeThis`). There is no
pipelining and no CHECK: IHAVE asks the peer itself.

| reply | when | what the run does |
| --- | --- | --- |
| 335 | before the article | the article is written and the peer's verdict is read |
| 235 | after the article | transferred: counted as `transferred`, cached in Redis |
| 435 | before or after the article | not wanted: counted as `unwanted`, cached in Redis. RFC 3977 puts it before the article only; a peer that answers it afterwards is accepted too, otherwise the article would be offered forever |
| 436 | before or after the article | try again later (the peer is paused): retried once at the end of the job, a second 436 is counted as `retry` and left for a later run |
| 437 | before or after the article | rejected: counted as `rejected`, cached in Redis, never offered again |
| 480 | any | authentication required: the worker requeues its job and exits, the run stops (exit code 1) |
| 500, 501 | any | the peer has no IHAVE: `nntp.ErrIHaveUnsupported`, the run stops (exit code 1) |
| 502 | any | transfer permission denied: `nntp.ErrIHaveUnsupported`, the run stops (exit code 1) |

Anything else, a read error or an EOF force-closes the connection (the article may be half-sent
and the peer's state is unknown); the job is requeued and `BootConnWorkers` boots a new worker.
A multi-line reply is malformed for IHAVE and is treated the same way: both replies are read with
`ReadCodeLine(0)`, so an unread continuation line can never desync the connection.

## -ihave auto|never|always

| value | behaviour |
| --- | --- |
| `auto` (default) | stream; the first `MODE STREAM` refusal (500/501, `nntp.ErrStreamingUnsupported`) switches the whole run to IHAVE. The fallback is logged once: `[TRANSFER] peer <host> has no streaming (MODE STREAM: ...): sending with IHAVE` |
| `always` | IHAVE from the start, even against a peer that offers streaming (for peers whose TAKETHIS is broken) |
| `never` | streaming only — the behaviour before the IHAVE fallback existed (see below) |

`-ihave never` against a peer without streaming is a loop by design: every `Pool.Get` fails with
the peer's 501, `BootConnWorkers` logs `failed to get connection from pool ... retry in: <d>` and
retries with a backoff of up to a minute **for as long as the tool runs**. Nothing is ever sent;
the run has to be stopped by hand. It is kept for peers that must never be fed with IHAVE.

## Flags that behave differently in IHAVE mode

- `-check-first` is ignored: there is no CHECK, IHAVE itself asks the peer.
- `-batch-check` still sizes the batches: each IHAVE job loads that many articles from the group
  database at once (they are then offered one by one).
- the Redis flags (`-redis-cache`, `-redis-ttl`, `-redis-clear-cache`) work in both modes, with the
  same keys (`pugleaf:tx:<host>:<port>:<message-id>`) and the same TTL.
- `-max-threads` is the number of connections and workers in both modes.

## Exit codes

- `0`: the run finished (articles may still be left over: 431/436 are retried in a later run).
- `1`: a peer that offers neither streaming nor IHAVE (or refuses it: 480/500/501/502) — a
  configuration error, logged as `peer has neither streaming nor IHAVE (or refuses it): stopping`;
  the running jobs drain first. The usual startup and database failures also exit 1.

## Example

```bash
./build/nntp-transfer -data ./data-test-ihave -host 127.0.0.1 -port 1119 \
    -ssl=false -ihave auto -redis-cache=false -max-threads 1 -group alt.test
```
