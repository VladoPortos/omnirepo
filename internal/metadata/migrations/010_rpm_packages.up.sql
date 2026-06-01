-- Migration 010_rpm_packages:
--
-- One row per (name, epoch, version, release, arch) RPM in a repo. Drives
-- repomd.xml + primary.xml.gz regen. NEVRA uniqueness keeps the
-- relation faithful to the RPM filesystem.

CREATE TABLE rpm_packages (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name          TEXT    NOT NULL,
    epoch         INTEGER NOT NULL DEFAULT 0,
    version       TEXT    NOT NULL,
    release       TEXT    NOT NULL,
    arch          TEXT    NOT NULL,
    summary       TEXT    NOT NULL DEFAULT '',
    description   TEXT    NOT NULL DEFAULT '',
    license       TEXT    NOT NULL DEFAULT '',
    url           TEXT    NOT NULL DEFAULT '',
    source_rpm    TEXT    NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    digest        TEXT    NOT NULL,
    filename      TEXT    NOT NULL,
    uploaded_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(repo_id, name, epoch, version, release, arch)
);
CREATE INDEX idx_rpm_packages_repo ON rpm_packages(repo_id);
CREATE INDEX idx_rpm_packages_digest ON rpm_packages(digest);
