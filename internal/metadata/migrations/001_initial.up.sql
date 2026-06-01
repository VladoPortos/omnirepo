-- Initial schema. Per-protocol tables are added in later numbered migrations.

-- schema_migrations is created by the migration runner itself
-- (migrations.ensureMigrationsTable) before any .up.sql runs, so the table
-- exists before this file is applied. It is repeated here as a documentary
-- anchor for the canonical schema — satisfied by this CREATE TABLE IF NOT EXISTS.
CREATE TABLE IF NOT EXISTS schema_migrations (
    name        TEXT PRIMARY KEY,
    applied_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE users (
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
CREATE INDEX idx_users_login ON users(login) WHERE deleted_at IS NULL;

CREATE TABLE sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_prefix  TEXT NOT NULL,
    token_sha256  TEXT NOT NULL,
    issued_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at    TIMESTAMP NOT NULL,
    UNIQUE(token_prefix, token_sha256)
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_lookup ON sessions(token_prefix);

CREATE TABLE projects (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    description_md  TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at      TIMESTAMP
);
CREATE INDEX idx_projects_name ON projects(name) WHERE deleted_at IS NULL;

CREATE TABLE project_members (
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    added_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, user_id)
);

CREATE TABLE api_keys (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_kind       TEXT NOT NULL CHECK(owner_kind IN ('user','project')),
    owner_user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
    owner_project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    token_prefix     TEXT NOT NULL,
    token_sha256     TEXT NOT NULL,
    last_used_at     TIMESTAMP,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at       TIMESTAMP,
    UNIQUE(token_prefix, token_sha256),
    CHECK(
      (owner_kind='user'    AND owner_user_id    IS NOT NULL AND owner_project_id IS NULL) OR
      (owner_kind='project' AND owner_project_id IS NOT NULL AND owner_user_id    IS NULL)
    )
);
CREATE INDEX idx_apikeys_lookup ON api_keys(token_prefix);

CREATE TABLE repos (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id         INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type               TEXT NOT NULL CHECK(type IN ('rpm','deb','pypi','docker','helm','git','raw')),
    name               TEXT NOT NULL,
    description_md     TEXT NOT NULL DEFAULT '',
    auto_scan          BOOLEAN NOT NULL DEFAULT 1,
    block_on_severity  TEXT NOT NULL DEFAULT 'none' CHECK(block_on_severity IN ('none','low','medium','high','critical')),
    public_read        BOOLEAN NOT NULL DEFAULT 0,
    size_bytes         INTEGER NOT NULL DEFAULT 0,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at         TIMESTAMP,
    UNIQUE(project_id, type, name)
);

CREATE TABLE s3_buckets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at  TIMESTAMP
);

CREATE TABLE audit_log (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor_user_id    INTEGER REFERENCES users(id),
    actor_api_key_id INTEGER REFERENCES api_keys(id),
    ip               TEXT NOT NULL DEFAULT '',
    user_agent       TEXT NOT NULL DEFAULT '',
    event_kind       TEXT NOT NULL,
    target_kind      TEXT NOT NULL DEFAULT '',
    target_id        TEXT NOT NULL DEFAULT '',
    outcome          TEXT NOT NULL DEFAULT 'ok',
    details_json     TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_audit_kind   ON audit_log(event_kind, occurred_at);
CREATE INDEX idx_audit_actor  ON audit_log(actor_user_id, occurred_at);
CREATE INDEX idx_audit_target ON audit_log(target_kind, target_id, occurred_at);

CREATE TABLE blob_uploads (
    digest     TEXT PRIMARY KEY,
    started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL
);

CREATE VIRTUAL TABLE repos_fts USING fts5(
    repo_name, project_name, description, type,
    content='', tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE artifacts_fts USING fts5(
    repo_id UNINDEXED, name, version, digest,
    content='', tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE cves_fts USING fts5(
    cve_id, package, summary,
    content='', tokenize='unicode61 remove_diacritics 2'
);
