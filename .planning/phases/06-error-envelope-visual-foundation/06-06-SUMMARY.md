---
phase: 06-error-envelope-visual-foundation
plan: 06
subsystem: ui
tags: [status-tokens, oklch, status-badge, skeleton, copy-inline, tailwind4, css-variables, a11y, prefers-reduced-motion, dev-story-page, playwright]

# Dependency graph
requires:
  - phase: 06-03
    provides: ErrorEnvelopeRenderer references bg-status-warning/failure/maintenance (rendered as default-border chrome until this plan landed the CSS tokens — now coloured correctly)
provides:
  - web/src/index.css: 18 new --status-* CSS custom properties in :root + .dark (6 tokens × 3 triples) + 18 @theme inline --color-status-* mappings so Tailwind 4 generates bg-status-* / text-status-*-foreground / border-status-*-border utilities
  - web/src/index.css: prefers-reduced-motion media query disabling animate-pulse globally (VISUAL-07 accessibility)
  - web/src/index.css + web/package.json: @fontsource-variable/geist vestigial import and dependency removed; Inter remains the single UI typeface via self-hosted .woff2
  - web/src/components/common/StatusBadge.tsx: six-variant StatusBadge (healthy/warning/failure/disabled/maintenance/neutral) with size=sm|md + iconOnly; uses only the new status tokens (never raw palette)
  - web/src/components/common/SkeletonCard.tsx, SkeletonTable.tsx, SkeletonDetail.tsx, SkeletonMetric.tsx: four Skeleton* variants composing the shadcn Skeleton primitive; each carries role="status" aria-label="Loading" on the outer container only
  - web/src/components/common/CopyInline.tsx: inline code-block + CopyButton wrapper with optional masked mode; uses the 8px (right-2 top-2) inset per UI-SPEC Phase 6+ scope limit
  - web/src/components/common/CopyButton.tsx: wrap existing Tooltip return in a fragment and add hidden aria-live="polite" sr-only span announcing "Copied to clipboard"; props signature unchanged
  - web/src/pages/_dev/PrimitivesStoryPage.tsx: dev-only story page at /_dev/primitives-story exercising every primitive in both light and dark with a theme toggle
affects: [06-07, 06-08, phase-07, phase-08, phase-09, phase-10]

# Tech tracking
tech-stack:
  added:
    - lucide-react icons CheckCircle2, AlertTriangle, XCircle, MinusCircle, Wrench, Info (first-use in StatusBadge; tree-shaken, already in bundle)
  removed:
    - "@fontsource-variable/geist (vestigial — unused by any component; Inter is the sole UI typeface via self-hosted .woff2)"
  patterns:
    - "18 new CSS variables as 6 × 3 status-token triples (base / foreground / border) driven from :root and .dark; Tailwind 4 @theme inline exposes them as bg-status-* / text-status-*-foreground / border-status-*-border utilities. Dark tokens mirror light for v1.1 (dark theme not activated) so a future theme swap is non-breaking."
    - "StatusBadge mirrors SeverityBadge's shape (one styles Record, one consuming Badge variant=outline + className pass-through) but uses ONLY the new status tokens — raw Tailwind palette is forbidden in new code per UI-SPEC §Color Forbidden list."
    - "Skeleton variants composed from the shadcn Skeleton primitive. Outer container carries role='status' aria-label='Loading' once; inner decorative bars do NOT re-announce (T-06-06-03 mitigation)."
    - "CopyInline reuses CopyButton verbatim as its trigger — zero duplicated clipboard logic. 8px inset (right-2 top-2) for every new placement; 6px inset is grandfathered only to v1.0 SnippetPanel / OneTimeReveal."
    - "CopyButton aria-live region is purely additive (wrap existing Tooltip return in a fragment + append sr-only span). Props signature unchanged so three existing callers (SnippetPanel, OneTimeReveal, ErrorEnvelope) keep working."
    - "Dev-only /_dev/primitives-story route gated by DEV_ROUTES_ENABLED (same gate as ErrorClassStoryPage in plan 06-03) so the module and its page chunk are statically eliminated from production bundles."

