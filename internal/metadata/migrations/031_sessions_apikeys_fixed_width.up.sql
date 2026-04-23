-- 031_sessions_apikeys_fixed_width.up.sql (F-04.3)
--
-- Sessions and api_keys stored time.Time values via modernc/sqlite's
-- default binding, which serializes Go-%v format:
--   "2026-04-22 12:43:05.12345678 +0000 UTC"
-- That trips the same lex-comparison trap fixed for audit_log in
-- migrations 029 + 030 — expires_at > CURRENT_TIMESTAMP and similar
-- predicates can drift by ≤1 s at sub-second boundaries, and the
-- fractional-second width varies per row.
--
-- The Go write path now stores DBTimestampLayout — fixed-width 30-char
-- ISO-8601 "YYYY-MM-DDTHH:MM:SS.NNNNNNNNNZ". This migration rewrites any
-- legacy Go-%v row to the same shape, and rows matching the canonical
-- 30-char width are skipped as a no-op.
--
-- Shape transformations:
--   in:  YYYY-MM-DD HH:MM:SS.<k-digit-fraction> +0000 UTC
--   in:  YYYY-MM-DD HH:MM:SS +0000 UTC
--   out: YYYY-MM-DDTHH:MM:SS.NNNNNNNNNZ  (always 30 chars)
--
-- The UPDATE uses a guard on length != 30 so re-runs + mixed-history DBs
-- stay idempotent.

-- PRE-MIGRATION CLEANUP ────────────────────────────────────────────────
-- sync_jobs.repo_id REFERENCES repos(id) with no ON DELETE action, so a
-- hard-deleted repo leaves orphan sync_jobs rows behind. The migration
-- runner runs PRAGMA foreign_key_check inside the same tx as the body
-- and rolls back on any violation (including pre-existing ones), which
-- would block this otherwise-unrelated timestamp-format rewrite. Prune
-- the dangling rows here. Future migration should tighten the FK to
-- ON DELETE CASCADE so this never re-accumulates.
DELETE FROM sync_jobs
 WHERE repo_id IS NOT NULL
   AND repo_id NOT IN (SELECT id FROM repos);

-- Same shape: project_id orphans if a project was hard-deleted.
DELETE FROM sync_jobs
 WHERE project_id IS NOT NULL
   AND project_id NOT IN (SELECT id FROM projects);

-- SESSIONS ─────────────────────────────────────────────────────────────
UPDATE sessions
SET issued_at = substr(issued_at, 1, 10) || 'T' || substr(issued_at, 12, 8)
  || CASE
       WHEN substr(issued_at, 20, 1) = '.' THEN
         '.' || substr((substr(issued_at, 21, 9) || '000000000'), 1, 9)
       ELSE
         '.000000000'
     END
  || 'Z'
WHERE issued_at IS NOT NULL AND length(issued_at) != 30;

UPDATE sessions
SET last_seen_at = substr(last_seen_at, 1, 10) || 'T' || substr(last_seen_at, 12, 8)
  || CASE
       WHEN substr(last_seen_at, 20, 1) = '.' THEN
         '.' || substr((substr(last_seen_at, 21, 9) || '000000000'), 1, 9)
       ELSE
         '.000000000'
     END
  || 'Z'
WHERE last_seen_at IS NOT NULL AND length(last_seen_at) != 30;

UPDATE sessions
SET expires_at = substr(expires_at, 1, 10) || 'T' || substr(expires_at, 12, 8)
  || CASE
       WHEN substr(expires_at, 20, 1) = '.' THEN
         '.' || substr((substr(expires_at, 21, 9) || '000000000'), 1, 9)
       ELSE
         '.000000000'
     END
  || 'Z'
WHERE expires_at IS NOT NULL AND length(expires_at) != 30;

-- API_KEYS ─────────────────────────────────────────────────────────────
-- Only last_used_at is written via time.Time binding; created_at and
-- revoked_at use CURRENT_TIMESTAMP (space-format, 19 chars) — intentional
-- for those since they are only read as display strings, never compared
-- against user-supplied timestamps.
UPDATE api_keys
SET last_used_at = substr(last_used_at, 1, 10) || 'T' || substr(last_used_at, 12, 8)
  || CASE
       WHEN substr(last_used_at, 20, 1) = '.' THEN
         '.' || substr((substr(last_used_at, 21, 9) || '000000000'), 1, 9)
       ELSE
         '.000000000'
     END
  || 'Z'
WHERE last_used_at IS NOT NULL AND length(last_used_at) != 30;
