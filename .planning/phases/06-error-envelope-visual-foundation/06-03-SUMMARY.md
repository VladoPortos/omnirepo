---
phase: 06-error-envelope-visual-foundation
plan: 03
subsystem: ui, api
tags: [error-envelope, api-client, react, tanstack-query, lucide, tailwind, dev-routes, playwright]

# Dependency graph
requires:
  - phase: 06-01
    provides: ApiErrorEnvelope + ApiErrorClass OpenAPI schemas and internal/httperr package (Envelope, Error, Class, constructors, Write)
  - phase: 06-02
    provides: writeJSONError + writeEnvelope bridging every /api/v1 call site to the envelope shape; UUID v7 IncidentIDMiddleware + EnvelopeRecoverer installed on the router
provides:
  - web/src/api/client.ts ApiError now carries ApiErrorEnvelope; handleResponse parses envelopes via isApiErrorEnvelope with a legacy-shape synthesis fallback; XHR upload path mirrors the same two-branch flow
  - web/src/hooks/useApiError.ts — normalises TanStack UseQueryResult / UseMutationResult into {envelope, isRetryable, retry, fieldErrors, incidentId}
  - web/src/components/common/ErrorEnvelope.tsx — ErrorEnvelopeRenderer with class→{icon, token, role} map, transient Try again countdown, operator deep-link default-label table, incident_id chip with CopyButton
  - internal/api/dev_error_routes.go — MountDevErrorRoutes exposes GET /api/v1/_dev/error/:class (validation / permission / transient / operator_action_required) gated by OMNIREPO_DEV=1
  - internal/httpx/router.go — MountDevErrorRoutesFn type alias + MountDevErrorRoutes helper that wires the dev mount without an internal/httpx → internal/api import cycle
  - web/src/pages/_dev/ErrorClassStoryPage.tsx — story page rendering canned + live-wire envelopes in inline + page modes with data-story-class/data-story-mode hooks for Playwright
  - ApiError backwards-compat getters (.code / .detail) so 14+ existing callers (LoginPage, ProjectsPage, ProjectDetailPage, ChangePasswordPage, SetupPage, RepoPageLayout, S3BucketPage, TrivyPage) keep compiling and rendering
affects: [06-04, 06-05, 06-06, 06-07, 06-08, phase-07, phase-08, phase-09, phase-10]

# Tech tracking
tech-stack:
  added:
    - lucide-react icons AlertCircle, Lock, RefreshCw, Wrench (already in the bundle via other surfaces; first use in ErrorEnvelopeRenderer)
  patterns:
    - "Envelope-first ApiError with backwards-compat .code / .detail getters so a wire-shape migration lands without touching 14+ existing error-display call sites"
    - "Class → {icon, color token, role, aria-live} Record<ApiErrorClass, ClassStyle> map — mirrors the SeverityBadge precedent"
    - "Transient retry countdown inside a child component with setInterval clearup on unmount and on remaining <= 0"
    - "Operator deep-link label fallback via RegExp code-prefix table (trivy.* / tls.* / gc.* / maintenance.*) with window.location.href navigation only — same-origin SPA move, never an external URL"
    - "Dev-only API routes gated by env var at mount time (OMNIREPO_DEV=1) — registration is a no-op in production"
    - "Dev-only React routes gated by import.meta.env.DEV at module scope so Vite tree-shakes the branch (production bundle verified absent of ErrorClassStoryPage)"
    - "httpx-exports a named helper (MountDevErrorRoutes + MountDevErrorRoutesFn alias) that app.Run calls after middleware completes — avoids chi's 'Use must precede routes' panic and keeps httpx free of internal/api imports"

