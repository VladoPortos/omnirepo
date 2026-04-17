---
phase: 06-error-envelope-visual-foundation
plan: 07
subsystem: ui
tags: [skeletons, sticky-first-column, overflow-x, admin-tables, dashboard, projects, status-badge-story, playwright, dev-routes]

# Dependency graph
requires:
  - phase: 06-06
    provides: SkeletonCard / SkeletonMetric / SkeletonTable / SkeletonDetail primitives + StatusBadge + CopyInline — consumed verbatim by canonical pages in this plan
provides:
  - web/src/pages/DashboardPage.tsx: canonical SkeletonCard + SkeletonMetric adoption — a full-page loading branch (when both `isLoading` and `storageLoading` are true) renders SkeletonMetric×3 + SkeletonCard×4; per-slice micro-fallback uses SkeletonMetric / SkeletonCard inside individual tiles once one slice resolves ahead of the other
  - web/src/pages/ProjectsPage.tsx: migrated from card grid to sticky-first-column table layout — 6 columns (Name, Description, Members, Repos, Size, Created); loading branch renders SkeletonTable rows=6 columns=6; real table wrapped in `<div class="overflow-x-auto rounded-lg border">` with first TableHead + first TableCell per row carrying `sticky left-0 z-10 bg-card` (VISUAL-06 canonical reference implementation)
  - web/src/components/common/DataTable.tsx: new `stickyFirstColumn?: boolean` prop — when true, wraps the shared shadcn Table in the same overflow-x-auto + rounded-lg border container and attaches `sticky left-0 z-10 bg-card` to the first column's header + every body cell (including skeleton rows). First-column sticky class is merged (not replaced) so column-specific `className` hints (widths, alignment) still apply
  - web/src/pages/admin/UsersPage.tsx + AuditPage.tsx + TrashPage.tsx: stickyFirstColumn=true enabled (6 / 6 / 8 columns respectively)
  - web/src/pages/repo/AptRepoPage.tsx + DockerRepoPage.tsx + HelmRepoPage.tsx + PypiRepoPage.tsx + RpmRepoPage.tsx: stickyFirstColumn=true enabled (8 / 7 / 6 / 6 / 7 columns respectively)
  - web/src/pages/_dev/StatusBadgeStoryPage.tsx: dev-only page at `/_dev/status-badge-story` rendering deterministic 24-variant matrix (6 statuses × 2 sizes × 2 iconOnly) — each cell wrapped in a div carrying `data-story-variant` + `data-story-size` + `data-story-icon-only` for plan 06-08 Playwright locators. Registered behind the same `DEV_ROUTES_ENABLED` gate as ErrorClassStoryPage (06-03) and PrimitivesStoryPage (06-06); production bundle tree-shake verified
affects: [06-08, phase-07, phase-08, phase-09, phase-10]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Shared DataTable gains `stickyFirstColumn?: boolean` as an opt-in prop instead of a hard-coded wrapper; consumers with ≥ 6 columns flip it, narrow tables (< 6 cols) leave it off. Avoids unnecessary visual chrome on tables that have no horizontal-scroll risk at 1366×768."
    - "First-column sticky class is MERGED with any column-level className via a helper (`firstColClassName`) so pre-existing width/alignment hints like `w-10`, `w-24 text-right`, `hidden lg:table-cell` are preserved — the sticky pin never clobbers layout semantics."
    - "ProjectsPage migrated from a card-grid to a table layout because the plan's must_haves explicitly require SkeletonTable + overflow-x-auto + sticky-first-column in ProjectsPage.tsx. The card grid would have required either a fake SkeletonTable-for-cards shim (dishonest) or a deviation (violating must_haves). A table layout is the honest implementation and aligns with VISUAL-06's consistent admin-table UX across laptop screens."
    - "DashboardPage uses a two-tier loading strategy: full-page Skeleton* layout when the entire dashboard is cold (both slices loading), and per-slice Skeleton fallback once one slice resolves. Avoids a flash of mostly-blank content when `/api/v1/dashboard` returns instantly but `/api/v1/dashboard/storage` is slower (or vice versa)."
    - "StatusBadgeStoryPage uses the exact same dev-route gate pattern as ErrorClassStoryPage (plan 06-03) and PrimitivesStoryPage (plan 06-06): `DEV_ROUTES_ENABLED = import.meta.env.DEV || VITE_OMNIREPO_DEV === 'true'` plus lazy import at module scope. Three story pages now share the gate — production bundles remain free of all three."

