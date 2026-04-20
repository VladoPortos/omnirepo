# Phase 8 SUMMARY — Upstream Mirror & Docker Clone

**Shipped:** 2026-04-20
**Status:** Complete
**Milestones:** M1–M6 (plans 08-01..08-06)
**Duration:** ~2.5 hours end-to-end across 6 plans

Phase 8 wired the UI for OmniRepo's already-shipped upstream-mirror
backend and shipped the v1.1 public-release gate. A user can now create
an APT/RPM/PyPI/Helm repo with `is_mirror=true` via `CreateRepoDialog`,
click **Sync now** to pull from upstream with live byte-level (or
step-based for Helm) progress, edit filter + credential + scan-on-sync
via `RepoSettingsTab`, and manage per-project upstream credentials via a
new `ProjectSettingsPage` tab. The Docker repo page's former "Pull
External" stub is now a real 3-state clone modal with live progress
polling. Every upload path across all 5 protocols rejects writes to a
mirror repo with a 403 envelope code=`repo_is_mirror`. A Codex rescue
pass caught 5 real-issue findings (upload-guard coverage gaps,
persistence-boundary hygiene, and an upgraded severity on the documented
CountRepoInflight race) — all fixed as atomic commits before closure.

## What shipped

### Backend (M1 — plan 08-01)

- Migration 024: 5 columns on `repos`
  (`is_mirror`, `mirror_upstream_url`, `mirror_filter_json`,
  `mirror_cred_id` with `ON DELETE SET NULL`, `scan_on_sync`) + 3 columns
  on `sync_jobs` (`progress_bytes`, `total_bytes`, `current_step`).
- `ReposRepo` round-trips all 5 mirror columns via
  `SetMirrorConfigInTx` + extended `Update`/`UpdateFields`;
  `SyncJobsRepo` exposes `CountRepoInflight` + `SetProgress`.
- `CreateRepo` / `PatchRepo` validation with 5 envelope codes:
  `mirror_type_unsupported`, `mirror_url_invalid`, `mirror_filter_invalid`,
  `mirror_url_immutable`, `mirror_cred_wrong_project`.
- `POST /sync` 3-way branch: mirror+empty reads repo row; mirror+body →
  400 `mirror_overrides_not_allowed`; non-mirror preserves v1.0 body-
  driven flow. 16 KiB body cap (`io.LimitReader(body, cap+1)` over-by-one
  trick). In-flight concurrency guard (409 `sync_already_running`).
- `MirrorGuard` + `MirrorGuardFixed` middleware wired into all 5 protocol
  upload paths (OCI + APT + RPM + PyPI + Helm) — mirror repos reject
  upload attempts with 403 envelope code=`repo_is_mirror`.
- REQUIREMENTS.md MIRROR-01..27 row shells.

### Progress tracking (M2 — plan 08-02)

- `internal/jobs/progress.go` `ProgressWriter` with 200 ms throttle +
  change-detect + Flush-bypass; `internal/jobs/counting_reader.go`
  shared `CountingReader` with zero-byte skip (`n > 0` guard).
- `GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}` and
  its list sibling emit the `progress_bytes` / `total_bytes` /
  `current_step` triple with COALESCE defaults so pre-08-01 rows scan.
  OpenAPI + `types_gen.go` regenerated via `go generate`.
- Per-protocol instrumentation: byte-level for OCI (layer sum + config
  bytes), APT (summed `Size:`), RPM (summed `primary.xml size`), PyPI
  (summed PEP 691 `file.size`); step-based for Helm per D-11
  (`total_bytes==0`, step = `chart N of M · <filename>`).

### UI (M3–M5 — plans 08-03, 08-04, 08-05)

- **Docker clone modal** (M3): 3-state `CloneImageDialog`
  (form → progress → result) with TanStack Query v5 `refetchInterval`
  polling every 500 ms via the new `useJobProgress` hook. Pure helpers
  `computeJobProgress` + `pollingDecision` are unit-testable without
  jsdom.
- **Mirror UI** (M4): `MirrorConfigSection` + 4 `FilterWidget*` components
  (APT/RPM/PyPI/Helm). PascalCase wire format (Names / Globs / Suites /
  Components / Arches) matches Go's default JSON encoding of untagged
  struct fields. `CreateRepoDialog` extracted from inline
  `ProjectDetailPage` and gains the mirror section. `SyncNowButton`
  shared across the 4 protocol pages. `RepoSettingsTab` Mirror config
  card at new route `/projects/:name/:type/:repo/settings`.
