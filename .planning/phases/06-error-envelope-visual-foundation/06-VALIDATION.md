---
phase: 6
slug: error-envelope-visual-foundation
created: 2026-04-17
source: 06-RESEARCH.md
status: draft
---

# Phase 6: Error Envelope & Visual Foundation — Validation

This file is the authoritative phase-gate checklist derived from the `## Validation Architecture` section of `06-RESEARCH.md`. Each item has a unique `V-06-XX` ID, a one-line description, and a `verified_by:` field naming the concrete plan task, test file, or build gate that provides the verification.

## Mapping to ROADMAP Success Criteria

| SC | Description | Covered by |
|----|-------------|------------|
| SC-1 | Every `/api/v1` error response emits a stable envelope with no internal-string leakage | V-06-01, V-06-02, V-06-12 |
| SC-2 | UI renders each error class with the correct icon, copy, CTA, and incident_id chip | V-06-03 |
| SC-3 | Visual-foundation primitives (StatusBadge, Skeletons, CopyInline/CopyButton) exist and are adopted at canonical sites | V-06-04, V-06-05, V-06-06 |
| SC-4 | Button hierarchy is consistent (primary vs destructive distinct) | V-06-07 |
| SC-5 | No horizontal scroll at 1366×768 on admin pages; WCAG AA contrast passes; vestigial typography removed | V-06-08, V-06-09, V-06-10, V-06-11 |

## Validation Items

- [ ] **V-06-01 — Envelope shape on every `/api/v1` error response.** Every 4xx/5xx response returned by handlers under `internal/api/` serializes as `{code, message, hint?, class, incident_id?, details?}` with `class` in `{validation, permission, transient, operator_action_required}`.
  - `verified_by:` plan 06-04 task 1 — Go integration test `internal/api/handlers_envelope_integration_test.go::TestEnvelope_AllClasses` driving every converted handler and asserting the envelope via `json.Unmarshal` into the generated `ApiErrorEnvelope` type.
  - **Requirements:** ERR-01

- [ ] **V-06-02 — No internal error strings leak.** Envelope `message` and `hint` never contain absolute paths (`^/`), Go source file references (`\.go:\d+:`), driver/db-internal strings, or wrapped error chains. Internal cause stays in slog only.
  - `verified_by:` plan 06-04 task 1 — Go unit test `internal/httperr/envelope_test.go::TestNoInternalLeakage` + integration test `TestEnvelope_MessagesAreSafe` (regex assertions on response bodies across every class) + Makefile `lint-protocol-redaction` gate (already present from phase 5) extended to cover the new `internal/httperr` boundary.
  - **Requirements:** ERR-03

- [ ] **V-06-03 — UI renders per class with incident_id chip.** `ErrorEnvelopeRenderer` produces the correct icon + color + copy + CTA for each of the four classes and shows the `Incident {id}` chip with a CopyButton when `incident_id` is present.
  - `verified_by:` plan 06-03 task 3 (creates dev-only ErrorClassStoryPage + backend `/api/v1/_dev/error/:class` routes) + plan 06-04 task 2 — Playwright spec `web/e2e/error-envelope.spec.ts` with one scenario per class (validation / permission / transient / operator_action_required) hitting the dev route, asserting the rendered icon + message + CTA + incident chip via `[data-story-class="<class>"]` locators.
  - **Requirements:** ERR-02, ERR-04, ERR-05, ERR-06, ERR-07

- [ ] **V-06-04 — StatusBadge variants render consistently.** All 6 status tokens × 2 sizes × 2 iconOnly values render with the expected OKLCH color tokens and icon per UI-SPEC.
  - `verified_by:` plan 06-07 task 3 (creates StatusBadgeStoryPage at `/_dev/status-badge-story`) + plan 06-08 task 3 — Playwright snapshot `web/e2e/visual-foundation.spec.ts::"StatusBadge matrix snapshot"` with `toHaveScreenshot('status-badge-matrix.png', { maxDiffPixelRatio: 0.01 })`.
  - **Requirements:** VISUAL-01, VISUAL-02

