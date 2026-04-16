DROP TABLE IF EXISTS trivy_db_meta;
DELETE FROM settings WHERE key = 'maintenance_mode';
