#!/usr/bin/env bash
# WorktreeCreate hook: create isolated worktrees under /tank0/claude/trees/<repo>/<name>
# instead of .claude/worktrees/, branched from the HEAD of the calling checkout.
# Input (stdin JSON): {"name": "...", "cwd": "...", ...}
# Output: the worktree path as the last stdout line; everything else goes to stderr.
set -euo pipefail

input=$(cat)
name=$(jq -r '.name // empty' <<<"$input")
cwd=$(jq -r '.cwd // empty' <<<"$input")
root=${CLAUDE_TREES_ROOT:-/tank0/claude/trees}

if [ -z "$name" ] || [ -z "$cwd" ]; then
	echo "worktree-create: missing name or cwd in hook input" >&2
	exit 1
fi
case "$name" in
	*/* | .* ) echo "worktree-create: invalid worktree name: $name" >&2; exit 1 ;;
esac
if [ ! -d "$root" ] || [ ! -w "$root" ]; then
	echo "worktree-create: $root is missing or not writable (fix ownership, or ask the user)" >&2
	exit 1
fi

top=$(git -C "$cwd" rev-parse --show-toplevel)
# Name the repo after the main checkout, also when called from inside another worktree.
common=$(git -C "$top" rev-parse --path-format=absolute --git-common-dir)
repo=$(basename "$(dirname "$common")")

dir="$root/$repo/$name"
branch="worktree-$name"
base=$(git -C "$top" rev-parse HEAD)

mkdir -p "$root/$repo"

if [ -d "$dir" ]; then
	echo "worktree-create: reusing existing $dir" >&2
elif git -C "$top" show-ref --verify --quiet "refs/heads/$branch"; then
	git -C "$top" worktree add "$dir" "$branch" >&2
	git -C "$dir" rev-parse HEAD >"$(git -C "$dir" rev-parse --absolute-git-dir)/claude-base"
else
	git -C "$top" worktree add -b "$branch" "$dir" "$base" >&2
	echo "$base" >"$(git -C "$dir" rev-parse --absolute-git-dir)/claude-base"
fi

echo "$dir"
