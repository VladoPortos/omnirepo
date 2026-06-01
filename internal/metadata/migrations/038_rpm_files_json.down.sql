-- 038_rpm_files_json.down.sql
-- Reverses 038. modernc.org/sqlite wraps SQLite 3.51 which supports
-- ALTER TABLE ... DROP COLUMN.
ALTER TABLE rpm_packages DROP COLUMN files_json;
