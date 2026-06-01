-- Phase 5: maintenance mode flag + Trivy DB metadata tracking.
-- maintenance_mode lives in settings (key='maintenance_mode', value='true'/'false').
-- Trivy DB metadata tracked in a dedicated table for history.

CREATE TABLE IF NOT EXISTS trivy_db_meta (
    id          INTEGER PRIMARY KEY,
    version     TEXT    NOT NULL DEFAULT '',
    source      TEXT    NOT NULL DEFAULT 'baked-in' CHECK(source IN ('baked-in','uploaded','online-pulled')),
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    applied_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    applied_by  INTEGER REFERENCES users(id)
);

-- Seed the initial maintenance_mode setting as 'false' if it doesn't exist.
INSERT OR IGNORE INTO settings(key, value) VALUES ('maintenance_mode', 'false');
