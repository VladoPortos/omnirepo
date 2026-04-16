---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 01
subsystem: api
tags: [openapi, oapi-codegen, fts5, pagination, sqlite, cursor]

requires:
  - phase: 01-foundation
    provides: "SQLite metadata layer, FTS5 virtual tables, auth types, chi router"
  - phase: 03-package-repos-rpm-apt-pypi-helm
    provides: "Protocol-specific FTS5 tables (rpm_fts, deb_fts, pypi_fts, helm_fts)"
  - phase: 04-s3-git
    provides: "S3 keys, git refs, migrations 016-019"
provides:
  - "OpenAPI 3.1 spec with 71 operationIds covering all /api/v1 endpoints"
  - "oapi-codegen-generated Go types (types_gen.go) for all API schemas"
  - "Cursor-based pagination primitives (EncodeCursor, DecodeCursor, ParsePaginationParams)"
  - "FTS5 UNION search across 7 virtual tables with ranked results"
  - "Migration 020: trivy_db_meta table + maintenance_mode setting"
affects: [05-02, 05-03, 05-04, 05-05, 05-06, 05-07, 05-08, 05-09, 05-10, 05-11, 05-12]

tech-stack:
  added: [oapi-codegen/v2@2.6.0, oapi-codegen/runtime@1.4.0]
  patterns: [cursor-pagination, fts5-union-search, openapi-types-only-generation]

key-files:
  created:
    - internal/api/openapi.yaml
    - internal/api/types_gen.go
    - internal/api/generate.go
    - internal/api/cursor.go
    - internal/api/cursor_test.go
    - internal/metadata/search.go
    - internal/metadata/search_test.go
    - internal/metadata/migrations/020_maintenance_trivydb.up.sql
    - internal/metadata/migrations/020_maintenance_trivydb.down.sql
  modified:
    - internal/api/types_phase1.go
    - internal/api/admin_phase1.go
    - go.mod
    - go.sum

key-decisions:
  - "ErrorResponse excluded from OpenAPI spec to avoid pointer-field conflicts with existing handler code in errors.go"
  - "LoginResponse/MeResponse fields marked required in spec so generated types use concrete (non-pointer) fields matching existing handler code"
  - "types_phase1.go reduced to type aliases bridging generated types to existing handler references"
  - "FTS5 search uses subquery wrapping per UNION ALL arm to enable per-arm LIMIT in SQLite compound selects"
  - "Hyphens preserved in FTS5 sanitizer for CVE ID matching (CVE-2026-0001)"

patterns-established:
  - "OpenAPI types-only generation: go:generate oapi-codegen -generate types -o types_gen.go"
  - "Cursor pagination: base64-URL JSON tuple with limit clamping [1, 200]"
  - "FTS5 UNION search: subquery-wrapped arms with per-arm LIMIT, final ORDER BY score"
  - "FTS5 query sanitization: strip operators, quote keywords, prefix-match tokens"

requirements-completed: [API-01, API-04, SRCH-03, SRCH-04]

duration: 13min
completed: 2026-04-16
---

# Phase 05 Plan 01: API Contracts & Search Foundation Summary

**OpenAPI 3.1 spec with 71 endpoints, oapi-codegen types, cursor pagination, FTS5 UNION search across 7 tables, and migration 020 for maintenance mode + Trivy DB metadata**

## Performance

- **Duration:** 13 min
- **Started:** 2026-04-16T09:17:40Z
- **Completed:** 2026-04-16T09:31:00Z
- **Tasks:** 2
- **Files modified:** 14

## Accomplishments
- Hand-written OpenAPI 3.1 spec covering all 71 /api/v1 endpoints (auth, projects, repos, scans, sync, git browsing, search, admin surfaces)
- oapi-codegen v2.6.0 generates Go types from spec; types coexist with existing Phase 1-4 handler code
- Cursor-based pagination with base64-URL JSON tuple encoding and limit clamping
- FTS5 UNION search across all 7 virtual tables with ranked results, kind filtering, and FTS5 operator sanitization
- Migration 020 adds trivy_db_meta table and seeds maintenance_mode setting

## Task Commits

1. **Task 1: OpenAPI 3.1 spec + oapi-codegen types generation** - `6056a62` (feat)
2. **Task 2: Cursor pagination + FTS5 UNION search + migration 020** - `fb98140` (feat)