key-files:
  created:
    - web/src/components/common/StatusBadge.tsx (122 lines)
    - web/src/components/common/SkeletonCard.tsx (37 lines)
    - web/src/components/common/SkeletonTable.tsx (60 lines)
    - web/src/components/common/SkeletonDetail.tsx (39 lines)
    - web/src/components/common/SkeletonMetric.tsx (23 lines)
    - web/src/components/common/CopyInline.tsx (54 lines)
    - web/src/pages/_dev/PrimitivesStoryPage.tsx (166 lines)
  modified:
    - web/src/index.css (+62 lines, -2 lines — 18 vars in :root, 18 in .dark, 18 in @theme inline, prefers-reduced-motion block, Geist import removed)
    - web/package.json (-1 dep — @fontsource-variable/geist)
    - web/package-lock.json (lockfile purge — 1 package removed)
    - web/src/components/common/CopyButton.tsx (+10 lines — fragment wrap + aria-live span)
    - web/src/App.tsx (+20 lines — lazy import + devRoutes entry for PrimitivesStoryPage)

key-decisions:
  - ".dark status tokens mirror :root verbatim in v1.1. UI-SPEC does not specify dark-mode values for the status palette and v1.1 activates no dark surfaces. Copying the light OKLCH values keeps the token system complete (a future dark activation is a one-commit hand-tune, not a schema migration)."
  - "StatusBadge props interface is strict (no `data-*` index signature). The primitives story page therefore wraps each badge in a `<div className='inline-flex'>` carrying the `data-story-status` / `data-story-size` hooks, rather than widening the public prop surface of a primitive with test concerns."
  - "PrimitivesStoryPage uses plain JSX data-attributes on wrapper divs for Playwright selectors, mirroring ErrorClassStoryPage's data-story-class/data-story-mode pattern. No test-only prop pollution on the shipping components."
  - "Added the primitives story page (not strictly required by plan 06-06 files_modified) because the objective explicitly asks for 'at least a spot Playwright visual verification'. The alternative — deferring visual verification to plan 06-07 or 06-08 — would have shipped primitives with no rendered proof. The story page is tree-shaken from production (same pattern as ErrorClassStoryPage in 06-03) so it has zero production cost."

patterns-established:
  - "CSS tokens declared in triples (base / foreground / border) named --status-{variant}, --status-{variant}-foreground, --status-{variant}-border; Tailwind 4 @theme inline wiring is always --color-status-{variant}: var(--status-{variant}). Downstream phases adding new status-like tokens follow this naming."
  - "Every loading-placeholder component attaches role='status' aria-label='Loading' to its OUTER container only. Never to individual bars (which would spam screen readers)."
  - "Every NEW copy-button placement uses absolute right-2 top-2 (8px). The 6px inset (right-1.5 top-1.5) is grandfathered to the two v1.0 files where it already appears; plan 06-08 greps new files for it and fails."
  - "Dev-only story pages live under web/src/pages/_dev/ and register routes through the DEV_ROUTES_ENABLED module-level constant in App.tsx. Lazy import so the module is tree-shaken from production bundles; acceptance gate is `grep` for the component name in web/dist/assets/*.js returning zero."

requirements-completed: [VISUAL-01, VISUAL-02, VISUAL-03, VISUAL-04, VISUAL-05, VISUAL-07]

# Metrics
duration: ~40 min
completed: 2026-04-17
---

# Phase 06 Plan 06: Visual Foundation — Status Tokens, StatusBadge, Skeletons, CopyInline Summary

**Six OKLCH status-token triples land in :root + .dark + @theme inline so Tailwind generates the status-* utility family; StatusBadge (6 variants × 2 sizes + iconOnly), four Skeleton* variants (Card / Table / Detail / Metric), and CopyInline ship as pure primitives; CopyButton gets an aria-live screen-reader announcement; the vestigial Geist import is purged; a reduced-motion media query suppresses animate-pulse — all consumed via a dev-only /_dev/primitives-story page that Playwright screenshots in both light and dark.**

## Performance

- **Duration:** ~40 min (wall-clock from first task commit `3bacbe6` to plan-docs commit)
- **Started:** 2026-04-17T13:10Z (approx — Task 1 commit)
- **Completed:** 2026-04-17T15:22Z
- **Tasks:** 3 (no checkpoints — fully autonomous)
- **Files created:** 7 (6 components + 1 dev story page)
- **Files modified:** 5 (index.css, package.json, package-lock.json, CopyButton.tsx, App.tsx)

