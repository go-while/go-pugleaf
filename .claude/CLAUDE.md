# go-pugleaf

@AGENTS.md

NNTP server, article fetcher and Gin web gateway for Usenet, in Go (see `go.mod` for the version).
Storage is SQLite: one main DB (`data/cfg/pugleaf.sq3`) plus one DB per newsgroup (`data/db/`),
and a message-id history index (`internal/history`, rebuilt with `cmd/history-rebuild`).

- Entry points: `cmd/<tool>/main.go` (24 tools; `cmd/web`, `cmd/nntp-fetcher`, `cmd/nntp-server` are the main ones).
- Core code: `internal/` (`nntp`, `database`, `processor`, `history`, `web`, `models`, `config`, `postmgr`, ...).
- Web templates: `web/templates/`. DB migrations: `internal/database/migrations/`.
- Longer layout notes: `.github/copilot-instructions.md` (partly stale: tool/package counts and "no tests" are outdated).
- Known bugs: `BUGS.md`. Feature docs: `docs/`, `THREAD_REBUILD_IMPLEMENTATION.md`.

## Checks ("green" means all of these pass)

```bash
gofmt -l <each touched dir>          # must print nothing
go vet ./...                          # clean baseline, ~2s
go build ./...
go test -race ./internal/database/... ./internal/web/... ./internal/history/... ./internal/nntp/... \
              ./internal/processor/... ./cmd/expire-news/... ./cmd/history-rebuild/...
```

- Add or extend `_test.go` files for new logic when practical; most packages have no tests yet.
- To build a binary use its own script, e.g. `./build_webserver.sh`, `./build_fetcher.sh`,
  `./build_history-rebuild.sh` (output: `build/`, built with `-race`).
- Never run `build_ALL.sh` or `Build_DEV.sh`: they `rm build/*` and generate checksums/update tarballs.

## Data safety (production data lives in this checkout)

- `./data`, `data_ram` (symlink to a ramdisk) and `data_local` are real data. Never run a binary or
  script against them, and never edit, move or delete anything in them. Every tool defaults to
  `-data ./data`, so always pass an explicit scratch root: `-data ./data-test-<slug>` (gitignored via `/data*`).
- Never run `history-rebuild.sh`, `recover-db.sh`, `delete_ALL_users.sh`, `manual_sqlite_migration_*.sh`.
- Release/deploy files are off-limits: `bump.sh`, `createUpdate.sh`, `createChecksums.sh`, `rsync*.sh`,
  `appVersion.txt`, `checksums.sha256*`, `update.tar.gz`, `getUpdate.sh*`.
- Don't start long-running servers (web, nntp-server, fetcher) without being asked; if you do, use
  non-default ports and a scratch data root, and stop them when done.

## Go conventions

- SQLite access goes through the helpers in `internal/database/sqlite_retry.go`
  (`RetryableExec`, `RetryableQuery`, `RetryableQueryRowScan`, `RetryableTransactionExec`, `RetryableStmtExec`, ...).
- Always `defer rows.Close()`; roll back transactions on every error path.
- Check every error (`.golangci.yml` enables errcheck/gosec/sqlclosecheck).
- Log with the existing bracketed prefixes of the component (`[WEB]`, `[FETCHER]`, `[HISTORY]`, `[BATCH]`, ...).
- Goroutines must stop on shutdown; don't add new unbounded goroutines or channels without a close path.
- Keep changes minimal and focused; match the surrounding code style.

## Git

- The working branch is `testing-001`. `main` is far behind: never use it as a base.
- Never push, force-push, rebase shared branches or rewrite history. The user pushes.
- Commit only when asked or when your agent role says so. End commit messages with the
  `Co-Authored-By:` trailer from the session's attribution instructions.

## Parallel work with subagents

- Plans: plan mode writes to `.claude/plans/queued/`; work moves them to `wip/`, then `done/`.
- Run a plan with the `run-plan` skill (`/run-plan <plan file>`). It splits the plan into waves and uses:
  - `pugleaf-implementer`: one slice, own worktree under `/tank0/claude/trees/go-pugleaf/`, commits on its branch.
  - `pugleaf-reviewer`: read-only review of one branch diff against its slice.
  - `pugleaf-verifier`: full checks, builds and end-to-end runs on a checkout, with scratch data only.
- Worktrees are created by `.claude/hooks/worktree-create.sh` from the calling checkout's HEAD.
  Worktrees that hold work are never deleted automatically; the user cleans up `/tank0/claude/trees`.
