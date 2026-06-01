-- 036_s3_multipart_actor_attribution.down.sql
-- Inverse of 036.up.sql — drops initiated_by_s3_key_id and restores NOT NULL
-- on initiated_by_user_id. Uses the same rebuild idiom.
--
-- This down migration WILL FAIL if any row has initiated_by_user_id IS NULL
-- (i.e., a v1.6+ S3-key-initiated upload). That is intentional: operators
-- rolling back to v1.5 must first abort or complete any pending v1.6-style
-- multipart uploads. The SELECT into _old enforces this implicitly via the
-- NOT NULL constraint on the rebuilt table.
--
-- The runner toggles PRAGMA foreign_keys=OFF for the duration of the body
-- and runs PRAGMA foreign_key_check before COMMIT, mirroring the up path.

CREATE TABLE s3_multipart_uploads_old (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    upload_id             TEXT    NOT NULL UNIQUE,
    bucket_id             INTEGER NOT NULL REFERENCES s3_buckets(id) ON DELETE CASCADE,
    key                   TEXT    NOT NULL,
    initiated_by_user_id  INTEGER NOT NULL REFERENCES users(id),
    initiated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    metadata_json         TEXT    NOT NULL DEFAULT '{}'
);

INSERT INTO s3_multipart_uploads_old
    (id, upload_id, bucket_id, key, initiated_by_user_id, initiated_at, metadata_json)
SELECT
    id, upload_id, bucket_id, key, initiated_by_user_id, initiated_at, metadata_json
FROM s3_multipart_uploads;

DROP TABLE s3_multipart_uploads;
ALTER TABLE s3_multipart_uploads_old RENAME TO s3_multipart_uploads;

CREATE INDEX idx_s3_multipart_uploads_bucket ON s3_multipart_uploads(bucket_id);
