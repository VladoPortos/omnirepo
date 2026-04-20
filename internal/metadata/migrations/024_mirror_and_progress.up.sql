-- Phase 8 Plan 01 (MIRROR-01..07): mirror-repo flag + sync progress columns.
--
-- repos grows 5 columns to represent an APT/RPM/PyPI/Helm repo that mirrors
-- an upstream archive. is_mirror + mirror_upstream_url are immutable post-
-- creation (enforced at the API layer); mirror_filter_json + mirror_cred_id +
-- scan_on_sync are editable. mirror_cred_id -> upstream_creds(id) carries
-- ON DELETE SET NULL so a deleted credential self-heals rather than wedging
-- the repo row — the next sync surfaces "credential missing" via the sync
-- handler's envelope.
--
-- sync_jobs grows 3 columns to carry byte-level progress for the UI polling
-- path. progress_bytes + total_bytes default 0; current_step is a short human
-- sentence such as "pulling hello_2.10-2_amd64.deb" or "chart 3 of 12".
-- Per D-11: APT/RPM/PyPI/OCI ship byte-level progress; Helm ships step-based
-- progress with total_bytes = 0 (index.yaml lacks chart sizes).

ALTER TABLE repos ADD COLUMN is_mirror           INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repos ADD COLUMN mirror_upstream_url TEXT;
ALTER TABLE repos ADD COLUMN mirror_filter_json  TEXT;
ALTER TABLE repos ADD COLUMN mirror_cred_id      INTEGER REFERENCES upstream_creds(id) ON DELETE SET NULL;
ALTER TABLE repos ADD COLUMN scan_on_sync        INTEGER NOT NULL DEFAULT 0;

ALTER TABLE sync_jobs ADD COLUMN progress_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_jobs ADD COLUMN total_bytes    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_jobs ADD COLUMN current_step   TEXT;