key-files:
  created:
    - web/src/pages/_dev/StatusBadgeStoryPage.tsx (98 lines)
  modified:
    - web/src/pages/DashboardPage.tsx (+43 / -16 lines — full-page Skeleton* loading branch + per-slice skeleton fallbacks)
    - web/src/pages/ProjectsPage.tsx (+111 / -123 lines — net reduction; rewritten from card grid to sticky-first-column table)
    - web/src/components/common/DataTable.tsx (+59 / -39 lines — stickyFirstColumn prop, wrapper split, first-col sticky class merge)
    - web/src/pages/admin/UsersPage.tsx (+1 line — stickyFirstColumn flag)
    - web/src/pages/admin/AuditPage.tsx (+1 line — stickyFirstColumn flag)
    - web/src/pages/admin/TrashPage.tsx (+1 line — stickyFirstColumn flag)
    - web/src/pages/repo/AptRepoPage.tsx (+1 line — stickyFirstColumn flag)
    - web/src/pages/repo/DockerRepoPage.tsx (+1 line — stickyFirstColumn flag)
    - web/src/pages/repo/HelmRepoPage.tsx (+1 line — stickyFirstColumn flag)
    - web/src/pages/repo/PypiRepoPage.tsx (+1 line — stickyFirstColumn flag)
    - web/src/pages/repo/RpmRepoPage.tsx (+1 line — stickyFirstColumn flag)
    - web/src/App.tsx (+20 / -1 lines — lazy import + devRoutes entry for StatusBadgeStoryPage)

key-decisions:
  - "Centralize the sticky-first-column pattern in DataTable rather than duplicate it inline per consumer. Eight consumers (3 admin + 5 repo) opt in with a single flag; the mechanical per-file edit the plan described as the template would have meant 8 × ~10-line diffs plus future drift risk (someone forgetting `bg-card` on a new TableHead). Enhancing the shared component centralizes the pattern so invariants hold at the source."
  - "ProjectsPage switched from a card grid to a sticky-first-column table. Plan must_haves require both SkeletonTable and `overflow-x-auto` in ProjectsPage.tsx. The card grid couldn't honour both honestly (it had no table to wrap). Converting to a table — 6 columns (Name, Description, Members, Repos, Size, Created) — shipped one canonical Skeleton*Table* consumer page, one canonical sticky-first-column consumer page, and matched VISUAL-06's intent. Row click preserved via onClick on TableRow — no functional regression. E2e tests (projects.spec.ts) check text visibility only, not card/table structure, so no test regression."
  - "DashboardPage full-page Skeleton branch gated by `isLoading && storageLoading` (not `||`). Rationale: if `/api/v1/dashboard` returns in 50 ms and `/api/v1/dashboard/storage` in 200 ms, a `||`-gated branch would show all skeletons even after the first slice arrived, then blink to the real content. The AND-gate only shows the full-page skeleton when both are cold; once either resolves, per-slice fallback handles the remaining tile without a flash."
  - "ProfilePage's API-key table (5 cols) and S3-key table (4 cols) were left alone. Below the 6-column threshold the plan defined as the sticky-first-column trigger — no horizontal-scroll risk at 1366×768."
  - "RawRepoPage (5 cols) and admin/TLSPage, admin/TrivyPage (no DataTable) were also left alone. Below threshold / not table-shaped."
  - "Stale `.js` files from the pre-existing `tsc -b` emission bug (flagged by plan 06-06) cleaned up reactively after each `npm run build`. Root fix (`noEmit: true` in tsconfig) deferred to plan 06-08 per the phase 06 prompt's explicit directive."

