# Security Policy

## Supported Versions

go-pugleaf is developed on the `testing-001` branch of
[go-while/go-pugleaf](https://github.com/go-while/go-pugleaf). Releases are
merged into `main` and tagged (`v0.6.19` is the latest release; the notes for
each release are in `docs/RELEASE-v<version>.md`). Security fixes go into
`testing-001` and the next release.

| Version | Security support |
| ------- | ---------------- |
| The latest release (`main`) and `testing-001` | :white_check_mark: |
| Older releases | :x: (upgrade to the latest first) |
| Forks of go-pugleaf | :x: (report to the fork) |

If you run an older release, please check whether the problem is still
present in the latest release or in `testing-001` before reporting it; it may
already have been fixed.

## Reporting a Vulnerability

Please do **not** report security vulnerabilities through GitHub issues, pull
requests, discussions, Usenet newsgroups or mailing lists: they are all
**public**.

Report them **privately** through
[GitHub Security Advisories](https://github.com/go-while/go-pugleaf/security/advisories/new)
for this repository. This opens a private report that only you and the
maintainers (and the collaborators they add) can see until it is resolved and
disclosed.

Please include as much of the following as you can:

- what the vulnerability is and its impact (for instance an authentication or
  access bypass, a forged or unchecked control message, SQL injection, a path
  traversal in the data directory, article loss or database corruption, a
  leaked password, session or token, cross-site scripting or request forgery
  in the web interface, denial of service through memory, CPU, disk or
  connections);
- the affected program(s) (`pugleaf-nntp-server`, the web server, the
  fetcher, `nntp-transfer`, `post-queue`, an importer such as `mbox-importer`
  or `rslight-importer`, `history-rebuild`, `usermgr`, another tool in
  `cmd/`, ...) and the release or commit you tested, ideally the commit that
  introduced the problem;
- the configuration it needs (command line flags, settings in the main
  database `data/cfg/pugleaf.sq3`, the peers and providers tables, the web
  admin settings, ...);
- step-by-step instructions to reproduce it, or a minimal proof of concept;
- a patch, if you have one;
- your assessment of the severity and of what an attacker needs, for instance:
  - any host that can connect to the NNTP or web port, before any login;
  - a logged-in NNTP reader or poster, or a registered web user;
  - a web administrator;
  - an enabled peer (a trusted feed in the peers table);
  - an upstream server the fetcher or `nntp-transfer` connects to (a
    provider);
  - an article that reaches the server through any feed, fetch or import,
    such as a control message or crafted headers, which can come from
    anywhere on Usenet;
  - local access to the user that runs go-pugleaf or to its data directory.

Please do not disclose the issue publicly before a fix is available. If you
have several unrelated issues, please submit one report for each.

### Reporting from a script

Private vulnerability reporting is enabled for this repository, so a report
can also be submitted through the GitHub API instead of the web form. Anyone
with a GitHub account can submit a private vulnerability report; repository
collaborator access is not required. Reports go to the maintainers for triage,
just like submissions through the web form. `gh` must be logged in:

```sh
gh api -X POST repos/go-while/go-pugleaf/security-advisories/reports \
    -f summary='nntp-server: one-line title' \
    -f description="$(cat report.md)"
```

## What to Expect

- The maintainers will acknowledge your report within a few days.
- We will investigate, keep you informed, and work on a fix in the private
  advisory (with a private fork where needed).
- Once a fix is ready, we will agree with you on a date to merge it and
  publish the advisory, and request a CVE where appropriate.
- You will be credited in the advisory and the release notes, unless you
  prefer to remain anonymous.

## Scope

This policy covers the source code in this repository: the programs in
`cmd/`, the packages in `internal/`, the web templates and static files in
`web/`, the database migrations, and the build and helper scripts.

Problems in third-party software that go-pugleaf uses (the Go toolchain and
standard library, SQLite and `mattn/go-sqlite3`, the `golang.org/x` modules,
Redis, Tor, the operating system) should be reported to those projects. If
go-pugleaf uses such software in an unsafe way, that is in scope here.