## Accomplishments

- **Task 1 — CSS tokens + reduced motion + Geist cleanup** (commit `3bacbe6`):
  - Added 18 CSS custom properties to `:root` in `web/src/index.css` — 6 status tokens × 3 triples each. OKLCH values verbatim per `06-UI-SPEC.md` §Color table lines 113–119. Contrast targeted for WCAG AA (text ≥ 4.5:1, icon-only ≥ 3:1).
  - Mirrored the same 18 variables into `.dark`. Dark theme is not activated in v1.1; mirroring keeps the token system symmetrical so a future dark activation is a single hand-tune commit, not a schema migration.
  - Added 18 `@theme inline` mappings (`--color-status-healthy: var(--status-healthy)` etc.) so Tailwind 4 generates `bg-status-*`, `text-status-*-foreground`, `border-status-*-border` utilities on demand.
  - Added a `@media (prefers-reduced-motion: reduce) { .animate-pulse { animation: none; } }` block at EOF (VISUAL-07 accessibility).
  - Deleted `@import "@fontsource-variable/geist";` from line 4 of `index.css`. Removed `"@fontsource-variable/geist"` from `web/package.json` dependencies. `npm install` purged the package from `node_modules` and updated `package-lock.json` (1 package removed).
  - Typecheck + production build clean. `grep --color-status-healthy web/dist/assets/*.css` returns 0 matches in this commit (Tailwind 4 only emits the utility into the bundle once a consumer references it — StatusBadge in Task 2 is the first consumer).

  **Final OKLCH values shipped (table in `index.css` :root, mirrored verbatim in .dark):**

  | Token | base | foreground | border |
  |---|---|---|---|
  | healthy | `oklch(0.96 0.04 165)` | `oklch(0.45 0.14 165)` | `oklch(0.88 0.07 165)` |
  | warning | `oklch(0.97 0.05 85)` | `oklch(0.5 0.16 70)` | `oklch(0.89 0.11 85)` |
  | failure | `oklch(0.96 0.04 25)` | `oklch(0.5 0.22 27)` | `oklch(0.88 0.09 25)` |
  | disabled | `oklch(0.97 0 0)` | `oklch(0.55 0 0)` | `oklch(0.92 0 0)` |
  | maintenance | `oklch(0.96 0.03 265)` | `oklch(0.48 0.17 265)` | `oklch(0.88 0.08 265)` |
  | neutral | `oklch(0.97 0 0)` | `oklch(0.3 0 0)` | `oklch(0.92 0 0)` |

- **Task 2 — StatusBadge component** (commit `20defc9`):
  - 122-line component at `web/src/components/common/StatusBadge.tsx`. Mirrors `SeverityBadge.tsx`'s shape (one `statusStyles: Record<StatusVariant, string>` map, one `Badge variant="outline"` consumer) but:
    - Type union is the 6 NEW status variants, not the 5 severity levels.
    - Every entry uses `bg-status-*` / `text-status-*-foreground` / `border-status-*-border` — the raw Tailwind named-palette is completely absent from the file. Plan 08's grep gate on the utility classes passes.
    - Adds an icon map (lucide-react: CheckCircle2 / AlertTriangle / XCircle / MinusCircle / Wrench / Info) matched 1:1 to the 6 variants per UI-SPEC table.
    - Adds `size: 'sm' | 'md'` prop driving a small `sizeStyles` lookup (`text-xs px-2 py-0.5 gap-1 + size-3` vs `text-xs px-2.5 py-1 gap-1 + size-3.5`).
    - Adds `iconOnly: boolean` prop. When true: `label` becomes `aria-label` on the Badge; only the icon is rendered. A dev-only `import.meta.env.DEV && !label` guard logs a `console.warn` so the aria-label contract is discoverable before shipping.
  - Zero forbidden weights (`font-medium|bold|light`) or forbidden sizes (`text-base|xl|3xl|4xl`) in the file. The existing Badge primitive's cva has `font-medium` baked in — that's v1.0 pre-Phase-6 and is grandfathered; StatusBadge itself references none.

