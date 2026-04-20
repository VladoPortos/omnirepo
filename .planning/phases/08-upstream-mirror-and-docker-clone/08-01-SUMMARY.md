---
phase: 08-upstream-mirror-and-docker-clone
plan: 01
subsystem: backend
tags: [backend, migration, sqlite, sync, middleware, mirror]
requires: [phase-06-envelope, phase-06-visual-foundation, phase-07-snippet-polish]
provides:
  - migration-024-mirror-and-progress
  - repo-mirror-columns
  - sync-jobs-progress-columns
  - sync-jobs-count-inflight
  - sync-jobs-set-progress
  - mirror-aware-sync-endpoint
  - mirror-guard-middleware
  - mirror-upload-block-5-protocols
affects:
  - internal/metadata (schema)
  - internal/api/repos (Create + Patch validation)
  - internal/httpx/sync_rest (mirror-aware branch, body cap, concurrency guard)
  - internal/protocol/{oci,deb,rpm,pypi,helm} (upload guard)
tech-stack:
  added: []
  patterns:
    - "dotted envelope codes with operator-facing local-token substring (repo.mirror_url_immutable, sync.sync_already_running, etc.)"
    - "3-way /sync branch: mirror+empty reads repo row; mirror+body 400; non-mirror preserves v1.0"
    - "io.LimitReader(body, cap+1) over-by-one trick to detect at/over limit without allocating past cap"
    - "MirrorGuardFixed for hard-coded-type routes; MirrorGuard for {type}-param OCI routes"
key-files:
  created:
    - internal/metadata/migrations/024_mirror_and_progress.up.sql
    - internal/metadata/migrations/024_mirror_and_progress.down.sql
    - internal/api/mirror_validate.go
    - internal/httpx/mirror_guard.go
    - internal/httpx/mirror_guard_test.go
    - internal/protocol/pypi/upload_legacy_test.go
    - internal/protocol/pypi/upload_pep694_test.go
    - .planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md
  modified:
    - .planning/REQUIREMENTS.md
    - internal/metadata/repos.go
    - internal/metadata/repos_test.go
    - internal/metadata/sync_jobs.go
    - internal/metadata/sync_jobs_test.go
    - internal/metadata/upstream_creds.go
    - internal/api/admin_phase1.go
    - internal/api/repos.go
    - internal/api/repos_test.go
    - internal/api/types_phase1.go
    - internal/httpx/sync_rest.go
    - internal/httpx/sync_rest_test.go
    - internal/protocol/deb/handler.go
    - internal/protocol/deb/handler_test.go
    - internal/protocol/rpm/handler.go
    - internal/protocol/rpm/handler_test.go
    - internal/protocol/pypi/handler.go
    - internal/protocol/helm/handler.go
    - internal/protocol/helm/handler_test.go
    - internal/protocol/oci/handler.go
    - internal/protocol/oci/manifests.go
    - internal/protocol/oci/manifests_test.go
    - internal/protocol/oci/blobs.go
    - internal/protocol/oci/blobs_test.go
decisions:
  - "Dotted envelope codes with operator-facing local-token substring (e.g. repo.mirror_url_immutable contains mirror_url_immutable). Satisfies the envelope schema regex while keeping plan-check greps and integration-test assertions source-grep-able."
  - "SetMirrorConfigInTx as a separate repo method rather than extending CreateInTx signature — preserves all existing CreateInTx call sites across the codebase and makes is_mirror creation a single explicit opt-in in handleCreateRepo."
  - "mirror_validate.go keeps api package cycle-free by duplicating the 4 protocol SyncFilter shapes (~12 lines) rather than importing internal/protocol/*. Alternative would have forced every existing api_test harness to pull protocol deps into scope."
  - "PATCH MirrorCredIDRaw uses *json.RawMessage so handler can distinguish absent / null / int — required to differentiate 'no change' from 'clear to NULL' without breaking existing struct-copy tests."
  - "3-way sync-endpoint branch: mirror+empty reads config from the repo row; mirror+body 400 mirror_overrides_not_allowed; non-mirror preserves v1.0 body-driven flow verbatim. No regressions in existing sync tests."
  - "CountRepoInflight + Enqueue are NOT atomic; documented T-08-01-04 residual risk (race window between check and insert). Worker pool's LeaseOne UPDATE ... RETURNING caps the cost at one wasted pending row."
  - "MirrorGuard wraps write routes at the chi Group / r.With level; read routes stay open. Mirror repos remain publicly readable."
  - "OCI uses r.With(mirrorGuard) per-route rather than a Group so only the write verbs get the middleware — Get/Head paths don't pay the DB lookup cost."
  - "PyPI PEP 694 tests renamed (TestPEP694Upload_*) to avoid collision with upload_legacy_test.go's TestUpload_*. Plan's grep check `TestUpload_MirrorRepoReturns403|TestUpload_NonMirrorRepoStillWorks` resolves against the legacy file per the plan intent."
