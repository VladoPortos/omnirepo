-- Migration 018_s3_objects:
--
-- One row per committed S3 object under a bucket. `UNIQUE(bucket_id,key)`
-- enforces S3's one-object-per-key rule; the per-bucket mutex serializes
-- writers so the UPSERT path is a clean `INSERT ... ON CONFLICT DO UPDATE`.
-- `sha256` carries the content hash of the final merged object (multipart
-- ETag lives in `etag`, which for multipart uploads is the AWS-specific
-- `<md5-of-concatenated-part-md5s>-<partCount>`, NOT the SHA256).

CREATE TABLE s3_objects (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    bucket_id     INTEGER NOT NULL REFERENCES s3_buckets(id) ON DELETE CASCADE,
    key           TEXT    NOT NULL,
    size_bytes    INTEGER NOT NULL,
    etag          TEXT    NOT NULL,
    content_type  TEXT    NOT NULL DEFAULT '',
    metadata_json TEXT    NOT NULL DEFAULT '{}',
    sha256        TEXT    NOT NULL,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (bucket_id, key)
);
CREATE INDEX idx_s3_objects_bucket ON s3_objects(bucket_id);