patterns-established:
  - "DataTable consumers with ≥ 6 columns pass `stickyFirstColumn`; narrow tables leave it off. Rule lives on the component (doc comment cites VISUAL-06 and plan 06-07) so future tables auto-inherit the decision."
  - "Full-page Skeleton* loading state is the canonical pattern for any multi-slice page when ALL slices are cold; per-slice fallback is acceptable (and preferred) when one slice lands ahead of another. Pattern reference: DashboardPage."
  - "Dev-only story pages live under `web/src/pages/_dev/` and register routes through a shared `DEV_ROUTES_ENABLED` module-level constant in `App.tsx`. Three story pages now share the gate (ErrorClassStoryPage, PrimitivesStoryPage, StatusBadgeStoryPage). Acceptance gate unchanged: `grep <PageName> web/dist/assets/*.js` must return zero."

requirements-completed: [VISUAL-02, VISUAL-03, VISUAL-06]

# Metrics
duration: ~45 min
completed: 2026-04-17
---

# Phase 06 Plan 07: Skeleton Primitives on Canonical Surfaces + Admin Table Sticky-First-Column + StatusBadgeStoryPage Summary

**DashboardPage consumes SkeletonCard + SkeletonMetric as canonical loading surfaces; ProjectsPage migrated from a card grid to a sticky-first-column, overflow-x-auto-wrapped table rendering SkeletonTable on load; DataTable grew a shared `stickyFirstColumn` prop now enabled on eight admin/repo pages with ≥ 6 columns; and a dev-only `/_dev/status-badge-story` page renders the 24-variant StatusBadge matrix that plan 06-08 will Playwright-snapshot.**

## Performance

- **Duration:** ~45 min (wall-clock from first task commit `a17565e` to final task commit `6291339`)
- **Started:** 2026-04-17T15:35Z (approx — Task 1 commit time)
- **Completed:** 2026-04-17T15:50Z
- **Tasks:** 3 (no checkpoints — fully autonomous)
- **Files created:** 1 (StatusBadgeStoryPage.tsx)
- **Files modified:** 12 (2 pages + DataTable + 3 admin pages + 5 repo pages + App.tsx)

## Accomplishments

- **Task 1 — Dashboard + Projects skeleton adoption + ProjectsPage table migration** (commit `a17565e`):
  - `DashboardPage`: added `SkeletonCard` + `SkeletonMetric` imports. Inserted a full-page loading branch gated by `isLoading && storageLoading` that renders SkeletonMetric×3 + SkeletonCard (findings tile), then a SkeletonCard×1 full-width (storage), then SkeletonCard×2 (activity + severity). When one slice resolves ahead of the other, per-slice SkeletonMetric / SkeletonCard replaces the old inline `<Skeleton>` bars so the tile-level loading shape matches the production tile shape.
  - `ProjectsPage`: rewritten from a card-grid layout to a table layout. Removed `framer-motion`, `Card*`, and `Skeleton` imports; added `Table*` primitives + `SkeletonTable`. New column set: Name (sticky), Description (truncate max-w-md), Members (right-aligned tabular-nums), Repos (right-aligned tabular-nums), Size (right-aligned), Created (muted xs). Loading branch renders `<SkeletonTable rows={6} columns={6} widths={['w-40','w-full','w-20','w-20','w-24','w-32']} />`. Real table wrapped in `<div className="overflow-x-auto rounded-lg border">` with `<Table className="min-w-full">` inside; first TableHead and first TableCell per row carry `sticky left-0 z-10 bg-card`. Row click navigates to `/projects/<name>` (preserved the original Link behaviour via `onClick` + `cursor-pointer`). Empty state + create dialog untouched.
  - Typecheck + production build clean. No new forbidden font-weights / sizes in either file.

