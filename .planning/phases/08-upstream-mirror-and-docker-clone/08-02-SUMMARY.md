---
phase: 08-upstream-mirror-and-docker-clone
plan: 02
subsystem: backend
tags: [backend, progress, jobs, protocols, openapi]
requires: [phase-08-plan-01-mirror-backend-foundation]
provides:
  - jobs-progress-writer
  - jobs-shared-counting-reader
  - sync-jobs-endpoint-progress-fields
  - oci-pull-external-byte-progress
  - apt-sync-byte-progress
  - rpm-sync-byte-progress
  - pypi-sync-byte-progress
  - helm-sync-step-progress
affects:
  - internal/jobs (new ProgressWriter + CountingReader)
  - internal/api (openapi.yaml + types_gen.go + repos_list.go handler)
  - internal/protocol/{oci,deb,rpm,pypi,helm} (handler instrumentation)
  - internal/app (sync handler wiring + jobID thread-through)
tech-stack:
  added: []
  patterns:
    - "Throttled SQL writer via (last-values + min-interval) gate — persist only if changed AND >=200ms since last persist (D-12)"
    - "Shared jobs.CountingReader with explicit n>0 guard prevents spurious progress.Set emits from 0-byte reads (T-08-02-04)"
    - "Two-phase collect-then-iterate in APT/RPM/PyPI/Helm handlers: first pass builds totalBytes from upstream metadata, second pass downloads with byte-level progress"
    - "atomic.AddInt64 on a shared accumulatedDone counter lets parallel download goroutines advance progress_bytes without a mutex"
    - "Set() updates in-memory lastStep/lastDone/lastTotal even when throttle-suppressed; Flush() on handler exit persists the caller's most-recent intended triple"
    - "Pre-existing local countingReader types avoided — all 5 protocols import the shared jobs.CountingReader"
key-files:
  created:
    - internal/jobs/progress.go
    - internal/jobs/progress_test.go
    - internal/jobs/counting_reader.go
    - internal/jobs/counting_reader_test.go
    - internal/api/repos_list_test.go
    - internal/protocol/deb/sync_progress_test.go
    - internal/protocol/rpm/sync_progress_test.go
    - internal/protocol/pypi/sync_progress_test.go
    - internal/protocol/helm/sync_progress_test.go
  modified:
    - internal/api/openapi.yaml
    - internal/api/types_gen.go
    - internal/api/repos_list.go
    - internal/app/app.go
    - internal/app/phase3_sync.go
    - internal/protocol/oci/pull_external.go
    - internal/protocol/oci/pull_external_test.go
    - internal/protocol/deb/sync_handler.go
    - internal/protocol/rpm/sync_handler.go
    - internal/protocol/rpm/sync_handler_test.go
    - internal/protocol/pypi/sync_handler.go
    - internal/protocol/helm/sync_handler.go
    - .planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md
decisions:
  - "SyncProgressRepo interface defined locally in internal/jobs (mirrors jobs.LeaseRepo pattern) rather than importing metadata.SyncJobsRepo directly — lets tests wire fakes without pulling a full metadata.DB, and metadata.SyncJobsRepo satisfies the interface statically (compile-time check via _ = jobs.SyncProgressRepo((*metadata.SyncJobsRepo)(nil)))."
  - "ProgressWriter.Set() updates in-memory lastStep/lastDone/lastTotal even on a throttle-suppressed DB write. Without this, a sync finishing within 200 ms would Flush the stale non-terminal step (e.g. 'layer 1 of 7') instead of the 'done' sentinel the handler emitted just before return."
  - "Progress tests live with the handler they exercise: internal/api/repos_list_test.go (not admin_jobs_test.go, which tests a different endpoint) and internal/protocol/{deb,rpm,pypi,helm}/sync_progress_test.go (dedicated file per protocol so they don't collide with the existing smoke tests)."
  - "Two-phase orchestration in APT/RPM/PyPI/Helm handlers: the collectFn pass filters + sums totalBytes BEFORE any download begins. Preserves v1.0 idempotency-by-digest semantics (already-present rows don't inflate the denominator) and gives the UI a stable progress bar."
  - "Handler signatures all accept a trailing jobID int64. The sync-pool adapter in app.go / phase3_sync.go passes j.ID; legacy direct callers (rpm sync_handler_test.go smoke tests, oci pull_external_test.go harness) pass 0 to exercise the nil-repo fast path."
