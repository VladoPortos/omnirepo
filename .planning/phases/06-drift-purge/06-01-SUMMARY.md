---
phase: 06-drift-purge
plan: 01
subsystem: database
tags: [sqlite, migration, schema, drift_purge, sync_jobs]

# Dependency graph
requires:
  - phase: 02-maintainer-viewer-rbac-split
    provides: migration 034 — most-recent triple template (m034_test.go shape)
  - phase: 08-mirror-and-progress (v1.4 / migration 024)
    provides: ADD COLUMN bool precedent on repos (is_mirror, scan_on_sync)
provides:
  - "repos.drift_purge INTEGER NOT NULL DEFAULT 0 — per-repo opt-in flag"
  - "sync_jobs.summary TEXT NOT NULL DEFAULT '{}' — top-level JSON object for drift_purged + future keys"
  - "Migration triple 035 (.up.sql + .down.sql + m035_test.go) auto-picked up by //go:embed *.sql"
affects:
  - "06-02 (repos.DriftPurge field, repos.go scanRepoRow + UpdateFields, handlePatchRepo mirror-only validation)"
  - "06-06 (per-protocol DriftAdapter implementations + sync_jobs SetSummaryDriftPurged writer)"
  - "06-07 (driftpurge.Run wired into 4 sync_handlers — reads drift_purge flag, writes summary JSON)"

# Tech tracking
tech-stack:
  added: []  # Zero new go.mod / npm dependencies
  patterns:
    - "ADD COLUMN INTEGER bool with DEFAULT 0 (cloned from migration 024 scan_on_sync)"
    - "ADD COLUMN TEXT JSON with DEFAULT '{}' (forwards-compat blob for future summary keys)"
    - "Migration triple naming: NNN_<snake_case_subject>.{up,down}.sql + mNNN_test.go (cloned from 034)"

key-files:
  created:
    - internal/metadata/migrations/035_repos_drift_purge_and_sync_summary.up.sql
    - internal/metadata/migrations/035_repos_drift_purge_and_sync_summary.down.sql
    - internal/metadata/migrations/m035_test.go
  modified: []

key-decisions:
  - "drift_purge is plain INTEGER NOT NULL DEFAULT 0 — no cross-column constraint to is_mirror (SQLite ADD COLUMN limitation; mirror-only invariant enforced at API layer in plan 06-02)"
  - "sync_jobs.summary is top-level JSON-object TEXT column with DEFAULT '{}' — D-21 wire-shape; doubles as v1.6 extension point"
  - "Down file is documentation-only (runner is up-only per runner.go)"

patterns-established:
  - "PRAGMA + DEFAULT round-trip test: scan PRAGMA table_info rows for the new column, assert type/notnull/dflt_value, then INSERT-without-explicit-value and SELECT to confirm DEFAULT applied"
  - "JSON DEFAULT dflt_value parsing: SQLite reports the literal stored form (`'{}'` with single quotes); test trims surrounding quotes via strings.Trim before comparing to `{}`"

requirements-completed:
  - DRIFTPURGE-04
  - DRIFTPURGE-03

# Metrics
duration: ~7min
completed: 2026-04-25
---

# Phase 6 Plan 1: Migration 035 Summary

**Migration 035 ships repos.drift_purge INTEGER NOT NULL DEFAULT 0 + sync_jobs.summary TEXT NOT NULL DEFAULT '{}' with PRAGMA + DEFAULT round-trip tests; zero new go.mod deps, zero Go source changes outside the test file.**

## Performance

- **Duration:** ~7 min
- **Started:** 2026-04-25T00:43Z
- **Completed:** 2026-04-25T00:50Z
- **Tasks:** 2
- **Files created:** 3
- **Files modified:** 0

## Accomplishments

- Migration 035 .up.sql ships two ADD COLUMN statements verbatim per plan: `repos.drift_purge INTEGER NOT NULL DEFAULT 0` and `sync_jobs.summary TEXT NOT NULL DEFAULT '{}'`.
- Migration 035 .down.sql is a documentation-only symmetry file (runner is up-only; modernc/sqlite supports DROP COLUMN but OmniRepo never rolls back in production).
- m035_test.go ships two `t.Parallel` test functions (`TestMigration035_AddsReposDriftPurge` + `TestMigration035_AddsSyncJobsSummary`) using the existing `openFreshDB` + `applyReal` helpers from runner_test.go.
- Both tests assert PRAGMA `table_info` reports the new column with the expected type / `notnull=1` / DEFAULT value, then INSERT a parent-FK-satisfying row WITHOUT specifying the new column and SELECT to confirm the DEFAULT was applied.
- Embed FS auto-picks up the new .up.sql + .down.sql via `//go:embed *.sql` glob in `embed.go` — zero edit to runner.go or embed.go required.
- Plan 06-02 can now scan a `DriftPurge bool` field from any repos SELECT that includes drift_purge in the column list. Plan 06-06 can write to `sync_jobs.summary` via `json_set()` or by replacing the string verbatim.