metrics:
  duration: "~29 min"
  tasks: 4
  files_touched: 24
  tests_added: 25
  commits: 5
  completed_date: 2026-04-20
---

# Phase 8 Plan 01: Upstream Mirror Backend Foundation — Summary

Mirror-aware backend shipped as one atomic vertical slice: migration 024
adds 5 columns to `repos` and 3 to `sync_jobs`; `CreateRepoRequest` and
`PatchRepoRequest` gain mirror fields with 5-branch validation;
`POST /sync` branches on `IsMirror` with a 16 KiB body cap and an
in-flight concurrency guard; `httpx.MirrorGuard` + `MirrorGuardFixed`
reject upload attempts on mirror repos across all 5 protocols (OCI, APT,
RPM, PyPI, Helm) with a 403 envelope code carrying the operator-facing
`repo_is_mirror` token.

## Task-by-task

### Task 0: REQUIREMENTS.md coverage counter

The MIRROR-01..27 rows + traceability table were already scaffolded
during phase-08 planning (commit `ce98703`). Only the coverage counter
(32/32 → 59/59) was stale; this task flipped it.

- **Files:** `.planning/REQUIREMENTS.md`
- **Commit:** `9557f95`
- **Verification:** `grep -cE '^- \[ \] \*\*MIRROR-' .planning/REQUIREMENTS.md` → 27;
  `grep -cE 'MIRROR-[0-9]+.*Phase 8' .planning/REQUIREMENTS.md` ≥ 27.

### Task 1: Migration 024 + ReposRepo / SyncJobsRepo extensions

Migration 024 adds exactly 5 columns to `repos`
(`is_mirror`, `mirror_upstream_url`, `mirror_filter_json`,
`mirror_cred_id` FK `upstream_creds(id) ON DELETE SET NULL`,
`scan_on_sync`) and exactly 3 to `sync_jobs`
(`progress_bytes`, `total_bytes`, `current_step`). `Repo` struct grows
5 fields; `SyncJob` grows 3. `scanRepoRow` + all 4 SELECT column lists
updated. New methods:

- `ReposRepo.SetMirrorConfigInTx(ctx, tx, repoID, MirrorConfig{...})` —
  called in the same writer-tx as `CreateInTx` so the repo row is
  never observable in a half-mirror state.
- `ReposRepo.Update` + `UpdateFields` grow `MirrorFilterJSON`,
  `MirrorCredID`, `MirrorCredIDSet` (distinguishes no-change from
  clear-to-NULL), `ScanOnSync`.
- `SyncJobsRepo.SetProgress(ctx, jobID, step, done, total)` writes the
  progress triple against the writer pool.
- `SyncJobsRepo.CountRepoInflight(ctx, repoID)` returns pending+running
  rows for the concurrency guard.

5 new tests round-trip every column and count combination.

- **Files:** migrations 024.up.sql + 024.down.sql; `repos.go`,
  `repos_test.go`, `sync_jobs.go`, `sync_jobs_test.go`.
- **Commit:** `06c69de`
- **Verification:** `go test ./internal/metadata/` fully green — new
  tests + all existing.

### Task 2: Create/Patch validation + mirror-aware /sync

`CreateRepoRequest` grows 5 optional fields (`is_mirror`,
`mirror_upstream_url`, `mirror_filter` as `json.RawMessage`,
`mirror_cred_id`, `scan_on_sync`). `handleCreateRepo` gates
`is_mirror=true` behind 4 validation branches:

| Branch | Envelope code | Trigger |
|--------|---------------|---------|
| A | `repo.mirror_type_unsupported` | type ∉ {deb,rpm,pypi,helm} |
| B | `repo.mirror_url_invalid` | URL not http(s) with non-empty host (T-08-01-03) |
| C | `repo.mirror_filter_invalid` | JSON shape doesn't match protocol SyncFilter |
| D | `repo.mirror_cred_wrong_project` | cross-project cred (T-08-01-07) |

`handlePatchRepo` grows the same fields plus
`MirrorCredIDRaw *json.RawMessage` so it can distinguish absent / null /
int. Setting `is_mirror` or `mirror_upstream_url` triggers 400
`repo.mirror_url_immutable` (MIRROR-02); filter / cred / scan_on_sync
flow into `metadata.UpdateFields`.

`internal/httpx/sync_rest.go` gets a mirror-aware 3-way branch:

- Body capped at 16 KiB via `io.LimitReader(body, MaxSyncBodyBytes+1)`;
  oversized or malformed → 400 `sync.invalid_request_body` (MIRROR-06).
- `repo.IsMirror && bodyEmpty` → read config from the repo row +
  enqueue.
- `repo.IsMirror && !bodyEmpty` → 400
  `sync.mirror_overrides_not_allowed` (MIRROR-05).
- `!repo.IsMirror` → v1.0 body-driven path verbatim.
- Before every enqueue: `CountRepoInflight > 0` → 409
  `sync.sync_already_running` (MIRROR-04).

`mirror_validate.go` keeps the `api` package cycle-free by duplicating
the 4 protocol SyncFilter shapes (~12 lines). `upstream_creds.go` grows
a thin `GetProjectID(ctx, id)` helper.

9 new repo-API tests + 5 new sync-REST tests; all green.

- **Commit:** `87dcdd8`
- **Verification:** `go test ./internal/api/` + `go test ./internal/httpx/`
  fully green.

### Task 3: MirrorGuard middleware + wire into 5 protocols

`internal/httpx/mirror_guard.go` exports:

- `MirrorGuard(repos, projects)` — reads `{type}` from chi URL params
  (OCI uses this via `/v2/{project}/{type}/{repo}/...`).
- `MirrorGuardFixed(repos, projects, fixedType)` — supplies the type
  directly for APT/RPM/PyPI/Helm whose mounts bake the type into the
  URL.

Both return 403 envelope `repo.repo_is_mirror` on `is_mirror=1` rows and
pass-through on resolution failure (missing project/repo) so downstream
404/500 shapes are preserved. Write-path wiring:

| Protocol | Route(s) gated |
|----------|-----------------|
| OCI | `POST/PATCH/PUT/DELETE /v2/{project}/{type}/{repo}/blobs/uploads/...` + `PUT/DELETE .../manifests/{reference}` (both 3- and 4-segment forms) |
| APT | `PUT/DELETE /{project}/deb/{repo}/pool/*` + `PATCH /{project}/deb/{repo}/suites` |
| RPM | `PUT/DELETE /{project}/rpm/{repo}/packages/{filename}` |
| PyPI | `POST /{project}/pypi/{repo}/legacy/` + PEP 694 session routes (create, upload, commit) |
| Helm | `PUT/DELETE /{project}/helm/{repo}/charts/{filename}` |

Read-only paths (GET/HEAD, download, index, public-key) stay open.
Mirror repos remain publicly readable.

Tests (14 new):

- `mirror_guard_test.go`: 6 unit tests (3 variants × both middleware
  forms × mirror/non-mirror/missing).
- 5 × 2 per-protocol integration tests (1 mirror-rejected, 1
  non-mirror pass-through) for DEB, RPM, PyPI, Helm, OCI.
- New files `upload_legacy_test.go` and `upload_pep694_test.go` (per
  plan).

- **Commit:** `caf0a4a`
- **Verification:** `go test ./...` fully green across 35 packages.

## Envelope codes introduced

