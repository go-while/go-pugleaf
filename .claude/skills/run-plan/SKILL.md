---
name: run-plan
description: Execute a go-pugleaf plan from .claude/plans (queued → wip → done) with parallel pugleaf-implementer waves in worktrees under /tank0/claude/trees, then review, merge and verify.
when_to_use: Only when the user asks to run, execute or implement a plan file (e.g. "/run-plan <file>", "work on the queued plan"). Not for small single-file changes.
argument-hint: "[plan file in .claude/plans/queued or wip]"
---
# Run a plan with parallel subagents

Plan argument: $ARGUMENTS

You are the orchestrator in the main session. Subagents can't spawn subagents, so you do the
splitting, spawning, merging and bookkeeping. Keep the user informed at each wave boundary.

## 1. Pick the plan
- If no argument: list `.claude/plans/queued/` and `.claude/plans/wip/` and ask which one.
- Move it to `.claude/plans/wip/` (`git mv` if tracked, else `mv`). Resuming a plan already in `wip/`
  is fine: read its Outcome/progress notes first.

## 2. Preflight
- `git status --short`: tracked uncommitted changes in the main checkout → stop and ask the user.
  Untracked files are fine.
- Record the starting branch (normally `testing-001`).
- Create the integration branch from it: `git switch -c plan-<slug>` (or switch to it when resuming).
  The WorktreeCreate hook branches every worktree from the main checkout's HEAD, so later waves
  automatically include merged earlier waves.
- Confirm `/tank0/claude/trees` is writable.

## 3. Waves
If the plan has no `## Waves` section, write one into the plan file first:
- Each slice: a name (short slug), goal, **owned files** (disjoint across slices in the same wave),
  and the checks/tests it must pass.
- Shared hotspots (`go.mod`, shared structs in `internal/models`, migrations, config keys) get a single
  owner, or you do them inline as wave 0 and commit on `plan-<slug>` before wave 1.
- Put slices that depend on another slice's API in a later wave.
- Show the waves to the user and wait for OK before spawning.

## 4. Spawn a wave
Spawn one `pugleaf-implementer` per slice, **all in a single message** so they run in parallel.
Task message template:

```
Plan: .claude/plans/wip/<plan>.md  — your slice: "<slice name>" (section: <heading>)
Base commit: <sha of plan-<slug> HEAD>
Owned files (edit only these):
- path/one.go
- path/two.go (new)
Goal: <1-3 sentences, copied/condensed from the slice>
Constraints: <interfaces other slices rely on, names to keep, things not to touch>
Checks: gofmt -l <dirs>; go vet ./...; go build ./...; go test -race <pkgs>
Report: worktree path, branch, commit SHAs, files changed, check results, open issues.
```

## 5. After a wave reports
1. Read each report. Branch names are `worktree-<name>`; `git log --oneline <base>..<branch>`.
2. Spawn `pugleaf-reviewer` for each branch in one message (task: branch, base, plan path, slice,
   owned files).
3. Blockers/majors: send them to the same implementer with SendMessage (its context is intact) and
   wait for the fix commit. Minor findings: fix, defer to leftovers, or drop, and say which.
4. Merge each branch into `plan-<slug>`: `git merge --no-ff worktree-<name>`. Resolve conflicts
   yourself; if a conflict reveals a design mismatch, stop and ask.
5. Run the CLAUDE.md checks on the merged tree. Fix integration breakage inline (commit on `plan-<slug>`)
   before starting the next wave.
6. Append a short progress note per wave to the plan file (branches, merge commits, deferred items).

## 6. Verify
Spawn `pugleaf-verifier` with: checkout path = main checkout (on `plan-<slug>`), plan path, binaries to
build, and the plan's end-to-end scenario with an explicit `-data ./data-test-<slug>` root.
Failures → back to step 5 (fix inline or via a new implementer slice), then verify again.

## 7. Finish
- Summarize for the user: what was built, commits, check and verification results, leftovers.
- Only after the user OKs it: `git switch <starting branch>` and `git merge --no-ff plan-<slug>`.
  Never push.
- Append `## Outcome` to the plan (integration branch, merge commit, worktree branches and paths,
  verification summary, leftovers/known issues), then move it to `.claude/plans/done/`.
- Leave the worktrees in `/tank0/claude/trees` and the `worktree-*` branches alone; the user cleans
  them up. Just list them in the Outcome.

## Plan structure template
Plans that run smoothly through this skill look like this:

```markdown
# Plan: <title>
## Context          — why, what exists, decisions made
## Design           — data flow, APIs, schema/config changes
## Waves
### Wave 0 (inline) — shared prerequisites, owned files
### Wave 1
- slice `<slug>`: goal; owned files; checks
### Wave 2 ...
## Checks           — commands that must pass on the merged tree
## End-to-end       — exact commands with -data ./data-test-<slug>, expected results
```
