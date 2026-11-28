# Fetcher group routing

`cmd/nntp-fetcher` (built as `pugleaf-fetcher`) can fetch different hierarchies from different
providers. Use it for the public servers that carry their own hierarchies, such as
`news.gmane.io` (`gmane.*`, `gwene.*`) or `news.tin.org` (`tin.*`, `ka.*`, `lists.*`). Your main
provider keeps everything else.

Two settings control it, both stored in the main database (migration 0039):

| setting | where | effect |
|---|---|---|
| provider routing | `providers.routing`, the "Fetcher routing" field of a provider | the provider is used only for the groups its patterns want; empty = no restriction |
| global routes (pins) | table `group_routes`, the "Global routing" card | a group an enabled route wants is fetched only from that route's provider, never from another one |

With no routing and no routes the fetcher works as it did before: every group comes from the
first enabled provider.

## Pattern syntax

The syntax is the same as `peers.in_patterns`, in the style of INN's newsfeeds:

- a list separated by commas, blanks, tabs or new lines, read from left to right;
- `*` matches any run of characters and `?` matches one character, always against the whole
  group name (case-sensitive: `tin.*` matches `tin.misc` but not `tin` itself);
- `!pattern` excludes the groups it matches;
- **the last pattern that matches decides.** A group that no pattern matches is not wanted;
- `@` (the newsfeeds poison prefix) is refused, so use `!`. A list made only of `!` patterns is
  refused because it can never want a group. `\`, `[`, `]`, control characters and a `!` inside
  a pattern are refused too (RFC 3977 wildmat rules).

The web normalizes a saved list to `pattern, pattern, ...` (at most 8192 bytes). The fetcher
refuses to start when an enabled provider or an enabled route has invalid patterns.

Example: `news.tin.org` carries `tin.*`, but an archive import may also hold `tin.it.*`
(Telecom Italia, from archive.org). news.tin.org does not carry those groups. The routing

```
tin.*, !tin.it.*, ka.*, lists.*
```

keeps `tin.it.*` away from news.tin.org: `tin.it.foo` matches `tin.*` and then `!tin.it.*`, and the
last match wins. Those groups stay with the primary.

## Which provider fetches a group

Some terms first:

- the **primary** is the first enabled provider (by priority, lower first) whose routing is empty;
- a **routed** provider is an enabled provider with routing patterns;
- a provider takes part in a run when it is the primary, a routed provider or the provider of an
  enabled route. For a fetch it also needs a host, a port and max connections above 0; an
  enabled provider without them counts as disabled. Any other enabled provider with empty
  routing is not used, as before.

For each selected group G the candidates are, in this order of precedence:

1. **Global routes.** If any enabled route wants G, only the providers of those routes are
   candidates. They are ordered by route priority (lower first), then provider priority. A route
   beats the provider's own routing. If the route's provider is disabled or missing, G is not
   fetched at all: **pins have no fallback.**
2. **Routed providers.** Otherwise the candidates are the routed providers whose patterns want G
   (in priority order), then the primary. **Routed groups fall back to the primary.**
3. Otherwise the primary alone. Without a primary, G has no candidate and is skipped.

At the start of a run the fetcher loads each participant's newsgroup list (`LIST`). The list is
cached for 24 hours in `<data>/cache/<host>.list`. The file name holds only the host, so two
providers on the same host share one file. A provider whose LIST fails or is empty gets no
groups in that run.

Each group goes to **one** provider per run: the first candidate whose LIST has it. A pinned group
that no pinned candidate lists is skipped. An unpinned group that no candidate lists is skipped
too, and is logged as not in the LIST of its last candidate.

**A provider that is down but has a fresh LIST cache.** The assignment only looks at the LIST.
When a routed provider is down but its cached LIST is less than 24 hours old, it still gets its
groups. Its pass then fails them at `GROUP`, and they wait for the next run. There is no failover
by design: the primary does not take them in that run, and no progress moves. In the log you
see the provider's line `[FETCHER-ROUTE]: provider '<name>': N groups`, then its pass
(`[FETCHER-ROUTE]: pass i/n: provider '<name>' ...`) with `[FETCHER]: Error in select ng=...` or
`pool.SelectGroup failed. connection EOF` per group and `downloaded 0 articles` in its
`pass done` line. A provider whose LIST fails (no cache, connection refused) instead gets no
groups: its unpinned groups fall back to the next candidate in the same run. `-routing-dry-run`
shows the assignment the next run will use (it reads the same cached LISTs).

## How one run works

- The fetcher reads providers and routes once, at the start of the run. Edits in the web apply
  to the next run.
- One pass per provider that got groups, in priority order, one after the other. Each pass uses
  that provider's own connection pool and connection limit. The fixed caps stay: eternal-september
  at most 3 connections, blueworldhosting at most 16. A pool is closed when its pass ends. A
  shutdown signal stops the run between passes.
- The fetch progress is stored per provider name and group. When the routing moves a group that
  already has articles to a provider without progress, that provider starts with a download by
  date from the last stored article, not with the whole group (see "Older articles after a
  routing change" below).
- If a provider answers `411` (no such group) for a group assigned to it, the group is counted
  and logged at the end of the pass. It is not tried on another provider in the same run.

The log lines start with `[FETCHER-ROUTE]`. They show the number of groups per provider, the
groups not in a provider's LIST, the pinned groups skipped, the groups without a provider, each
pass, and the 411 count of a pass. A line that names groups lists at most the first 10.

## Older articles after a routing change

A group that already has articles and has no progress on the provider it now goes to (you routed
or pinned it there, or added a new provider) starts on that provider at the date of its newest
local article. The provider's older articles of that group, for example gmane's archive of a
group the primary carried before, are **not fetched automatically**. The fetcher logs one
`[FETCHER-ROUTE]` line per such group (the first 10 of a pass, then a count) with the provider,
the start date and the command to fetch the rest:

```
[FETCHER-ROUTE]: provider 'news.gmane.io' has no progress for group 'gmane.comp.lang.c', which has local articles: starting at the newest local article date 2026-11-20, older articles of the group on this provider are not fetched; to fetch them run: pugleaf-fetcher -download-start-date YYYY-MM-DD -group gmane.comp.lang.c -provider news.gmane.io (YYYY-MM-DD: the oldest date wanted, before 2026-11-20)
```

To fetch the backlog, run the fetcher once with the oldest date you want, the groups and that
provider:

```
./pugleaf-fetcher -download-start-date 2010-01-01 -group 'gmane.comp.*' -provider news.gmane.io
```

This rescans those groups on that provider only, from that date, and moves only that provider's
progress; the other providers' progress stays as it is. `-provider` must be the provider the
routing assigns the groups to (the `ROUTE` lines of `-routing-dry-run` show it). Each run fetches
at most `-max-batch` articles per group; the next normal runs go on from the stored progress.
Articles the database already has are found by message-id in the history index (on by default)
and not stored twice. The admin page shows a
notice with the number of groups this affects when you save a routing field or a global route.

## Flags

| flag | effect |
|---|---|
| `-group 'a.*,b.*;c.test'` | several patterns, separated by blanks, `,` or `;`. Each pattern is an exact name, a name prefix with a final `*` (`alt.*` = every database group starting with `alt.`) or `$all`. `''`, `$all` and `*` select every group. This is not wildmat: `!` and `?` have no meaning here |
| `-provider NAME` | run only this provider's pass: only the groups the routing assigns to it are fetched. The run still loads the LIST of every provider that takes part, because the assignment is global (a group goes to the first candidate whose LIST has it). The provider must take part in the routing (see above), otherwise the fetcher exits with an error. It is also the provider for `-fetch-newsgroups-from-remote` and `-reset-progress`. For `-reset-progress` any provider name works, because the progress is only a stored number |
| `-routing-dry-run` | load the LISTs, print the assignment and exit 0 without fetching articles. It needs no `-nntphostname`. With `-provider`, only that provider's `ROUTE` lines are printed. Together with `-reset-progress` or `-fetch-newsgroups-from-remote` the fetcher refuses to start (exit code 2): the dry run changes nothing, run those on their own |

Dry-run output, one line per selected group, then a summary:

```
ROUTE gmane.discuss -> news.gmane.io
SKIP pin.test pinned: provider pinsrv: its LIST failed
SKIP pin.other pinned: provider pinsrv: not in its LIST
SKIP dead.test pinned: pinned provider disabled (or host, port or max conns not set)
SKIP alt.gone not in the LIST of news.example.net
SKIP x.test no provider
[FETCHER-ROUTE] dry run: 1 groups routed, 5 skipped, no articles fetched
```

A pinned `SKIP` line gives the reason per pinned provider (joined by `; ` when several are
pinned): its LIST failed, or the group is not in its LIST. When every pinned provider is
disabled (or lacks host, port or max connections) it says so.

`-fetch-newsgroups-from-remote` uses the primary or `-provider`. It adds only the groups that the
routing lets that provider serve (the provider is one of the group's candidates). It uses the
same provider rules as a fetch run: a provider without host, port or max connections above 0
counts as disabled, so it is never the list provider and the groups pinned to it are not added.
Example:

```
./pugleaf-fetcher -fetch-newsgroups-from-remote '$all' -fetch-newsgroups-force -provider news.gmane.io
```

With routing `gmane.*, gwene.*` this adds only the `gmane.*` and `gwene.*` groups from that server's
list, as inactive groups. Groups pinned to another provider are left out too.

## Admin pages

All routing settings are on the providers tab (`/admin?tab=providers`):

- **Provider forms.** The create and edit forms have a "Fetcher routing" field. The providers
  table shows the routing, cut to 40 characters.
- **Presets.** The create form has a preset menu with the public servers that newsdeef.eu lists
  under "Hierarchies from other servers". Picking one fills host, name and routing. Port and SSL
  you type yourself (119 without SSL when unsure). Nothing is saved until you press Add Provider.
- **Global routing card.** A table of the routes (patterns, provider, priority, notes) with
  enable/disable, edit and delete, and a form to add a route. The form endpoints are
  `POST /admin/routes`, `/admin/routes/update`, `/admin/routes/delete` and `/admin/routes/toggle`.
- **Deleting providers.** A provider that a route points to cannot be deleted. Delete or move
  the route first.
- **Backlog notice.** After you save a provider whose routing changed (or a new provider with
  routing), or add or edit a global route, the success message says how many existing groups
  that already have articles the patterns match, and that their older articles on that provider
  are not fetched automatically (see "Older articles after a routing change").

The preset list (`internal/web/provider_presets.go`):

| host | routing |
|---|---|
| news.gmane.io | `gmane.*, gwene.*` |
| news.tin.org | `tin.*, !tin.it.*, ka.*, lists.*` (newsdeef lists `tin.*, ka.*, lists.*`; see the example above) |
| nntp.lore.kernel.org | `com.*, dev.linux.*, net.*, org.*, tech.*` |
| news.php.net | `php.*` |
| nntp.perl.org | `perl.*` |
| news.povray.org | `povray.*, moray.*, irtc.*` |
| news.digitalmars.com | `digitalmars.*, D, D.gnu` |
| news.grc.com | `grc.*` |
| lugnet.com | `lugnet.*` |
| fltk.org | `fltk.*` |
| fysh.org | `fysh.*` |
| news.dbase.com | `dbase.*` |
| news.kraft-s.ru | `kraft.*` |
| news.hispagatos.org | `hispagatos.*` |

Only nntp-fetcher reads the routing. The web's own provider connections and `cmd/post-queue`
ignore it.
