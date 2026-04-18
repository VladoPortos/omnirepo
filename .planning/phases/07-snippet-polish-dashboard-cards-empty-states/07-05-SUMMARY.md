---
phase: 07-snippet-polish-dashboard-cards-empty-states
plan: 05
subsystem: [admin-api, dashboard-ui]
tags: [backend, admin, dashboard, api, tdd]
requires:
  - phase-06 StatusBadge + status token CSS (Phase 6 primitive foundation)
  - api.Deps (DB.Reader, existing writeJSON/writeJSONError helpers)
  - internal/auth/policy.go ActionTriggerGC (reused for read-only summary)
  - TanStack Query v5 (useQuery with `enabled` gate)
provides:
  - GET /api/v1/admin/jobs/summary endpoint (D-06 locked shape)
  - jobsSummaryResponse struct (wire shape contract)
  - Six pure threshold functions (storage/failures/scanFindings/jobs/tls/trivyDB)
  - AdminJobsSummary TS interface
  - useAdminJobsSummary TanStack hook (queryKey ['admin','jobs','summary'])
affects:
  - internal/api/admin_phase1.go (mount site — one-line insertion)
  - web/src/api/queries.ts (hook + interface appended)
tech-stack-added: []
tech-stack-patterns:
  - "three per-bucket COUNT queries instead of one FILTER aggregate (modernc-friendly)"
  - "pointer time.Time fields marshal to null when sql.NullTime.Valid is false"
  - "per-function typed overrides object (not aggregated blob)"
  - "boundary semantics: warn-inclusive / fail-inclusive per UI-SPEC table"
  - "jobsVariant returns ONLY healthy/warning/failure (no new StatusBadge variants)"
key-files-created:
  - internal/api/admin_jobs.go
  - internal/api/admin_jobs_test.go
  - web/src/lib/dashboard-thresholds.ts
  - web/src/lib/__tests__/dashboard-thresholds.test.ts
key-files-modified:
  - internal/api/admin_phase1.go
  - web/src/api/queries.ts
decisions:
  - "[07-05] /admin/jobs/summary reuses ActionTriggerGC gate — no new policy action for read-only summary"
  - "[07-05] Three per-bucket COUNT queries instead of FILTER aggregate — simpler review, same perf on indexed sync_jobs"
  - "[07-05] jobsVariant returns only healthy/warning/failure — D-02 locks that set, no new StatusBadge variants invented"
  - "[07-05] Six individual override objects per threshold function, not a single DashboardThresholds blob — typed, test-simple, admin-friendly"
  - "[07-05] Status='pending' on the wire is exposed as 'queued' in the JSON body — operator-friendly naming while preserving sync_jobs schema"
metrics:
  duration: "5m13s"
  completed-date: "2026-04-18"
  tasks-completed: "2 / 2"
  files-touched: "6"
---

# Phase 7 Plan 05: Dashboard Data Sources Summary

**One-liner:** New `/api/v1/admin/jobs/summary` endpoint (D-06 locked
shape, super-admin-gated via `ActionTriggerGC`) plus six pure D-02
threshold utilities and a TanStack hook — the infrastructure Wave 2's
Composition row (plan 07-07) consumes for the C-1…C-6 dashboard cards.

## What Shipped

### Backend — `GET /api/v1/admin/jobs/summary`

- **Wire shape (D-06 locked):**

  ```json
  {
    "running":          <int>,
    "queued":           <int>,    // maps to sync_jobs.status='pending'
    "failed_last_24h":  <int>,
    "last_completed_at": "<RFC3339>" | null,
    "last_failed_at":    "<RFC3339>" | null
  }
  ```

- **Handler:** `internal/api/admin_jobs.go` — `jobsSummaryResponse`
  struct + `mountAdminJobs` + `handleJobsSummary`.
- **Mount site:** `internal/api/admin_phase1.go` line 269 — one-line
  `d.mountAdminJobs(r)` inserted immediately after `d.mountAdminGC(r)`
  inside the phase-1 super-admin `r.Group` so the parent
  `SessionOrAPIKey` + `membershipResolver` chain applies unchanged.
- **Auth:** `RequireCan(auth.ActionTriggerGC)` — the same super-admin
  gate `/admin/gc`, `/admin/trivy`, `/admin/tls` already use. No new
  policy action introduced for a read-only summary (CONTEXT §D-06).

### Backend — handler tests (3)

`internal/api/admin_jobs_test.go`:

