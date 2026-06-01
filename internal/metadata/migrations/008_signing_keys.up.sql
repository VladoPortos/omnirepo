-- Migration 008_signing_keys:
--
-- Per-repo OpenPGP signing keypair store. private_enc is AES-GCM-encrypted
-- via internal/crypto/aead.go. One row per (repo_id, scope='repo')
-- — every APT/RPM repo owns its own RSA-4096 key. Only
-- SigningKeysRepo.LookupPrivate decrypts; SigningKeyMeta never carries
-- private bytes.

CREATE TABLE signing_keys (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id         INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    scope           TEXT    NOT NULL CHECK (scope IN ('repo')),
    key_kind        TEXT    NOT NULL CHECK (key_kind IN ('gpg_rsa4096')),
    public_armored  TEXT    NOT NULL,
    private_enc     BLOB    NOT NULL,
    fingerprint     TEXT    NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(repo_id, scope)
);
CREATE INDEX idx_signing_keys_fingerprint ON signing_keys(fingerprint);