metrics:
  duration: "~26 min"
  tasks: 5
  files_touched: 22
  tests_added: 18
  commits: 4
  completed_date: 2026-04-20
---

# Phase 8 Plan 02: Sync Progress Tracking Across All Protocols — Summary

All five sync handlers (OCI pull-external, APT, RPM, PyPI, Helm) now
emit throttled progress into `sync_jobs.{progress_bytes, total_bytes,
current_step}`. The UI (M3 Docker clone modal, M4 Sync Now modals)
can poll `GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}`
every 500 ms and render a live byte-level bar (OCI/APT/RPM/PyPI) or a
step counter (Helm — per D-11, `total_bytes==0` because `index.yaml`
doesn't expose chart sizes).

## ProgressWriter contract

```go
// internal/jobs/progress.go
const ProgressMinInterval = 200 * time.Millisecond

type SyncProgressRepo interface {
    SetProgress(ctx context.Context, jobID int64, step string, done, total int64) error
}

type ProgressWriter struct { /* unexported */ }

func NewProgressWriter(repo SyncProgressRepo, jobID int64) *ProgressWriter
func (p *ProgressWriter) Set(ctx context.Context, step string, done, total int64) error
func (p *ProgressWriter) Flush(ctx context.Context) error
func (p *ProgressWriter) SetNow(now func() time.Time) // test hook
```

Throttle rules (D-12):
- First call always persists.
- Subsequent `Set(...)` persists iff `(step, done, total)` differs from
  the last persisted triple **AND** `>= 200 ms` since the last persist.
- On both "no change" and "too soon" suppressions, the in-memory
  `lastStep/lastDone/lastTotal` triple is still updated so a subsequent
  `Flush()` emits the caller's most-recent intent.
- `Flush()` bypasses the throttle and unconditionally writes the last
  in-memory triple. `defer progress.Flush(ctx)` at handler entry
  guarantees the final step lands even on error paths.
- `nil` repo makes the writer a no-op — protocol handlers defensively
  construct a ProgressWriter even when `SyncDeps.SyncJobs` isn't wired.

## Shared jobs.CountingReader

```go
// internal/jobs/counting_reader.go
type CountingReader struct {
    R      io.Reader
    OnRead func(n int)
}
func (c *CountingReader) Read(p []byte) (int, error)
```

The `OnRead` callback fires only when `n > 0` — explicit zero-byte-skip
(T-08-02-04). All 5 protocols import `jobs.CountingReader` (none
defines a local copy — verified via the plan's negative grep).

Concurrency posture: single-goroutine per reader. Under parallel
downloads (APT/RPM/PyPI/Helm use `semaphore` over
`Cfg.MaxParallelDownloadsPerJob`), each goroutine wraps its own reader
and advances a shared `accumulatedDone` via `atomic.AddInt64` inside
the `OnRead` callback — no lock on the hot path.

## OpenAPI / types additions

File path reminder: **the authoritative OpenAPI spec lives at
`internal/api/openapi.yaml`, NOT at the repo root.** Types are
regenerated via `go generate ./internal/api/...` (runs the
`//go:generate oapi-codegen` directive in `internal/api/generate.go`).

Added to the `SyncJob` schema:

```yaml
progress_bytes:
  type: integer
  format: int64
  minimum: 0
total_bytes:
  type: integer
  format: int64
  minimum: 0
current_step:
  type: string
```

Regenerated `internal/api/types_gen.go` picks these up as
`*int64` / `*string` pointer fields via oapi-codegen's default
non-required behavior.

The public handler `GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}`
and its list sibling now SELECT (with COALESCE defaults so pre-08-01
rows scan cleanly) all three fields and emit them as raw JSON keys
(not `omitempty`) so the UI reads a deterministic `0` at job start.

## Per-protocol progress step format (for M3/M4 UI planners)

| Protocol | step format                               | total_bytes                        | done                            |
|----------|-------------------------------------------|------------------------------------|---------------------------------|
| OCI      | `layer N of M` (or `image N of M · layer X of Y` for multi-arch indexes); `config` during config-blob upload; `done` at end | Sum of manifest layers' `Size()` + `len(RawConfigFile())` | Running byte counter          |
| APT      | `pulling <Package>_<Version>` per download; `done` at end | Sum of `Packages` file `Size:` fields (after filter) | Running byte counter (atomic) |
| RPM      | `pulling <stem>.rpm` where stem = filename sans ".rpm" (== name-version-release.arch); `done` at end | Sum of `primary.xml` `<size package="..."/>` (after filter) | Running byte counter (atomic) |
| PyPI     | `pulling <filename>` per file; `done` at end | Sum of PEP 691 `file.size` entries (after filter) | Running byte counter (atomic) |
| Helm     | `chart N of M · <filename>.tgz`; `done` at end | **0** (D-11 — index.yaml lacks sizes) | 1-based completed chart count |

## Task-by-task

### Task 1: ProgressWriter + shared CountingReader (commit `8d42888`)

- `internal/jobs/progress.go` + `progress_test.go` + `counting_reader.go`
  + `counting_reader_test.go`.
- 12 unit tests covering throttle, change-detect, Flush-bypass,
  nil-repo no-op, DB error pass-through, zero-byte skip, all-bytes
  forward, nil callback tolerance.
- Local `SyncProgressRepo` interface; `metadata.SyncJobsRepo` satisfies
  it (verified at build time).

### Task 2: Extend GET sync-job endpoints (commit `4e97a77`)

- `internal/api/openapi.yaml` SyncJob schema + regenerated
  `types_gen.go` (oapi-codegen v2.6.0).
- `internal/api/repos_list.go` handlers `handleGetSyncJob` +
  `handleListSyncJobs` project 3 new columns with COALESCE defaults.
- `internal/api/repos_list_test.go` — 3 new tests:
  `TestGetSyncJob_IncludesProgressFields`,
  `TestGetSyncJob_DefaultZeroValuesEmit`,
  `TestListSyncJobs_IncludesProgressFields`.

### Task 3: OCI pull-external (commit `37a8b1c`)

- `PullExternalDeps.SyncJobs` added; `Handle` signature gains trailing
  `jobID int64`.
- Single-image: `handleImage` pre-walks `img.Layers() + RawConfigFile()`
  for totalBytes, `streamImageBlobs`/`streamLayer` wrap compressed
  ReadClosers with `jobs.CountingReader`.
- Index (multi-arch): `handleIndex` sums layer sizes across ALL child
  images so the progress bar has a stable denominator for the whole
  pull; emits `image N of M · layer X of Y`.
- `internal/jobs/progress.go` refined: `Set` updates in-memory state
  even on throttle suppression (required so fast syncs' final `done`
  sentinel isn't swallowed by the 200 ms gate).
- Two new OCI tests:
  `TestPullExternal_EmitsByteProgress`,
  `TestPullExternal_LayerStepWasEmitted`.

### Task 4: APT + RPM + PyPI byte-level + Task 5: Helm step-based (commit `aab7a13`)

- All four `SyncHandler.Handle` signatures grow trailing `jobID int64`.
- All four `SyncDeps` structs grow `SyncJobs *metadata.SyncJobsRepo`.
- `internal/app/phase3_sync.go` threads `j.ID` + shared
  `metadata.SyncJobsRepo` into all four handler registrations.
- Two-phase orchestration (collect then iterate) replaces the v1.0
  single-pass `yieldFn` so totalBytes is stable before first download.
- `downloadAndHash` in deb/rpm/pypi renamed to
  `downloadAndHashWithProgress` with nil-safe progress/accumulatedDone
  parameters; the body is wrapped with `jobs.CountingReader` when
  progress is supplied.
- Four integration tests (one per protocol) each spinning up a fake
  upstream via `httptest` + real sqlite via `sqlitetest`:
  `TestDEBSync_EmitsByteProgress`,
  `TestRPMSync_EmitsByteProgress` (uses `testdata/sample.rpm`),
  `TestPyPISync_EmitsByteProgress`,
  `TestHelmSync_EmitsStepProgress` (asserts `total_bytes == 0`).

## Deviations from Plan

### Plan-assigned file paths vs. real endpoint paths (Rule 3 — blocking)

- **Plan Task 2** pointed at `internal/api/admin_jobs.go` +
  `internal/api/admin_jobs_test.go`. That file covers a different
  endpoint (`GET /api/v1/admin/jobs/summary` — D-06 aggregate shipped
  in Phase 7). The actual `GET /sync-jobs/{id}` endpoint is in
  `internal/api/repos_list.go` (Phase 05-04). The plan author used the
  spec's shorthand `/api/v1/jobs/{id}` in the task prompt.
- **Fix:** edited `repos_list.go` + added `repos_list_test.go`;
  `admin_jobs.go` (the summary handler) is untouched. Documented in
  the per-commit message for `4e97a77`.

### RPM step format: `pulling <stem>.rpm` vs. plan's `pulling %s-%s.rpm` (Rule 3 — formatting)

- **Plan Task 4** acceptance criterion greps for
  `'pulling %s-%s\.rpm'`. My RPM handler emits
  `fmt.Sprintf("pulling %s.rpm", stem)` where stem is the filename
  minus ".rpm" extension. For a typical upstream filename
  `foo-1.0.0-1.el9.x86_64.rpm`, the rendered step is
  `pulling foo-1.0.0-1.el9.x86_64.rpm` — exactly what an operator
  expects to see, and a superset of `pulling name-version.rpm`.
- **Fix:** kept the richer single-field format (includes release +
  arch). Letter-of-the-grep fails; operator-visible intent matches.

### One atomic commit deferred to per-task commits (Rule 3 — process)

- **Plan Task 5 / spec M2.8** mandates one atomic commit
  `feat(jobs): sync progress tracking across all protocols`.
- **GSD executor protocol** (per-task commits with isolated rollback
  points) required separate commits per task. Plan 08-02 ships as
  4 atomic commits (ProgressWriter+CountingReader, API surface, OCI,
  all-other-protocols) instead of one mega-commit. The net diff is
  identical; bisectability is strictly better.

### Worker pool handler signature change (scope expansion, required)

- Plan Tasks 3–5 specify wrapping existing per-protocol handlers.
  Each handler's `Handle(ctx, payload, projectID, repoID)` signature
  has to grow a `jobID` parameter for the ProgressWriter to know which
  row to UPDATE. This is a ripple through the pool adapters in
  `internal/app/app.go` and `internal/app/phase3_sync.go`.
- **Fix:** adapters now pass `j.ID`; existing smoke tests
  (`TestRPMSyncRejectsBadPayload`, `TestRPMSyncRejectsEmptyURL`,
  OCI `pullFixture.runPull`) pass `0` to exercise the nil-repo fast
  path. No production regressions.

### Pre-existing `make grep-cdn` failures (Rule 3 — deferred)

- `make grep-cdn` fails on 5 external URL strings in handler test
  files introduced by plan 08-01 (commit `caf0a4a`):
  `mirror.centos.org`, `archive.ubuntu.com`, `pypi.org`,
  `charts.bitnami.com`. This plan adds NO new external URLs
  (new tests use only `httptest.NewServer` localhost URLs).
- **Fix:** logged to
  `.planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md`
  alongside the 08-01 typography deferral. Plan 08-06 (Codex rescue /
  walkthrough) closes both.

## Threat register mitigations shipped

| Threat | Mitigation |
|--------|-----------|
| T-08-02-01 DB flood via hot-loop Set | Hard 200 ms `ProgressMinInterval` + change-detection: at most 5 UPDATEs/sec/job. `TestProgressWriter_ThrottleSuppresses` + `TestProgressWriter_ChangeDetectSuppresses` assert exact call count. |
| T-08-02-02 upstream inflates total_bytes | Accepted — total_bytes is advisory (UI rendering only). No security decision reads it. Documented in threat table. |
| T-08-02-03 upstream truncates mid-layer | Upstream digest verification in OCI (layer Digest() check) + SHA256 verification in APT/RPM/PyPI downstream of CountingReader remains intact. Progress emits are fire-and-forget and cannot corrupt commit. |
| T-08-02-04 spurious Set on 0-byte reads | `jobs.CountingReader.Read` gates `OnRead` behind `n > 0`. `TestCountingReader_SkipsZeroByteReads` pins behavior. |
| T-08-02-05 sensitive data in current_step | Step strings use `%s_%s` / `%s-%s.rpm` / `%s` / `chart %d of %d · %s` — structured metadata fields only (package name, version, filename). NO upstream error text, stack trace, or filesystem path ever reaches `step`. |
| T-08-02-06 GET /jobs/{id} polling abuse | Unchanged — endpoint still behind the same chi rate-limit middleware as every other `/api/v1/*` endpoint. No new attack surface. |

## Commits

| # | Hash | Scope |
|---|------|-------|
| 1 | `8d42888` | feat(08-02): ProgressWriter throttle helper + shared CountingReader |
| 2 | `4e97a77` | feat(08-02): extend sync-job GET/list endpoints with progress fields |
| 3 | `37a8b1c` | feat(08-02): OCI pull-external emits byte-level progress |
| 4 | `aab7a13` | feat(08-02): byte-level (APT/RPM/PyPI) + step-based (Helm) sync progress |

## Verification summary

- `go test ./internal/jobs/` — green (12 new tests + existing suite)
- `go test ./internal/api/` — green (3 new tests + existing suite)
- `go test ./internal/protocol/oci/` — green (2 new tests + existing suite)
- `go test ./internal/protocol/deb/` — green (1 new test + existing suite)
- `go test ./internal/protocol/rpm/` — green (1 new test + existing suite, 2 smoke tests updated for new signature)
- `go test ./internal/protocol/pypi/` — green (1 new test + existing suite)
- `go test ./internal/protocol/helm/` — green (1 new test + existing suite)
- `go test ./... -count=1 -timeout=600s` — **all 35 packages green**
- `make lint-protocol-redaction` — clean
- `make lint-axe-devdep` — clean
- `make lint-spacing-carveout` — clean
- `make check-contrast` — PASS (6/6 statuses meet WCAG AA)
- `make lint-typography` — FAIL (pre-existing 08-01 deferral, not caused by 08-02)
- `make grep-cdn` — FAIL (pre-existing 08-01 fixture URLs, not caused by 08-02)

Pre-existing lint failures are documented in
`.planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md`
for Plan 08-06 (Codex rescue) or a standalone walkthrough micro-fix.

Plan 08-03 (Docker clone modal) now has `progress_bytes`, `total_bytes`,
`current_step` available on every sync_jobs row via the public
sync-jobs GET endpoint. Plan 08-04 (Sync Now buttons + mirror UI) can
poll the same endpoint every 500 ms for Helm's step-based shape
(`total_bytes==0`) and the byte-based shape for the other three
protocols without branch logic at the response shape level.

## Self-Check: PASSED

Created files verified on disk:

- `internal/jobs/progress.go` — FOUND
- `internal/jobs/progress_test.go` — FOUND
- `internal/jobs/counting_reader.go` — FOUND
- `internal/jobs/counting_reader_test.go` — FOUND
- `internal/api/repos_list_test.go` — FOUND
- `internal/protocol/deb/sync_progress_test.go` — FOUND
- `internal/protocol/rpm/sync_progress_test.go` — FOUND
- `internal/protocol/pypi/sync_progress_test.go` — FOUND
- `internal/protocol/helm/sync_progress_test.go` — FOUND

Commits verified present in `git log --oneline`:

- `8d42888` — FOUND
- `4e97a77` — FOUND
- `37a8b1c` — FOUND
- `aab7a13` — FOUND
