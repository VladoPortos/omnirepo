-- Migration 043_maven_artifacts:
--
-- Table backing the Maven repository protocol. One row per primary
-- artifact file (.jar/.pom/.war/...) deployed by mvn/gradle; checksum
-- sidecars (.sha1/.md5/...) and maven-metadata.xml live on disk only —
-- the deploy plugin uploads and maintains them itself, so they carry no
-- artifact-level meaning for listings.
--
-- path is the repo-relative storage path (group dirs/artifact/version/
-- file) and is the stable identity; GAV columns are parsed from it at
-- upload time for the UI/content listing.

CREATE TABLE maven_artifacts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    group_id     TEXT    NOT NULL,
    artifact_id  TEXT    NOT NULL,
    version      TEXT    NOT NULL,
    classifier   TEXT    NOT NULL DEFAULT '',
    extension    TEXT    NOT NULL,
    filename     TEXT    NOT NULL,
    path         TEXT    NOT NULL,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    sha256       TEXT    NOT NULL DEFAULT '',
    uploaded_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(repo_id, path)
);
CREATE INDEX idx_maven_artifacts_repo ON maven_artifacts(repo_id);
CREATE INDEX idx_maven_artifacts_gav ON maven_artifacts(repo_id, group_id, artifact_id, version);
