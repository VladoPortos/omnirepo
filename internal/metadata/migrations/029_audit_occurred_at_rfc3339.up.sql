-- 029_audit_occurred_at_rfc3339.up.sql
--
-- audit_log.occurred_at historically held time.Time values serialized via
-- modernc.org/sqlite's default *time.Time path, which is Go's
-- time.Time.String() format: "2026-04-22 12:43:05.123456789 +0000 UTC".
-- SQLite's built-in date/time functions can't parse this shape, and it
-- breaks lexicographic comparison against RFC3339 strings — exactly what
-- the admin audit endpoint's from/to filters and keyset pagination rely on.
-- The write path has been normalized to RFC3339Nano (see internal/audit);
-- this migration rewrites every legacy row to the same shape so filters
-- and pagination work consistently against historical data too.
--
-- Shape transformation, UTC only (Go UTC values always carried " +0000 UTC"):
--   in:  YYYY-MM-DD HH:MM:SS[.fractional] +0000 UTC   (length 26..39)
--   out: YYYY-MM-DDTHH:MM:SS[.fractional]Z           (length 20..30)
-- Rows in the RFC3339Nano shape (written post-fix) are left alone.

UPDATE audit_log
SET occurred_at = substr(occurred_at, 1, 10) || 'T'
                  || substr(occurred_at, 12, length(occurred_at) - 21)
                  || 'Z'
WHERE occurred_at LIKE '%+0000 UTC';
