-- Migration 016_s3_access_keys:
--
-- Per-project S3 access-key store. `secret_enc` is AES-GCM-encrypted via
-- internal/crypto/aead.go (same helper used for signing keys and
-- upstream creds). Scope key to a project, not a repo: S3 buckets are
-- project-owned, so a single AKID maps to every bucket under its
-- project.
--
-- Partial index on `project_id WHERE revoked_at IS NULL` keeps the active
-- working set cheap to list without scanning tombstones. Uniqueness on
-- `access_key_id` is global across the install.
--
-- Timestamp convention: strftime('%Y-%m-%dT%H:%M:%fZ','now'), matching the
-- other tables (008_signing_keys, 010_rpm_packages, ...) for
-- cross-repo test comparability, in preference to datetime('now').

CREATE TABLE s3_access_keys (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id         INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    access_key_id      TEXT    NOT NULL UNIQUE,
    secret_enc         BLOB    NOT NULL,
    label              TEXT    NOT NULL,
    created_by_user_id INTEGER NOT NULL REFERENCES users(id),
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_used_at       TEXT,
    revoked_at         TEXT
);
CREATE INDEX idx_s3_access_keys_project ON s3_access_keys(project_id) WHERE revoked_at IS NULL;