## Task Commits

Each task was committed atomically with `--no-verify` (parallel worktree mode):

1. **Task 1: Migration 035 .up.sql + .down.sql files** — `8ba4651` (feat)
2. **Task 2: m035_test.go PRAGMA + DEFAULT round-trip** — `8db489f` (test)

_Note: Strict TDD RED-then-GREEN ordering wasn't observed because Task 1 lands the schema and Task 2's tests assert against the schema. Inverting the order would have produced a build failure (missing column) rather than a clean test failure. Task 2 was authored after Task 1 such that the test was GREEN on first run. The TDD spirit ("test asserts the contract; without the SQL the test fails") is preserved — removing Task 1 today and re-running Task 2 would surface a `column "drift_purge" not found` error._

## Files Created/Modified

### Created

- `internal/metadata/migrations/035_repos_drift_purge_and_sync_summary.up.sql` — 20 lines. Two `ALTER TABLE ... ADD COLUMN` statements with multi-line comment blocks documenting D-17 + D-21 + the mirror-only invariant placement decision.
- `internal/metadata/migrations/035_repos_drift_purge_and_sync_summary.down.sql` — 12 lines. Documentation-only; conceptual rollback `DROP COLUMN` lines as comments.
- `internal/metadata/migrations/m035_test.go` — 145 lines. Two `t.Parallel` tests; uses `openFreshDB` + `applyReal` from runner_test.go. PRAGMA `table_info` scan + INSERT + SELECT round-trip per test.

### Modified

None — plan invariant respected (zero Go source changes outside the test file; zero edits to embed.go / runner.go).

## Exact .up.sql bytes shipped

```
-- 035_repos_drift_purge_and_sync_summary.up.sql
-- v1.5 Phase 6 — Drift purge (DRIFTPURGE-04, D-17 + D-21).
--
-- Step 1: repos.drift_purge.
-- Per-repo opt-in flag for drift purge (default off on upgrade —
-- D-17 preserves v1.4 additive-only behaviour). Mirror-only
-- invariant (reject drift_purge=true on non-mirror repos) is
-- enforced at the API layer (handlePatchRepo, plan 06-02) — not
-- via a cross-column constraint, because SQLite ADD COLUMN does
-- not support a column rule that references other columns.
ALTER TABLE repos ADD COLUMN drift_purge INTEGER NOT NULL DEFAULT 0;

-- Step 2: sync_jobs.summary.
-- Per-sync JSON summary blob used by driftpurge.engine to stamp
-- a `drift_purged` integer key per D-21. Kept as TEXT (JSON) so
-- future summary keys (files_added, bytes_downloaded, etc.) can
-- land without another migration.
-- Default '{}' so any code path reading `summary` before a sync
-- ran observes a valid empty object, not NULL.
ALTER TABLE sync_jobs ADD COLUMN summary TEXT NOT NULL DEFAULT '{}';
```

## PRAGMA output observed in tests

After applyReal, both new columns appear in their respective `PRAGMA table_info(...)` output with the expected attributes:

- `PRAGMA table_info(repos)` → row for `drift_purge` with `type='INTEGER'`, `notnull=1`, `dflt_value="0"` (string-typed scan target).
- `PRAGMA table_info(sync_jobs)` → row for `summary` with `type='TEXT'`, `notnull=1`, `dflt_value=` literal `'{}'` (with single quotes — SQLite reports stored DEFAULTs verbatim for string types). Test trims surrounding single quotes via `strings.Trim(s, "'")` before comparing to `{}`.

INSERT-without-explicit-column round-trip confirmed:

- `INSERT INTO repos(project_id, type, name) VALUES (1, 'pypi', 'test')` → `SELECT drift_purge FROM repos WHERE ...` returns `0`.
- `INSERT INTO sync_jobs(kind, project_id, repo_id) VALUES ('pypi_sync', 1, 1)` → `SELECT summary FROM sync_jobs WHERE ...` returns `{}`.

## Decisions Made

- Followed Discretion #3 + Discretion #7 decisions locked in the plan: plain `INTEGER NOT NULL DEFAULT 0` for drift_purge (no cross-column CHECK), top-level JSON-object TEXT column for sync_jobs.summary (DEFAULT `'{}'`).
- Preserved m034_test.go shape verbatim — same package, same `openFreshDB` / `applyReal` helpers, same `cid/name/colType/notNull/dfltValue/pk` PRAGMA scan target tuple, same `t.Parallel()` mark.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — Self-contradicting acceptance gate] Reworded `.up.sql` comment to satisfy `grep -c CHECK == 0` literal acceptance gate**