- **Upstream creds CRUD** (M5): 3 TanStack mutations
  (`useCreateUpstreamCred` / `usePatchUpstreamCred` /
  `useDeleteUpstreamCred`). `UpstreamCredDialog` (create + edit modes
  share form; blank-preserves-existing PATCH). `UpstreamCredsTab` table
  with inline Dialog-composed delete confirmation carrying the
  mirror-orphan warning. `ProjectSettingsPage` mounted at new route
  `/projects/:name/settings`.

### Tests + verification (M6 — plan 08-06)

- 5 per-protocol fake-upstream integration tests
  (`*_mirror_integration_test.go`): each proves
  (a) first-sync ingest N artifacts, (b) progress row reaches final
  state, (c) second-sync idempotency (row count stable + total_bytes
  == 0).
- 6 new Playwright specs across M3–M6: mirror-create, mirror-sync-now,
  mirror-settings, upstream-creds, docker-clone, mirror-upload-rejected.
  All parse via `--list` (79 tests across 22 files).
- **Codex rescue**: 9 questions, 5 real-issue findings applied as atomic
  commits (see "Codex rescue" section below).
- Full `go test ./...` green across 35 packages; `npm run build` clean;
  78/78 vitest green.

## Envelope codes introduced

| Wire code | HTTP | Source |
|-----------|------|--------|
| `repo.mirror_type_unsupported` | 400 | `internal/api/repos.go handleCreateRepo` |
| `repo.mirror_url_invalid` | 400 | `handleCreateRepo` |
| `repo.mirror_filter_invalid` | 400 | `handleCreateRepo`, `handlePatchRepo` |
| `repo.mirror_url_immutable` | 400 | `handlePatchRepo` |
| `repo.mirror_cred_wrong_project` | 400 | `handleCreateRepo`, `handlePatchRepo` |
| `repo.repo_is_mirror` | 403 | `internal/httpx/mirror_guard.go` (MirrorGuard / MirrorGuardFixed) |
| `sync.mirror_overrides_not_allowed` | 400 | `internal/httpx/sync_rest.go` |
| `sync.sync_already_running` | 409 | `internal/httpx/sync_rest.go` |
| `sync.invalid_request_body` | 400 | `internal/httpx/sync_rest.go` (16 KiB body cap) |

## Routes added

| Route | Handler |
|-------|---------|
| `GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}` — progress fields | `internal/api/repos_list.go:handleGetSyncJob` |
| `/projects/:name/:type/:repo/settings` (React Router) | `RepoSettingsTab` |
| `/projects/:name/settings` (React Router) | `ProjectSettingsPage` with Upstream creds tab |

## Deviations + notable decisions

- **APT/RPM/PyPI/Helm filter JSON uses PascalCase** (Names, Suites,
  Components, Arches, Globs) — the Go structs in
  `internal/protocol/*/upstream_parse.go` carry no JSON tags, so
  `encoding/json` serialises field names verbatim. Verified via grep
  across all 4 protocol packages.
- **`MirrorGuardFixed` variant** introduced because APT/RPM/PyPI/Helm
  mount hard-coded type segments in their URLs (`/{project}/deb/{repo}/...`
  etc.) rather than a `{type}` chi param.
