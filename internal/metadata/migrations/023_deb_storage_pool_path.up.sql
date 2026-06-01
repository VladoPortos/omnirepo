-- F-T6 · Phase 4 follow-up: deb_packages must remember the actual pool-
-- relative path the client PUT to, not just the basename. Packages.gz
-- previously synthesised `pool/<prefix>/<pkg>/<file>` from package name only,
-- which drops the `<component>` segment (e.g. `main/`) and the
-- source-package folder for Debian's canonical layout. Apt fetched the
-- synthesised URL and got 404.
--
-- storage_pool_path holds the full pool-relative path, e.g.
-- `pool/main/n/nano/nano_6.2-1ubuntu0.1_amd64.deb`. Backfill existing rows
-- with the legacy synthesis so Packages.gz stays consistent with disk until
-- those rows are re-PUT — not a perfect representation but matches exactly
-- what regen used to emit, preserving the pre-migration behaviour.

ALTER TABLE deb_packages ADD COLUMN storage_pool_path TEXT NOT NULL DEFAULT '';

-- Backfill using the legacy prefix rule:
--   prefix = 'lib' + substr(package, 4, 1)  when package LIKE 'lib_'
--   prefix = substr(package, 1, 1)          otherwise
-- This mirrors componentPrefix() in internal/protocol/deb/regen.go.
UPDATE deb_packages
SET storage_pool_path = 'pool/' ||
    CASE
        WHEN package LIKE 'lib_%' AND length(package) >= 4
            THEN substr(package, 1, 4)
        WHEN length(package) = 0
            THEN 'x'
        ELSE substr(package, 1, 1)
    END
    || '/' || package || '/' || filename
WHERE storage_pool_path = '';

CREATE INDEX IF NOT EXISTS idx_deb_packages_pool_path ON deb_packages(repo_id, storage_pool_path);