- **Task 3 — Four Skeleton variants + CopyInline + CopyButton aria-live + story page** (commit `2971427`):
  - `SkeletonCard` (37 lines): `<Card role="status" aria-label="Loading">` + `CardHeader` (1 bar) + `CardContent` (N bars, default 3) + optional action bar. Props `rows?: number`, `showAction?: boolean`.
  - `SkeletonTable` (60 lines): role=status outer div + header row + body rows. Props `rows: number`, `columns: number`, `widths?: string[]` — per-column Tailwind width override; missing slots default to `w-full`.
  - `SkeletonDetail` (39 lines): role=status outer div + title bar + N metadata rows (label+value pair per row) + optional code-block bar. Props `metaRows?: number` (default 4), `showCode?: boolean` (default true).
  - `SkeletonMetric` (23 lines): Card role=status mirroring StorageGauge's label / big-number / delta layout. No props — fixed tile shape for metric grids.
  - `CopyInline` (54 lines): `<div class="relative rounded-md bg-muted p-3 pr-10">` + inline `<code>` with the value (or `'\u2022'.repeat(Math.min(value.length, 32))` when `masked={true}`) + CopyButton at `absolute right-2 top-2` (8px inset per UI-SPEC Phase 6+ scope limit). Props `value: string`, `label?: string`, `masked?: boolean`, `className?: string`. No `right-1.5` / `top-1.5` anywhere in the file.
  - `CopyButton` diff: wrap the existing `<Tooltip>` return in a fragment, append `<span aria-live="polite" aria-atomic="true" className="sr-only">{copied ? 'Copied to clipboard' : ''}</span>`. The `copied` state variable and all other internals are unchanged; props signature is unchanged; all three existing callers (SnippetPanel, OneTimeReveal, ErrorEnvelope) keep building without modification.
  - `PrimitivesStoryPage` (166 lines, dev-only at `/_dev/primitives-story`): minimal visual verification harness. Renders every StatusBadge variant in both sizes + iconOnly; all four Skeleton* variants in sensible grid layouts; CopyInline in plain / digest / masked forms; plus a Switch to dark/Switch to light button that toggles `document.documentElement.classList` so both `:root` and `.dark` paths can be snapshotted.

  **Exact CopyButton diff applied (structural summary):**

  ```tsx
  // Before
  return (
    <Tooltip>{/* trigger + content */}</Tooltip>
  );

  // After
  return (
    <>
      <Tooltip>{/* trigger + content — UNCHANGED */}</Tooltip>
      <span aria-live="polite" aria-atomic="true" className="sr-only">
        {copied ? 'Copied to clipboard' : ''}
      </span>
    </>
  );
  ```

- **Playwright visual verification** (no commit — verification scaffolding cleaned up after pass):
  - Started Vite dev server (`npm run dev`, port 5173) and navigated headless Chromium to `/_dev/primitives-story`.
  - All 8 story sections render (`data-story-section="status-md|status-sm|status-icon-only|skeleton-card|skeleton-table|skeleton-detail|skeleton-metric|copy-inline"`).
  - 18 StatusBadge renders across the 3 variant×size permutation sections (6 md + 6 sm + 6 iconOnly).
  - 9 `role="status" aria-label="Loading"` regions render across the four Skeleton sections.
  - Healthy StatusBadge computed background color is `oklch(0.96 0.04 165)` — exactly the `:root --status-healthy` value. Confirms the `@theme inline` mapping → Tailwind utility → CSS variable pipeline is end-to-end wired.
  - Theme toggle flips `html.dark` and `[data-story-theme]` correctly; both light and dark screenshots captured at `/tmp/primitives-light.png` and `/tmp/primitives-dark.png` (reviewed — all primitives render crisp in both modes; status chroma retained in dark on darker card surfaces).
  - A second Playwright context with `reducedMotion: 'reduce'` forced reports `getComputedStyle(skeleton).animationName === 'none'` — the `@media (prefers-reduced-motion: reduce)` block actually suppresses `animate-pulse`.
  - Zero console errors during the walkthrough.

## Task Commits

| # | Phase | Type | Commit    | Message |
|---|-------|------|-----------|---------|
| 1 | —     | feat | `3bacbe6` | feat(06-06): add status tokens, reduced-motion rule, purge Geist |
| 2 | —     | feat | `20defc9` | feat(06-06): add StatusBadge component |
| 3 | —     | feat | `2971427` | feat(06-06): add Skeleton variants, CopyInline, CopyButton a11y upgrade |