- **openapi.yaml lives at `internal/api/openapi.yaml`** (co-located with
  `generate.go`'s `go:generate` directive); no repo-root `openapi.yaml`.
- **`RepoSettingsTab` + `ProjectSettingsPage` were created fresh** —
  no pre-existing repo-settings or project-settings surface to extend.
- **Helm integration test (`sync_mirror_integration_test.go`)** is
  scope-distinct from the pre-existing Phase-7 OCI-sourced helm tests
  (`helm/oci_mirror_test.go` + `oci/helm_mirror_test.go`).
- **Docker model intentionally differs** — no `is_mirror` flag on
  Docker repos. Per-click "Clone external image" modal clones individual
  Docker images into a user-chosen Docker repo (D-03). The
  `CloneImageDialog` opens repeatedly on demand.
- **`is_mirror` + `mirror_upstream_url` are structurally immutable**
  at both the API boundary (400 `mirror_url_immutable`) and the UI
  layer (`RepoSettingsTab` Save handler explicitly excludes both
  fields from the PATCH body — T-08-04-01 mitigation).
- **Blank-preserves-existing** PATCH semantics for upstream
  credentials — the UI strips password/token keys from the PATCH body
  entirely when left blank so the backend preserves the stored secret
  (T-08-05-03). Type-layer secret exclusion: `UpstreamCred` has no
  password/token fields.
- **Auto-mode checkpoint resolution**: Plan 08-06 is `autonomous: false`
  with a human-verify checkpoint. Under
  `workflow.auto_advance: true`, the executor auto-approved the
  checkpoint AFTER performing the Codex pass + applying all real-issue
  fixes — the auto-approval closed the checkpoint, it did not skip the
  required Codex action.

## Deferred to v1.2 (per design spec non-goals)

- **Drift purge / stale-artifact tracking** — accumulator semantics only
  in v1.1. Requires `mirror_sources` table + per-artifact `last_seen_at`
  + stale badge UI.
- **Scheduled / recurring sync** — external cron curl satisfies v1.1.
- **Git mirror** — v1.2.
- **Pull-through proxy cache** — different architecture (lazy-fetch on
  first client request).
- **Air-gap enforcement of `AllowExternalActions`** — config flag exists,
  unused in v1.1.
- **Raw / S3 upstream support** — no enumerable upstream index, feature
  doesn't fit.
- **Change-upstream-URL flow** — delete+recreate is the escape hatch.

## Codex rescue

Full dialogue + triage table in `08-06-CODEX-RESCUE.md`. Summary:

- 9 questions asked (correctness / leakage / middleware / race concerns).
- 5 real-issue findings applied as atomic commits:
  - **Q2** `sync_jobs.last_error` leakage →
    `internal/jobs/pool.go sanitizeJobError` (commit `9369e71`).
    Scrubs Authorization headers + `/var/lib/omnirepo/*` + `/tmp/*` paths,
    truncates to 1 KiB. Raw error continues to flow to slog for operators.
  - **Q3a** PyPI `DELETE /packages/{filename}` outside MirrorGuard →
    moved into `MirrorGuardFixed(..., "pypi")` group (commit `4844bb1`).
  - **Q3b** OCI tag DELETE routes outside MirrorGuard → wrapped both
    `{project}/{type}/{repo}/tags/{tag}` forms with `mirrorGuard`
    (commit `4844bb1`).
  - **Q5** `current_step` unbounded → `MaxStepLen=1 KiB` + `clampStep`
    with UTF-8-safe truncation (commit `9369e71`).
  - **Q7** CountRepoInflight/Enqueue race upgraded beyond T-08-01-04's
    documented severity → new `CountRepoInflightTx` runs the check
    inside the writer tx, making check+Enqueue atomic via SQLite's
    writer-pool serialisation (commit `65acd35`).
- 4 noise findings discarded with rationale (1, 3c, 4, 6, 8, 9).

All 5 applied fixes ship with at least one regression test.

## Stats

- Plans: 6 (08-01..08-06)
- Tasks: ~24 (08-01 grew to 4 tasks during gap-closure revision; 08-06
  had 5 tasks including the Codex-rescue gate + phase-closure paperwork)
- Files created/modified: ~60
- New tests: ~35 across Go integration tests + Playwright specs + vitest
  unit tests + Codex-fix regression tests
- Codex-applied fixes: 3 atomic commits (5 findings across them)
- Commits: ~25 spanning all 6 plans

## Verification summary (final)

- `go test ./... -count=1 -timeout=300s` — all 35 packages green
- `go build ./...` — clean
- `go vet ./...` — clean
- `npm run build` — clean (1,339 kB bundle)
- `npm test -- --run` — 78/78 vitest green
- `npx playwright test --list` — 79 tests parse across 22 files
- `make lint-protocol-redaction` / `check-contrast` /
  `lint-spacing-carveout` / `lint-axe-devdep` — all clean
- `make lint-typography` / `make grep-cdn` — pre-existing failures
  unchanged, documented in `deferred-items.md`, zero new additions from
  any Phase-8 plan

Phase 8 is closed; v1.1 is shippable for the upstream-mirror + Docker-
clone surface.
