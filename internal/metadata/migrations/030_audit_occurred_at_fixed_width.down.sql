-- 030_audit_occurred_at_fixed_width.down.sql
--
-- No-op: the up migration strictly tightens variable-width RFC3339Nano rows
-- into fixed-width ISO-8601. Unpadding back introduces the lex-order bug
-- that motivated migration 030 in the first place.

SELECT 1;