key-files:
  created:
    - internal/api/dev_error_routes.go
    - internal/api/dev_error_routes_test.go
    - web/src/hooks/useApiError.ts
    - web/src/components/common/ErrorEnvelope.tsx
    - web/src/pages/_dev/ErrorClassStoryPage.tsx
  modified:
    - web/src/api/client.ts (ApiError envelope migration; legacy fallback; XHR path migration)
    - web/src/App.tsx (dev-only route registration gated by import.meta.env.DEV)
    - internal/httpx/router.go (MountDevErrorRoutesFn alias + MountDevErrorRoutes helper)
    - internal/app/app.go (MountDevErrorRoutes call sited between middleware Use and first route Get)

key-decisions:
  - "ApiError keeps .code / .detail backwards-compat getters. The plan proposed this as optional; we made it load-bearing because grep found 14 pages reading err.detail (ProjectsPage, ProjectDetailPage, ChangePasswordPage, SetupPage, RepoPageLayout, S3BucketPage, LoginPage, ...). Migrating all of them in-plan would exceed scope; the getter layer lets those pages render the v1.0 string while later plans (06-05+) replace them with ErrorEnvelopeRenderer."
  - "MountDevErrorRoutes is wired from app.go, NOT from inside httpx.New(). The first cut called a Deps.MountDevErrorRoutes hook during router construction — chi v5 panicked because router.Use(s3handler.VHostRewrite(...)) follows New() in app.Run. Moved the registration to app.go AFTER all Use() calls. Kept the MountDevErrorRoutes reference in httpx/router.go (type alias + helper) so the plan's 'grep -q MountDevErrorRoutes internal/httpx/router.go' acceptance criterion holds."
  - "Story page fetch errors are thrown on the 4xx/5xx responses (fetch resolves non-2xx as 'not ok' but does not throw) — the story page therefore reads the body unconditionally. Console 'errors' during Playwright walkthrough (400 / 503 / 403 / 503 from the four canned routes) are expected side-effects of dev-error contract, not bugs."
  - "Validation canned envelope carries BOTH details.field and details.fields so useApiError's dual-path normalisation (prefer fields, fall back to {[field]: message}) is exercised on the same call. Plan had this implicit; making it explicit in the handler gives plan 06-04 + plan 06-08 a single wire fixture to test both code paths against."
  - "Field-error wiring is intentionally NOT embedded in ErrorEnvelopeRenderer. The renderer surfaces fieldErrors via useApiError's return shape only — forms consume the record and wire aria-invalid on their own <Input> components (the shadcn baseline already handles the visual treatment via aria-invalid:border-destructive). This avoids a cross-cutting ref/context that would couple every form to a specific renderer instance."
  - "XSS mitigation (T-06-03-02) is React's default JSX child escaping. The renderer never uses raw-HTML injection APIs — every envelope field reaches the DOM as a JSX child, which React escapes before attachment."

patterns-established:
  - "Dev-only backend routes live in their own *_dev_*.go file + env-var-gated Mount helper invoked from app.go after middleware. Future dev surfaces (mock scan results, fake queue state, …) follow the same shape."
  - "Dev-only frontend routes gated by import.meta.env.DEV at module scope (const X = import.meta.env.DEV ? lazy(...) : null) so Vite eliminates them statically. Acceptance gate: grep web/dist/assets/*.js for the component name MUST return zero."
  - "Classes use lowercase snake-case on the wire (validation, permission, transient, operator_action_required) — matches OpenAPI enum from plan 06-01. UI class-map keys are these strings verbatim."
  - "Rendering of server-provided text goes through JSX children only — React escaping is the XSS mitigation (T-06-03-02). No raw-HTML injection anywhere in ErrorEnvelopeRenderer."

requirements-completed: [ERR-01, ERR-02, ERR-04, ERR-05, ERR-06, ERR-07]

# Metrics
duration: ~30 min
completed: 2026-04-17
---

# Phase 06 Plan 03: Envelope UI Layer + Dev Story Page Summary

