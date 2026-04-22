-- 028_api_keys_live_name_unique.up.sql (F-03.4)
--
-- The api_keys table identifies rows by their numeric id, but the
-- profile / admin UIs identify keys by name — "alice-dev", "ci-prod",
-- etc. The app layer rejects duplicate live names via a list-then-
-- insert guard (internal/api/apikeys.go, project_apikeys.go), but two
-- concurrent POSTs can both read "no duplicate" and both insert. Add
-- partial unique indexes so the DB enforces live-name uniqueness
-- per-owner. Revoked keys (revoked_at IS NOT NULL) are excluded so
-- rotation ("create new, revoke old") never needs a rename.
--
-- One index per owner kind because the ownership columns are nullable
-- by schema design (exactly one is non-null per row — CHECK in the
-- original table).

CREATE UNIQUE INDEX IF NOT EXISTS idx_apikeys_user_live_name
  ON api_keys(owner_user_id, name)
  WHERE owner_kind='user' AND revoked_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_apikeys_project_live_name
  ON api_keys(owner_project_id, name)
  WHERE owner_kind='project' AND revoked_at IS NULL;