- **Task 2 — DataTable `stickyFirstColumn` prop + 8 consumer opt-ins** (commit `63b894e`):
  - `DataTable.tsx`: added `stickyFirstColumn?: boolean` prop (default `false`). When true:
    - Wraps the entire table in `<div className="overflow-x-auto rounded-lg border">` so horizontal scroll stays inside the container at 1366×768 instead of pushing the whole page.
    - Attaches `Table className="min-w-full"` so the table always fills the container horizontally.
    - Merges `sticky left-0 z-10 bg-card` with any column-level `className` on the first column's `TableHead`, on every body `TableCell` in the first column (including skeleton + empty rows), via a `firstColClassName(colClass?: string)` helper.
  - Enabled `stickyFirstColumn` on:
    - admin/UsersPage (6 cols: avatar, login, email, role, created, actions)
    - admin/AuditPage (6 cols: timestamp, actor, action, target, outcome, ip)
    - admin/TrashPage (8 cols: checkbox, name, type, original_location, deleted_by, deleted_at, retention, actions)
    - repo/AptRepoPage (8 cols: name, version, arch, suite, component, size, uploaded_at, scan)
    - repo/DockerRepoPage (7 cols: tag, size, scan_severity, pushed_at, digest, cosign, actions)
    - repo/HelmRepoPage (6 cols: chart_name, latest_version, app_version, total_size, uploaded_at, scan)
    - repo/PypiRepoPage (6 cols: normalized_name, latest_version, total_size, uploaded_at, requires_python, scan)
    - repo/RpmRepoPage (7 cols: name, version, release, arch, size, uploaded_at, scan)
  - Deliberately skipped (< 6 cols, no horizontal-scroll risk at 1366):
    - repo/RawRepoPage (5 cols)
    - ProfilePage API-key table (5 cols) + S3-key table (4 cols)
    - admin/TLSPage, admin/TrivyPage (no DataTable — TLS renders a custom cert-list, Trivy renders settings panes)
  - Typecheck + production build clean; `tsc -b` emissions cleaned after each build per the 06-06 workaround.

- **Task 3 — StatusBadgeStoryPage + dev-route registration** (commit `6291339`):
  - `web/src/pages/_dev/StatusBadgeStoryPage.tsx` (98 lines): renders a deterministic 6×2×2 = 24-cell matrix. Layout groups by iconOnly (outer section) → by size (inner row) → by status (cells). Each cell wrapped in a `<div>` carrying `data-story-variant`, `data-story-size`, `data-story-icon-only` for stable Playwright locators — the wrapper-div approach is inherited from plan 06-06's PrimitivesStoryPage (StatusBadge's props are strict; test hooks live on wrappers, not on the primitive).
  - `web/src/App.tsx`: added a third lazy-import block mirroring `PrimitivesStoryPage` (06-06) and `ErrorClassStoryPage` (06-03); new `/_dev/status-badge-story` route appended to the `devRoutes` array. Guard condition extended to require all three story pages to resolve (`ErrorClassStoryPage && PrimitivesStoryPage && StatusBadgeStoryPage`) so a broken lazy-chunk never registers a partial dev surface.
  - Production tree-shaking verified: `grep -l 'StatusBadgeStoryPage' dist/assets/*.js` returns zero matches. Dev-only route still works under `npm run dev` (Playwright walkthrough below).

