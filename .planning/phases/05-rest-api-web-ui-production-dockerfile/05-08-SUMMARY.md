---
phase: 05-rest-api-web-ui-production-dockerfile
plan: 08
subsystem: ui
tags: [react, shiki, git, syntax-highlighting, diff-viewer, blame, dompurify]

# Dependency graph
requires:
  - phase: 05-05
    provides: "Common UI components, RepoPageLayout, DataTable"
  - phase: 05-06
    provides: "Repo detail pages pattern, SnippetPanel, router"
provides:
  - "GitRepoPage with file tree, commit log, blame, diff, branch comparison"
  - "Shiki syntax highlighting singleton with on-demand language loading"
  - "DOMPurify-sanitized code rendering (defense-in-depth)"
  - "Git API query hooks (refs, tree, blob, commits, blame, compare)"
affects: [05-09, 05-10, 05-11]

# Tech tracking
tech-stack:
  added: [shiki, dompurify, react-diff-viewer-continued]
  patterns: [on-demand-language-loading, dompurify-sanitized-html-rendering, unified-diff-parsing]

key-files:
  created:
    - web/src/lib/highlight.ts
    - web/src/components/git/FileTree.tsx
    - web/src/components/git/FileViewer.tsx
    - web/src/components/git/RefSelector.tsx
    - web/src/components/git/CommitLog.tsx
    - web/src/components/git/DiffViewer.tsx
    - web/src/components/git/BlameViewer.tsx
    - web/src/components/git/CommitDetail.tsx
    - web/src/components/git/BranchCompare.tsx
    - web/src/pages/repo/GitRepoPage.tsx
  modified:
    - web/src/api/queries.ts
    - web/src/pages/repo/RepoDetailRouter.tsx
    - web/package.json

key-decisions:
  - "Shiki core with on-demand language loading to avoid bundling all grammars"
  - "DOMPurify sanitization of Shiki output as defense-in-depth (T-05-08-01)"
  - "base-ui render prop pattern instead of asChild for link buttons"
  - "Unified diff parsing for react-diff-viewer-continued input"

patterns-established:
  - "Shiki singleton: create once, load languages on demand via dynamic import"
  - "DOMPurify allowlist: only pre/code/span tags with class/style attrs"
  - "Git browse state: ref + path + viewingFile managed in GitRepoPage"

requirements-completed: [UI-08]

# Metrics
duration: 10min
completed: 2026-04-16
---

# Phase 5 Plan 08: Git Repo Detail Page Summary

**Full Git repository browser with Shiki syntax highlighting, split-pane diffs via react-diff-viewer-continued, per-line blame, and branch comparison**

## Performance

- **Duration:** 10 min
- **Started:** 2026-04-16T10:48:46Z
- **Completed:** 2026-04-16T10:59:38Z
- **Tasks:** 2
- **Files modified:** 16

## Accomplishments
- Built complete Git repository browser with file tree navigation, syntax-highlighted code viewer, commit log, blame viewer, diff viewer, and branch comparison
- Implemented Shiki highlighter singleton with on-demand language loading (D-38) keeping bundle size minimal
- Added DOMPurify defense-in-depth sanitization for all Shiki HTML output (T-05-08-01)
- File viewer enforces 1 MB display limit with download fallback (T-05-08-02)

## Task Commits

Each task was committed atomically:

1. **Task 1: Git repo page + file tree + syntax highlighting** - `e779eb8` (feat)
2. **Task 2: Commit log + diff viewer + blame viewer + branch comparison** - `da92b83` (feat)

## Files Created/Modified
- `web/src/lib/highlight.ts` - Shiki highlighter singleton with on-demand language loading
- `web/src/components/git/FileTree.tsx` - GitHub-style file tree with folder/file icons
- `web/src/components/git/FileViewer.tsx` - Syntax-highlighted code viewer with DOMPurify
- `web/src/components/git/RefSelector.tsx` - Branch/tag selector dropdown
- `web/src/components/git/CommitLog.tsx` - Paginated commit history with avatars
- `web/src/components/git/DiffViewer.tsx` - Split-pane diff using react-diff-viewer-continued
- `web/src/components/git/BlameViewer.tsx` - Per-line blame with alternating block shading
- `web/src/components/git/CommitDetail.tsx` - Single commit view with per-file diffs
- `web/src/components/git/BranchCompare.tsx` - Branch comparison with commit and file diffs
- `web/src/pages/repo/GitRepoPage.tsx` - Git repo detail page with Files/Commits/Refs/Compare tabs
- `web/src/api/queries.ts` - Added 7 Git API query hooks
- `web/src/pages/repo/RepoDetailRouter.tsx` - Added git case to repo type router

## Decisions Made
- Used `shiki/core` with `createHighlighterCore` instead of full Shiki bundle to enable on-demand language loading and keep initial bundle small
- Used `render` prop (base-ui pattern) instead of `asChild` for link-as-button composition
- Added BranchCompare as a 4th tab (beyond plan's 3 tabs) since it was specified in the plan action items
- Unified diff parsing extracts old/new content from patch strings for react-diff-viewer-continued

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed base-ui Button composition pattern**
- **Found during:** Task 1 (FileViewer)
- **Issue:** Used `asChild` prop which doesn't exist on base-ui Button (Radix pattern, not base-ui)
- **Fix:** Changed to `render={<a ... />}` prop which is the base-ui composition pattern
- **Files modified:** web/src/components/git/FileViewer.tsx
- **Committed in:** e779eb8

**2. [Rule 1 - Bug] Fixed Select onValueChange null handling**
- **Found during:** Task 1 (RefSelector)
- **Issue:** base-ui Select's `onValueChange` passes `string | null` but handler expected `string`
- **Fix:** Added null guard: `(val) => { if (val) onRefChange(val); }`
- **Files modified:** web/src/components/git/RefSelector.tsx
- **Committed in:** e779eb8

**3. [Rule 1 - Bug] Fixed Shiki text language fallback**
- **Found during:** Task 1 (highlight.ts)
- **Issue:** `@shikijs/langs/text` module doesn't exist; Shiki handles 'text' as built-in
- **Fix:** Skip loadLanguage for 'text' lang (built-in to Shiki core)
- **Files modified:** web/src/lib/highlight.ts
- **Committed in:** e779eb8

---

**Total deviations:** 3 auto-fixed (3 bugs)
**Impact on plan:** All auto-fixes necessary for type safety and correctness. No scope creep.

## Issues Encountered
None beyond the auto-fixed items above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Git repo browser fully wired, ready for integration testing
- All Git API hooks in place for any additional Git UI features

---
*Phase: 05-rest-api-web-ui-production-dockerfile*
*Completed: 2026-04-16*
