---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 03
subsystem: api
tags: [admin, audit, maintenance, trash, trivy, tls, users, rest-api]

requires:
  - phase: 05-01
    provides: cursor pagination primitives, OpenAPI types, base admin endpoints
provides:
  - Admin audit log endpoint with cursor pagination and filters
  - Functional maintenance mode middleware (settings-backed, 503+Retry-After)
  - Trash management endpoints (list, restore, purge)
  - Settings CRUD endpoint
  - Trivy DB admin (status, upload with path-traversal prevention, online pull)
  - Full user CRUD (list with pagination, get detail, patch email/admin/password-reset)
  - TLS certificate history and current cert info
affects: [05-09-admin-ui, 05-10-production-dockerfile]

tech-stack:
  added: []
  patterns:
    - "mountAdmin* pattern for admin endpoint registration on chi.Router"
    - "Atomic tarball extraction with path traversal prevention for Trivy DB upload"
    - "Settings-backed middleware (MaintenanceMode reads from SettingsRepo)"

key-files:
  created:
    - internal/api/admin_audit.go
    - internal/api/admin_maintenance.go
    - internal/api/admin_trash.go
    - internal/api/admin_settings.go
    - internal/api/admin_trivy.go
    - internal/api/admin_users_full.go
    - internal/api/admin_tls_history.go
    - internal/api/admin_audit_test.go
    - internal/api/admin_maintenance_test.go
    - internal/api/admin_trash_test.go
    - internal/api/admin_trivy_test.go
    - internal/api/admin_users_full_test.go
    - internal/httpx/middleware_maintenance_test.go
  modified:
    - internal/httpx/middleware_maintenance.go
    - internal/httpx/router.go
    - internal/api/admin_phase1.go
    - internal/app/app.go
    - internal/metadata/users.go
    - internal/metadata/repos.go
    - internal/tls/certholder.go

key-decisions:
  - "Reuse ActionTriggerGC gate for all admin endpoints (super-admin-only) rather than adding new action constants"
  - "MaintenanceMode middleware accepts *SettingsRepo parameter (nil-safe backward compat)"
  - "Trivy DB upload extracts to DataRoot/tmp then atomic rename to DataRoot/trivy/db"

patterns-established:
  - "mountAdmin* method pattern: func (d Deps) mountAdminXxx(r chi.Router) with RequireCan gate"
  - "Settings-backed feature flags: read key from settings table, compare string value"

requirements-completed: [OPS-03, OPS-05, OPS-07, OPS-09, SCAN-09, SCAN-10, SCAN-11, API-03, API-06]

duration: 11m
completed: 2026-04-16
---

# Phase 05 Plan 03: Admin API Endpoints Summary

**Admin REST surface: audit log with cursor pagination + filters, functional maintenance mode middleware, trash management, Trivy DB upload/pull/status, full user CRUD, TLS cert history, settings CRUD**

## Performance

- **Duration:** 11 min
- **Started:** 2026-04-16T09:44:01Z
- **Completed:** 2026-04-16T09:55:55Z
- **Tasks:** 2
- **Files modified:** 21

## Accomplishments
- Upgraded maintenance middleware from pass-through stub to functional (reads settings table, returns 503+Retry-After for write methods when maintenance_mode=true, GET/HEAD/OPTIONS always pass through)
- Built 7 admin endpoint files covering audit log browsing, maintenance toggle, trash list/restore/purge, settings CRUD, Trivy DB status/upload/pull, user list/detail/patch, TLS cert history/current
- 29 tests total (16 Task 1 + 13 Task 2) covering all endpoints including 403 gates, cursor pagination, path traversal prevention

## Task Commits

1. **Task 1: Admin audit + maintenance + trash + settings endpoints** - `4dfca32` (feat)
2. **Task 2: Admin Trivy DB + users full CRUD + TLS history endpoints** - `7932009` (feat)

## Files Created/Modified
- `internal/httpx/middleware_maintenance.go` - Functional maintenance mode (reads settings, blocks writes with 503)
- `internal/httpx/router.go` - Pass Settings to MaintenanceMode middleware
- `internal/api/admin_audit.go` - GET /admin/audit with cursor pagination + 6 filter params
- `internal/api/admin_maintenance.go` - GET/POST /admin/maintenance toggle
- `internal/api/admin_trash.go` - GET/POST/DELETE /admin/trash management
- `internal/api/admin_settings.go` - GET/PATCH /admin/settings CRUD
- `internal/api/admin_trivy.go` - Trivy DB status/upload/pull with atomic swap
- `internal/api/admin_users_full.go` - User list/get/patch with project memberships
- `internal/api/admin_tls_history.go` - TLS cert history from uploaded dir + current cert info
- `internal/api/admin_phase1.go` - Wire all 7 mount functions into Mount()
- `internal/app/app.go` - Pass Settings to httpx.Deps
- `internal/metadata/users.go` - Add ListAll() and UpdateEmail() methods
- `internal/metadata/repos.go` - Add Restore() method
- `internal/tls/certholder.go` - Add Current() method

## Decisions Made
- Reuse ActionTriggerGC as the super-admin gate for all admin endpoints rather than adding new action constants (keeps policy table simple)
- MaintenanceMode middleware signature changed from no-arg to accepting *metadata.SettingsRepo (nil-safe for tests)
- Trivy DB upload uses temp dir under DataRoot/tmp + atomic os.Rename to DataRoot/trivy/db (T-05-03-01 mitigation)
- Audit log pagination uses keyset on (occurred_at DESC, id DESC) for stable ordering
- User list returns project membership names as a tag array for admin UI display

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing Critical] Added ReposRepo.Restore() method**
- **Found during:** Task 1 (admin_trash.go restore handler)
- **Issue:** Plan referenced restoring soft-deleted repos but ReposRepo had no Restore method
- **Fix:** Added Restore(ctx, id) that clears deleted_at to nil
- **Files modified:** internal/metadata/repos.go
- **Committed in:** 4dfca32

**2. [Rule 2 - Missing Critical] Added UsersRepo.ListAll() and UpdateEmail()**
- **Found during:** Task 2 (admin_users_full.go list/patch handlers)
- **Issue:** UsersRepo had no list method and no email update method
- **Fix:** Added ListAll() returning all live users sorted by login, and UpdateEmail(ctx, id, email)
- **Files modified:** internal/metadata/users.go
- **Committed in:** 7932009

**3. [Rule 2 - Missing Critical] Added CertHolder.Current() method**
- **Found during:** Task 2 (admin_tls_history.go current cert handler)
- **Issue:** CertHolder had Get(*tls.ClientHelloInfo) but no simple accessor for the admin endpoint
- **Fix:** Added Current() returning *tls.Certificate directly from the atomic pointer
- **Files modified:** internal/tls/certholder.go
- **Committed in:** 7932009

---

**Total deviations:** 3 auto-fixed (all Rule 2 - missing critical functionality)
**Impact on plan:** All auto-fixes were necessary to implement the planned endpoints. No scope creep.

## Issues Encountered
- fmt.Sscanf multi-value context error in Trivy status handler — fixed by using proper two-value assignment

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- All admin endpoints ready for admin UI pages (Plan 09)
- Maintenance middleware functional — admin can toggle maintenance mode via REST
- Trivy DB admin endpoints ready for vulnerability scanning admin page

---
*Phase: 05-rest-api-web-ui-production-dockerfile*
*Completed: 2026-04-16*

## Self-Check: PASSED
