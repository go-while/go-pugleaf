#!/usr/bin/env bash
# WorktreeRemove hook: remove a worktree created by worktree-create.sh only if the agent
# left no work in it (no uncommitted changes, no new commits). Otherwise exit 1 so
# Claude Code keeps it on disk; the user cleans up /tank0/claude/trees.
# Input (stdin JSON): {"worktree_path": "...", ...}
set -euo pipefail

input=$(cat)
path=$(jq -r '.worktree_path // empty' <<<"$input")
root=${CLAUDE_TREES_ROOT:-/tank0/claude/trees}

case "$path" in
	"$root"/*) ;;
	*) echo "worktree-remove: refusing $path (not under $root)" >&2; exit 1 ;;
esac
[ -d "$path" ] || exit 0

if [ -n "$(git -C "$path" status --porcelain)" ]; then
	echo "worktree-remove: keeping $path (uncommitted changes)" >&2
	exit 1
fi

gitdir=$(git -C "$path" rev-parse --absolute-git-dir)
base=$(cat "$gitdir/claude-base" 2>/dev/null || true)
head=$(git -C "$path" rev-parse HEAD)
if [ -z "$base" ] || [ "$head" != "$base" ]; then
	echo "worktree-remove: keeping $path (has commits or no recorded base)" >&2
	exit 1
fi

branch=$(git -C "$path" symbolic-ref --quiet --short HEAD || true)
main=$(dirname "$(git -C "$path" rev-parse --path-format=absolute --git-common-dir)")

git -C "$main" worktree remove "$path" >&2
case "$branch" in
	worktree-*) git -C "$main" branch -D "$branch" >&2 ;;
esac
