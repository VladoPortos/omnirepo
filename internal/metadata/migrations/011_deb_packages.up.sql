-- Migration 011_deb_packages:
--
-- One row per (suite, package, version, architecture) Debian package.
-- Packages/Release regeneration joins against apt_suites via
-- suite_id to group rows by (suite, component, arch).

CREATE TABLE deb_packages (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    suite_id      INTEGER NOT NULL REFERENCES apt_suites(id) ON DELETE CASCADE,
    package       TEXT    NOT NULL,
    version       TEXT    NOT NULL,
    architecture  TEXT    NOT NULL,
    maintainer    TEXT    NOT NULL DEFAULT '',
    section       TEXT    NOT NULL DEFAULT '',
    priority      TEXT    NOT NULL DEFAULT '',
    depends       TEXT    NOT NULL DEFAULT '',
    description   TEXT    NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    digest        TEXT    NOT NULL,
    filename      TEXT    NOT NULL,
    uploaded_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(repo_id, suite_id, package, version, architecture)
);
CREATE INDEX idx_deb_packages_repo ON deb_packages(repo_id);
CREATE INDEX idx_deb_packages_digest ON deb_packages(digest);
