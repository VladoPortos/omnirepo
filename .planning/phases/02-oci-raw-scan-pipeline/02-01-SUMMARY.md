---
phase: 02-oci-raw-scan-pipeline
plan: 01
subsystem: metadata
tags: [sqlite, fts5, oci, scans, migrations, leases]
requires:
  - internal/metadata/db.go
  - internal/metadata/migrations/001_initial.up.sql
  - internal/metadata/sqlitetest/sqlitetest.go
provides:
  - internal/metadata/migrations/002_jobs.up.sql
  - internal/metadata/migrations/003_oci.up.sql
  - internal/metadata/migrations/004_upstream_creds.up.sql
  - internal/metadata/migrations/005_fts_contentless_delete.up.sql
  - internal/metadata.DockerBlobsRepo
  - internal/metadata.DockerManifestsRepo
  - internal/metadata.DockerTagsRepo
  - internal/metadata.BlobUploadSessionsRepo
  - internal/metadata.SyncJobsRepo
  - internal/metadata.ScansRepo
  - internal/metadata.VulnerabilitiesRepo
  - internal/metadata.IndexRepo
  - internal/metadata.DeleteRepoFTS
  - internal/metadata.IndexArtifact
  - internal/metadata.IndexArtifactDelete
  - internal/metadata.IndexVulnerability
  - internal/metadata.DeleteVulnerabilitiesByScan
affects:
  - internal/metadata/migrations/001_initial.up.sql (FTS5 schema superseded by 005)
tech-stack:
  added: []
  patterns:
    - "UPDATE ... RETURNING single-statement lease (SQLite 3.35+)"
    - "BLOB columns for byte-identical manifest round-trip"
    - "Contentful FTS5 tables for standard DELETE/SELECT semantics"
key-files:
  created:
    - internal/metadata/migrations/002_jobs.up.sql
    - internal/metadata/migrations/002_jobs.down.sql
    - internal/metadata/migrations/003_oci.up.sql
    - internal/metadata/migrations/003_oci.down.sql
    - internal/metadata/migrations/004_upstream_creds.up.sql
    - internal/metadata/migrations/004_upstream_creds.down.sql
    - internal/metadata/migrations/005_fts_contentless_delete.up.sql
    - internal/metadata/migrations/005_fts_contentless_delete.down.sql
    - internal/metadata/docker_blobs.go
    - internal/metadata/docker_blobs_test.go
    - internal/metadata/docker_manifests.go
    - internal/metadata/docker_manifests_test.go
    - internal/metadata/docker_tags.go
    - internal/metadata/docker_tags_test.go
    - internal/metadata/blob_upload_sessions.go
    - internal/metadata/blob_upload_sessions_test.go
    - internal/metadata/sync_jobs.go
    - internal/metadata/sync_jobs_test.go
    - internal/metadata/scans.go
    - internal/metadata/scans_test.go
    - internal/metadata/vulnerabilities.go
    - internal/metadata/vulnerabilities_test.go
    - internal/metadata/fts.go
    - internal/metadata/fts_test.go
  modified:
    - internal/metadata/migrations/runner_test.go (added phase 2 tests)
decisions:
  - "FTS5 tables rebuilt without content='' (migration 005): content='' forbids DELETE and column SELECT, blocking D-40 inline-write helpers. Cost: minor text duplication on low-volume tables; benefit: standard SQL works."
  - "DockerTagsRepo.Upsert uses SELECT-then-INSERT-OR-REPLACE (not RETURNING): SQLite FTS/ON CONFLICT RETURNING semantics don't cleanly surface the old value on conflict; the two-step form is provably correct."
  - "VulnerabilitiesRepo.InsertBatch enforces a caller-supplied cap (T-02-01-06) rather than a hard-coded one so tests and scan handler can choose the right limit."
  - "Manifest Insert probes for existing row before INSERT so same-digest/same-body is idempotent but same-digest/different-body returns ErrManifestDigestConflict."
  - "DockerBlobsRepo.DecRef uses UPDATE ... WHERE ref_count > 0 + rows-affected check to guard T-02-01-03 (no silent underflow)."
metrics:
  duration: ~25m
  tasks: 3
  files: 24 created, 1 modified
  completed: 2026-04-15
---

# Phase 2 Plan 01: Schema Migrations + Typed Repos + FTS5 Helpers — Summary

JWT-less phase 2 data plane: three SQLite migrations (jobs, OCI, upstream_creds), seven typed repos with atomic lease/refcount semantics, and FTS5 write helpers that run inline with base-table mutations (D-40).

## Column Ordering (final)

Chosen to match plan action block verbatim. Notable choices:

- `sync_jobs.log` placed after `next_run_at` (per D-17) so SYNC-06 capture is colocated with other state/telemetry columns, not wedged between mutable-state fields.
- `scans` splits timing into `leased_at`, `started_at`, `finished_at`, `next_run_at` — four distinct phases instead of folding them; makes retry/recover queries unambiguous.
- `vulnerabilities` carries the full text fields (`title`, `description`) inline; no join to a separate CVE dictionary. Phase 5 can project a read-only dictionary view on top if needed.
- `upstream_creds` stores `password_enc` AND `token_enc` as separate columns (not one opaque `secret_enc`) so the OCI pull client can pick the right auth mode without a schema round-trip.

