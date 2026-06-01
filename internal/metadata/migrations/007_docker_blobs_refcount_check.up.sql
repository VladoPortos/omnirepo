-- add CHECK (ref_count >= 0) to docker_blobs so negative refcounts
-- can never land. Before this migration, DecRef returned ErrRefCountUnderflow
-- from the application layer only; a direct UPDATE or a buggy code path
-- could still persist a negative value. SQLite doesn't support ALTER TABLE
-- ADD CONSTRAINT, so rebuild the table via the CREATE-new/INSERT/DROP-old/
-- RENAME dance. No table in the schema carries a FOREIGN KEY to docker_blobs
-- (digest references are tracked in manifests' BLOB body, not as FKs), so
-- no FK cleanup is needed.

CREATE TABLE docker_blobs_new (
    digest          TEXT PRIMARY KEY,
    size_bytes      INTEGER NOT NULL,
    ref_count       INTEGER NOT NULL DEFAULT 0 CHECK (ref_count >= 0),
    last_touched_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO docker_blobs_new(digest, size_bytes, ref_count, last_touched_at)
SELECT digest, size_bytes, ref_count, last_touched_at FROM docker_blobs;

DROP TABLE docker_blobs;
ALTER TABLE docker_blobs_new RENAME TO docker_blobs;

CREATE INDEX idx_docker_blobs_gc ON docker_blobs(ref_count, last_touched_at);
