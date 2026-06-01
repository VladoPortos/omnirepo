-- 037_audit_actor_s3_key_id.down.sql
-- Reverses 037 by dropping the index and column.
DROP INDEX IF EXISTS idx_audit_actor_s3;
ALTER TABLE audit_log DROP COLUMN actor_s3_key_id;
