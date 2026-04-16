---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 12
subsystem: testing
tags: [e2e, playwright, api-test, bench, air-gap]
dependency_graph:
  requires: [05-07, 05-08, 05-09, 05-10, 05-11]
  provides: [TEST-03, TEST-04, TEST-05, D-48, D-49]
  affects: [web/e2e, internal/api, test/bench/throughput]
tech_stack:
  added: ["@playwright/test"]
  patterns: [e2e-golden-path, api-integration-test, bench-build-tag]
key_files:
  created:
    - web/playwright.config.ts
    - web/e2e/login.spec.ts
    - web/e2e/dashboard.spec.ts
    - web/e2e/projects.spec.ts
    - web/e2e/upload.spec.ts
    - web/e2e/search.spec.ts
    - web/e2e/admin.spec.ts
    - web/e2e/profile.spec.ts
    - web/e2e/airgap.spec.ts
    - web/tsconfig.e2e.json
    - internal/api/api_test.go
    - test/bench/throughput/throughput_test.go
  modified:
    - web/package.json
    - web/package-lock.json
    - Makefile
decisions:
  - "E2E tests use resilient selectors with fallback patterns for UI elements"
  - "Air-gap test uses Node.js fs module directly instead of shell commands"
  - "API integration tests cover 14 endpoint groups as separate TestAPI_ functions"
  - "Bench throughput test gated behind //go:build bench tag"
  - "Separate tsconfig.e2e.json for Playwright tests needing Node.js types"
metrics:
  duration: 10m
  completed: "2026-04-16T11:38:00Z"
  tasks: 3
  files: 15
---

# Phase 05 Plan 12: E2E + API Tests + Bench + Air-Gap Gates Summary

Playwright E2E suite covering golden path with API integration tests exercising every REST endpoint group and bench throughput target

## Task Results

### Task 1: Playwright E2E suite + air-gap gate
**Commit:** cc1d751

Created 8 E2E test specs plus Playwright config and air-gap verification:

- **login.spec.ts**: Sign-in form rendering, dark mode default, invalid credentials error, valid login with forced password change redirect, password change completion, logout flow
- **dashboard.spec.ts**: Dashboard heading, stat cards (storage/repos/users), quick action buttons (Create Project, Upload Artifact)
- **projects.spec.ts**: Golden path: create project via UI, create repos of each type via API then verify in UI
- **upload.spec.ts**: File upload via dropzone on raw repo detail page
- **search.spec.ts**: Search input rendering, query execution, filter chip interaction
- **admin.spec.ts**: All 7 admin pages (Maintenance, TLS, GC, Trash, Users, Audit, Trivy) render with key elements
- **profile.spec.ts**: User info display, API key creation with one-time reveal, password change
- **airgap.spec.ts**: D-49 air-gap gate: walks dist/ with fs module (no shell exec), checks for external URLs; also verifies no CDN script/link tags in index.html

### Task 2: API integration tests + bench target
**Commit:** 1edcd8c

- **internal/api/api_test.go**: 14 TestAPI_ functions covering auth (login/change-password/logout/stale-cookie), projects (create/list/get/delete/404), members (add/remove), repos (create 7 types/list/patch/delete/wipe), search (query/kind filter), admin audit (list/filter/pagination), admin maintenance (get/toggle-on/toggle-off), admin trash (list), admin trivy (db status), admin users (list/create/get/edit/delete), admin TLS (current/history), admin GC (trigger 202), profile (get-me/patch-me/api-keys CRUD with one-time secret reveal), dashboard (repo_count/user_count)
- **test/bench/throughput/throughput_test.go**: BenchmarkUploadThroughput (10 MiB parallel uploads with bytes/sec reporting) and BenchmarkScanThroughput (dashboard read throughput), gated behind `//go:build bench`
- **Makefile**: Added `bench-throughput` target; `bench` now includes throughput benchmarks

### Task 3: Human verification checkpoint
Auto-approved (auto_advance=true). Phase 5 complete.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed Trivy endpoint path**
- **Found during:** Task 2
- **Issue:** Plan specified `/api/v1/admin/trivy` but actual endpoint is `/api/v1/admin/trivy/db/status`
- **Fix:** Updated test to use correct path
- **Files modified:** internal/api/api_test.go

**2. [Rule 1 - Bug] Fixed GC endpoint path**
- **Found during:** Task 2
- **Issue:** Plan specified `/api/v1/admin/gc/trigger` and `/admin/gc/status` but actual endpoint is `/api/v1/admin/gc` (POST only)
- **Fix:** Updated test to use correct path, removed non-existent status endpoint
- **Files modified:** internal/api/api_test.go

**3. [Rule 1 - Bug] Fixed API key field name and status code**
- **Found during:** Task 2
- **Issue:** API key creation uses `name` field (not `label`) and returns 201 (not 200); response has `secret` field
- **Fix:** Updated test to use correct field name, accept 201, assert `secret` presence
- **Files modified:** internal/api/api_test.go

**4. [Rule 1 - Bug] Removed maintenance 503 assertion**
- **Found during:** Task 2
- **Issue:** Maintenance middleware is applied at server level (httpx package), not in api.Mount(); test server doesn't wire it
- **Fix:** Removed 503 assertion, kept toggle on/off verification with state assertions
- **Files modified:** internal/api/api_test.go

## Verification Results

- All 14 TestAPI_ tests pass (go test -run TestAPI_ passes)
- TypeScript compilation passes for both main app and e2e tests
- Bench test compiles with bench build tag (go vet -tags=bench passes)
- Makefile bench-throughput target added

## Self-Check: PASSED

All 12 created files verified on disk. Both commit hashes (cc1d751, 1edcd8c) confirmed in git log.
