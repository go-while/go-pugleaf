# .claude — Claude Code setup for go-pugleaf

| Path | What it is |
|------|------------|
| `CLAUDE.md` | Project instructions loaded in every session and by custom subagents (imports `AGENTS.md`). |
| `AGENTS.md` | Short agent rules (worktree location). |
| `settings.json` | Shared settings: plans directory (`plans/queued`), worktree base (`head`), extra directory `/tank0/claude/trees`, permissions, worktree hooks. Personal overrides go in `settings.local.json` (gitignored). |
| `hooks/worktree-create.sh` | Creates isolated worktrees at `/tank0/claude/trees/<repo>/<name>` on branch `worktree-<name>`, from the calling checkout's HEAD. |
| `hooks/worktree-remove.sh` | Removes a worktree (and its `worktree-*` branch) only if the agent left no uncommitted changes and no commits beyond the recorded base; otherwise it stays on disk. |
| `agents/pugleaf-implementer.md` | Implements one plan slice in its own worktree and commits there (Opus 5, high effort). |
| `agents/pugleaf-reviewer.md` | Read-only review of one branch against its slice (no Edit/Write tools; Opus 5, high effort). |
| `agents/pugleaf-verifier.md` | Runs checks, builds and end-to-end scenarios with scratch data (`-data ./data-test-<slug>`); reports, never fixes code (Opus 5, high effort). |
| `skills/run-plan/` | `/run-plan <plan>`: orchestrates waves of implementers, reviews, merges into `plan-<slug>`, verifies. |
| `plans/queued`, `wip`, `done` | Plan lifecycle. Plan mode writes new plans to `queued/`; `/run-plan` moves a plan to `wip/` when it starts and to `done/` (with an `## Outcome` section) after the merge. |

## Typical flow
1. Start `claude` in the repo, switch to plan mode (Shift+Tab), describe the work. The plan lands in `plans/queued/`.
2. Run `/run-plan .claude/plans/queued/<plan>.md`. Claude moves the plan to `plans/wip/`, creates `plan-<slug>` from your
   branch, proposes waves (written into the plan if it has no `## Waves` section), you confirm, agents run in
   parallel; reviews, merges into `plan-<slug>` and verification follow.
3. Review the summary; on your OK Claude merges `plan-<slug>` into your branch, appends `## Outcome` to the plan and moves
   it to `plans/done/`. Nothing is pushed.

## Notes
- Project hooks need workspace trust: accept the trust prompt the first time you run `claude` here.
- Worktrees that hold commits or uncommitted changes stay in `/tank0/claude/trees/go-pugleaf/` together with their
  `worktree-*` branches; clean them up with `git worktree remove <path>` and `git branch -D <branch>`.
- `/agents`, `/hooks`, `/context` show what loaded.