## Files Created/Modified
- `internal/api/openapi.yaml` - Complete OpenAPI 3.1 specification for all /api/v1 endpoints
- `internal/api/types_gen.go` - oapi-codegen generated Go types from the spec
- `internal/api/generate.go` - go:generate directive for oapi-codegen
- `internal/api/cursor.go` - Cursor pagination primitives (EncodeCursor, DecodeCursor, ParsePaginationParams)
- `internal/api/cursor_test.go` - 14 table-driven cursor tests
- `internal/metadata/search.go` - FTS5 UNION search (SearchAll) across 7 virtual tables
- `internal/metadata/search_test.go` - 8 search tests (all tables, kind filter, empty, special chars, CVE, RPM)
- `internal/metadata/migrations/020_maintenance_trivydb.up.sql` - trivy_db_meta + maintenance_mode seed
- `internal/metadata/migrations/020_maintenance_trivydb.down.sql` - rollback
- `internal/api/types_phase1.go` - Reduced to aliases for generated types
- `internal/api/admin_phase1.go` - MeResponse field name update (ID -> Id)

## Decisions Made
- ErrorResponse excluded from OpenAPI spec to avoid type conflicts with the hand-written version in errors.go (both cannot coexist in one package)
- Response type fields marked `required` in spec so oapi-codegen generates concrete types (not pointers) matching existing handler code patterns
- types_phase1.go uses type aliases to bridge generated types to existing references without breaking handler code
- FTS5 UNION ALL arms wrapped in subqueries to enable per-arm LIMIT in SQLite compound selects
- Hyphens preserved in FTS5 query sanitizer because CVE IDs (CVE-2026-0001) require them

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] oapi-codegen 3.1 nullable type incompatibility**
- **Found during:** Task 1 (types generation)
- **Issue:** oapi-codegen v2.6.0 doesn't support OAS 3.1 union-type nullable (`type: [string, "null"]`); throws parse error
- **Fix:** Replaced 3.1-style nullable with 3.0-compatible `nullable: true` pattern
- **Files modified:** internal/api/openapi.yaml
- **Committed in:** 6056a62

**2. [Rule 1 - Bug] Generated type field naming and pointer conflicts**
- **Found during:** Task 1 (build verification)
- **Issue:** Generated types used pointer fields for optional properties and `Id` instead of `ID`; conflicted with existing handler struct literals
- **Fix:** Marked critical response fields as `required` in spec; updated admin_phase1.go field reference; reduced types_phase1.go to aliases
- **Files modified:** internal/api/openapi.yaml, internal/api/admin_phase1.go, internal/api/types_phase1.go
- **Committed in:** 6056a62

**3. [Rule 3 - Blocking] Missing oapi-codegen/runtime vendor dependency**
- **Found during:** Task 1 (build)
- **Issue:** Generated types import `github.com/oapi-codegen/runtime/types` which was not vendored
- **Fix:** `go get github.com/oapi-codegen/runtime@latest && go mod vendor`
- **Files modified:** go.mod, go.sum, vendor/
- **Committed in:** 6056a62

**4. [Rule 1 - Bug] SQLite UNION ALL with per-arm ORDER BY**
- **Found during:** Task 2 (search tests)
- **Issue:** SQLite rejects ORDER BY inside individual UNION ALL arms without subquery wrapping
- **Fix:** Wrapped each FTS5 query arm in `SELECT * FROM (...)` subquery
- **Files modified:** internal/metadata/search.go
- **Committed in:** fb98140

**5. [Rule 1 - Bug] FTS5 hyphen stripping broke CVE ID search**
- **Found during:** Task 2 (CVE search test)
- **Issue:** sanitizeFTS5Query stripped hyphens, turning "CVE-2026-0001" into "CVE20260001" which didn't match indexed data
- **Fix:** Removed hyphen from the strip list; hyphens are safe inside FTS5 double-quoted tokens
- **Files modified:** internal/metadata/search.go
- **Committed in:** fb98140

---

**Total deviations:** 5 auto-fixed (3 bugs, 2 blocking)
**Impact on plan:** All auto-fixes necessary for correctness. No scope creep.

## Issues Encountered
None beyond the auto-fixed deviations above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- OpenAPI spec defines all endpoint contracts for Plans 02-12
- Generated types ready for handler implementation
- Cursor pagination ready for all list endpoints
- FTS5 search ready for the /api/v1/search handler
- Migration 020 ready for maintenance mode and Trivy DB status handlers

## Self-Check: PASSED

---
*Phase: 05-rest-api-web-ui-production-dockerfile*
*Completed: 2026-04-16*
