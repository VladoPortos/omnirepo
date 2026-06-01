-- Migration 003_oci: OCI/Docker data plane.
-- docker_blobs is the CAS refcount table; docker_manifests stores manifest
-- bodies byte-for-byte (BLOB) so docker-content-digest survives round-trip.

CREATE TABLE docker_blobs (
    digest          TEXT PRIMARY KEY,
    size_bytes      INTEGER NOT NULL,
    ref_count       INTEGER NOT NULL DEFAULT 0,
    last_touched_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_docker_blobs_gc ON docker_blobs(ref_count, last_touched_at);

-- docker_manifests: (repo_id, digest) identity; body is BLOB.
CREATE TABLE docker_manifests (
    digest      TEXT NOT NULL,
    repo_id     INTEGER NOT NULL REFERENCES repos(id),
    media_type  TEXT NOT NULL,
    body        BLOB NOT NULL,
    size_bytes  INTEGER NOT NULL,
    ref_count   INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, digest)
);
CREATE INDEX idx_docker_manifests_digest ON docker_manifests(digest);

-- docker_tags: mutable pointer (repo_id, tag) -> digest.
CREATE TABLE docker_tags (
    repo_id     INTEGER NOT NULL REFERENCES repos(id),
    tag         TEXT NOT NULL,
    digest      TEXT NOT NULL,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, tag)
);
CREATE INDEX idx_docker_tags_digest ON docker_tags(repo_id, digest);

-- blob_upload_sessions: per-uuid chunked-upload state (distinct from the
-- digest-keyed blob_uploads registry in 001_initial which is the GC
-- exclusion set). Pruned by the GC sweep past expires_at.
CREATE TABLE blob_upload_sessions (
    uuid          TEXT PRIMARY KEY,
    repo_id       INTEGER NOT NULL REFERENCES repos(id),
    bytes_so_far  INTEGER NOT NULL DEFAULT 0,
    started_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_patch_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at    TIMESTAMP NOT NULL
);
CREATE INDEX idx_blob_upload_sessions_expiry ON blob_upload_sessions(expires_at);
