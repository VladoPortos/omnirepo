---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 05
subsystem: ui
tags: [react, typescript, tanstack-query, react-router, framer-motion, shadcn, sidebar, auth, theme]

# Dependency graph
requires:
  - phase: 05-02
    provides: shadcn components, Tailwind 4 setup, fonts, index.css
  - phase: 05-04
    provides: OpenAPI spec, Go API endpoints, auth handlers
provides:
  - API client (fetch wrapper with auth, error handling, upload progress)
  - TypeScript types mirroring all OpenAPI schemas
  - TanStack Query hooks for all REST endpoints
  - Auth hooks (login, logout, changePassword, useMe)
  - Theme hook with localStorage persistence and dark default
  - Maintenance mode hook
  - App shell layout (sidebar + breadcrumbs + maintenance banner)
  - Login and forced password change pages
  - React Router 7 with auth guards and code-split admin routes
  - 404 page
affects: [05-06, 05-07, 05-08, 05-09, 05-10, 05-11]

# Tech tracking
tech-stack:
  added: [framer-motion@12.38.0 (updated)]
  patterns: [ApiClient singleton, TanStack Query structured keys, auth guard HOC, lazy admin routes]

key-files:
  created:
    - web/src/api/client.ts
    - web/src/api/types.ts
    - web/src/api/queries.ts
    - web/src/hooks/useAuth.ts
    - web/src/hooks/useTheme.ts
    - web/src/hooks/useMaintenance.ts
    - web/src/components/layout/Sidebar.tsx
    - web/src/components/layout/Breadcrumbs.tsx
    - web/src/components/layout/MaintenanceBanner.tsx
    - web/src/components/layout/AppShell.tsx
    - web/src/pages/LoginPage.tsx
    - web/src/pages/ChangePasswordPage.tsx
    - web/src/pages/NotFoundPage.tsx
    - web/src/App.tsx
  modified:
    - web/src/main.tsx
    - web/package.json

key-decisions:
  - "framer-motion upgraded from 12.7.4 to 12.38.0 to fix motion-dom export compatibility"
  - "Admin page stubs created as default exports for lazy loading; future plans replace them"
  - "Sidebar state persisted in localStorage (not cookie) for consistency with theme storage"

patterns-established:
  - "ApiClient singleton: all API calls go through api.get/post/patch/del with typed returns"
  - "TanStack Query keys: structured as ['entity', ...params] with invalidation on mutations"
  - "Auth guard pattern: AuthGuard + MustChangePasswordGuard wrap AppShell in router"
  - "Lazy admin routes: React.lazy with .catch fallback for code splitting"

requirements-completed: [UI-05, UI-13]

# Metrics
duration: 6min
completed: 2026-04-16
---

# Phase 5 Plan 05: SPA Shell Summary

**React SPA shell with API client, auth flow (login + forced password change), collapsible sidebar, breadcrumbs, dark-mode-first theme toggle, and maintenance banner**

## Performance

- **Duration:** 6 min
- **Started:** 2026-04-16T10:14:06Z
- **Completed:** 2026-04-16T10:20:58Z
- **Tasks:** 2
- **Files modified:** 39

## Accomplishments
- Complete API client with typed fetch wrapper, cookie auth, 401 redirect, XHR upload progress
- Full TypeScript type coverage of all OpenAPI schemas (User, Project, Repo, Scan, Git, S3Key, etc.)
- App shell layout: collapsible sidebar (shadcn Sidebar), breadcrumbs, sticky maintenance banner
- Login and forced password change pages with framer-motion animations per D-02/D-05
- React Router 7 with createBrowserRouter, auth guards, and lazy-loaded admin routes

## Task Commits

Each task was committed atomically:

1. **Task 1: API client + types + auth hooks + theme hook** - `ca081d4` (feat)
2. **Task 2: App shell + login + forced password change + 404** - `e87c08b` (feat)

## Files Created/Modified
- `web/src/api/client.ts` - Fetch wrapper with auth, error handling, upload progress
- `web/src/api/types.ts` - TypeScript types for all OpenAPI schemas
- `web/src/api/queries.ts` - TanStack Query hooks with structured keys
- `web/src/hooks/useAuth.ts` - Auth composition hook (login, logout, changePassword)
- `web/src/hooks/useTheme.ts` - Dark/light theme toggle with localStorage
- `web/src/hooks/useMaintenance.ts` - Maintenance mode status hook
- `web/src/components/layout/Sidebar.tsx` - Collapsible sidebar with nav items and user menu
- `web/src/components/layout/Breadcrumbs.tsx` - Route-based breadcrumbs
- `web/src/components/layout/MaintenanceBanner.tsx` - Sticky amber banner with disable button
- `web/src/components/layout/AppShell.tsx` - Layout composition
- `web/src/pages/LoginPage.tsx` - Centered card login with error states
- `web/src/pages/ChangePasswordPage.tsx` - Forced password change with validation
- `web/src/pages/NotFoundPage.tsx` - 404 page with Dashboard CTA
- `web/src/App.tsx` - Router with AuthGuard, MustChangePasswordGuard, lazy admin routes
- `web/src/main.tsx` - QueryClientProvider + RouterProvider + dark mode init
- `web/src/pages/admin/*.tsx` - Stub pages for lazy loading (7 files)

## Decisions Made
- framer-motion upgraded from 12.7.4 to 12.38.0 (version shipped with package.json had incompatible motion-dom exports)
- Admin page stubs created as proper default-export modules for React.lazy compatibility; future plans replace them with real implementations
- Sidebar collapse state uses localStorage key `omnirepo-sidebar-open` (consistent with theme in localStorage rather than cookies)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Updated framer-motion to fix motion-dom export error**
- **Found during:** Task 2 (npm run build)
- **Issue:** framer-motion 12.7.4 imported `isBezierDefinition` from motion-dom which doesn't export it in the installed version
- **Fix:** Updated framer-motion to 12.38.0 via `npm install framer-motion@latest`
- **Files modified:** web/package.json, web/package-lock.json
- **Verification:** `npm run build` succeeds
- **Committed in:** e87c08b (Task 2 commit)

**2. [Rule 3 - Blocking] Created admin page stubs for lazy imports**
- **Found during:** Task 2 (tsc --noEmit)
- **Issue:** App.tsx lazy imports referenced admin page modules that didn't exist yet
- **Fix:** Created 7 stub admin pages with default exports
- **Files modified:** web/src/pages/admin/{Users,Audit,TLS,Trivy,GC,Trash,Maintenance}Page.tsx
- **Verification:** `npx tsc --noEmit` passes
- **Committed in:** e87c08b (Task 2 commit)

---

**Total deviations:** 2 auto-fixed (2 blocking issues)
**Impact on plan:** Both fixes necessary for build to succeed. No scope creep.

## Issues Encountered
None beyond the deviations documented above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- App shell ready for all downstream page plans (dashboard, projects, repo detail, search, admin screens)
- API client and query hooks available for immediate use by all UI feature plans
- Auth flow complete; all protected routes will render within the AppShell

---
*Phase: 05-rest-api-web-ui-production-dockerfile*
*Completed: 2026-04-16*

## Self-Check: PASSED

All 15 key files verified present. Both task commits (ca081d4, e87c08b) verified in git log.
