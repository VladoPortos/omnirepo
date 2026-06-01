-- 029_audit_occurred_at_rfc3339.down.sql
--
-- No-op: the up migration strictly tightens the stored format (non-ISO →
-- ISO). Rewriting back to the Go-%v shape would regress the bug this
-- migration was created to fix. If the downgrade is ever needed for
-- rollback, drop the audit_log rows instead — they're observational data.

SELECT 1;
