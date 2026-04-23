-- 032_sync_jobs_cascade_fk.up.sql (F-06.8 follow-up, Codex Q4)
--
-- sync_jobs FKs to projects + repos had no ON DELETE action, so a
-- hard-deleted repo or project left dangling sync_jobs rows that the
-- admin-jobs summary, GC scheduler, and migration 031's post-commit
-- foreign_key_check all had to reason about.
--
-- Migration 031 cleaned the orphans once; this migration prevents
-- recurrence by retrofitting ON DELETE CASCADE onto both FKs. SQLite
-- does not support `ALTER TABLE` to change an FK constraint, so the
-- standard rename-copy-drop dance (as documented at
-- https://www.sqlite.org/lang_altertable.html#otheralter) applies.

-- 1. Rename the live table.
ALTER TABLE sync_jobs RENAME TO sync_jobs_old;

-- 2. Recreate with the hardened FKs and all subsequent ALTER-added
--    columns inlined in their original order.
CREATE TABLE sync_jobs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    kind           TEXT    NOT NULL,
    project_id     INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    repo_id        INTEGER REFERENCES repos(id)    ON DELETE CASCADE,
    payload_json   TEXT    NOT NULL DEFAULT '{}',
    status         TEXT    NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','running','done','failed')),
    attempts       INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT    NOT NULL DEFAULT '',
    leased_by      TEXT    NOT NULL DEFAULT '',
    leased_at      TIMESTAMP,
    next_run_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    log            TEXT    NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    progress_bytes INTEGER NOT NULL DEFAULT 0,
    total_bytes    INTEGER NOT NULL DEFAULT 0,
    current_step   TEXT,
    files_synced   INTEGER NOT NULL DEFAULT 0
);

-- 3. Copy rows. 031 already pruned orphans, so a straight SELECT is safe.
INSERT INTO sync_jobs (
    id, kind, project_id, repo_id, payload_json, status, attempts,
    last_error, leased_by, leased_at, next_run_at, log,
    created_at, updated_at,
    progress_bytes, total_bytes, current_step, files_synced
)
SELECT
    id, kind, project_id, repo_id, payload_json, status, attempts,
    last_error, leased_by, leased_at, next_run_at, log,
    created_at, updated_at,
    progress_bytes, total_bytes, current_step, files_synced
FROM sync_jobs_old;

-- 4. Drop the old table.
DROP TABLE sync_jobs_old;

-- 5. Recreate the two partial indexes used by the dispatcher.
CREATE INDEX idx_sync_jobs_pending ON sync_jobs(status, next_run_at)
    WHERE status = 'pending';
CREATE INDEX idx_sync_jobs_running ON sync_jobs(status, leased_at)
    WHERE status = 'running';
