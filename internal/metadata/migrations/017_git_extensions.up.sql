-- Phase 4 migration 017_git_extensions (Plan 04-02, D-35, D-36):
--
-- Two additions:
--   1. repos.git_max_push_bytes INTEGER NULL — per-repo override for the
--      global git push-size cap (D-35). NULL = inherit cfg.repos.git.max_push_bytes.
--   2. git_refs mirror table — populated synchronously by the post-ReceivePack
--      walker (D-37). Drives UI browsing without hitting the bare-repo on
--      every page view. CHECK constraint on `type` keeps ref classification
--      tight; UNIQUE(repo_id,name) enforces one row per ref-name per repo.

ALTER TABLE repos ADD COLUMN git_max_push_bytes INTEGER NULL;

CREATE TABLE git_refs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    target     TEXT    NOT NULL,
    type       TEXT    NOT NULL CHECK (type IN ('branch','tag','symbolic','other')),
    updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (repo_id, name)
);
CREATE INDEX idx_git_refs_repo ON git_refs(repo_id);
