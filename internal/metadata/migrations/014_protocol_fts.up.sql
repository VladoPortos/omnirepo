-- Phase 3 migration 014_protocol_fts (Plan 03-01, D-27):
--
-- Four FTS5 virtual tables, one per package protocol. Shared column shape
-- (name/version/arch_or_runtime/summary) so a single SRCH-02 query can
-- UNION across them. repo_id is UNINDEXED (used only for scope filtering).
-- Inserts and deletes run inside the writer tx for strong read-after-write
-- consistency (no async indexer).

CREATE VIRTUAL TABLE rpm_fts USING fts5(
    repo_id UNINDEXED, name, version, arch_or_runtime, summary,
    tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE deb_fts USING fts5(
    repo_id UNINDEXED, name, version, arch_or_runtime, summary,
    tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE pypi_fts USING fts5(
    repo_id UNINDEXED, name, version, arch_or_runtime, summary,
    tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE helm_fts USING fts5(
    repo_id UNINDEXED, name, version, arch_or_runtime, summary,
    tokenize='unicode61 remove_diacritics 2'
);
