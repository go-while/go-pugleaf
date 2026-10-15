# Issues (ideas and follow-ups not tied to a plan)

- **CI on push/PR (`.github/workflows/tests.yml`)** — recorded 2026-10-15 during `audit-delta-fixes` (plan Q2, OPS-4g).
  Today the only workflow, `release.yml`, runs on a `v*` tag or a manual dispatch; pushes to `testing-001` and pull
  requests get no check from GitHub. Proposed: `tests.yml` on `push` (branches `testing-001`, `main`) and
  `pull_request` with the same `test` job the release workflow gets from D12 (`go vet ./...`, `go test -race` over the
  CLAUDE.md package list, `go-version-file: go.mod`) plus a `known-bugs` job (`continue-on-error: true`) running
  `PUGLEAF_KNOWN_BUGS=1 go test -count=1 ./internal/nntp/... ./cmd/nntp-server/...`. Known costs: the race run takes
  ~7 min locally (10–15 min on a free runner); a few NNTP tests are load sensitive (`AUTHINFO PASS` i/o timeouts seen
  when five suites ran at once), so expect an occasional red check until those timeouts are looked at.

- **Post queue: attempt counter for upstream 441 (`post_queue` table)** — recorded 2026-10-15 during the optional
  wave O1 of `audit-delta-fixes` (A-intake-10). A web post that the upstream server rejects with 441 is retried every
  10 s forever; the queue has no attempt column, so nothing ever parks it. Proposed: migration `0030_main_post_queue_attempts.sql`
  adding `attempts INTEGER NOT NULL DEFAULT 0` (and `last_error TEXT`), `ProcessPendingPosts` increments it on each
  441 and stops retrying after N (config key, default 5), the admin post-queue page shows the count and keeps the
  manual retry. Left out of slice `adf-opt-postqueue` because a migration has no owner inside that wave.