## Indexes

All per plan + two partial indexes on `status='pending'` and `status='running'` for the dispatcher's two hot paths (pending lease, stale-running sweep). `idx_scans_artifact` carries `finished_at` as the last key so `LatestForArtifact` is an index-only range scan.

## Lease Test Result

`TestSyncJobsLeaseRace`: 8 goroutines, 1 seeded pending row, concurrent `LeaseOne` calls. Observed **exactly 1 winner** across every run (including `-race`). modernc/sqlite + writer pool size 1 + `_txlock=immediate` + single-statement `UPDATE ... RETURNING` together guarantee atomicity without any explicit SELECT-then-UPDATE window.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking] FTS5 `content=''` forbade DELETE and column SELECT**

- **Found during:** Task 3 (fts.go tests).
- **Issue:** 001_initial.up.sql created the three FTS5 virtual tables with `content=''` (external content disabled). That option makes `DELETE FROM repos_fts WHERE rowid=?` a no-op and makes `SELECT col FROM fts WHERE MATCH ...` return NULL for indexed columns. DeleteRepoFTS, IndexArtifactDelete, and the orphan-sweep in DeleteVulnerabilitiesByScan are all defined as `DELETE FROM ...` — they could not work against content='' tables.
- **Fix:** Added migration 005_fts_contentless_delete (up + down): drops and recreates the three FTS5 tables without `content=''`. Tokenizer and column layout unchanged. Cost is small text duplication on low-volume tables (<1M rows target); benefit is that the D-40 helpers work with standard SQL and tests can read back column values.
- **Files modified:** internal/metadata/migrations/005_fts_contentless_delete.{up,down}.sql
- **Commit:** dbc079a

**2. [Rule 1 — Bug] COALESCE(finished_at, created_at) failed modernc TIMESTAMP decode**

- **Found during:** Task 2 (scans tests).
- **Issue:** modernc/sqlite returns COALESCE'd TIMESTAMP values as TEXT, failing `time.Time` scan with `"unsupported Scan, storing driver.Value type string into type *time.Time"`.
- **Fix:** Drop the COALESCE; `LatestForArtifact` filters to `status='done'` so `finished_at` is always set. Scan into `sql.NullTime` for safety on an unfinished row (shouldn't happen but harmless guard).
- **Files modified:** internal/metadata/scans.go
- **Commit:** 0dc1f7b

### D-17/D-09 Adherence

No deviations from D-17 (job row columns) or D-09 (upstream_creds schema). Column names, types, CHECK constraints, UNIQUE constraints, and index set match the action block verbatim.

## Test Evidence

- `go test -mod=vendor -race -count=1 ./internal/metadata/...` — PASS (including TestSyncJobsLeaseRace, TestPhase2MigrationsApply, TestSyncJobsLeaseReturning, TestManifestBodyByteIdentical, TestDockerBlobs_DecRefUnderflow, TestVulnerabilities_InsertBatchCapEnforced, TestFTS_DeleteVulnerabilitiesByScan_CascadesFTSOnOrphansOnly, TestFTS_IndexVulnerability_SQLInjectionShaped).
- `go build -mod=vendor ./...` — clean.
- Full repo `go test -mod=vendor -race -count=1 ./...` — all Phase 1 tests remain green (no regressions in auth, storage, audit, tls, api, app).

## Threat Model Coverage

| Threat ID | Mitigation shipped |
|-----------|--------------------|
| T-02-01-01 | `body BLOB NOT NULL` + TestManifestBodyByteIdentical (byte-for-byte round-trip asserted) |
| T-02-01-02 | Single-statement `UPDATE ... RETURNING` leases + TestSyncJobsLeaseRace (8 goroutines, 1 winner) |
| T-02-01-03 | `DecRef WHERE ref_count > 0` + rows-affected guard + ErrRefCountUnderflow + TestDockerBlobs_DecRefUnderflow |
| T-02-01-04 | upstream_creds schema shipped; REST projections that hide `*_enc` belong to Phase 02-02 (noted in decisions) |
| T-02-01-05 | Parameterized exec in all FTS helpers + TestFTS_IndexVulnerability_SQLInjectionShaped (payload stays data) |
| T-02-01-06 | VulnerabilitiesRepo.InsertBatch cap parameter + TestVulnerabilities_InsertBatchCapEnforced (11 rows, cap=10 → ErrVulnBatchTooLarge, 0 rows after tx rollback) |

## Commits

- bd2a7a6 `test(02-01): add failing tests for phase 2 migrations (002/003/004)` (RED)
- 42288b5 `feat(02-01): add phase 2 migrations (jobs, oci, upstream_creds)` (GREEN — migrations)
- 0dc1f7b `feat(02-01): add typed repos for phase 2 tables (D-02, D-15, D-17)`
- dbc079a `feat(02-01): add FTS5 inline-write helpers (D-40, SRCH-01)`

## Self-Check

All claimed created files exist (24 new files verified via `ls`). All claimed commit hashes resolvable via `git log --oneline`. Full `go test -race` suite green.

## Self-Check: PASSED
