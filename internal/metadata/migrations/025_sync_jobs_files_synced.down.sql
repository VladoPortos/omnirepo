-- modernc sqlite ships 3.51.x which supports ALTER TABLE DROP COLUMN (3.35+).
-- Mirrors the 017/023/024 down-migration pattern.

ALTER TABLE sync_jobs DROP COLUMN files_synced;
