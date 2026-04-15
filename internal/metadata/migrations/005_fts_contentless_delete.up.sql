-- Phase 2 migration 005_fts_contentless_delete (Rule 3 deviation):
--
-- 001_initial created the FTS5 virtual tables with content='' (external
-- content disabled). That option forbids `DELETE FROM fts WHERE ...` and
-- standard SELECT column reads return NULL for indexed columns — which
-- blocks the D-40 inline-write helpers (DeleteRepoFTS,
-- IndexArtifactDelete, DeleteVulnerabilitiesByScan) from doing their job.
--
-- Fix: drop+recreate the three FTS5 tables without content='' so they
-- own their text and standard DELETE/SELECT work. Cost is a small
-- duplication of repo/artifact/CVE text fields; these are low-volume
-- (<1M rows target) and FTS5 is already designed to own its index.
-- Tokenizer stays unicode61+diacritics per D-41.
--
-- The tables are populated from scratch during Phase 2 operations, so no
-- data migration is required at this point (Phase 1 seed paths don't
-- write to FTS5 yet).

DROP TABLE IF EXISTS repos_fts;
DROP TABLE IF EXISTS artifacts_fts;
DROP TABLE IF EXISTS cves_fts;

CREATE VIRTUAL TABLE repos_fts USING fts5(
    repo_name, project_name, description, type,
    tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE artifacts_fts USING fts5(
    repo_id UNINDEXED, name, version, digest,
    tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE cves_fts USING fts5(
    cve_id, package, summary,
    tokenize='unicode61 remove_diacritics 2'
);