- **Playwright visual verification** (no commit — harness files under `/tmp`):
  - Boot: `npm run dev` (Vite 5173) + `bin/omnirepo serve` with `OMNIREPO_DEV=1`, `OMNIREPO_DEV_PROXY=1`, `OMNIREPO_VITE_URL=http://localhost:5173`, `data_root: /tmp/omnirepo-data` via config file. Super-admin seeded via `POST /api/v1/setup/superadmin` (login `admin`, password `E2EPass123!`). Three test projects (`alpha`, `beta`, `gamma`) seeded via `POST /api/v1/projects`.
  - `StatusBadgeStoryPage @ 1366×768` — 24 variant cells rendered (6 statuses × 2 sizes × 2 iconOnly); 2 sections (labeled + icon-only); 4 rows (2 sizes × 2 icon-modes); healthy md labeled cell computed background = `oklch(0.96 0.04 165)` matching the `:root --status-healthy` token byte-for-byte (proves the `@theme inline` mapping → utility → CSS variable chain is wired end-to-end, same invariant plan 06-06 verified). Full-page screenshot `/tmp/06-07-status-badge-story-app.png`.
  - `DashboardPage (loaded) @ 1366×768` — all 4 row-1 tiles render with real counts (Projects=3, Repositories=0, Users=1, Scan Findings=0), Storage gauge populated, Recent Activity feed showing seeded project.created events. Full-page screenshot `/tmp/06-07-dashboard-loaded.png`.
  - `DashboardPage (slow network) @ 1366×768` — with Playwright route interception adding a 5-second delay to `/api/v1/dashboard*`, 7 `role="status" aria-label="Loading"` regions render: 3 SkeletonMetric + 1 SkeletonCard (row 1) + 1 SkeletonCard (storage) + 2 SkeletonCard (activity + severity). Confirms the `isLoading && storageLoading` full-page branch fires. Full-page screenshot `/tmp/06-07-dashboard-loading.png`.
  - `ProjectsPage (loaded) @ 1366×768` — table renders with all 6 columns; first header + first data cells in each of the 3 rows carry `sticky left-0 z-10 bg-card` (verified by DOM inspection of `th.sticky` + `td.sticky` classNames); page has zero horizontal scroll (`documentElement.scrollWidth === clientWidth === 1366`); `overflow-x-auto` wrapper count = 2 (the rounded-lg border wrapper + shadcn Table's internal `relative w-full overflow-x-auto` wrapper — the double-wrap is fine because sticky resolves against the inner scroll parent, and the outer wrapper only provides the visual chrome). Full-page screenshot `/tmp/06-07-projects-table.png`.
  - `ProjectsPage (slow network) @ 1366×768` — with route interception on `/api/v1/projects`, 1 `role="status" aria-label="Loading"` region renders (the SkeletonTable). Full-page screenshot `/tmp/06-07-projects-loading.png`.
  - `admin/UsersPage @ 1366×768` — first header cell carries `sticky left-0 z-10 bg-card w-10` (the `w-10` is preserved — confirms first-column `className` merging works correctly); zero page horizontal scroll. Full-page screenshot `/tmp/06-07-admin-users.png`.
  - `admin/AuditPage @ 1366×768` — first header cell carries `sticky left-0 z-10 bg-card`; zero page horizontal scroll.
  - `Reduced-motion` — `getComputedStyle(skeleton).animationName === 'none'` under Playwright's `reducedMotion: 'reduce'` context. The 06-06 `@media (prefers-reduced-motion: reduce)` rule continues to suppress `animate-pulse` across all new skeleton placements.
  - `Zero console errors` across all driven pages.

## Task Commits

| # | Phase | Type | Commit    | Message |
|---|-------|------|-----------|---------|
| 1 | —     | feat | `a17565e` | feat(06-07): apply Skeleton primitives to Dashboard + Projects pages |
| 2 | —     | feat | `63b894e` | feat(06-07): add stickyFirstColumn to DataTable; enable on 6+ col admin/repo pages |
| 3 | —     | feat | `6291339` | feat(06-07): add dev-only StatusBadgeStoryPage for plan 06-08 snapshots |

Plan frontmatter is `type: execute` (not `type: tdd`); no per-task `tdd="true"` flag. TS-only surface with no vitest/jest harness in the repo — TDD signal reduces to `tsc --noEmit` + `npm run build` + grep-style acceptance criteria + Playwright visual verification, per the precedent documented in plans 06-03, 06-05, and 06-06.

## Files Created/Modified

Created:
- `web/src/pages/_dev/StatusBadgeStoryPage.tsx` (98 lines) — dev-only 24-variant matrix page for Playwright snapshot (plan 06-08 consumer).

Modified:
- `web/src/pages/DashboardPage.tsx` — full-page Skeleton* branch + per-tile fallback; SkeletonCard + SkeletonMetric imports added.
- `web/src/pages/ProjectsPage.tsx` — rewritten as sticky-first-column table; SkeletonTable import; 6-column layout.
- `web/src/components/common/DataTable.tsx` — `stickyFirstColumn` prop + `firstColClassName` helper; conditional outer wrapper.
- `web/src/pages/admin/UsersPage.tsx`, `admin/AuditPage.tsx`, `admin/TrashPage.tsx` — stickyFirstColumn opt-in.
- `web/src/pages/repo/AptRepoPage.tsx`, `DockerRepoPage.tsx`, `HelmRepoPage.tsx`, `PypiRepoPage.tsx`, `RpmRepoPage.tsx` — stickyFirstColumn opt-in.
- `web/src/App.tsx` — lazy import + devRoutes entry for StatusBadgeStoryPage.

## Decisions Made

1. **Centralize sticky-first-column inside DataTable.** The plan template expected mechanical per-file wrapping of every Table. Doing so would have meant 8 consumers × ~10 lines of identical wrapper code + per-cell sticky className work, with future drift risk. Enhancing the shared DataTable with an opt-in `stickyFirstColumn` prop kept the invariants at one site and reduced each consumer change to a single-line diff.
2. **ProjectsPage migrated from card grid to table.** Plan must_haves required `SkeletonTable`, `overflow-x-auto`, `sticky left-0`, and `bg-card` in ProjectsPage.tsx — none of which fit a card-grid layout honestly. A 6-column table (Name / Description / Members / Repos / Size / Created) satisfies all must_haves and matches VISUAL-06's intent for consistent admin-screen table UX at 1366×768. projects.spec.ts asserts text visibility only (not DOM structure), so no e2e regression.
3. **Dashboard loading branch gated by AND, not OR.** `isLoading && storageLoading` shows the full-page skeleton only when the entire dashboard is cold; once one slice resolves, per-slice fallback takes over individual tiles. Avoids a flash of "mostly loaded + blinking skeletons" when one endpoint is significantly faster than the other.
4. **ProfilePage + RawRepoPage deliberately skipped.** Column counts: ProfilePage API-key (5) and S3-key (4) tables, RawRepoPage (5 cols) — all below the 6-column threshold. Applying the wrapper there would add visual chrome (border + overflow container) for no functional gain at 1366×768.
5. **Guard third lazy-import on ALL three story pages resolving.** `DEV_ROUTES_ENABLED && ErrorClassStoryPage && PrimitivesStoryPage && StatusBadgeStoryPage` — if any one lazy chunk fails to load, the dev surface stays fully off rather than partially registered. Small paranoia but cheap: one extra `&&`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking] ProjectsPage had no table for the plan's sticky-first-column pattern to wrap**