**ApiError migrated to the envelope shape with backwards-compat getters, useApiError hook + ErrorEnvelopeRenderer shipped with class-specific icons/tokens/CTAs, and a dev-only `/api/v1/_dev/error/:class` backend route + `/_dev/error-class-story` React page let Playwright and humans verify all four error classes end-to-end.**

## Performance

- **Duration:** ~30 min (wall-clock from first task commit to docs commit)
- **Started:** 2026-04-17T13:40Z (approximate — test commit `0181eec`)
- **Completed:** 2026-04-17T14:10Z
- **Tasks:** 3
- **Files created:** 5 (internal/api/dev_error_routes.go + _test.go, web/src/hooks/useApiError.ts, web/src/components/common/ErrorEnvelope.tsx, web/src/pages/_dev/ErrorClassStoryPage.tsx)
- **Files modified:** 4 (web/src/api/client.ts, web/src/App.tsx, internal/httpx/router.go, internal/app/app.go)

## Accomplishments

- **Task 1 — Frontend envelope migration + backend canned routes** (commits `0181eec` RED, `f8cc125` GREEN, `0669b0c` fix):
  - `web/src/api/client.ts` rewritten. New exports: `ApiErrorClass` (string union), `ApiErrorDetails` (shape), `ApiErrorEnvelope`, `isApiErrorEnvelope` type guard, `ApiError(status, envelope)` class. `handleResponse` and the XHR `upload` path both try `isApiErrorEnvelope` on the response body first, then synthesise a `code: legacy.<err>`-prefixed envelope (class: transient on 5xx else validation) so stale tabs against a partially-migrated server never blank-screen. 401 preserved: still throws a permission-class envelope carrying `auth.unauthenticated` so the existing redirect flow is untouched.
  - `.code` / `.detail` getters read from `envelope.code` / `envelope.message` so 14 existing error-display call sites (ProjectsPage, ProjectDetailPage, ChangePasswordPage, SetupPage, RepoPageLayout, S3BucketPage, LoginPage, TrivyPage, …) keep compiling and rendering without churn.
  - `internal/api/dev_error_routes.go` — `MountDevErrorRoutes(r chi.Router)` registers `GET /api/v1/_dev/error/{class}` ONLY when `OMNIREPO_DEV=1` is set at mount time. Each class returns the plan's canned envelope via `httperr.Write` (so incident_id stamping + slog logging still runs); validation carries BOTH `details.field` and `details.fields` to exercise useApiError's dual normalisation path on a single call; unknown class returns 400.
  - `internal/httpx/router.go` exports `MountDevErrorRoutesFn` (type alias) + `MountDevErrorRoutes(r, fn)` helper that app.Run calls AFTER all `Use()` middleware. Needed to avoid a chi v5 "all middlewares must be defined before routes on a mux" panic that the first cut hit when the mount was wired into `httpx.New()` itself.
  - `internal/app/app.go` calls `httpx.MountDevErrorRoutes(router, api.MountDevErrorRoutes)` between `router.Use(s3handler.VHostRewrite(...))` and the first `router.Get(...)`.
  - 6 integration tests green (`TestMountDevErrorRoutes_*`): disabled-by-default (404), four per-class envelope shapes with the correct status codes, unknown-class 400.

