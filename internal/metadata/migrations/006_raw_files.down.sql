-- Phase 2 migration 006_raw_files — down.
DROP INDEX IF EXISTS idx_raw_files_modified;
DROP TABLE IF EXISTS raw_files;