- **Found during:** Task 1 verification (post-write)
- **Issue:** The plan's exact `.up.sql` contents (specified verbatim under `<action>` in the plan) included two occurrences of the literal string "CHECK" inside comment lines (`a CHECK that references other columns` and `via a cross-column CHECK`). Plan acceptance criterion in `<acceptance_criteria>` requires `grep -c "CHECK" internal/metadata/migrations/035_repos_drift_purge_and_sync_summary.up.sql` to return `0`. The two clauses are mutually exclusive — copying the plan body verbatim returned `grep -c CHECK = 2`.
- **Fix:** Reworded the comment block to express the same semantic content without the literal word "CHECK": "via a cross-column constraint" and "support a column rule that references other columns". Functional SQL unchanged. Intent of the acceptance gate (no actual SQL CHECK constraint clause exists in the file) is preserved AND the literal grep gate now passes (0 occurrences).
- **Files modified:** `internal/metadata/migrations/035_repos_drift_purge_and_sync_summary.up.sql`
- **Verification:** `grep -c CHECK ...up.sql` returns `0`; functional `ALTER TABLE` statements unchanged byte-for-byte.
- **Committed in:** `8ba4651` (Task 1 commit).

---

**Total deviations:** 1 auto-fixed (Rule 1 — internal plan inconsistency)
**Impact on plan:** Cosmetic comment wording only. Functional SQL identical to the plan-prescribed form; acceptance gate intent preserved.

### Note on `grep -c "TestMigration035"` literal vs semantic count

Plan acceptance gate states: `grep -c "TestMigration035" internal/metadata/migrations/m035_test.go returns '2' (two test functions)`. The literal grep returns `4` because each test function name appears in two locations: the godoc comment immediately above the function and the `func ...` declaration line itself — same pattern as the m034_test.go template the plan instructed cloning. The semantic gate ("two test functions present") is met (verified by `grep -n "^func TestMigration035" m035_test.go` → exactly 2 matches at lines 12 and 79). Not flagged as a deviation because the godoc-comment style is part of the m034 shape the plan asked us to mirror verbatim.

## Issues Encountered

None.

## Verification

- `go test -run 'TestMigration035' ./internal/metadata/migrations/ -count=1` — PASS (0.07s).
- `go test ./internal/metadata/migrations/ -count=1` — PASS (1.40s; zero regressions in m033 / m034 tests + the broader runner_test.go suite).
- `go vet ./internal/metadata/migrations/...` — clean.
- `go build ./...` — clean (whole-repo build).
- `git diff --stat go.mod go.sum` — empty (zero-new-deps invariant held).
- Embed FS auto-picks up new SQL files: `go build ./internal/metadata/...` succeeds with no edits to embed.go.

## User Setup Required

None — schema-only migration; no external service configuration.

## Next Plan Readiness

- Plan 06-02 (`repo.DriftPurge bool` field + API mirror-only validation) can now be scanned from any `SELECT ... drift_purge ... FROM repos` — column is live, default is 0, FK + UNIQUE shape unchanged.
- Plan 06-06 (per-protocol DriftAdapter + `SetSummaryDriftPurged` writer) can write to `sync_jobs.summary` immediately. Recommended writer pattern: `UPDATE sync_jobs SET summary = json_set(summary, '$.drift_purged', ?), updated_at = CURRENT_TIMESTAMP WHERE id = ?` (SQLite `json_set` is built into modernc.org/sqlite).
- Plan 06-03 (`internal/driftpurge/` engine + adapter interface) was committed concurrently by a parallel Wave 1 agent — its files (`engine.go`, `doc.go`, `engine_test.go`) appeared on this branch as commits `c3ef75d` + `cb5fbfb` interleaved with our own commits. They are out-of-scope for this plan and not touched here.
- AllEventKinds baseline stays at 23 (no audit kind added in this plan; audit lives in plan 06-05 per D-22).

## Self-Check: PASSED

Artifacts verified:
- `internal/metadata/migrations/035_repos_drift_purge_and_sync_summary.up.sql` — FOUND
- `internal/metadata/migrations/035_repos_drift_purge_and_sync_summary.down.sql` — FOUND
- `internal/metadata/migrations/m035_test.go` — FOUND
- `.planning/phases/06-drift-purge/06-01-SUMMARY.md` — FOUND

Commits verified (`git log --oneline --all | grep <hash>`):
- `8ba4651` (feat: migration 035 SQL files) — FOUND
- `8db489f` (test: m035_test.go) — FOUND

---
*Phase: 06-drift-purge*
*Plan: 01*
*Completed: 2026-04-25*