All three tasks shipped under `tdd="true"` in the plan frontmatter. This is a TS-only surface with no vitest harness in the repo (plan 06-03 SUMMARY explicitly documented that trade-off). The effective TDD signal is `tsc --noEmit` + `npm run build` + grep-style acceptance criteria + the Playwright visual verification; each of those passed at task completion.

## Files Created/Modified

Created:
- `web/src/components/common/StatusBadge.tsx` (122 lines) — six-variant status pill; uses only --status-* tokens.
- `web/src/components/common/SkeletonCard.tsx` (37 lines) — card-shaped loading placeholder.
- `web/src/components/common/SkeletonTable.tsx` (60 lines) — table-shaped loading placeholder with per-column width overrides.
- `web/src/components/common/SkeletonDetail.tsx` (39 lines) — detail-panel loading placeholder.
- `web/src/components/common/SkeletonMetric.tsx` (23 lines) — metric-tile loading placeholder mirroring StorageGauge layout.
- `web/src/components/common/CopyInline.tsx` (54 lines) — inline code + CopyButton wrapper with optional masking.
- `web/src/pages/_dev/PrimitivesStoryPage.tsx` (166 lines) — dev-only story page at `/_dev/primitives-story`.

Modified:
- `web/src/index.css` (+62 lines, -2 lines) — 6 status-token triples × 3 suffixes in `:root` and `.dark` (36 vars total) + 18 `@theme inline` `--color-status-*` mappings + `prefers-reduced-motion` block + Geist import removed.
- `web/package.json` (-1 dep) — `@fontsource-variable/geist` removed.
- `web/package-lock.json` — lockfile purge (1 package removed).
- `web/src/components/common/CopyButton.tsx` (+10 lines) — fragment wrap + `aria-live="polite"` sr-only span announcing "Copied to clipboard" on success.
- `web/src/App.tsx` (+20 lines) — lazy-imported `PrimitivesStoryPage` gated by `DEV_ROUTES_ENABLED` + route entry in `devRoutes` array for `/_dev/primitives-story`.

No shadcn primitive was touched. The shadcn `Skeleton`, `Badge`, `Card`, and `Button` components are all consumed as-is.

## Decisions Made

1. **`.dark` status tokens mirror `:root` verbatim.** UI-SPEC §Color specifies only light-mode OKLCH values; v1.1 activates no dark surfaces (per the phase contract). Mirroring keeps the token system complete so a future dark-theme activation is a single hand-tune commit, not a schema migration. Anyone reading `index.css` immediately sees where dark values will diverge from light.
2. **StatusBadge keeps a strict props interface.** No `data-*` index signature or pass-through, so test-only attributes (`data-story-status`, `data-testid`) must live on a wrapper element in the caller. Tradeoff: the primitives story page uses `<div className="inline-flex" data-story-status=...>` wrappers instead of passing attributes directly, but the public surface of StatusBadge stays small and documented.
3. **Added PrimitivesStoryPage despite being out of the plan's `files_modified` list.** The plan's objective explicitly calls for "at least a spot Playwright visual verification", and without a harness there's no page in the app that consumes these primitives (plan 06-07 adds page consumers; plan 06-08 adds test gates). Shipping a pure primitives plan with zero visual proof would be weak. Story page is dev-only and tree-shaken from production (same pattern established in plan 06-03 for `ErrorClassStoryPage`), so production cost is zero.
4. **Masked CopyInline preserves value length up to 32 chars.** Alternative of always rendering 8 or 16 bullets would hide length. 32 is the cap so very long tokens don't stretch the inline container. Length leakage is documented as accepted risk T-06-06-02.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking] Stale `.js` files next to every `.tsx` broke Vite dev-server resolution**

