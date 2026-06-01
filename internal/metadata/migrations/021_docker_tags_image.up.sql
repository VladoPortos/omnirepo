-- Migration 021: extend docker_tags with an `image` column so the /v2 surface
-- can host Helm OCI artifacts. Helm CLI always appends the chart name as a
-- 4th path segment (e.g. /v2/proj/helm/repo/hello-chart/manifests/0.1.0),
-- meaning a single OmniRepo helm repo hosts N OCI "images" (one per chart).
-- The prior `(repo_id, tag)` PK collides when two charts share a version
-- string. New PK is `(repo_id, image, tag)`; existing Docker rows are
-- migrated with image = '' so per-repo tag uniqueness is preserved.
--
-- docker_manifests stays keyed by (repo_id, digest). Manifest rows are
-- content-addressed, so ref_count across image boundaries still works:
-- every docker_tags row that points at a digest holds one reference,
-- regardless of which image it belongs to.

CREATE TABLE docker_tags_new (
    repo_id     INTEGER NOT NULL REFERENCES repos(id),
    image       TEXT NOT NULL DEFAULT '',
    tag         TEXT NOT NULL,
    digest      TEXT NOT NULL,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, image, tag)
);

INSERT INTO docker_tags_new(repo_id, image, tag, digest, updated_at)
SELECT repo_id, '', tag, digest, updated_at FROM docker_tags;

DROP INDEX IF EXISTS idx_docker_tags_digest;
DROP TABLE docker_tags;
ALTER TABLE docker_tags_new RENAME TO docker_tags;
CREATE INDEX idx_docker_tags_digest ON docker_tags(repo_id, digest);
CREATE INDEX idx_docker_tags_image  ON docker_tags(repo_id, image);
