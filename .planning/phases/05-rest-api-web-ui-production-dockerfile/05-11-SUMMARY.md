---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 11
subsystem: infra
tags: [dockerfile, multi-stage, trivy, makefile, spa, production]

# Dependency graph
requires:
  - phase: 05-03
    provides: SPA build pipeline and embedded web assets
  - phase: 05-04
    provides: Dev proxy and SPA handler in httpx package
provides:
  - Production 4-stage Dockerfile (node -> go -> trivy -> alpine)
  - Makefile targets for frontend, build-all, docker, e2e, bench
  - First-boot Trivy DB seeding from baked /opt/trivy-db/
  - SPA serving wired into app startup (embedded or dev proxy)
affects: [05-12, deployment, ci]

# Tech tracking
tech-stack:
  added: []
  patterns: [multi-stage-dockerfile, first-boot-seed, spa-notfound-handler]

key-files:
  created:
    - internal/app/seed_trivy_test.go
  modified:
    - Dockerfile
    - Makefile
    - internal/app/app.go

key-decisions:
  - "SeedTrivyDB exported for testability; called with hardcoded /opt/trivy-db path from Run"
  - "Duplicate dev target replaced with OMNIREPO_DEV=1 parallel Go+Vite variant"

patterns-established:
  - "First-boot seed pattern: check baked dir exists, check target empty, copy files"

requirements-completed: [AIR-01, AIR-02, AIR-03, OPS-09]

# Metrics
duration: 5min
completed: 2026-04-16
---

# Phase 05 Plan 11: Production Dockerfile + Makefile + Trivy DB Seed Summary

**4-stage Dockerfile (node/go/trivy/alpine), Makefile build-all/docker/e2e targets, and first-boot Trivy DB seeding with SPA wiring**

## Performance

- **Duration:** 5 min
- **Started:** 2026-04-16T11:21:24Z
- **Completed:** 2026-04-16T11:26:08Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- Production 4-stage Dockerfile: node:22-alpine SPA build, golang:1.25-alpine Go binary with embedded SPA, aquasec/trivy:0.69.3 DB bake, alpine:3.21 runtime with non-root UID 1000
- Makefile targets: frontend, build-all, docker (with version injection), dev (Go+Vite parallel), e2e (Playwright), bench (sqlite+git)
- SeedTrivyDB copies baked DB from /opt/trivy-db/ to data volume on first boot; silent no-op outside Docker
- SPA handler wired as router NotFound: embedded dist/ in production, reverse proxy to Vite :5173 in dev mode

## Task Commits

Each task was committed atomically:

1. **Task 1: Production Dockerfile (4-stage)** - `371b1a9` (feat)
2. **Task 2: Makefile targets + Trivy DB first-boot seed** - `e21bddc` (feat)

## Files Created/Modified
- `Dockerfile` - 4-stage multi-stage build (node -> go -> trivy -> alpine)
- `Makefile` - Added frontend, build-all, docker, dev, e2e, bench targets
- `internal/app/app.go` - SeedTrivyDB function, SPA/dev-proxy wiring in Run()
- `internal/app/seed_trivy_test.go` - 3 tests: no-baked-dir, first-boot copy, skip-existing

## Decisions Made
- Exported SeedTrivyDB with bakedDir parameter for testability (Run passes hardcoded "/opt/trivy-db")
- Replaced old simple `dev:` target with OMNIREPO_DEV=1 parallel Go+Vite variant to avoid duplicate target warning

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed duplicate Makefile dev target**
- **Found during:** Task 2
- **Issue:** Old `dev:` target (line 14) conflicted with new `dev:` target causing Makefile override warning
- **Fix:** Removed the old simple `dev:` target, kept the new OMNIREPO_DEV=1 version
- **Files modified:** Makefile
- **Verification:** `make build` runs without warnings

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** Trivial fix to avoid Makefile warning. No scope creep.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Dockerfile ready for `docker build` (requires network for npm ci and trivy DB download)
- SPA wired into Go server via embedded FS or dev proxy
- Plan 05-12 (final integration/verification) can proceed

## Self-Check: PASSED

All files exist, all commits verified.

---
*Phase: 05-rest-api-web-ui-production-dockerfile*
*Completed: 2026-04-16*