1. **`TestAdminJobsSummary_SuperAdmin_ReturnsShape`** — seeds four
   `sync_jobs` rows (1 running + 1 pending + 1 failed in the last 24h +
   1 done completed now), asserts all 5 D-06 keys are present in the
   response body, asserts `running==1`, `queued==1`,
   `failed_last_24h==1`, and asserts `last_completed_at` /
   `last_failed_at` are both non-null RFC3339 strings.
2. **`TestAdminJobsSummary_NonSuperAdmin_403`** — logs in a plain user,
   asserts the `ActionTriggerGC` gate rejects with 403.
3. **`TestAdminJobsSummary_Unauthenticated_401`** — no session cookie,
   asserts `SessionOrAPIKey` middleware rejects with 401.

Factory: reuses the existing `newGCRESTServer(t)` verbatim — it already
wires `GCDeps.SyncJobs` into `api.Deps`, so `admin_jobs` shares exactly
the same in-process `sync_jobs` table.

### SQL form taken — per-bucket COUNT (NOT FILTER aggregate)

The plan carried forward an "Assumption A2" fallback from research
(switch `COUNT(*) FILTER (WHERE status='running')` → `SUM(CASE WHEN
status='running' THEN 1 ELSE 0 END)` if modernc rejected FILTER). In
implementation, **neither form was used**. The handler issues three
separate single-bucket `COUNT(*) WHERE status=...` queries instead.
Rationale:

- **Simpler to review:** each query maps 1:1 to a response field; no
  mental arithmetic linking `filter.position → response.field`.
- **Same perf on sync_jobs:** small indexed table, three round trips
  are free; the single-aggregate form's theoretical advantage
  disappears below a few hundred rows.
- **Side-steps Assumption A2 entirely:** if FILTER syntax ever
  regressed we'd need to refactor three spots anyway — the per-bucket
  form just lives where we already want it to.

`last_completed_at` / `last_failed_at` still use
`ORDER BY updated_at DESC LIMIT 1` against `sync_jobs.updated_at`
(the column flipped by `MarkDone`/`MarkFailed`/
`MarkPermanentlyFailed` per `metadata/sync_jobs.go`).

### Frontend — threshold utilities

`web/src/lib/dashboard-thresholds.ts` — six pure functions (all
return `StatusVariant`):

| Function                       | Inputs                                                         | Defaults                                                     |
| ------------------------------ | -------------------------------------------------------------- | ------------------------------------------------------------ |
| `storageVariant`               | `used`, `total`, `overrides?`                                  | `warnRatio=0.70`, `failRatio=0.90` — boundary inclusive-of-warn; `total<=0 → disabled` |
| `failuresVariant`              | `count`, `overrides?`                                          | `warnUpper=5` — `0 → healthy`, `1..5 → warning`, `>5 → failure` |
| `scanFindingsVariant`          | `currentCritical`, `neverScanned`, `overrides?`                | `warnUpper=5`; `neverScanned → disabled`                     |
| `jobsVariant`                  | `running`, `queued`, `failedLast24h`, `lastCompletedAt`        | **Healthy/Warning/Failure ONLY** — no new variants invented  |
| `tlsVariant`                   | `daysRemaining`, `hasUploadedCert`, `overrides?`               | `warnDays=30`, `failDays=14`; `!hasUploadedCert → disabled`  |
| `trivyDBVariant`               | `ageDays`, `everInitialised`, `overrides?`                     | `warnDays=7`, `failDays=30`; `!everInitialised → disabled`   |

Each function carries a typed optional `overrides` object (six
separate shapes, not one aggregated blob per RESEARCH Open Question
3). This keeps TypeScript narrow: callers can't accidentally pass
Trivy overrides to `storageVariant`.

### Frontend — threshold tests (54 boundary cases)

`web/src/lib/__tests__/dashboard-thresholds.test.ts` — 54 vitest
assertions across the six functions. Boundary coverage for each
numeric input: just-below, equal, just-above the warn and fail
thresholds; plus `disabled`-precedence assertions for the three
functions that return `disabled` on a flag input.

**Edge cases caught by tests:**

- `storageVariant(10, -5)` — defensive `total <= 0 → disabled` (not
  just `total == 0`).
- `jobsVariant(5, 0, 5, null)` — `failed == running+queued` (not
  strictly greater) maps to **healthy**, not warning. This prevents a
  busy-but-recovering pool from flashing warning.
- `tlsVariant(14, true)` — equal to the fail threshold is still
  warning (the UI-SPEC table reads "failure if `<14`", not `<=14`).
