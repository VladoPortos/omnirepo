-- Phase 4 migration 019_s3_multipart (Plan 04-02, D-18, D-19):
--
-- Staging state for S3 multipart uploads. `upload_id` (UUIDv4) is the
-- client-visible handle; uniqueness is global across buckets so the parts
-- table can FK on `upload_id` directly without composing a (bucket,key)
-- pair. Filesystem staging lives at /var/lib/omnirepo/tmp/s3/<uploadId>/.
--
-- On CompleteMultipartUpload the merged object is atomically renamed into
-- place (internal/storage/atomic.go), an s3_objects row is upserted, and
-- these two tables' rows for the upload are deleted — the ON DELETE CASCADE
-- on s3_multipart_parts drops the part rows automatically.
--
-- The cleanup job (D-21) aborts rows older than 24h to reclaim staging.

CREATE TABLE s3_multipart_uploads (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    upload_id             TEXT    NOT NULL UNIQUE,
    bucket_id             INTEGER NOT NULL REFERENCES s3_buckets(id) ON DELETE CASCADE,
    key                   TEXT    NOT NULL,
    initiated_by_user_id  INTEGER NOT NULL REFERENCES users(id),
    initiated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    metadata_json         TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_s3_multipart_uploads_bucket ON s3_multipart_uploads(bucket_id);

CREATE TABLE s3_multipart_parts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    upload_id   TEXT    NOT NULL REFERENCES s3_multipart_uploads(upload_id) ON DELETE CASCADE,
    part_number INTEGER NOT NULL,
    size_bytes  INTEGER NOT NULL,
    md5         TEXT    NOT NULL,
    uploaded_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (upload_id, part_number)
);
CREATE INDEX idx_s3_multipart_parts_upload ON s3_multipart_parts(upload_id);
