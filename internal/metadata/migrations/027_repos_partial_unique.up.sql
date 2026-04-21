-- 027_repos_partial_unique.up.sql (F-7 continuation — repos table)
--
-- Migration 026 fixed users / projects / s3_buckets so the uniqueness
-- constraint only applies to live rows. `repos` had the same mistake:
-- UNIQUE(project_id, type, name) at the table level meant a soft-deleted
-- repo kept its slot forever, so re-creating (e.g. acme/docker/images
-- after deleting the old one) came back 409 even though the original
-- was invisible.
--
-- This rebuild drops the compound table-level UNIQUE and re-asserts the
-- same uniqueness via a partial UNIQUE index restricted to live rows.
-- We preserve every column added through migrations 001..024 (the full
-- current schema is captured below), every CHECK constraint, and every
-- FK reference FROM `repos` (→ projects, → upstream_creds).
--
-- The runner wraps this file in BEGIN IMMEDIATE with PRAGMA foreign_keys
-- already disabled, and runs foreign_key_check before COMMIT to catch
-- orphan inserts — so the FK references TO repos from other tables
-- (apt_suites, blob_upload_sessions, deb_packages, docker_manifests,
-- docker_tags, git_refs, helm_charts, pypi_files, raw_files,
-- rpm_packages, scans, signing_keys, sync_jobs) resolve cleanly after
-- RENAME because we preserve row ids verbatim.

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
    scan_on_sync         INTEGER NOT NULL DEFAULT 0
);
INSERT INTO repos_new (
    id, project_id, type, name, description_md, auto_scan, block_on_severity,
    public_read, size_bytes, created_at, deleted_at, metadata_state,
    last_regen_error, git_max_push_bytes, is_mirror, mirror_upstream_url,
    mirror_filter_json, mirror_cred_id, scan_on_sync
)
SELECT
    id, project_id, type, name, description_md, auto_scan, block_on_severity,
    public_read, size_bytes, created_at, deleted_at, metadata_state,
    last_regen_error, git_max_push_bytes, is_mirror, mirror_upstream_url,
    mirror_filter_json, mirror_cred_id, scan_on_sync
FROM repos;
DROP TABLE repos;
ALTER TABLE repos_new RENAME TO repos;
CREATE UNIQUE INDEX idx_repos_project_type_name
    ON repos(project_id, type, name) WHERE deleted_at IS NULL;
