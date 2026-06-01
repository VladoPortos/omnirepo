-- 036_s3_multipart_actor_attribution.up.sql
-- S3 multipart actor attribution.
--
-- Adds initiated_by_s3_key_id (nullable, FK to s3_access_keys) and relaxes
-- initiated_by_user_id from NOT NULL to NULLable so multipart uploads
-- initiated via SigV4 can attribute the actual S3 access key without
-- fabricating a user. Backfill is NOT required — there are no committed
-- multipart rows pre-v1.6 in any deployment.
--
-- SQLite cannot drop NOT NULL via ALTER COLUMN, so we use the standard
-- table-rebuild idiom (CREATE _new -> INSERT SELECT -> DROP -> RENAME).
-- The s3_multipart_parts.upload_id FK references s3_multipart_uploads(upload_id)
-- by column name; the rebuild preserves that column unchanged so the FK
-- survives intact (verified by TestMigration036_PartsFKSurvives).
--
-- The migration runner (internal/metadata/migrations/runner.go) already
-- toggles PRAGMA foreign_keys=OFF at the connection level for the duration
-- of every migration body and runs PRAGMA foreign_key_check before COMMIT,
-- so we do not toggle it again here. (Adding our own PRAGMA inside the
-- runner-managed transaction would be a no-op at best and a confusion hazard
-- at worst.)

CREATE TABLE s3_multipart_uploads_new (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    upload_id               TEXT    NOT NULL UNIQUE,
    bucket_id               INTEGER NOT NULL REFERENCES s3_buckets(id) ON DELETE CASCADE,
    key                     TEXT    NOT NULL,
    initiated_by_user_id    INTEGER REFERENCES users(id),
    initiated_by_s3_key_id  INTEGER REFERENCES s3_access_keys(id),
    initiated_at            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    metadata_json           TEXT    NOT NULL DEFAULT '{}'
);

INSERT INTO s3_multipart_uploads_new
    (id, upload_id, bucket_id, key, initiated_by_user_id, initiated_at, metadata_json)
SELECT
    id, upload_id, bucket_id, key, initiated_by_user_id, initiated_at, metadata_json
FROM s3_multipart_uploads;

DROP TABLE s3_multipart_uploads;
ALTER TABLE s3_multipart_uploads_new RENAME TO s3_multipart_uploads;

CREATE INDEX idx_s3_multipart_uploads_bucket ON s3_multipart_uploads(bucket_id);