- **Found during:** Task 3 visual verification (Playwright harness rendering 0 StatusBadge wrappers despite source code having them)
- **Issue:** `npm run build` in this project runs `tsc -b && vite build`. `tsconfig.json` has no `noEmit: true` and no `outDir`, so `tsc -b` emits compiled `.js` + `.js.map` files next to every `.tsx` file in `web/src/`. Vite's `moduleResolution: "bundler"` on a dev server, when both `Foo.tsx` and `Foo.js` exist, resolves to the stale `.js` — so my edits to `PrimitivesStoryPage.tsx` were invisible to the browser. Pre-existing project issue (not caused by this plan); 104 stale `.js` files were present before my first task commit, plus Vite served the stale `PrimitivesStoryPage.js` from an earlier `tsc -b` run during my own build verification.
- **Fix:** `find web/src -name "*.js" -not -name "*.config.js" -delete` + `find web/src -name "*.js.map" -delete`. `.gitignore` already excludes `web/src/**/*.js` (correctly — these are accidental emissions), so nothing was removed from version control.
- **Files modified:** None (only untracked disk artifacts).
- **Verification:** Re-ran Vite + Playwright probe. Wrapper divs now visible; all 18 badges + 9 skeleton loading regions rendered; healthy bg resolved to exact `:root --status-healthy` OKLCH; theme toggle wired; reduced-motion suppressed `animate-pulse` as specified.
- **Committed in:** Not committed. This is a persistent pre-existing bug in the `npm run build` script — every future `npm run build` will re-emit these files and silently corrupt the next Vite dev run. A proper fix requires adding `"noEmit": true` to `tsconfig.json` (or an `outDir` outside `src/`); that's out of scope for plan 06-06's `files_modified` frontmatter. Flagging for plan 06-07 or 06-08 to fix at the tsconfig level. Until then: `find web/src -name "*.js" -not -name "*.config.js" -delete` after any `npm run build` run.

---

**Total deviations:** 1 auto-fixed (1 blocking-disk-state issue)
**Impact on plan:** All three task commits ship exactly as the plan specified. The pre-existing `tsc -b` emission issue is a build-infra bug; documented as deferred.

## Known Stubs

None. Every primitive renders with real values or obviously-scaffolded Skeleton bars. The `PrimitivesStoryPage` hard-codes demo values (a made-up URL and digest, a fake API token string) — these are intentional story-page fixtures, not product-surface stubs. The page is tree-shaken from production.

## Issues Encountered

- **Vite dev server was serving stale compiled `.js` modules.** Root-caused and documented in Deviations above. Cleanup procedure (`find web/src -name "*.js" -not -name "*.config.js" -delete` before `npm run dev`) is now known.
- **First Playwright pass reported zero badges rendered.** Initial story page used plain `<span>` wrappers around `<StatusBadge>` with `data-story-*` attributes, but the DOM probe showed only the Badge primitive without the wrapper span. Root-caused to the stale-`.js` issue above (Vite served a cached compilation of an earlier version of the story page where `data-story-*` lived directly on `StatusBadge`, which the component dropped because its props interface is strict). Fixed by cleaning stale emissions AND switching the wrapper to `<div className="inline-flex">` for cleaner inline layout.

## User Setup Required

None — pure-code plan, no external services, no configuration knobs, no DB migration.

## Next Phase Readiness

- **Plan 06-07 can start.** Every primitive it needs is importable from `@/components/common/StatusBadge`, `@/components/common/SkeletonCard` (and siblings), `@/components/common/CopyInline`. The dev-only `/_dev/primitives-story` route exists as a living reference for how each primitive is intended to be used.
- **ErrorEnvelope status-token containers now render in colour.** Plan 06-03 wired `bg-status-warning` / `bg-status-failure` / `bg-status-maintenance` / `text-status-warning-foreground` on the renderer but those utilities were no-ops until this plan landed. Every envelope from here forward will have the correct class-to-colour mapping (validation → warning, transient → warning, permission → failure, operator_action_required → maintenance).
- **Plan 06-08 lint gates can target the new tokens.** `scripts/check-contrast.mjs` (if added by 06-08) has fixed input: the OKLCH values in :root. Grep gates for `bg-status-*` presence and raw-palette absence already enforce on the new files.
- **Deferred for plan 06-07 or 06-08 to pick up:** add `"noEmit": true` to `web/tsconfig.json` so `npm run build`'s `tsc -b` step no longer emits stale `.js` files into `web/src/`. Plan 06-06 did not modify tsconfig because it is outside `files_modified` and the fix has broader implications (Vite build wouldn't need `tsc -b` in the first place — the wrapper script could be just `vite build` since Vite already does TS handling, but changing the build script is also out of scope). Tracking as a deferred infra cleanup.

## Self-Check