- [ ] **V-06-05 — Skeleton primitives used in canonical sites.** `SkeletonCard`, `SkeletonTable`, `SkeletonMetric`, `SkeletonDetail` exist and at least one canonical usage (DashboardPage, ProjectsPage, one repo-detail page) renders the skeleton during `isLoading`.
  - `verified_by:` plan 06-07 task 1 (applies SkeletonCard + SkeletonMetric to DashboardPage and SkeletonTable to ProjectsPage) + plan 06-08 task 3 — Playwright assertion `web/e2e/visual-foundation.spec.ts -g "skeleton"` intercepts the load route and asserts the `[role="status"][aria-label="Loading"]` region is visible before data arrives. Grep gate: `grep -q "SkeletonCard\|SkeletonMetric" web/src/pages/DashboardPage.tsx` and `grep -q "SkeletonTable" web/src/pages/ProjectsPage.tsx` in plan 06-07 acceptance.
  - **Requirements:** VISUAL-03

- [ ] **V-06-06 — CopyInline / CopyButton ship with aria-live.** CopyButton announces "Copied to clipboard" via a hidden `aria-live="polite"` span on successful copy; CopyInline composes CopyButton with the 8px right-2/top-2 inset.
  - `verified_by:` plan 06-06 task 3 acceptance (`grep -q 'aria-live="polite"' web/src/components/common/CopyButton.tsx`, `grep -q 'right-2 top-2' web/src/components/common/CopyInline.tsx`, and negative grep for the 6px carve-out) + plan 06-08 task 3 Playwright assertion `-g "aria-live"` that the hidden span updates to "Copied to clipboard" after clicking the copy button on a seeded page.
  - **Requirements:** VISUAL-04

- [ ] **V-06-07 — Destructive vs primary button distinction.** shadcn `variant="destructive"` buttons render with the `--destructive` token (red family) and `variant="default"` with the primary token; visual snapshot confirms the two are distinguishable.
  - `verified_by:` plan 06-08 task 3 — Playwright snapshot `web/e2e/visual-foundation.spec.ts::"button hierarchy"` against a dev-only story fragment OR against an existing page that renders both variants (e.g. ConfirmDialog on AdminUsersPage). Covered secondarily by `a11y-audit.spec.ts` ensuring label contrast.
  - **Requirements:** VISUAL-05

- [ ] **V-06-08 — 1366×768 admin pages have no horizontal scroll.** `document.documentElement.scrollWidth <= clientWidth` on `/dashboard`, `/projects`, `/admin/users`, `/admin/audit`, `/admin/trash` at viewport 1366×768.
  - `verified_by:` plan 06-07 tasks 1 + 2 (apply sticky-first-column wrapper to admin tables with 6+ columns) + plan 06-08 task 3 — Playwright spec `web/e2e/responsive.spec.ts` with `test.use({ viewport: { width: 1366, height: 768 } })` iterating the admin route list and asserting `scrollWidth <= clientWidth`.
  - **Requirements:** VISUAL-06

- [ ] **V-06-09 — WCAG AA contrast passes.** Each of the 6 `--status-*` text/fill pairs in `web/src/index.css` has contrast ratio ≥ 4.5:1 in both `:root` and `.dark`. Axe-core audit flags zero AA violations on 5 seeded pages.
  - `verified_by:` plan 06-08 task 1 — `scripts/check-contrast.mjs` wired into `make check-contrast` and `make test`; plan 06-08 task 3 — Playwright spec `web/e2e/a11y-audit.spec.ts` running `AxeBuilder` against 5 key pages (login, dashboard, projects, admin/users, admin/audit) and failing on any WCAG 2.1 AA violation.
  - **Requirements:** VISUAL-08

