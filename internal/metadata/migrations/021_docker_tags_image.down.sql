-- Down migration 021: drop the image column, reverting docker_tags to
-- (repo_id, tag) PK. Rows with image != '' are discarded because the
-- legacy schema cannot represent them without collision.

CREATE TABLE docker_tags_old (
    repo_id     INTEGER NOT NULL REFERENCES repos(id),
    tag         TEXT NOT NULL,
    digest      TEXT NOT NULL,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, tag)
);

INSERT OR IGNORE INTO docker_tags_old(repo_id, tag, digest, updated_at)
SELECT repo_id, tag, digest, updated_at FROM docker_tags WHERE image = '';

DROP INDEX IF EXISTS idx_docker_tags_digest;
DROP INDEX IF EXISTS idx_docker_tags_image;
DROP TABLE docker_tags;
ALTER TABLE docker_tags_old RENAME TO docker_tags;
CREATE INDEX idx_docker_tags_digest ON docker_tags(repo_id, digest);
