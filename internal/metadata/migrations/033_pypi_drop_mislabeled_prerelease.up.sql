-- Drop pypi_files + pypi_fts rows whose stored
-- version doesn't start with a digit. Per PEP 440 every version begins
-- with a digit (epoch `N!` or release segment `N.N...`), so a
-- non-digit-led version is unambiguously the product of the pre-fix
-- LastIndex("-") sdist parser mis-attributing a dashed pre-release
-- suffix (`foo-1.0.0-rc1.tar.gz` landed in pypi_files as
-- version="rc1"). Only sdists are affected — the wheel code-path used
-- SplitN and identified the version slot correctly.
--
-- Recovery model: DELETE here + mark affected repos dirty so the next
-- sync re-inserts the rows with the fixed parseSdistFilename parser.
-- The on-disk artefact under {proj}/pypi/{repo}/packages/{filename} is
-- NOT touched — next sync's PathStore.Put overwrites it byte-for-byte.
-- FindByFilename idempotency check returns nil once the row is gone, so
-- the download path runs exactly once per affected file.

-- Mark every repo that owns at least one affected row as dirty so the
-- regen coalescer refreshes the PEP 503 simple-index HTML. Must run
-- BEFORE the DELETE below — after the DELETE the WHERE clause has
-- nothing to correlate on.
UPDATE repos SET metadata_state = 'dirty'
WHERE id IN (
    SELECT DISTINCT repo_id FROM pypi_files
    WHERE kind = 'sdist'
      AND version NOT GLOB '[0-9]*'
);

-- FTS5 companion rows first (pypi_fts is keyed on name/version, not on
-- pypi_files.id). Correlate strictly to sdist rows we're about to drop
-- so the DELETE doesn't clobber wheel FTS entries that happen to carry
-- a non-digit-led version, restricting to kind='sdist' via EXISTS
-- correlation.
DELETE FROM pypi_fts
WHERE rowid IN (
    SELECT fts.rowid FROM pypi_fts fts
    WHERE EXISTS (
        SELECT 1 FROM pypi_files pf
        WHERE pf.repo_id = fts.repo_id
          AND pf.project_normalized = fts.name
          AND pf.version = fts.version
          AND pf.kind = 'sdist'
          AND pf.version NOT GLOB '[0-9]*'
    )
);

DELETE FROM pypi_files
WHERE kind = 'sdist'
  AND version NOT GLOB '[0-9]*';
