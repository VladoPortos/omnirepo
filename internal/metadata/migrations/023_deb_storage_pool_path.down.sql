DROP INDEX IF EXISTS idx_deb_packages_pool_path;
-- SQLite pre-3.35 couldn't drop columns; modernc.org/sqlite wraps 3.51 which
-- supports ALTER TABLE ... DROP COLUMN. Safe to roll back.
ALTER TABLE deb_packages DROP COLUMN storage_pool_path;
