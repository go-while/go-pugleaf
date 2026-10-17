# Plan: nntp-testkit-land — publish the kit, land the go-pugleaf migration

## Context
Plan nntp-testkit (done, 2026-10-17) built `../nntp-testkit` (main 7af5a9d) and migrated go-pugleaf's
`internal/nntp` tests to it on branch `plan-nntp-testkit` (14352fb) with a `replace => ../nntp-testkit`
directive. The brief forbids merging that branch into testing-001 until the kit is on GitHub and tagged v0.1.0.
Prerequisites that only the user can do: create github.com/go-while/nntp-testkit, push `main`, tag `v0.1.0`.

## Design
- go-pugleaf: on `plan-nntp-testkit`, drop the `replace`, `go get github.com/go-while/nntp-testkit@v0.1.0`,
  `go mod tidy` (go.sum gains the kit's hash), re-run the baseline comparison (207 results), the CLAUDE.md checks
  and `.github/workflows/tests.yml` logic (no go.work needed any more), then merge `--no-ff` into testing-001.
- While at it (optional slice): switch the harness client to strict mode (`Strict: true` in tryDial/hNewClient/
  sessDialRaw); the probe during the migration passed all 207 tests. Any new failure is a real wire bug of
  go-pugleaf's server and gets a `knownBug` marker or a fix, not a loosened client.
- go-pugleaf docs: CLAUDE.md "Checks" note that internal/nntp tests need the kit (module cache); copilot
  instructions mention of the fake helpers.

## Waves
### Wave 0 (inline)
- Confirm the tag resolves: `GOFLAGS=-mod=mod go list -m github.com/go-while/nntp-testkit@v0.1.0`.
- go.mod/go.sum swap on `plan-nntp-testkit`, checks, commit.
### Wave 1
- slice `harness-strict` (optional): internal/nntp/nntp-server-harness_test.go, nntp-server-session_test.go
  (sessDialRaw); checks: full internal/nntp under race, known bugs list unchanged.
- slice `docs`: .claude/CLAUDE.md, .github/copilot-instructions.md.
## Checks
CLAUDE.md list; baseline comparison as in the done plan's Outcome.
## End-to-end
`go test -race -count=1 -v ./internal/nntp/` → 207 PASS; `PUGLEAF_KNOWN_BUGS=1 …` → only T-nntp-article-1.
