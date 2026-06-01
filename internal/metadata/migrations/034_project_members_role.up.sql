-- 034_project_members_role.up.sql
-- Maintainer/Viewer RBAC split.
-- Adds a role column to project_members and a nullable role
-- column to api_keys. Backfills existing rows.

-- Step 1: project_members.role.
-- DEFAULT 'maintainer' auto-backfills every existing row.
-- New inserts pass role explicitly at application layer (default 'viewer').
ALTER TABLE project_members
  ADD COLUMN role TEXT NOT NULL DEFAULT 'maintainer'
  CHECK(role IN ('maintainer','viewer'));

-- Step 2: api_keys.role.
-- NULL          = user-owned key (inherits user's runtime role from membership map).
-- 'maintainer' or 'viewer' = project-owned key (minted role wins at request time).
-- NOTE: cross-column CHECK (owner_kind='project' requires non-NULL role) is
-- NOT expressible in ADD COLUMN due to a SQLite limitation.
-- The application layer enforces this invariant in MintProjectAPIKey.
ALTER TABLE api_keys
  ADD COLUMN role TEXT DEFAULT NULL
  CHECK(role IS NULL OR role IN ('maintainer','viewer'));

-- Step 3: backfill existing project-owned keys to 'maintainer' —
-- preserves v1.4 behaviour where every project-scoped key could publish.
UPDATE api_keys SET role = 'maintainer'
WHERE owner_kind = 'project';