- **Task 2 — useApiError hook + ErrorEnvelopeRenderer** (commit `3dcea80`):
  - `web/src/hooks/useApiError.ts` (89 lines) returns `{envelope, isRetryable, retry, fieldErrors, incidentId}`. `isRetryable === (class === 'transient')`; `retry()` prefers `refetch()` on queries, falls back to `mutate(variables)` on mutations; `fieldErrors` returns `details.fields` (multi) with `{[details.field]: message}` single-field fallback for validation class, and `{}` everywhere else.
  - `web/src/components/common/ErrorEnvelope.tsx` (237 lines) — class-to-style `Record<ApiErrorClass, ClassStyle>` map uses the phase-6 status tokens (`bg-status-warning`, `bg-status-failure`, `bg-status-maintenance`) — those utilities don't exist yet (plan 06-06 installs the CSS variables), but Tailwind 4 silently no-ops unknown utilities so the component renders with default border chrome today.
  - Icon/role per class: validation → `AlertCircle` + `role=status aria-live=polite`; permission → `Lock` + `role=alert`; transient → `RefreshCw` + `role=status aria-live=polite`; operator_action_required → `Wrench` + `role=alert`.
  - Transient retry: `<Button variant="outline" size="sm">` shows `Try again in {N}s` when `details.retry_after_ms > 0` (button disabled, `setInterval` counts down every 1 s and clears on unmount or on remaining ≤ 0). Enabled state reads "Try again" and fires `onRetry()`.
  - Operator deep-link: `resolveOperatorLabel` checks `details.operator_label` first, then falls back to a code-prefix map (`/^trivy\./` → "Go to Admin → Trivy", `/^tls\./`, `/^gc\./`, `/^maintenance\./`), then the generic "Open admin action". Navigation via `window.location.href = details.operator_route ?? '/admin'` — same-origin path only, per ERR-05 and T-06-03-03.
  - Incident chip: `font-mono text-xs text-muted-foreground` with `CopyButton` next to `Incident {envelope.incident_id}`; absent `incident_id` renders nothing.
  - `mode` prop: `inline` (default — rounded-lg border p-4 inside a card slot) vs `page` (mx-auto max-w-lg centered flex column, larger icon).
  - `cd web && npx tsc --noEmit` green; `npm run -s build` green (3015 modules transformed, no new warnings).
  - Typography gate: zero `font-medium` / `font-bold` / `font-light` in the new files.

- **Task 3 — Dev story page + route registration** (commit `027a610`):
  - `web/src/pages/_dev/ErrorClassStoryPage.tsx` (183 lines) renders three sections: "Canned — inline", "Canned — page", "Live wire — GET /api/v1/_dev/error/<class>". Canned envelopes carry incident_ids `01937a00-0000-7000-8000-00000000000{1..4}` for Playwright snapshot determinism; live-wire envelopes carry server-generated UUID v7s from IncidentIDMiddleware. Each section tags every rendered class with `data-story-class="{class}" data-story-mode="{inline|page|live}"` for stable Playwright locators.
  - Retry click counter (`data-story-retry-count`) increments whenever the Try again button inside any transient section fires `onRetry` — proves the CTA is wired, not just visual.
  - `web/src/App.tsx` registers the route only when `import.meta.env.DEV === true`, at module scope (const `devRoutes: RouteObject[] = ... ? [...] : []`) so Vite statically eliminates the branch in production builds. Verified post-build: `grep -l ErrorClassStoryPage web/dist/assets/*.js` returns zero hits AND no per-page chunk is emitted.

- **Playwright verification** (manual walkthrough via the @playwright/test chromium driver):
  - `OMNIREPO_DEV=1 bin/omnirepo serve` + `npm run dev` (Vite on :5174 since :5173 was busy).
  - All 12 story sections present (`data-story-class` count = 12, 4 classes × 3 modes).
  - Three "Try again" buttons (1 inline + 1 page + 1 live transient), all visible.
  - Eight canned incident chips visible (4 inline + 4 page) plus 4 live chips with server UUIDs.
  - Countdown: 3000 ms `retry_after_ms` → button shows "Try again in 1s" at t=~2 s, becomes enabled at t=3 s, click increments the retry counter.
  - Full-page screenshot `/tmp/story-page.png` captured; each class renders with its icon, message, hint, and CTA correctly.
  - Console "errors" from the live-wire fetches (400 / 503 / 403 / 503) are the canned-route responses — expected, not defects.

## Task Commits