- [ ] **V-06-10 — `@fontsource-variable/geist` removed from index.css and package.json.** The vestigial import on `web/src/index.css` line 4 is deleted; the package is absent from `web/package.json` dependencies.
  - `verified_by:` plan 06-06 task 1 acceptance: `! grep -R "@fontsource-variable/geist\|fontsource-variable/geist" web/ | grep -v node_modules` AND `! grep -q "@fontsource-variable/geist" web/package.json`.
  - **Requirements:** VISUAL-07

- [ ] **V-06-11 — No `font-medium | font-bold | font-light` in new files.** New Phase 6 files (and new usages added in Phase 6 modifications) use the Inter-only weight palette; forbidden weight utility classes appear nowhere.
  - `verified_by:` plan 06-08 task 2 — `make lint-typography` Makefile target running a grep gate against `web/src/` excluding an allowlist of pre-existing v1.0 files (documented in the target). Each plan task that creates/modifies a TSX file also carries a file-level `! grep -E 'font-medium|font-bold|font-light' <file>` acceptance check.
  - **Requirements:** VISUAL-07

- [ ] **V-06-12 — Envelope schema compiles through oapi-codegen types pipeline.** `components.schemas.ApiErrorEnvelope` and `ApiErrorClass` round-trip through `oapi-codegen v2.6.0` into `internal/api/types_gen.go` as a typed struct + enum constants; `go build ./internal/api/...` exits 0 after regeneration.
  - `verified_by:` plan 06-01 task 1 acceptance: `go build ./internal/api/... && grep -q "ApiErrorEnvelope" internal/api/types_gen.go && grep -q "ApiErrorClass" internal/api/types_gen.go`. Regeneration command (`go generate ./internal/api/...` or repo-canonical equivalent) exits 0.
  - **Requirements:** ERR-01

## Test Framework Summary

| Property | Value |
|----------|-------|
| Backend framework | Go `testing` stdlib (`go test ./...`) |
| Frontend e2e framework | `@playwright/test` 1.56+ |
| A11y audit | `@axe-core/playwright` (devDependency only — license gate via Makefile) |
| Contrast check | `scripts/check-contrast.mjs` (pure-Node, no deps) |
| Quick run | `go test -mod=vendor ./internal/httperr/... ./internal/api/...` |
| Full suite | `make test` |
| Phase gate | `make test` green + `npx playwright test web/e2e/error-envelope.spec.ts web/e2e/visual-foundation.spec.ts web/e2e/responsive.spec.ts web/e2e/a11y-audit.spec.ts` green |

## Sampling Rate

- **Per task commit:** `go test ./internal/httperr/... ./internal/api/...` + relevant Playwright spec (scoped by test filter).
- **Per wave merge:** `make test` + `npx playwright test web/e2e/error-envelope.spec.ts web/e2e/visual-foundation.spec.ts web/e2e/responsive.spec.ts`.
- **Phase gate:** Full `make test` green + all four Playwright specs green before `/gsd-verify-work`.

## Exit Criteria

- [ ] Every `V-06-XX` item above is checked.
- [ ] `make test` exits 0 on a clean checkout.
- [ ] All four Playwright specs pass under headless Chromium.
- [ ] `scripts/check-contrast.mjs` reports PASS on every status text/fill pair.
- [ ] No regression in existing Playwright specs (`admin.spec.ts`, `dashboard.spec.ts`, etc.).

## Deferred / Not In Phase 6

The following coverage items from RESEARCH.md Wave 0 Gaps are explicitly deferred (see RESEARCH.md Open Questions Q1 resolution):

- `web/src/api/client.test.ts` (vitest unit test for envelope parsing) — coverage provided by plan 06-04 Go integration tests + plan 06-08 Playwright e2e.
- `web/src/hooks/useApiError.test.tsx` (vitest unit test for hook normalization) — same coverage strategy.

These are deferred because Phase 6 does not add vitest to the toolchain. Revisit if the v1.1 TS surface grows substantially.
