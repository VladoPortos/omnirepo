-- Phase 3 migration 009_apt_suites (Plan 03-01, D-25):
--
-- APT repositories are sliced by (suite, component, architecture) tuples.
-- Each distinct tuple for a repo yields one row; deb_packages link here via
-- suite_id so Packages/Release regeneration can partition cleanly.

CREATE TABLE apt_suites (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    suite         TEXT    NOT NULL,
    component     TEXT    NOT NULL,
    architecture  TEXT    NOT NULL,
    UNIQUE(repo_id, suite, component, architecture)
);
CREATE INDEX idx_apt_suites_repo ON apt_suites(repo_id);