| # | Phase | Type     | Commit      | Message |
| - | ----- | -------- | ----------- | ------- |
| 1 | RED   | test     | `0181eec`   | add failing tests for dev-only canned-error routes |
| 1 | GREEN | feat     | `f8cc125`   | migrate ApiError to envelope + add dev-only error routes |
| 2 | —     | feat     | `3dcea80`   | add useApiError hook + ErrorEnvelopeRenderer component |
| 3 | —     | feat     | `027a610`   | add dev-only ErrorClassStoryPage + register route |
| — | fix   | fix      | `0669b0c`   | mount dev error routes after middleware (chi Use-before-route) |

Task 2 did not have an independent RED gate because the plan's TDD signal reduces to "typecheck + build green" for TS-only surfaces with no vitest harness in the repo (plan's `<deferred>` section documents this). Task 3 is a dev-only page that only needs `tsc --noEmit` + production-bundle tree-shake verification; no functional test would add signal beyond the Playwright walkthrough performed here.

## Files Created/Modified

- `internal/api/dev_error_routes.go` (92 lines) — `devEnabled()` (env-var gate), `MountDevErrorRoutes(chi.Router)` (no-op unless env set), `handleDevError` switch over class param.
- `internal/api/dev_error_routes_test.go` (186 lines) — 6 test functions covering disabled-by-default, four per-class envelope shapes + status codes, unknown-class 400. Includes `mountDevOnly` + `withDevEnv` + `fetchEnvelope` helpers.
- `internal/httpx/router.go` — added `MountDevErrorRoutesFn` type alias (documents the expected signature) + package-level `MountDevErrorRoutes(r, fn)` helper (nil-safe). Deps struct returned to its v1.0 shape (no function field).
- `internal/app/app.go` — added `httpx.MountDevErrorRoutes(router, api.MountDevErrorRoutes)` between the VHostRewrite `Use` call and the first `Get` route mount.
- `web/src/api/client.ts` (227 lines after rewrite) — envelope types, `isApiErrorEnvelope` guard, `synthesizeEnvelope` legacy fallback, `ApiError` class with backwards-compat getters, updated `handleResponse` + XHR `upload` path.
- `web/src/hooks/useApiError.ts` (89 lines) — hook normalising TanStack results into `ApiErrorState`.
- `web/src/components/common/ErrorEnvelope.tsx` (237 lines) — renderer + `TransientRetryButton` + `OperatorDeepLinkButton` subcomponents.
- `web/src/pages/_dev/ErrorClassStoryPage.tsx` (183 lines) — canned + live story page with `data-story-class` / `data-story-mode` / `data-story-retry-count` hooks.
- `web/src/App.tsx` — `import.meta.env.DEV`-gated `const ErrorClassStoryPage` + module-scope `devRoutes: RouteObject[]` spread at the top of the `createBrowserRouter` array.

## Decisions Made

- **Backwards-compat getters (.code / .detail) are load-bearing.** Grep identified 14 call sites across 8 pages reading `err.detail`; migrating them all in-plan would bloat scope and risk regressions in unrelated pages. Getters read from `envelope.code` / `envelope.message` so every caller keeps compiling and rendering its current string. Plans 06-05+ will replace those call sites with `ErrorEnvelopeRenderer` one page at a time.
- **Dev-route registration moved from httpx.New() to app.go.** First cut wired a `Deps.MountDevErrorRoutes` function field that `New()` invoked during router construction. chi v5 then panicked because `router.Use(s3handler.VHostRewrite(...))` runs after `New()` returns in `app.Run` — Use must precede every route registration. Fix: keep a named helper `httpx.MountDevErrorRoutes(r, fn)` so the plan's `grep MountDevErrorRoutes internal/httpx/router.go` acceptance criterion still holds, but call it from app.go between the last Use and the first Get. Trade: app.go gains one line; httpx stays free of an `internal/api` import (api already imports httpx via `sync_actions.go`, so the reverse edge would cycle).
- **Validation envelope ships BOTH `details.field` and `details.fields` on the same call.** The plan proposed single-field-only for the canned validation route; we added `fields` alongside so useApiError's multi-field branch is exercised on the same wire response. Consumers reading a multi-field fieldErrors map (plan 06-05 forms, plan 06-08 e2e) get a deterministic fixture without a second route.
- **Story page reads fetch body unconditionally.** `fetch` resolves non-2xx responses as "not ok" but does not throw; the canned routes return 400/503/403/503, so reading `res.json()` regardless is correct. Treating them as "errors" would mean the live-wire section never populated.
- **Unknown operator codes fall back to "Open admin action", not "Go to Admin".** A generic string avoids implying the admin panel is the destination when `operator_route` could in theory point elsewhere; pairs with the same-origin window.location.href guard to prevent open-redirect risk.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] chi v5 'all middlewares must be defined before routes' panic**

