-- Migration 042_npm_packages:
--
-- Tables backing the npm registry protocol.
--
-- npm_packages: one row per published (package, version). version_json
-- carries the version manifest exactly as the client published it
-- (minus _attachments); the packument endpoint reassembles the document
-- from these rows and rewrites dist.tarball to this server's URL.
--
-- npm_dist_tags: one row per (package, tag) — npm publish sends the tag
-- (usually "latest") in the publish body; npm install resolves through it.

CREATE TABLE npm_packages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    version      TEXT    NOT NULL,
    description  TEXT    NOT NULL DEFAULT '',
    version_json TEXT    NOT NULL,
    tarball      TEXT    NOT NULL,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    shasum       TEXT    NOT NULL DEFAULT '',
    integrity    TEXT    NOT NULL DEFAULT '',
    uploaded_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(repo_id, name, version)
);
CREATE INDEX idx_npm_packages_repo ON npm_packages(repo_id);
CREATE INDEX idx_npm_packages_name ON npm_packages(repo_id, name);

CREATE TABLE npm_dist_tags (
    repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name     TEXT    NOT NULL,
    tag      TEXT    NOT NULL,
    version  TEXT    NOT NULL,
    PRIMARY KEY (repo_id, name, tag)
);
