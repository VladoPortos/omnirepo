-- Composite index for the "latest scan per artifact" correlated subquery
-- used by every listXContent handler in internal/api/repo_content.go. The
-- subquery does `SELECT MAX(id) FROM scans WHERE repo_id=? AND
-- artifact_kind=? AND artifact_id=?`; without this index, SQLite falls
-- back to a full scan of the scans table once per content row, which
-- quadratically degrades repos with thousands of artifacts.
CREATE INDEX IF NOT EXISTS idx_scans_repo_kind_artifact_id
    ON scans(repo_id, artifact_kind, artifact_id, id);
