-- Reverts 040: narrows repos.type back to the pre-'go' set. Rows of
-- type='go' must be removed first or the copy fails the CHECK.
DELETE FROM repos WHERE type = 'go';
CREATE TABLE repos_new (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id           INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type                 TEXT NOT NULL CHECK(type IN ('rpm','deb','pypi','docker','helm','git','raw')),
    name                 TEXT NOT NULL,
    description_md       TEXT NOT NULL DEFAULT '',
    auto_scan            BOOLEAN NOT NULL DEFAULT 1,
    block_on_severity    TEXT NOT NULL DEFAULT 'none' CHECK(block_on_severity IN ('none','low','medium','high','critical')),
    public_read          BOOLEAN NOT NULL DEFAULT 0,
    size_bytes           INTEGER NOT NULL DEFAULT 0,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at           TIMESTAMP,
    metadata_state       TEXT NOT NULL DEFAULT 'clean' CHECK (metadata_state IN ('clean','dirty','regenerating')),
    last_regen_error     TEXT NOT NULL DEFAULT '',
    git_max_push_bytes   INTEGER NULL,
    is_mirror            INTEGER NOT NULL DEFAULT 0,
    mirror_upstream_url  TEXT,
    mirror_filter_json   TEXT,
    mirror_cred_id       INTEGER REFERENCES upstream_creds(id) ON DELETE SET NULL,
    scan_on_sync         INTEGER NOT NULL DEFAULT 0,
    drift_purge          INTEGER NOT NULL DEFAULT 0
);
INSERT INTO repos_new (
    id, project_id, type, name, description_md, auto_scan, block_on_severity,
    public_read, size_bytes, created_at, deleted_at, metadata_state,
    last_regen_error, git_max_push_bytes, is_mirror, mirror_upstream_url,
    mirror_filter_json, mirror_cred_id, scan_on_sync, drift_purge
)
SELECT
    id, project_id, type, name, description_md, auto_scan, block_on_severity,
    public_read, size_bytes, created_at, deleted_at, metadata_state,
    last_regen_error, git_max_push_bytes, is_mirror, mirror_upstream_url,
    mirror_filter_json, mirror_cred_id, scan_on_sync, drift_purge
FROM repos;
DROP TABLE repos;
ALTER TABLE repos_new RENAME TO repos;
CREATE UNIQUE INDEX idx_repos_project_type_name
    ON repos(project_id, type, name) WHERE deleted_at IS NULL;