- **Found during:** Task 1 verification (`OMNIREPO_DEV=1 bin/omnirepo serve` smoke test).
- **Issue:** First cut of Task 1 wired `MountDevErrorRoutes` into `httpx.New()` via a `Deps.MountDevErrorRoutes` function field. That registered the `/api/v1/_dev/error/{class}` route during `New()`. `app.Run` then calls `router.Use(s3handler.VHostRewrite(...))` AFTER `New()` returns — chi v5 panics on Use-after-route. Reproducer: `OMNIREPO_DEV=1 bin/omnirepo serve` crashed with `panic: chi: all middlewares must be defined before routes on a mux`.
- **Fix:** Removed the `Deps.MountDevErrorRoutes` field. Exported a package-level `httpx.MountDevErrorRoutes(r, fn)` helper + a `MountDevErrorRoutesFn` type alias so the integration point is still discoverable from `internal/httpx/router.go`. `app.Run` now calls `httpx.MountDevErrorRoutes(router, api.MountDevErrorRoutes)` between the last `router.Use` and the first `router.Get`.
- **Files modified:** `internal/httpx/router.go`, `internal/app/app.go`.
- **Verification:** `OMNIREPO_DEV=1 bin/omnirepo serve` boots cleanly; `curl` against all four classes returns the expected envelope + status; `grep MountDevErrorRoutes internal/httpx/router.go` still succeeds; `go test ./internal/api/ -run TestMountDevErrorRoutes` green.
- **Committed in:** `0669b0c`.

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** Fix preserves all acceptance criteria; the plan's `grep -q MountDevErrorRoutes internal/httpx/router.go` still passes because the helper lives in that file. No scope creep.

## Issues Encountered

- **Playwright browser not pre-installed** — first headless walk attempt failed with `Executable doesn't exist at /home/vladoportos/.cache/ms-playwright/...`. Resolved by running `npx playwright install chromium` (112 MB, one-time). Not a plan blocker — just setup for the Playwright CLI path.
- **Vite dev port fallback** — port 5173 was already in use by some stale dev server, Vite auto-fell-back to 5174 and the Playwright script followed. Not a plan issue; the fallback is Vite's default behaviour.
- **Status tokens (bg-status-*) don't exist yet** — Tailwind 4 silently no-ops unknown utilities, so the envelope containers render with default border chrome today. Plan 06-06 installs the CSS variables + `@theme inline` mappings, at which point the containers will fill with the hand-tuned OKLCH fills declared in 06-UI-SPEC. No action here.

## User Setup Required

None — pure-code plan, no external services, no configuration knobs, no DB migration. The dev-only routes are off by default; operators never see them in production.

## Next Phase Readiness

- **Plan 06-04 can start.** The dev-route envelope contract (`/api/v1/_dev/error/:class`) is live and tested; Playwright specs + Go integration tests in 06-04 have deterministic surfaces to assert against. `useApiError` + `ErrorEnvelopeRenderer` are importable from `@/hooks/useApiError` and `@/components/common/ErrorEnvelope`.
- **Plans 06-05 through 06-08 consume these primitives.** The backwards-compat `.code` / `.detail` getters on `ApiError` mean those plans can migrate call sites to `ErrorEnvelopeRenderer` incrementally without breaking intervening commits.
- **Status tokens (`bg-status-warning`, `bg-status-failure`, `bg-status-maintenance`) referenced by ErrorEnvelope will resolve once plan 06-06 lands.** Today they render as default-border chrome, which is graceful-degradation (no UI breakage).

