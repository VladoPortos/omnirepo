-- Reverse 005: drop and restore the original content='' FTS5 tables.
DROP TABLE IF EXISTS repos_fts;
DROP TABLE IF EXISTS artifacts_fts;
DROP TABLE IF EXISTS cves_fts;

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
