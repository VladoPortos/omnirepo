-- Migration 013_helm_charts:
--
-- One row per Helm chart .tgz. index.yaml regeneration walks rows by
-- repo_id. keywords_json / maintainers_json are canonical JSON strings so
-- index.yaml can emit them without re-parsing the chart.

CREATE TABLE helm_charts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id         INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    version         TEXT    NOT NULL,
    app_version     TEXT    NOT NULL DEFAULT '',
    description     TEXT    NOT NULL DEFAULT '',
    keywords_json   TEXT    NOT NULL DEFAULT '[]',
    maintainers_json TEXT   NOT NULL DEFAULT '[]',
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    digest          TEXT    NOT NULL,
    filename        TEXT    NOT NULL,
    uploaded_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(repo_id, name, version)
);
CREATE INDEX idx_helm_charts_repo ON helm_charts(repo_id);
CREATE INDEX idx_helm_charts_digest ON helm_charts(digest);
