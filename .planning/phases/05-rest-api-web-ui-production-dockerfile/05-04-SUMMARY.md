---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 04
subsystem: api
tags: [search, fts5, profile, apikeys, git-browse, spa, devproxy, dashboard]

requires:
  - phase: 05-01
    provides: "cursor pagination, FTS5 search, OpenAPI types"
  - phase: 05-02
    provides: "web/embed.go with dist/ assets"

provides:
  - "Search API with FTS5 MATCH and project membership filtering"
  - "Profile PATCH endpoint with diff-audit"
  - "User API key CRUD with shown-once secret"
  - "Projects list paginated with member_count/repo_count"
  - "Project detail with members and repos"
  - "Project activity feed from audit_log"
  - "Repos list + sync job log endpoints"
  - "Dashboard with storage/repo/user counts and scan findings"
  - "Git browse API (tree/blob/commits/commit/blame/compare)"
  - "SPA handler with index.html fallback"
  - "Dev proxy to Vite on :5173"

affects: [05-05, 05-06, 05-07, 05-08]

tech-stack:
  added: []
  patterns:
    - "Shown-once secret pattern for API key creation (same as s3_keys)"
    - "Diff-audit pattern for profile updates"
    - "SPA fallback via fs.Stat + index.html redirect"
    - "go-git v6 PlainOpen for read-only repo browsing"

key-files:
  created:
    - internal/api/search.go
    - internal/api/search_test.go
    - internal/api/profile.go
    - internal/api/profile_test.go
    - internal/api/apikeys.go
    - internal/api/projects_full.go
    - internal/api/projects_full_test.go
    - internal/api/repos_list.go
    - internal/api/dashboard.go
    - internal/api/dashboard_test.go
    - internal/api/git_browse.go
    - internal/api/git_browse_test.go
    - internal/httpx/spa.go
    - internal/httpx/devproxy.go
    - internal/httpx/spa_test.go
  modified:
    - internal/api/admin_phase1.go
    - internal/metadata/apikeys.go
    - internal/metadata/users.go

key-decisions:
  - "FTS data populated via IndexRepo (not automatic on repos.Create); search tests seed FTS explicitly"
  - "Git browse uses go-git v6 PlainOpen for read-only tree/blob/commit walking"
  - "Blob size capped at 5 MB, blame at 1 MB for DoS mitigation (T-05-04-02)"
  - "SPA handler only serves from embedded dist/ FS, never host filesystem (T-05-04-03)"
  - "Dashboard storage_total_bytes=0 (filesystem df unavailable in pure Go without syscall)"

patterns-established:
  - "mountX(r chi.Router) method pattern for modular endpoint groups"
  - "actorIsProjectMember inline check for project-scoped endpoints"

requirements-completed: [SRCH-03, SRCH-04, API-05, API-06, SYNC-06, OPS-04, UI-02, UI-03]

duration: 12m
completed: 2026-04-16
---

# Phase 05 Plan 04: Remaining API Endpoints + SPA + Git Browse Summary

**Search, profile, API keys, projects, repos, dashboard, git browse API with go-git v6 tree walking, plus SPA handler and dev proxy infrastructure**

## Performance

- **Duration:** 12 min
- **Started:** 2026-04-16T09:58:12Z
- **Completed:** 2026-04-16T10:10:12Z
- **Tasks:** 2
- **Files modified:** 19

## Accomplishments

### Task 1: Search + profile + API keys + projects list + dashboard endpoints
- **Search API** (`GET /api/v1/search`): FTS5 MATCH across repos_fts with kind/severity/project filters; results filtered by actor's project membership for non-super-admins
- **Profile** (`PATCH /api/v1/me`): email and avatar_seed update with diff-audit pattern
- **API keys** (`GET/POST/DELETE /api/v1/me/api-keys`): CRUD with shown-once secret discipline (same pattern as S3 keys)
- **Projects list** (`GET /api/v1/projects`): paginated with member_count, repo_count, size_bytes; filtered by membership
- **Project detail** (`GET /api/v1/projects/{name}`): full detail with members list and repos
- **Project activity** (`GET /api/v1/projects/{name}/activity`): recent audit events scoped to project
- **Repos list** (`GET /api/v1/projects/{name}/repos`): paginated repos with sync-jobs sub-endpoints
- **Sync job log** (`GET .../sync-jobs`, `GET .../sync-jobs/{id}`): SYNC-06 compliance
- **Dashboard** (`GET /api/v1/dashboard`): storage, repo count, user count, scan findings summary

### Task 2: SPA handler + dev proxy + git browse API
- **SPA handler** (`internal/httpx/spa.go`): serves embedded dist/ with index.html fallback for client-side routing; only serves from go:embed FS (T-05-04-03)
- **Dev proxy** (`internal/httpx/devproxy.go`): reverse proxy to Vite on :5173 when OMNIREPO_DEV=1
- **Git browse API** (7 endpoints under `/api/v1/projects/{name}/repos/git/{repo}/`):
  - `/refs` — list from git_refs table
  - `/tree/{ref}/*` — directory listing via go-git tree walking
  - `/blob/{ref}/*` — file content with binary detection (5 MB limit)
  - `/commits/{ref}` — paginated commit log
  - `/commit/{sha}` — single commit with changed files
  - `/blame/{ref}/*` — per-line blame (1 MB limit)
  - `/compare/{base}...{head}` — diff between two refs

## Test Coverage

- `search_test.go`: 3 tests (empty query, FTS data, membership filtering)
- `profile_test.go`: 3 tests (email change, avatar seed, empty email rejection)
- `projects_full_test.go`: 4 tests (pagination, member count, detail, non-member denied)
- `dashboard_test.go`: 2 tests (stats returned, unauthenticated denied)
- `git_browse_test.go`: 4 tests (tree, blob, commits, non-member denied) with programmatic bare repo creation
- `spa_test.go`: 5 tests (root, static asset, unknown path fallback, deep path, IsDevMode)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] FTS data not auto-populated on repo creation**
- **Found during:** Task 1 (search tests)
- **Issue:** repos.Create does not automatically populate repos_fts; search tests returned 0 results
- **Fix:** Created seedRepoWithFTS helper that calls IndexRepo inside WriteTx alongside CreateInTx
- **Files modified:** internal/api/search_test.go

**2. [Rule 2 - Missing functionality] UsersRepo missing UpdateAvatarSeed and Count methods**
- **Found during:** Task 1 (profile + dashboard endpoints)
- **Issue:** Profile needs UpdateAvatarSeed, dashboard needs user Count
- **Fix:** Added both methods to internal/metadata/users.go
- **Files modified:** internal/metadata/users.go

**3. [Rule 2 - Missing functionality] APIKeysRepo missing ListByUser method**
- **Found during:** Task 1 (API keys list endpoint)
- **Issue:** No method to list a user's API keys
- **Fix:** Added ListByUser to internal/metadata/apikeys.go
- **Files modified:** internal/metadata/apikeys.go

## Commits

| Task | Commit | Description |
|------|--------|-------------|
| 1 | 4331840 | Search, profile, API keys, projects list, repos list, dashboard endpoints |
| 2 | 1e48a26 | SPA handler, dev proxy, git browse API |

## Self-Check: PASSED

- All 15 created files verified on disk
- Both commit hashes (4331840, 1e48a26) verified in git log
- `go build ./...` succeeds
- All 21 tests pass across internal/api and internal/httpx