| Wire code | HTTP | Handler source |
|-----------|------|----------------|
| `repo.mirror_type_unsupported` | 400 | `handleCreateRepo` |
| `repo.mirror_url_invalid` | 400 | `handleCreateRepo` |
| `repo.mirror_filter_invalid` | 400 | `handleCreateRepo`, `handlePatchRepo` |
| `repo.mirror_url_immutable` | 400 | `handlePatchRepo` |
| `repo.mirror_cred_wrong_project` | 400 | `handleCreateRepo`, `handlePatchRepo` |
| `repo.repo_is_mirror` | 403 | `MirrorGuard` / `MirrorGuardFixed` |
| `sync.mirror_overrides_not_allowed` | 400 | `/sync` |
| `sync.sync_already_running` | 409 | `/sync` |
| `sync.invalid_request_body` | 400 | `/sync` (body cap) |

Local-token substring preserved in each code so grep-based plan-check
assertions and integration tests resolve against the wire body.

## Deviations from Plan

### Rule 1 — Bug: Sync body 16 KiB cap
- **Found during:** Task 2 wiring
- **Issue:** Plan text used `16 * 1024` for `MaxSyncBodyBytes` and a
  plain `io.LimitReader(r.Body, MaxSyncBodyBytes+1)`. The +1 trick is
  critical — without it, a body that is exactly `MaxSyncBodyBytes`
  bytes becomes indistinguishable from a body that is
  `MaxSyncBodyBytes + N`.
- **Fix:** Shipped with the +1 as documented.

### Rule 2 — Missing critical functionality: Dotted envelope codes
- **Found during:** Task 2 envelope helper selection
- **Issue:** Plan snippets referenced `httperr.BadRequest`,
  `httperr.Conflict`, `httperr.Forbidden` helpers that don't exist in
  `internal/httperr` (which only provides `Validation`, `Permission`,
  `Transient`, `OperatorRequired`, `Internal`). Plan also used raw
  codes like `repo_is_mirror` that fail the envelope schema regex
  `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`.
- **Fix:** Adopted dotted codes (`repo.repo_is_mirror`,
  `sync.sync_already_running`, etc.) whose local token preserves the
  plan-token substring so the plan's grep assertions + test body
  checks still resolve. Routed 403s through `httperr.Permission` and
  legacy-400/409/500 through `writeJSONError` (api package) /
  `writeJSONErr` (httpx package). No schema violations.

### Rule 3 — Blocking fix: `CreateInTx` signature preservation
- **Found during:** Task 1 Insert extension
- **Issue:** Extending `CreateInTx` to accept 5 mirror fields would
  have cascaded to ~15 call sites across `internal/api`,
  `internal/app`, `internal/protocol/git`, and the DEB/RPM/HELM test
  harnesses.
- **Fix:** Added `SetMirrorConfigInTx(ctx, tx, repoID, MirrorConfig)`
  as a separate method called in the same writer-tx as `CreateInTx`
  when `is_mirror=true`. Preserves all v1.0 callers byte-for-byte.

### Rule 3 — Blocking fix: PyPI PEP 694 test collision
- **Found during:** Task 3 pypi test file split
- **Issue:** Plan mandates two test files (`upload_legacy_test.go` +
  `upload_pep694_test.go`) each with the same test names
  (`TestUpload_MirrorRepoReturns403` / `...NonMirrorRepoStillWorks`).
  Go forbids duplicate function names in the same package.
- **Fix:** Legacy keeps the canonical names; PEP 694 uses
  `TestPEP694Upload_MirrorRepoReturns403` /
  `TestPEP694Upload_NonMirrorRepoStillWorks`. Both files exist;
  plan's verify grep `TestUpload_MirrorRepoReturns403` resolves
  against the legacy file. PEP 694 tests are covered separately.

### Rule 3 — Blocking fix: existing `pep694CreateSession` helper
- **Found during:** first run of pypi tests
- **Issue:** Plan's test skeleton used a bespoke
  `pep694CreateSession(t, srvURL, projName, repoName, auth)` helper
  that collided with the existing `handler_test.go` helper
  `pep694CreateSession(t, srvURL, proj, repo, name, version, auth) (sid, status)`.
- **Fix:** Renamed the mirror-guard helper to
  `pep694CreateSessionRaw` which returns a full `*http.Response` so
  the guard tests can assert on body text.