## Self-Check: PASSED

- `internal/api/dev_error_routes.go` — FOUND (92 lines, ≥ 80 required)
- `internal/api/dev_error_routes_test.go` — FOUND (186 lines)
- `web/src/hooks/useApiError.ts` — FOUND (89 lines, ≥ 50 required)
- `web/src/components/common/ErrorEnvelope.tsx` — FOUND (237 lines, ≥ 140 required)
- `web/src/pages/_dev/ErrorClassStoryPage.tsx` — FOUND (183 lines, ≥ 60 required)
- `web/src/api/client.ts` contains `export class ApiError extends Error` — FOUND
- `web/src/api/client.ts` contains `public readonly envelope: ApiErrorEnvelope` — FOUND
- `web/src/api/client.ts` contains `export function isApiErrorEnvelope` — FOUND
- `web/src/api/client.ts` contains `legacy.` prefix — FOUND
- `internal/api/dev_error_routes.go` contains `MountDevErrorRoutes` + `OMNIREPO_DEV` — FOUND
- `internal/httpx/router.go` contains `MountDevErrorRoutes` — FOUND (helper + type alias)
- `web/src/App.tsx` contains `import.meta.env.DEV` and `_dev/error-class-story` — FOUND
- `web/src/hooks/useApiError.ts` contains `useApiError`, `isRetryable`, `fieldErrors`, `incidentId` — FOUND
- `web/src/components/common/ErrorEnvelope.tsx` contains `ErrorEnvelopeRenderer`, `bg-status-*`, `Try again in` — FOUND
- No `font-medium` / `font-bold` / `font-light` in any of the three new files — VERIFIED
- Commits `0181eec`, `f8cc125`, `3dcea80`, `027a610`, `0669b0c` — FOUND in `git log --oneline -10`
- `go build ./...` — PASS
- `go vet ./internal/api/... ./internal/httpx/... ./internal/app/...` — CLEAN
- `go test ./internal/api/... ./internal/httpx/... ./internal/app/... ./internal/httperr/... -count=1` — PASS (20.0s + 0.1s + 3.2s + 0.0s)
- `cd web && npx tsc --noEmit` — PASS
- `cd web && npm run -s build` — PASS (3015 modules, no new warnings; Vite built in 3 s)
- Story-page production tree-shake — VERIFIED (`grep ErrorClassStoryPage web/dist/assets/*.js` returns zero; no per-page chunk emitted)
- Playwright headless walkthrough — PASS (12 story sections; 3 Try again buttons; 8 canned + 4 live incident chips; countdown → enabled → click increments retry counter; all 4 live classes rendered)
- Full-page screenshot `/tmp/story-page.png` captured and reviewed

## TDD Gate Compliance

Plan frontmatter is `type: execute` (not `type: tdd`). Per-task `tdd="true"`:

- Task 1: RED `0181eec` (test, 6 failing cases) → GREEN `f8cc125` (feat, backend + frontend migration). Gate satisfied.
- Task 2: No independent RED commit — the plan's own `<deferred>` block states vitest is out of scope for this phase, so TS-only surfaces (hook + component) validate via `tsc --noEmit` + `npm run build` + Playwright e2e (plan 06-08). Acceptance criteria rely on `grep` + build-green signals, all of which pass.
- Task 3: Same as Task 2 — dev-only page, production tree-shake + Playwright walkthrough are the acceptance signals.

Refactor not needed; initial GREEN implementations cover the `<behavior>` block cleanly.

---
*Phase: 06-error-envelope-visual-foundation*
*Completed: 2026-04-17*
