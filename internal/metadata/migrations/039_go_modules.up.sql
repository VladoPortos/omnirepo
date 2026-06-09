-- Migration 039_go_modules:
--
-- One row per hosted Go module version (GOPROXY protocol). module_path is
-- the DECODED module path (e.g. github.com/Azure/azure-sdk); the on-disk
-- storage key uses the GOPROXY-escaped form (uppercase → "!"+lowercase).
-- The /@v/list, .info, and @latest endpoints are computed from these rows;
-- .mod and .zip are served from disk.

CREATE TABLE go_modules (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    module_path  TEXT    NOT NULL,
    version      TEXT    NOT NULL,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    digest       TEXT    NOT NULL,
    uploaded_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(repo_id, module_path, version)
);
CREATE INDEX idx_go_modules_repo ON go_modules(repo_id);
CREATE INDEX idx_go_modules_module ON go_modules(repo_id, module_path);
