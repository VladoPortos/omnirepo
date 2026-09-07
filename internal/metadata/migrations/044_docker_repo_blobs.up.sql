-- CAS deduplication is global; access to its bytes is repository scoped.
CREATE TABLE docker_repo_blobs (
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    digest TEXT NOT NULL REFERENCES docker_blobs(digest) ON DELETE CASCADE,
    PRIMARY KEY(repo_id, digest)
);
CREATE INDEX idx_docker_repo_blobs_digest ON docker_repo_blobs(digest);

-- Only actual stored image manifest config/layer references establish legacy
-- ownership. Never grant every repository access to the global CAS inventory.
-- Cast BLOB JSON to TEXT for SQLite JSON implementations that reject BLOBs.
INSERT OR IGNORE INTO docker_repo_blobs(repo_id, digest)
SELECT m.repo_id, b.digest
FROM docker_manifests m JOIN docker_blobs b
ON b.digest = json_extract(CASE WHEN json_valid(CAST(m.body AS TEXT))
    THEN CAST(m.body AS TEXT) ELSE '{}' END, '$.config.digest')
WHERE json_type(CASE WHEN json_valid(CAST(m.body AS TEXT))
    THEN CAST(m.body AS TEXT) ELSE '{}' END, '$.manifests') IS NOT 'array';
INSERT OR IGNORE INTO docker_repo_blobs(repo_id, digest)
SELECT m.repo_id, b.digest
FROM docker_manifests m,
json_each(CASE WHEN json_valid(CAST(m.body AS TEXT))
    THEN CAST(m.body AS TEXT) ELSE '{}' END, '$.layers') l
JOIN docker_blobs b ON b.digest = json_extract(CASE WHEN l.type='object' THEN l.value ELSE '{}' END, '$.digest')
WHERE json_type(CASE WHEN json_valid(CAST(m.body AS TEXT))
    THEN CAST(m.body AS TEXT) ELSE '{}' END, '$.manifests') IS NOT 'array'
AND json_type(CASE WHEN json_valid(CAST(m.body AS TEXT))
    THEN CAST(m.body AS TEXT) ELSE '{}' END, '$.layers') = 'array';
