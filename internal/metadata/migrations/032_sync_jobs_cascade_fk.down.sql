-- 032_sync_jobs_cascade_fk.down.sql
--
-- Reverse the FK tightening by rebuilding the table without the
-- ON DELETE CASCADE clauses. Mirrors the up-migration's rename-copy-drop.

ALTER TABLE sync_jobs RENAME TO sync_jobs_old;

CREATE TABLE sync_jobs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    kind           TEXT    NOT NULL,
    project_id     INTEGER REFERENCES projects(id),
    repo_id        INTEGER REFERENCES repos(id),
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

DROP TABLE sync_jobs_old;

CREATE INDEX idx_sync_jobs_pending ON sync_jobs(status, next_run_at)
    WHERE status = 'pending';
CREATE INDEX idx_sync_jobs_running ON sync_jobs(status, leased_at)
    WHERE status = 'running';