- `trivyDBVariant(7, true)` — equal to warn threshold stays healthy
  (UI-SPEC: "warning `>7` days").

### Frontend — TanStack hook

Appended to `web/src/api/queries.ts` after `useDashboardStorage`:

- **`AdminJobsSummary`** interface — 1:1 mirror of the D-06 wire
  shape; timestamps typed as `string | null`.
- **`useAdminJobsSummary(enabled: boolean)`** — queryKey
  `['admin', 'jobs', 'summary']`, `queryFn` hits `/admin/jobs/summary`,
  `staleTime: 60_000` (per UI-SPEC §Interaction Patterns threshold
  display cadence), `enabled` gates fetching so non-super-admins
  never issue a 403-generating request.

## Deviations from Plan

### None — plan executed exactly as written, with one implementation-detail refinement

- **SQL shape refinement (not a deviation):** the plan's Action
  section hand-wrote the handler with a single FILTER-clause
  aggregate and documented Assumption A2 as fallback. Implementation
  chose the per-bucket form directly (see "SQL form taken" above).
  This stays within the `<behavior>` contract the plan locked — the
  wire shape, auth gate, and count semantics are identical. No test
  changes were required.

- **`jobsVariant` signature kept `lastCompletedAt` parameter** even
  though every `idle` path now maps to `healthy` (per plan's Phase 7
  checker fix). Rationale: the card UI in plan 07-07 will use the
  timestamp for a tooltip ("last successful run: X") without needing
  a second threshold function. Parameter is marked `_lastCompletedAt`
  to silence the unused-variable lint without breaking the call-site
  shape the plan tests already exercise.

## Authentication Gates

None encountered. Both tasks were pure-code changes with no external
service dependencies.

## Test Results

| Gate                                 | Result                                     |
| ------------------------------------ | ------------------------------------------ |
| `go test -run TestAdminJobsSummary`  | 3/3 passed                                 |
| `go test ./internal/api/`            | all passed (20.0s, zero regressions)       |
| `make lint-protocol-redaction`       | clean                                      |
| `cd web && npm run test -- --run`    | 63/63 passed (54 new + 9 pre-existing)     |
| `cd web && npm run build`            | green (2.87s)                              |
| `make lint-typography`               | clean                                      |
| `make lint-spacing-carveout`         | clean                                      |

## Commits

| Hash      | Kind     | Message                                                                 |
| --------- | -------- | ----------------------------------------------------------------------- |
| `2c16eb2` | test     | `test(07-05): add failing tests for admin jobs summary handler`         |
| `f26f3e9` | feat     | `feat(07-05): admin jobs summary endpoint for dashboard C-4 card`       |
| `84ddf51` | test     | `test(07-05): add failing tests for dashboard threshold utilities`      |
| `6fb0134` | feat     | `feat(07-05): dashboard threshold utilities + admin jobs summary hook`  |

TDD flow preserved: RED commit → GREEN commit per task (4 commits,
2 tasks × 2 gates each). No REFACTOR commits were needed.

## Downstream Consumers (plan 07-07)

- `useAdminJobsSummary(isSuperAdmin)` + `jobsVariant(...)` → C-4
  Background Jobs card
- `storageVariant(used, total)` → C-1 Storage Status card (already
  has data via `useDashboardStorage`)
- `failuresVariant(count)` → C-2 Recent Failures card
- `scanFindingsVariant(critical, never)` → C-3 Scan Findings Trend
- `tlsVariant(daysRemaining, hasUploadedCert)` → C-5 TLS Cert Expiry
  (data via existing `GET /api/v1/admin/tls/current`)
- `trivyDBVariant(ageDays, everInitialised)` → C-6 Trivy DB Freshness
  (data via existing `GET /api/v1/admin/trivy/db/status`)

All six pieces are now importable and boundary-tested. Plan 07-07 can
compose the Composition row with zero backend work.

## Self-Check: PASSED

Verified:

- `internal/api/admin_jobs.go` → FOUND
- `internal/api/admin_jobs_test.go` → FOUND
- `internal/api/admin_phase1.go` → FOUND (mount line verified at L269)
- `web/src/lib/dashboard-thresholds.ts` → FOUND
- `web/src/lib/__tests__/dashboard-thresholds.test.ts` → FOUND
- `web/src/api/queries.ts` → FOUND (hook + interface verified)
- Commit `2c16eb2` → FOUND
- Commit `f26f3e9` → FOUND
- Commit `84ddf51` → FOUND
- Commit `6fb0134` → FOUND
