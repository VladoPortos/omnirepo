---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 07
subsystem: ui
tags: [react, typescript, milkdown, docker-registry, rpm, apt, pypi, helm, s3, raw]

# Dependency graph
requires:
  - phase: 05-05
    provides: common components (DataTable, Dropzone, SnippetPanel, SeverityBadge, InlineSearch, FilterChips, CopyButton)
  - phase: 05-06
    provides: project pages, App.tsx router, format utilities
provides:
  - 7 repo detail pages (Docker, RPM, APT, PyPI, Helm, RAW, S3)
  - RepoDetailRouter dispatching by repo.type
  - ScanSummary component with severity bar chart and CVE table
  - MarkdownEditor (Milkdown WYSIWYG) for repo README editing
  - RepoPageLayout shared layout with breadcrumb, tabs, settings
affects: [05-08, 05-09, 05-10]

# Tech tracking
tech-stack:
  added: ["@milkdown/kit", "@milkdown/react", "@milkdown/theme-nord"]
  patterns: [shared-repo-layout, type-dispatch-router, grouped-table-pattern]

key-files:
  created:
    - web/src/pages/repo/RepoDetailRouter.tsx
    - web/src/pages/repo/RepoPageLayout.tsx
    - web/src/pages/repo/DockerRepoPage.tsx
    - web/src/pages/repo/RpmRepoPage.tsx
    - web/src/pages/repo/AptRepoPage.tsx
    - web/src/pages/repo/PypiRepoPage.tsx
    - web/src/pages/repo/HelmRepoPage.tsx
    - web/src/pages/repo/RawRepoPage.tsx
    - web/src/pages/repo/S3BucketPage.tsx
    - web/src/components/common/ScanSummary.tsx
    - web/src/components/common/MarkdownEditor.tsx
  modified:
    - web/src/App.tsx

key-decisions:
  - "RepoPageLayout extracts shared breadcrumb/tabs/settings/danger-zone for all repo types"
  - "PyPI and Helm pages use grouped table pattern (expand to see versions)"
  - "RAW and S3 pages share prefix/directory navigation pattern with breadcrumb"
  - "ScanSummary renders severity bar chart with proportional color segments"
  - "MarkdownEditor uses Milkdown with nord theme and commonmark preset"

patterns-established:
  - "Shared repo layout: RepoPageLayout provides common structure, type-specific content via children"
  - "Type dispatch: RepoDetailRouter switches on repo.type to render correct page component"
  - "Grouped expandable tables: PyPI/Helm group artifacts by name, expand to show versions"
  - "File browser pattern: RAW/S3 use prefix navigation with breadcrumb and up-arrow"

requirements-completed: [UI-08, UI-12]

# Metrics
duration: 9min
completed: 2026-04-16
---

# Phase 05 Plan 07: Repo Detail Pages Summary

**7 repo detail pages (Docker/RPM/APT/PyPI/Helm/RAW/S3) with type-specific layouts, scan summary component, and Milkdown markdown editor**

## Performance

- **Duration:** 9 min
- **Started:** 2026-04-16T10:35:43Z
- **Completed:** 2026-04-16T10:45:41Z
- **Tasks:** 2
- **Files modified:** 14

## Accomplishments
- All 7 non-Git repo detail pages with type-specific custom layouts per D-09 through D-17
- RepoDetailRouter dispatches to correct page based on repo.type from URL params
- ScanSummary component with severity bar chart, CVE table, rescan/SBOM actions
- MarkdownEditor wrapping Milkdown WYSIWYG with commonmark and nord theme
- Shared RepoPageLayout with breadcrumb, tabs (Content/Scan/Settings), snippet panel, danger zone

## Task Commits

Each task was committed atomically:

1. **Task 1: RepoDetailRouter + Docker + package repos + RAW + S3** - `429c9d3` (feat)
2. **Task 2: MarkdownEditor + npm deps** - `ed584de` (feat)

## Files Created/Modified
- `web/src/pages/repo/RepoDetailRouter.tsx` - Type dispatch router
- `web/src/pages/repo/RepoPageLayout.tsx` - Shared layout with breadcrumb, tabs, settings
- `web/src/pages/repo/DockerRepoPage.tsx` - Docker tag list with scan badges, cosign, pull/promote
- `web/src/pages/repo/RpmRepoPage.tsx` - RPM package table with Arch column
- `web/src/pages/repo/AptRepoPage.tsx` - APT packages with Suite/Component filter chips
- `web/src/pages/repo/PypiRepoPage.tsx` - PyPI grouped by normalized project name
- `web/src/pages/repo/HelmRepoPage.tsx` - Helm grouped by chart name with App Version
- `web/src/pages/repo/RawRepoPage.tsx` - File browser with directory tree navigation
- `web/src/pages/repo/S3BucketPage.tsx` - Prefix drill-down with bucket stats
- `web/src/components/common/ScanSummary.tsx` - Severity bar chart and CVE table
- `web/src/components/common/MarkdownEditor.tsx` - Milkdown WYSIWYG editor
- `web/src/App.tsx` - Replaced placeholder RepoDetailRouter with real import

## Decisions Made
- Created RepoPageLayout to extract common repo page elements (breadcrumb, tabs, settings, danger zone) reducing duplication across 7 pages
- RAW and S3 pages created in Task 1 (not Task 2) because RepoDetailRouter imports them and Task 1 verification requires compilation
- PyPI and Helm use grouped expandable table pattern for multi-version artifacts
- ScanSummary severity bar chart uses proportional width segments with color coding

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] RAW and S3 pages created in Task 1 instead of Task 2**
- **Found during:** Task 1 (RepoDetailRouter compilation)
- **Issue:** RepoDetailRouter imports all page components; build fails without RAW and S3 pages
- **Fix:** Created full RAW and S3 page implementations in Task 1
- **Files modified:** web/src/pages/repo/RawRepoPage.tsx, web/src/pages/repo/S3BucketPage.tsx
- **Verification:** tsc --noEmit passes, npm run build succeeds
- **Committed in:** 429c9d3 (Task 1 commit)

**2. [Rule 1 - Bug] Fixed lucide-react title prop type error**
- **Found during:** Task 1 (TypeScript compilation)
- **Issue:** lucide-react icons don't accept `title` as a prop
- **Fix:** Wrapped icons with `<span title="...">` instead
- **Files modified:** web/src/pages/repo/DockerRepoPage.tsx
- **Verification:** tsc --noEmit passes
- **Committed in:** 429c9d3 (Task 1 commit)

---

**Total deviations:** 2 auto-fixed (1 blocking, 1 bug)
**Impact on plan:** Task ordering adjusted for compilation dependency. No scope creep.

## Issues Encountered
None

## Known Stubs
- All repo pages have empty data arrays (e.g., `const tags: DockerTag[] = []`) -- these are intentional placeholders that will be populated when artifact-specific API queries are added in future plans
- MarkdownEditor is created but not yet wired into the Settings tab -- will be integrated when repo settings API is connected

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- All 7 repo detail pages ready for API data wiring
- ScanSummary ready to receive real scan data from scan API queries
- MarkdownEditor ready for integration into repo settings forms

## Self-Check: PASSED

All 12 created files verified on disk. Both task commits (429c9d3, ed584de) found in git log.

---
*Phase: 05-rest-api-web-ui-production-dockerfile*
*Completed: 2026-04-16*
