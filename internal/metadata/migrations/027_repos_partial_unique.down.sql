-- 027_repos_partial_unique.down.sql
--
-- Reverse of 027: restore the table-level UNIQUE(project_id, type, name)
-- and drop the partial UNIQUE index. This can fail if a (project_id,
-- type, name) triple has both a live row and a soft-deleted row — that
-- state is legal under 027 but illegal under the restored schema.

DROP INDEX IF EXISTS idx_repos_project_type_name;
CREATE TABLE repos_old (
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
    UNIQUE(project_id, type, name)
);
INSERT INTO repos_old SELECT
    id, project_id, type, name, description_md, auto_scan, block_on_severity,
    public_read, size_bytes, created_at, deleted_at, metadata_state,
    last_regen_error, git_max_push_bytes, is_mirror, mirror_upstream_url,
    mirror_filter_json, mirror_cred_id, scan_on_sync
FROM repos;
DROP TABLE repos;
ALTER TABLE repos_old RENAME TO repos;
