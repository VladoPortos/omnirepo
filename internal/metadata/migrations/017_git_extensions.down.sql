-- modernc sqlite ships 3.51.x which supports ALTER TABLE DROP COLUMN (3.35+)
-- so dropping git_max_push_bytes is a single statement (mirrors 015_down).
DROP INDEX IF EXISTS idx_git_refs_repo;
DROP TABLE IF EXISTS git_refs;
ALTER TABLE repos DROP COLUMN git_max_push_bytes;
