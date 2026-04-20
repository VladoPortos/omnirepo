-- Quick task 260420-d03-sync-pill-file-count (D-03 closure).
--
-- sync_jobs.files_synced carries the per-job count of newly-added
-- files at sync completion. Written once at end-of-sync via
-- SyncJobsRepo.SetFilesSynced (not through the throttled ProgressWriter
-- path — one write per job, no hot-loop concern). Default 0 covers
-- running jobs and legacy rows pre-dating this column.

ALTER TABLE sync_jobs ADD COLUMN files_synced INTEGER NOT NULL DEFAULT 0;
