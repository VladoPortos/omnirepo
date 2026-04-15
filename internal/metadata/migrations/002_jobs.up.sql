-- Phase 2 migration 002_jobs: dispatcher-leased job rows (D-15, D-17, D-44)
-- and scan pipeline tables (SCAN-06). See CONTEXT.md for column rationale.

-- sync_jobs: dispatcher leases via single-statement UPDATE ... RETURNING
-- (D-15, requires SQLite >=3.35). Two-pool runner shares this table via
-- `kind`: 'pull_external' | 'promote' | 'gc' | future sync kinds.
CREATE TABLE sync_jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT    NOT NULL,
    project_id    INTEGER REFERENCES projects(id),
    repo_id       INTEGER REFERENCES repos(id),
    payload_json  TEXT    NOT NULL DEFAULT '{}',
    status        TEXT    NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','running','done','failed')),
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT    NOT NULL DEFAULT '',
    leased_by     TEXT    NOT NULL DEFAULT '',
    leased_at     TIMESTAMP,
    next_run_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    log           TEXT    NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_sync_jobs_pending ON sync_jobs(status, next_run_at) WHERE status='pending';
CREATE INDEX idx_sync_jobs_running ON sync_jobs(status, leased_at)   WHERE status='running';

-- scans: per-artifact vulnerability scan state. Leases use the same
-- UPDATE ... RETURNING pattern as sync_jobs. severity_summary_json is a
-- denormalized bag used by the block_on_severity gate.
CREATE TABLE scans (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id                INTEGER NOT NULL REFERENCES repos(id),
    artifact_kind          TEXT    NOT NULL,
    artifact_id            TEXT    NOT NULL,
    status                 TEXT    NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending','running','done','failed')),
    attempts               INTEGER NOT NULL DEFAULT 0,
    last_error             TEXT    NOT NULL DEFAULT '',
    leased_by              TEXT    NOT NULL DEFAULT '',
    leased_at              TIMESTAMP,
    next_run_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at             TIMESTAMP,
    finished_at            TIMESTAMP,
    severity_summary_json  TEXT    NOT NULL DEFAULT '{}',
    sbom_path              TEXT    NOT NULL DEFAULT '',
    trivy_db_version       TEXT    NOT NULL DEFAULT '',
    created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_scans_pending  ON scans(status, next_run_at) WHERE status='pending';
CREATE INDEX idx_scans_artifact ON scans(repo_id, artifact_kind, artifact_id, finished_at);

-- vulnerabilities: one row per CVE finding per scan. cves_fts mirrors
-- (cve_id, package, summary) for search (D-40).
CREATE TABLE vulnerabilities (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id           INTEGER NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    cve_id            TEXT NOT NULL,
    severity          TEXT NOT NULL,
    package_name      TEXT NOT NULL,
    package_version   TEXT NOT NULL DEFAULT '',
    fixed_version     TEXT NOT NULL DEFAULT '',
    title             TEXT NOT NULL DEFAULT '',
    description       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_vuln_scan ON vulnerabilities(scan_id);
CREATE INDEX idx_vuln_cve  ON vulnerabilities(cve_id);
