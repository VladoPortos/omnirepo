-- 028_api_keys_live_name_unique.down.sql (F-03.4)
DROP INDEX IF EXISTS idx_apikeys_user_live_name;
DROP INDEX IF EXISTS idx_apikeys_project_live_name;
