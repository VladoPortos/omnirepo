-- Reverse dependency order of 001_initial.up.sql. .down files are required
-- even though they are not currently run.

DROP TABLE IF EXISTS cves_fts;
DROP TABLE IF EXISTS artifacts_fts;
DROP TABLE IF EXISTS repos_fts;

DROP TABLE IF EXISTS blob_uploads;

DROP INDEX IF EXISTS idx_audit_target;
DROP INDEX IF EXISTS idx_audit_actor;
DROP INDEX IF EXISTS idx_audit_kind;
DROP TABLE IF EXISTS audit_log;

DROP TABLE IF EXISTS s3_buckets;

DROP TABLE IF EXISTS repos;

DROP INDEX IF EXISTS idx_apikeys_lookup;
DROP TABLE IF EXISTS api_keys;

DROP TABLE IF EXISTS project_members;

DROP INDEX IF EXISTS idx_projects_name;
DROP TABLE IF EXISTS projects;

DROP INDEX IF EXISTS idx_sessions_lookup;
DROP INDEX IF EXISTS idx_sessions_user;
DROP TABLE IF EXISTS sessions;

DROP INDEX IF EXISTS idx_users_login;
DROP TABLE IF EXISTS users;

DROP TABLE IF EXISTS settings;

DROP TABLE IF EXISTS schema_migrations;
