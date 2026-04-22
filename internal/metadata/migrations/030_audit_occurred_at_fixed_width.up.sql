-- 030_audit_occurred_at_fixed_width.up.sql (F-04.2 Codex-pass follow-up)
--
-- Migration 029 normalized audit_log.occurred_at from Go-%v to RFC3339Nano,
-- but RFC3339Nano strips trailing zeros (Go's .999999999 format verb). That
-- leaves a subtle lex-order trap: a zero-ns row (".Z") lex-sorts AFTER a
-- sub-second row in the same second (".123Z") because 'Z' (0x5A) > '.' (0x2E).
-- The endpoint's ORDER BY occurred_at and keyset cursor comparisons assume
-- lex == chronological, so mixed-width fractions produce wrong order.
--
-- The write path has been switched to a fixed-width layout (9-digit ns,
-- trailing Z — see internal/audit/audit.go `DBTimestampLayout`). This
-- migration repairs the in-place ones by padding any short fraction out to
-- 9 digits and adding .000000000 to whole-second rows.
--
-- Shape transformations (all rows end in 'Z' post-029):
--   in:  YYYY-MM-DDTHH:MM:SS.<k-digit-fraction>Z   (1 <= k <= 9)
--   in:  YYYY-MM-DDTHH:MM:SSZ                      (no fraction)
--   out: YYYY-MM-DDTHH:MM:SS.000000000Z            (always 9 digits)
--
-- LIKE `____-__-__T__:__:__.%Z` is matched on any row with a fraction; the
-- length check separates it from the no-fraction shape. Rows already at the
-- canonical 30-char width are skipped (WHERE length != 30).

-- Short-fraction rows: pad the fraction out to 9 digits.
UPDATE audit_log
SET occurred_at =
    substr(occurred_at, 1, 19)
    || '.'
    || substr(substr(occurred_at, 21, length(occurred_at) - 21) || '000000000', 1, 9)
    || 'Z'
WHERE occurred_at LIKE '____-__-__T__:__:__.%Z'
  AND length(occurred_at) <> 30;

-- Zero-fraction rows: insert a full fractional component.
UPDATE audit_log
SET occurred_at = substr(occurred_at, 1, 19) || '.000000000Z'
WHERE occurred_at LIKE '____-__-__T__:__:__Z';