### Deferred (pre-existing, NOT caused by Phase 8)
- `make test` fails `lint-typography` on
  `web/src/App.tsx`, `web/src/components/common/ArtifactDetail.tsx`,
  `web/src/pages/repo/AptRepoPage.tsx`,
  `web/src/pages/repo/ScanReportPage.tsx`. Verified via `git stash` on
  main at 87dcdd8. Logged to
  `.planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md`
  for a later plan (08-06 Codex rescue or walkthrough micro-fix).

## Known Stubs

None in this plan. Every column, middleware, handler is wired to real
behavior and exercised by tests.

## Threat Flags

None — the plan's threat register (T-08-01-01..07) is fully addressed:

| Threat | Mitigation shipped |
|--------|---------------------|
| T-08-01-01 | mirror_filter_json re-validated on Create via `validateMirrorFilter`; stored as TEXT and will be re-parsed at sync time (Plan 08-02+) |
| T-08-01-02 | MirrorGuard uses `FindByTriple` with exact `(project, type, repo)` match; no case-folding |
| T-08-01-03 | `validateMirrorUpstreamURL` checks scheme ∈ {http,https} + non-empty Host; covered by `TestCreateRepo_MirrorRejectsBadURL` |
| T-08-01-04 | Race documented in `sync_jobs.go` godoc; worker pool LeaseOne UPDATE…RETURNING is authoritative |
| T-08-01-05 | All error emissions go through httperr envelope helpers or `writeJSONError` bridge — no `%v` interpolation |
| T-08-01-06 | `io.LimitReader(body, MaxSyncBodyBytes+1)` + explicit over-limit check; covered by `TestSync_RejectsOversizedBody` |
| T-08-01-07 | `mirrorCredOwnership` checks `upstream_creds.project_id` on both Create + Patch; covered by `TestCreateRepo_MirrorRejectsCrossProjectCred` + `TestPatchRepo_RejectsCrossProjectCred` |

## Verification summary

- `go test ./internal/metadata/` — green
- `go test ./internal/api/` — green
- `go test ./internal/httpx/` — green
- `go test ./internal/protocol/{deb,rpm,pypi,helm,oci}/` — all green
- `go test ./...` — 35 packages green
- `go vet ./...` — clean
- `go build ./...` — clean

Plan 08-02 (progress writer helper + /jobs endpoint extension) now has
the columns (`progress_bytes`, `total_bytes`, `current_step`) and the
method (`SyncJobsRepo.SetProgress`) pre-built. Plan 08-03 (Docker
clone modal) has the `CountRepoInflight` + 409 guard pre-built. Plans
08-04 / 08-05 (UI wiring) have the `CreateRepoRequest` mirror fields +
`PatchRepoRequest` editable-three pre-built.

## Commits

| # | Hash | Scope |
|---|------|-------|
| 0 | `9557f95` | docs(08-01): bump REQUIREMENTS coverage to 59/59 |
| 1 | `06c69de` | feat(08-01): migration 024 + mirror columns + progress + inflight count |
| 2 | `87dcdd8` | feat(08-01): mirror validation on Create/Patch repo + mirror-aware /sync |
| 3 | `caf0a4a` | feat(08-01): MirrorGuard middleware + wire into 5 protocol upload paths |
| 4 | `2d71bff` | docs(08-01): log pre-existing lint-typography failures as deferred |

Duration: ~29 min end-to-end (2026-04-20T02:23:06Z → 2026-04-20T02:51:55Z).

## Self-Check: PASSED

Created files verified on disk:

- `internal/metadata/migrations/024_mirror_and_progress.up.sql` — FOUND
- `internal/metadata/migrations/024_mirror_and_progress.down.sql` — FOUND
- `internal/api/mirror_validate.go` — FOUND
- `internal/httpx/mirror_guard.go` — FOUND
- `internal/httpx/mirror_guard_test.go` — FOUND
- `internal/protocol/pypi/upload_legacy_test.go` — FOUND
- `internal/protocol/pypi/upload_pep694_test.go` — FOUND
- `.planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md` — FOUND

Commits verified present in `git log --oneline`:

- `9557f95` — FOUND
- `06c69de` — FOUND
- `87dcdd8` — FOUND
- `caf0a4a` — FOUND
- `2d71bff` — FOUND
