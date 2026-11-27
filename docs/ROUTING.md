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

## How one run works

- The fetcher reads providers and routes once, at the start of the run. Edits in the web apply
  to the next run.
- One pass per provider that got groups, in priority order, one after the other. Each pass uses
  that provider's own connection pool and connection limit. The fixed caps stay: eternal-september
  at most 3 connections, blueworldhosting at most 16. A pool is closed when its pass ends. A
  shutdown signal stops the run between passes.
- The fetch progress is stored per provider name and group. When the routing moves a group that
  already has articles to a provider without progress, that provider starts with a download by
  date from the last stored article, not with the whole group.
- If a provider answers `411` (no such group) for a group assigned to it, the group is counted
  and logged at the end of the pass. It is not tried on another provider in the same run.

The log lines start with `[FETCHER-ROUTE]`. They show the number of groups per provider, the
groups not in a provider's LIST, the pinned groups skipped, the groups without a provider, each
pass, and the 411 count of a pass. A line that names groups lists at most the first 10.

## Flags

| flag | effect |
|---|---|
| `-group 'a.*,b.*;c.test'` | several patterns, separated by blanks, `,` or `;`. Each pattern is an exact name, a name prefix with a final `*` (`alt.*` = every database group starting with `alt.`) or `$all`. `''`, `$all` and `*` select every group. This is not wildmat: `!` and `?` have no meaning here |
| `-provider NAME` | run only this provider's pass. The provider must take part in the routing (see above), otherwise the fetcher exits with an error. It is also the provider for `-fetch-newsgroups-from-remote` and `-reset-progress`. For `-reset-progress` any provider name works, because the progress is only a stored number |
| `-routing-dry-run` | load the LISTs, print the assignment and exit 0 without fetching articles. It needs no `-nntphostname`. With `-provider`, only that provider's `ROUTE` lines are printed |

Dry-run output, one line per selected group, then a summary:

```
ROUTE gmane.discuss -> news.gmane.io
SKIP pin.test pinned: no pinned provider lists it
SKIP alt.gone not in the LIST of news.example.net
SKIP x.test no provider
[FETCHER-ROUTE] dry run: 1 groups routed, 3 skipped, no articles fetched
```

`-fetch-newsgroups-from-remote` uses the primary or `-provider`. It adds only the groups that the
routing lets that provider serve (the provider is one of the group's candidates). Example:

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