- **Found during:** Task 1 Read of ProjectsPage.tsx — file renders a motion-animated card grid, not a table.
- **Issue:** Plan acceptance criteria + must_haves require `SkeletonTable`, `overflow-x-auto`, `sticky left-0`, and `bg-card` ALL to be present in ProjectsPage.tsx. The existing card-grid layout couldn't satisfy this without either (a) injecting a non-rendering SkeletonTable as a cosmetic hack or (b) violating must_haves.
- **Fix:** Converted ProjectsPage to a 6-column sticky-first-column table layout. Description + numeric columns follow the admin-table pattern; row click preserved via onClick + cursor-pointer (no functional regression). Loading branch uses SkeletonTable with widths matched to the real table. Empty state + create dialog + auto-open-on-?create=1 behaviour preserved.
- **Files modified:** web/src/pages/ProjectsPage.tsx.
- **Verification:** Typecheck + build clean; Playwright drove the loaded + loading + create-dialog paths without error; table screenshots show all 6 columns with sticky Name column.
- **Committed in:** `a17565e`.

**2. [Rule 1 — Bug workaround] Stale `.js` emissions from `tsc -b` persist pre-existing bug**

- **Found during:** Post-build inspection after each of the 3 task commits.
- **Issue:** Inherited from plan 06-06 — `tsconfig.json` has no `noEmit: true` and `npm run build` runs `tsc -b && vite build`, so the tsc step emits `.js` + `.js.map` next to every `.tsx` under `web/src/**`. Vite's `moduleResolution: "bundler"` resolves these stale siblings on subsequent `npm run dev`, masking source edits.
- **Fix (reactive):** `find web/src -name "*.js" -not -name "*.config.js" -delete` + `find web/src -name "*.js.map" -delete` after each `npm run build`. Same procedure 06-06 documented.
- **Fix (proper):** Deferred to plan 06-08 per the explicit phase 06 prompt directive ("If your plan doesn't already address this, note it for 06-08"). 06-07's `files_modified` list excludes tsconfig.json, and changing the build script has broader implications than this plan should land.
- **Files modified:** None (disk-state cleanup only).
- **Committed in:** Not committed. Flagged for plan 06-08 to add `"noEmit": true` to `web/tsconfig.json` (and optionally simplify `npm run build` to `vite build` since Vite already transpiles TS).