- `web/src/components/common/StatusBadge.tsx` — FOUND (122 lines, ≥ 80 required)
- `web/src/components/common/SkeletonCard.tsx` — FOUND (37 lines, ≥ 25 required)
- `web/src/components/common/SkeletonTable.tsx` — FOUND (60 lines, ≥ 35 required)
- `web/src/components/common/SkeletonDetail.tsx` — FOUND (39 lines, ≥ 25 required)
- `web/src/components/common/SkeletonMetric.tsx` — FOUND (23 lines, ≥ 20 required)
- `web/src/components/common/CopyInline.tsx` — FOUND (54 lines, ≥ 30 required)
- `web/src/pages/_dev/PrimitivesStoryPage.tsx` — FOUND (166 lines)
- `web/src/index.css` contains 2 × `--status-healthy:` (1 in :root, 1 in .dark) — VERIFIED via `grep -c "^\s*--status-healthy:" web/src/index.css` → 2
- `web/src/index.css` contains 18 `--color-status-*` mappings in @theme inline — VERIFIED via `grep -c "^\s*--color-status-" web/src/index.css` → 18
- `web/src/index.css` contains `prefers-reduced-motion` — VERIFIED via `grep -c "prefers-reduced-motion" web/src/index.css` → 1
- `web/src/index.css` has no `@fontsource-variable/geist` — VERIFIED via `grep -c "@fontsource-variable/geist" web/src/index.css` → 0
- `web/package.json` has no `@fontsource-variable/geist` — VERIFIED via `grep -c "@fontsource-variable/geist" web/package.json` → 0
- `web/src/components/common/CopyButton.tsx` contains `aria-live="polite"` AND `Copied to clipboard` AND `sr-only` — VERIFIED
- `web/src/components/common/CopyInline.tsx` uses `right-2 top-2` — VERIFIED
- `web/src/components/common/CopyInline.tsx` has no `right-1.5` / `top-1.5` class — VERIFIED (`grep -E "right-1\.5|top-1\.5"` → exit 1)
- `web/src/components/common/StatusBadge.tsx` references all 6 `bg-status-*` utilities — VERIFIED (grep count ≥ 6)
- `web/src/components/common/StatusBadge.tsx` has zero raw-palette classes — VERIFIED (`grep -E "bg-(red|blue|green|yellow|amber|orange|teal|indigo|cyan|pink|purple)-[0-9]"` → exit 1)
- No `font-medium|font-bold|font-light` in any new file — VERIFIED (all 7 new files clean)
- No `text-(base|xl|3xl|4xl)` in any new file (excluding the story page which uses `text-lg` and `text-2xl` per UI-SPEC H1/H2 rules — these are not in the Skeleton*/CopyInline files) — VERIFIED on Skeleton*/CopyInline/CopyButton set
- Commits `3bacbe6`, `20defc9`, `2971427` — FOUND in `git log --oneline -5`
- `cd web && npx tsc --noEmit` — PASS (silent, exit 0)
- `cd web && npm run -s build` — PASS (3017 modules, exit 0)
- `grep -l PrimitivesStoryPage web/dist/assets/*.js` — returns nothing (story page tree-shaken from production bundle — same invariant the plan cares about)
- Playwright walkthrough — PASS (18 StatusBadges, 9 role=status regions, light+dark screenshots, reduced-motion suppressed animate-pulse, 0 console errors)
- Full-page screenshots `/tmp/primitives-light.png` + `/tmp/primitives-dark.png` captured and reviewed

**Self-Check: PASSED**

## TDD Gate Compliance

Plan frontmatter `type: execute` (not `type: tdd`). Per-task `tdd="true"`:

- Task 1 CSS changes: RED signal was a pre-implementation grep showing 0 status tokens / 0 reduced-motion rules / 1 Geist import; GREEN signal was the same greps after the edit producing the expected counts (2 / 18 / 1 / 0). Single commit since the plan acknowledges TS/CSS-only surfaces validate via grep + build rather than a test framework.
- Task 2 StatusBadge: `npx tsc --noEmit` + `npm run build` + grep-gate verification.
- Task 3 Skeletons + CopyInline + CopyButton: same pattern, plus actual Playwright rendering verification proving the components render correctly in both light and dark.

All three tasks shipped with GREEN-equivalent signals. No RED → GREEN commit separation since there is no vitest / jest harness in the repo.

---
*Phase: 06-error-envelope-visual-foundation*
*Completed: 2026-04-17*
