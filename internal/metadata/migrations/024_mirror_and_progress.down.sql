-- modernc sqlite ships 3.51.x which supports ALTER TABLE DROP COLUMN (3.35+).
-- Mirrors the 017/023 down-migration pattern.

ALTER TABLE sync_jobs DROP COLUMN current_step;
ALTER TABLE sync_jobs DROP COLUMN total_bytes;
ALTER TABLE sync_jobs DROP COLUMN progress_bytes;

ALTER TABLE repos DROP COLUMN scan_on_sync;
ALTER TABLE repos DROP COLUMN mirror_cred_id;
ALTER TABLE repos DROP COLUMN mirror_filter_json;
ALTER TABLE repos DROP COLUMN mirror_upstream_url;
ALTER TABLE repos DROP COLUMN is_mirror;
