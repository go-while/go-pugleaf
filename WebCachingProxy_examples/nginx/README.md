# nginx in front of the go-pugleaf webserver

TLS, rate limits and a page cache for anonymous visitors. This is the setup that runs on
arc1-ca.pugleaf.net (2026-11-11). Tested with nginx 1.26.

| File | Goes to | What it does |
|---|---|---|
| `conf.d/pugleaf-ratelimit.conf` | `/etc/nginx/conf.d/` | rate-limit zones: 5 req/s per IP, 1 req/s per IP for crawler User-Agents, connection counting |
| `conf.d/pugleaf-cache.conf` | `/etc/nginx/conf.d/` | cache zone (`/var/cache/nginx/pugleaf`, 5 GB) and the bad-bot map that keeps blocked bots out of the cache |
| `snippets/pugleaf-proxy.conf` | `/etc/nginx/snippets/` | proxy headers shared by every location |
| `sites-available/pugleaf.example.org` | `/etc/nginx/sites-available/` | the site: HTTP→HTTPS, certbot webroot, limits, cache |
| `intern/pugleaf.nginx.template`, `intern/nginx.template.sh` | | the same site with placeholders, for proxies that serve several pugleaf hosts |

## Install

```sh
cp conf.d/pugleaf-*.conf /etc/nginx/conf.d/
cp snippets/pugleaf-proxy.conf /etc/nginx/snippets/
sed 's/news.example.org/your.host.name/g' sites-available/pugleaf.example.org > /etc/nginx/sites-available/pugleaf
ln -s ../sites-available/pugleaf /etc/nginx/sites-enabled/pugleaf
mkdir -p /var/cache/nginx/pugleaf && chown www-data: /var/cache/nginx/pugleaf
nginx -t && systemctl reload nginx
```

In the go-pugleaf admin settings set **Reverse Proxy Addresses** to the proxy's address
(`127.0.0.1, ::1` when nginx runs on the same host); only then does the webserver take the
client IP from `X-Forwarded-For` (rate limits, logs, IP blocks).

## What gets cached

- Only anonymous `GET`/`HEAD`. A request with the `session_id` cookie (logged in) or an
  `Authorization` header always goes to go-pugleaf.
- Pages 5 minutes (200, 301), 404 one minute, `/static/` one hour.
- Never: 403 and 429 (they depend on the client, e.g. the bad-bot block), responses with
  `Set-Cookie` and responses go-pugleaf marks `Cache-Control: private` (thread trees).
- User-Agents on go-pugleaf's bad-bot list bypass the cache, so they still get go-pugleaf's
  403. **Keep the regex in `conf.d/pugleaf-cache.conf` in step with the admin setting
  "Bad Bots"** of the server behind the proxy.
- `X-Cache-Status` (`MISS`, `HIT`, `BYPASS`, `STALE`, `UPDATING`) shows what happened.
- When go-pugleaf is down or slow, nginx serves the stale copy (`proxy_cache_use_stale`).

Empty the cache: `rm -rf /var/cache/nginx/pugleaf/* && systemctl reload nginx`.

## Rate limits

- Everyone: 5 requests/s per IP, bursts of 30 served at once; above that `429`.
- Crawlers (User-Agent contains bot, crawl, spider, …): 1 request/s per IP, 5 queued.
- At most 20 simultaneous connections per IP. `/static/` has no request limit.
- The zones are per nginx instance: when one proxy serves several pugleaf hosts, a client's
  requests to all of them count together.

Check it: `curl -s -o /dev/null -w '%{http_code}\n' -A 'TestBot/1.0' https://your.host.name/ping`
six times in a row gives `200` and then `429`.

## Changes from the old sample (2025)

The old `nginx.conf`, `filter.inc` and `novabbs.pugleaf.net` are gone: the cache key held the
session cookie (one cache copy per logged-in user), 403 was cached for an hour (a blocked bot's
403 was then served to everyone on that URL), `filter.inc` refused Firefox/Chrome 7–109 and
common words in User-Agents, and `location = ^/admin$` never matched (`=` takes no regex).
Bot blocking is go-pugleaf's job (admin setting "Bad Bots"); nginx only rate-limits.
