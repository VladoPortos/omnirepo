-- Phase 2 migration 006_raw_files (plan 02-08):
--
-- Table backing the RAW pass-through protocol handler (D-27..D-31, RAW-01..05).
-- One row per file stored in /var/lib/omnirepo/repos/<proj>/raw/<repo>/<path>.
-- The handler keeps this row + the FTS5 artifacts_fts index + (optionally) a
-- scans-row enqueue in the same writer tx as the PathStore.Put call, so
-- crash-before-commit leaves nothing referenced and crash-after-commit means
-- the file really is on disk.

CREATE TABLE raw_files (
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    path        TEXT    NOT NULL,
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    mime        TEXT    NOT NULL DEFAULT '',
    sha256      TEXT    NOT NULL DEFAULT '',
    modified    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, path)
);
CREATE INDEX idx_raw_files_modified ON raw_files(modified);
