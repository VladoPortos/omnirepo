-- Phase 2 migration 004_upstream_creds: project-scoped upstream credential
-- store (D-09, D-10). Pulled forward from Phase 4; Phase 3 SYNC-05 reuses.
-- Encrypted at rest via internal/crypto/aead (AES-GCM-256, master key in
-- settings.upstream_creds_aead_key).

CREATE TABLE upstream_creds (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    host                TEXT NOT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('docker','rpm','apt','pypi','helm')),
    username            TEXT NOT NULL DEFAULT '',
    password_enc        TEXT NOT NULL DEFAULT '',
    token_enc           TEXT NOT NULL DEFAULT '',
    created_by_actor_id INTEGER REFERENCES users(id),
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, host, kind)
);
CREATE INDEX idx_upstream_creds_project ON upstream_creds(project_id);
