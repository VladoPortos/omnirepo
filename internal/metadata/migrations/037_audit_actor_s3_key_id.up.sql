-- 037_audit_actor_s3_key_id.up.sql
-- v1.7 Phase 3 — S3AUDIT-01.
--
-- Adds actor_s3_key_id (nullable, FK to s3_access_keys) to audit_log so
-- that S3-authenticated state-changing operations attribute the actual
-- S3 access key. Pre-v1.7, ActorKindS3Key collapsed to (actor_user_id=NULL,
-- actor_api_key_id=NULL) — losing protocol-level provenance for SigV4
-- requests. v1.6 Phase 3 D-09 explicitly deferred this column to v1.7+.
--
-- Backfill not required: pre-v1.7 audit_log rows for S3 actions already
-- carry NULL in both actor_user_id and actor_api_key_id, which is the
-- exact same shape they will have in actor_s3_key_id (NULL for
-- pre-existing rows, populated going forward).
--
-- An index on (actor_s3_key_id, occurred_at) mirrors the existing
-- idx_audit_actor pattern from 001_initial.up.sql so admin audit
-- queries filtered by S3 key id stay O(log n) without a full table
-- scan.
ALTER TABLE audit_log ADD COLUMN actor_s3_key_id INTEGER REFERENCES s3_access_keys(id);

CREATE INDEX idx_audit_actor_s3 ON audit_log(actor_s3_key_id, occurred_at);
