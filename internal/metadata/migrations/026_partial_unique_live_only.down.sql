-- 026_partial_unique_live_only.down.sql (F-7 rollback)
--
-- Reverse of 026: restore the table-level UNIQUE on login/name and drop the
-- new partial UNIQUE indexes. Note: this rollback can fail if two or more
-- rows share a login/name once soft-delete timestamps are ignored, because
-- that state is exactly what the up migration makes representable — if a
-- user has re-created a previously deleted login between up and down, the
-- INSERT SELECT below will violate UNIQUE. Operators need to clean up
-- collisions manually before rolling back.

-- The runner flips foreign_keys OFF on the connection before BEGIN and
-- back ON after COMMIT, so no PRAGMA is needed inline here.

-- --- users ---------------------------------------------------------------
DROP INDEX IF EXISTS idx_users_login;
CREATE TABLE users_old (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    login                    TEXT NOT NULL UNIQUE,
    email                    TEXT NOT NULL,
    avatar_seed              TEXT NOT NULL DEFAULT '',
    password_hash            TEXT NOT NULL,
    is_super_admin           BOOLEAN NOT NULL DEFAULT 0,
    must_change_password     BOOLEAN NOT NULL DEFAULT 0,
    password_changed_at      TIMESTAMP,
    created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at               TIMESTAMP
);
INSERT INTO users_old (id, login, email, avatar_seed, password_hash, is_super_admin,
                       must_change_password, password_changed_at, created_at, deleted_at)
SELECT id, login, email, avatar_seed, password_hash, is_super_admin,
       must_change_password, password_changed_at, created_at, deleted_at
FROM users;
DROP TABLE users;
ALTER TABLE users_old RENAME TO users;
CREATE INDEX idx_users_login ON users(login) WHERE deleted_at IS NULL;

-- --- projects ------------------------------------------------------------
DROP INDEX IF EXISTS idx_projects_name;
CREATE TABLE projects_old (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    description_md  TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at      TIMESTAMP
);
INSERT INTO projects_old (id, name, description_md, created_at, deleted_at)
SELECT id, name, description_md, created_at, deleted_at FROM projects;
DROP TABLE projects;
ALTER TABLE projects_old RENAME TO projects;
CREATE INDEX idx_projects_name ON projects(name) WHERE deleted_at IS NULL;

-- --- s3_buckets ----------------------------------------------------------
DROP INDEX IF EXISTS idx_s3_buckets_name;
CREATE TABLE s3_buckets_old (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at  TIMESTAMP
);
INSERT INTO s3_buckets_old (id, name, project_id, created_at, deleted_at)
SELECT id, name, project_id, created_at, deleted_at FROM s3_buckets;
DROP TABLE s3_buckets;
ALTER TABLE s3_buckets_old RENAME TO s3_buckets;
