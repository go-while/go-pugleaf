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

## Progress
### Wave 0 (inline, plan-nntp-testkit-land)
- 63a383b merge of plan-nntp-testkit; 71417ad go.mod `require github.com/go-while/nntp-testkit v0.1.0` (tag pushed
  2026-10-17, resolves via proxy), replace dropped, go.sum has the kit hashes; bc01d3a docs. Baseline comparison on
  71417ad: 207 PASS = baseline minus the three moved TestCliFake*; known bugs only T-nntp-article-1; nntp cmds ok.
### Wave 1
- `harness-strict` (worktree /tank0/claude/trees/go-pugleaf/agent-a09482663b07e7637, worktree-agent-a09482663b07e7637,
  383a8c4): Strict true in tryDial, hNewClient, sessDialRaw; 207 results unchanged, 0 strict failures; known bugs
  unchanged; PUGLEAF_TEST_DEADLINE=5s run ok. Merged without a separate review (4-line change, fully re-run).

## Outcome
- Integration branch `plan-nntp-testkit-land`, merged into `testing-001` as **2ba422f** on 2026-10-17 (unpushed).
  Includes the migration branch `plan-nntp-testkit` (14352fb).
- Worktrees (left for the user): /tank0/claude/trees/go-pugleaf/agent-a09482663b07e7637 (worktree-agent-a09482663b07e7637,
  383a8c4, harness-strict); the migration worktree agent-af8031e986478fd0b from plan nntp-testkit.
- Verification: full CLAUDE.md checks on 5d07bb9 green (gofmt empty, vet, build, 31 packages under race, known bugs
  only T-nntp-article-1); internal/nntp 207 results = baseline minus the three TestCliFake* now in the kit; strict
  harness client: 0 failures.
- Leftovers: none. The kit's conformance package is plan nntp-testkit-conformance (queued).
