-- 026_partial_unique_live_only.up.sql (F-7)
--
-- The initial schema put soft-delete columns on users, projects, and
-- s3_buckets but kept the *table-level* UNIQUE(login) / UNIQUE(name)
-- constraints in place. That meant a row soft-deleted with
-- deleted_at != NULL still held the login/name slot, so re-creating a
-- user/project/bucket with the same name came back 409 "login exists"
-- even though the resource was invisible to every admin UI and REST
-- endpoint — effectively leaking state with no path to see or purge it.
--
-- Fix: rebuild those three tables without the table-level UNIQUE and
-- restore uniqueness via partial indexes that exclude soft-deleted rows
-- (users + projects already had a non-unique partial index; we drop and
-- recreate it as UNIQUE. s3_buckets only had the table-level UNIQUE;
-- we add the matching partial UNIQUE index).
--
-- Strategy: SQLite has no ALTER TABLE DROP CONSTRAINT, so we use the
-- standard rebuild recipe — CREATE TABLE new, INSERT SELECT preserving
-- row ids, DROP old, RENAME new. FK references (sessions → users,
-- project_members → users/projects, api_keys → users/projects, repos →
-- projects, audit_log → users, s3_objects → s3_buckets, …) all point at
-- numeric ids that we preserve verbatim. The runner sets
-- `PRAGMA foreign_keys=OFF` on the connection before BEGIN and restores
-- it after COMMIT, which is the canonical SQLite recipe for table
-- rebuilds — `defer_foreign_keys=ON` is not sufficient here because the
-- intermediate DROP→RENAME state trips the commit-time check even when
-- every id resolves cleanly. Restoring `foreign_keys=ON` afterwards
-- implicitly runs foreign_key_check, catching any bad data we left.

-- --- users ---------------------------------------------------------------
CREATE TABLE users_new (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    login                    TEXT NOT NULL,
    email                    TEXT NOT NULL,
    avatar_seed              TEXT NOT NULL DEFAULT '',
    password_hash            TEXT NOT NULL,
    is_super_admin           BOOLEAN NOT NULL DEFAULT 0,
    must_change_password     BOOLEAN NOT NULL DEFAULT 0,
    password_changed_at      TIMESTAMP,
    created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at               TIMESTAMP
);
INSERT INTO users_new (id, login, email, avatar_seed, password_hash, is_super_admin,
                       must_change_password, password_changed_at, created_at, deleted_at)
SELECT id, login, email, avatar_seed, password_hash, is_super_admin,
       must_change_password, password_changed_at, created_at, deleted_at
FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
CREATE UNIQUE INDEX idx_users_login ON users(login) WHERE deleted_at IS NULL;

-- --- projects ------------------------------------------------------------
CREATE TABLE projects_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    description_md  TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at      TIMESTAMP
);
INSERT INTO projects_new (id, name, description_md, created_at, deleted_at)
SELECT id, name, description_md, created_at, deleted_at FROM projects;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;
CREATE UNIQUE INDEX idx_projects_name ON projects(name) WHERE deleted_at IS NULL;

-- --- s3_buckets ----------------------------------------------------------
CREATE TABLE s3_buckets_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at  TIMESTAMP
);
INSERT INTO s3_buckets_new (id, name, project_id, created_at, deleted_at)
SELECT id, name, project_id, created_at, deleted_at FROM s3_buckets;
DROP TABLE s3_buckets;
ALTER TABLE s3_buckets_new RENAME TO s3_buckets;
CREATE UNIQUE INDEX idx_s3_buckets_name ON s3_buckets(name) WHERE deleted_at IS NULL;
