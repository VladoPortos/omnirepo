-- 038_rpm_files_json.up.sql
--
-- H-7: filelists.xml was always emitted with zero <file> entries because the
-- per-package file index was parsed at upload but never persisted, and regen
-- rebuilds package metadata from the DB. dnf needs filelists.xml to resolve
-- file-based dependencies (Requires: /bin/sh, /usr/bin/python3, ...).
--
-- Store the parsed file list as a JSON array on the package row. It is read
-- wholesale per package by the repodata regen, never queried per-file, so a
-- JSON column is sufficient (no normalized side table needed).
ALTER TABLE rpm_packages
  ADD COLUMN files_json TEXT NOT NULL DEFAULT '[]';
