-- Inverse of 007. Rebuild docker_blobs without the CHECK so existing
-- deployments can roll back cleanly.

CREATE TABLE docker_blobs_old (
    digest          TEXT PRIMARY KEY,
    size_bytes      INTEGER NOT NULL,
    ref_count       INTEGER NOT NULL DEFAULT 0,
    last_touched_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO docker_blobs_old(digest, size_bytes, ref_count, last_touched_at)
SELECT digest, size_bytes, ref_count, last_touched_at FROM docker_blobs;

DROP TABLE docker_blobs;
ALTER TABLE docker_blobs_old RENAME TO docker_blobs;

CREATE INDEX idx_docker_blobs_gc ON docker_blobs(ref_count, last_touched_at);
