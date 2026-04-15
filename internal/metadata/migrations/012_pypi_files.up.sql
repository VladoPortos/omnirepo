-- Phase 3 migration 012_pypi_files (Plan 03-01, D-26):
--
-- One row per PEP-503 file (wheel or sdist) stored in a PyPI repo. The
-- project_normalized column is the PEP-503 normalized name (lowercase, '-'
-- collapsed from `[-_.]+`); Simple index regeneration (03-04) groups by it.

CREATE TABLE pypi_files (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id              INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    project_normalized   TEXT    NOT NULL,
    version              TEXT    NOT NULL,
    filename             TEXT    NOT NULL,
    kind                 TEXT    NOT NULL CHECK (kind IN ('wheel','sdist')),
    requires_python      TEXT    NOT NULL DEFAULT '',
    size_bytes           INTEGER NOT NULL DEFAULT 0,
    digest               TEXT    NOT NULL,
    core_metadata_json   TEXT    NOT NULL DEFAULT '{}',
    uploaded_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(repo_id, filename)
);
CREATE INDEX idx_pypi_files_norm ON pypi_files(repo_id, project_normalized);
CREATE INDEX idx_pypi_files_digest ON pypi_files(digest);