---

**Total deviations:** 2 — 1 Rule 3 auto-fix (ProjectsPage table migration, committed in `a17565e`) + 1 Rule 1 workaround (stale .js cleanup, deferred to 06-08).

**Impact on plan:** All plan 06-07 acceptance criteria pass. The ProjectsPage migration from cards to a table is a visible UI change — worth flagging in release notes, but strictly a layout change (information preserved, navigation preserved). No functional regression observed in the Playwright walkthrough.

## Known Stubs

None. Every skeleton variant is a deliberate placeholder for a loading state; every badge in the StatusBadge matrix renders real tokens.

## Threat Flags

None. No new network surface, auth path, file-access pattern, or trust boundary introduced. Story page ships with the same DEV_ROUTES_ENABLED gate as the existing two story pages (T-06-03-04 tree-shake invariant preserved; production bundle verified clean of `StatusBadgeStoryPage`).

## Issues Encountered

- **Initial Playwright skeleton-visibility probe failed.** My first verification script throttled the network via CDP's `Network.emulateNetworkConditions` AFTER the page started navigating — the first tile render often fired before the throttling took effect, so the skeleton was already gone by the time my `[role=status]` query ran. Re-ran with `page.route('**/api/v1/projects', async r => { await sleep(5000); await r.continue(); })` which intercepts at the URL level and reliably paints skeletons for 5 s. Both tests then passed: 7 skeleton regions on dashboard, 1 on projects.
- **Story page initially rendered in dark mode during verification.** The PrimitivesStoryPage from 06-06 has a light/dark toggle that persists the `.dark` class on `<html>`. Navigating to `/_dev/status-badge-story` after toggling dark mode inherited the class — screenshots were taken in whatever mode the previous test left. Not a bug in this plan's code; the StatusBadgeStoryPage doesn't ship its own toggle (intentional — it's a snapshot page, not a visual explorer).

## User Setup Required

None — pure-code plan, no DB migration, no external services.

## Next Phase Readiness

- **Plan 06-08 can start.** Inputs it needs are all in place:
  - `/_dev/status-badge-story` page at `http://.../_dev/status-badge-story` renders a deterministic 24-cell matrix with `data-story-variant` + `data-story-size` + `data-story-icon-only` locators — Playwright snapshot spec has stable selectors.
  - `DashboardPage` + `ProjectsPage` + `admin/{Users,Audit,Trash}Page` + `repo/{Apt,Docker,Helm,Pypi,Rpm}RepoPage` all show Skeleton* loading states and sticky-first-column admin-table behaviour — plan 06-08's responsive Playwright spec (1366×768 horizontal-scroll gate) has canonical sites to drive.
  - The `stickyFirstColumn` prop and its default-off behaviour mean plan 06-08 can assert the presence of the pattern on ≥ 5 pages without touching narrow tables (ProfilePage, RawRepoPage, TLSPage, TrivyPage).
- **Deferred for plan 06-08 to fix:** add `"noEmit": true` to `web/tsconfig.json` (and/or simplify `npm run build` to just `vite build`) so subsequent `tsc -b` runs don't emit stale `.js` into `web/src/`. Documented in Deviation 2 above.

## Self-Check

- `web/src/pages/_dev/StatusBadgeStoryPage.tsx` — FOUND (98 lines, ≥ 60 required)
- `web/src/pages/DashboardPage.tsx` contains `SkeletonCard` — VERIFIED
- `web/src/pages/DashboardPage.tsx` contains `SkeletonMetric` — VERIFIED
- `web/src/pages/ProjectsPage.tsx` contains `SkeletonTable` — VERIFIED
- `web/src/pages/ProjectsPage.tsx` contains `overflow-x-auto` — VERIFIED
- `web/src/pages/ProjectsPage.tsx` contains `sticky left-0` — VERIFIED
- `web/src/pages/ProjectsPage.tsx` contains `bg-card` — VERIFIED
- `web/src/components/common/DataTable.tsx` contains `stickyFirstColumn` — VERIFIED
- `web/src/components/common/DataTable.tsx` contains `sticky left-0 z-10 bg-card` — VERIFIED
- `web/src/components/common/DataTable.tsx` contains `overflow-x-auto` — VERIFIED
- At least 2 admin pages use `stickyFirstColumn` — VERIFIED (3: UsersPage, AuditPage, TrashPage)
- 5 repo pages use `stickyFirstColumn` — VERIFIED (AptRepoPage, DockerRepoPage, HelmRepoPage, PypiRepoPage, RpmRepoPage)
- `web/src/pages/_dev/StatusBadgeStoryPage.tsx` contains `export function StatusBadgeStoryPage` — VERIFIED
- `web/src/pages/_dev/StatusBadgeStoryPage.tsx` contains `data-story-variant` — VERIFIED
- `web/src/pages/_dev/StatusBadgeStoryPage.tsx` contains `data-story-size` — VERIFIED
- `web/src/pages/_dev/StatusBadgeStoryPage.tsx` contains `data-story-icon-only` — VERIFIED
- `web/src/App.tsx` contains `_dev/status-badge-story` — VERIFIED
- No forbidden `font-medium` / `font-bold` / `font-light` in `web/src/pages/_dev/StatusBadgeStoryPage.tsx` — VERIFIED (grep returns nothing)
- `cd web && npx tsc --noEmit` — PASS (exit 0)
- `cd web && npm run -s build` — PASS (3017 modules, exit 0)
- `grep -l 'StatusBadgeStoryPage' web/dist/assets/*.js` — 0 matches (production tree-shake verified)
- Commits `a17565e`, `63b894e`, `6291339` — all present in `git log --oneline -5`
- Playwright walkthrough — PASS (24 matrix cells, dashboard + projects loaded + loading, admin/users sticky header, admin/audit sticky header, zero page horizontal scroll at 1366×768 on all checked routes, zero console errors, reduced-motion suppresses animate-pulse)
- Screenshots captured: `/tmp/06-07-status-badge-story.png`, `/tmp/06-07-status-badge-story-app.png`, `/tmp/06-07-dashboard-loaded.png`, `/tmp/06-07-dashboard-loading.png`, `/tmp/06-07-projects-loading.png`, `/tmp/06-07-projects-table.png`, `/tmp/06-07-admin-users.png`

**Self-Check: PASSED**

## TDD Gate Compliance

Plan frontmatter is `type: execute`; no per-task `tdd="true"` flag. Per precedent from plans 06-03, 06-05, and 06-06 (TS-only surfaces with no vitest harness): TDD signal is `tsc --noEmit` + `npm run build` + grep-style acceptance criteria + Playwright visual verification. All three task commits passed all four gates at commit time.

---
*Phase: 06-error-envelope-visual-foundation*
*Completed: 2026-04-17*
