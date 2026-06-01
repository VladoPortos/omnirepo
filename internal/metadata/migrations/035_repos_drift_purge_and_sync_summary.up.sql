-- 035_repos_drift_purge_and_sync_summary.up.sql
-- Drift purge.
--
-- Step 1: repos.drift_purge.
-- Per-repo opt-in flag for drift purge (default off on upgrade —
-- preserves v1.4 additive-only behaviour). Mirror-only
-- invariant (reject drift_purge=true on non-mirror repos) is
-- enforced at the API layer (handlePatchRepo) — not
-- via a cross-column constraint, because SQLite ADD COLUMN does
-- not support a column rule that references other columns.
ALTER TABLE repos ADD COLUMN drift_purge INTEGER NOT NULL DEFAULT 0;

-- Step 2: sync_jobs.summary.
-- Per-sync JSON summary blob used by driftpurge.engine to stamp
-- a `drift_purged` integer key. Kept as TEXT (JSON) so
-- future summary keys (files_added, bytes_downloaded, etc.) can
-- land without another migration.
-- Default '{}' so any code path reading `summary` before a sync
-- ran observes a valid empty object, not NULL.
ALTER TABLE sync_jobs ADD COLUMN summary TEXT NOT NULL DEFAULT '{}';
