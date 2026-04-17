---
gsd_state_version: 1.0
milestone: v1.1
milestone_name: v1.1-immediate-polish
status: verifying
stopped_at: Completed 06-08-PLAN.md — Phase 6 test gates in place
last_updated: "2026-04-17T14:13:23.692Z"
last_activity: 2026-04-17 — Plan 06-08 completed (Phase 6 test gates: WCAG AA contrast hard-gate via scripts/check-contrast.mjs, typography + 6px spacing carve-out greps, visual-foundation snapshot + responsive 1366×768 + a11y-audit axe-core Playwright specs, @axe-core/playwright devDep-only gate, tsconfig noEmit:true root-fix; --status-disabled-foreground Rule-1 darkened to pass AA; make test + 3 new Playwright specs all green; Phase 6 closes).
progress:
  total_phases: 5
  completed_phases: 1
  total_plans: 8
  completed_plans: 8
  percent: 100
---

# STATE: OmniRepo

**Last updated:** 2026-04-17

## Project Reference

- **Core value**: A single container that hosts every artifact type a corporate team produces or consumes — Docker images, Linux packages, Python wheels, Helm charts, raw blobs, S3 objects, Git repos — with vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.
- **Current focus**: v1.1 "Immediate Product Polish" — UI/UX quality-of-life pass (client snippets, empty states, health dashboard, failure messaging, saved filters, repo overview pages, visual-language polish). No core protocol reworks; additive backend endpoints only where needed to power the new UI.
- **Granularity**: coarse (5 phases, numbered 6 through 10, continuing from v1.0's last phase 5)

## Current Position

Phase: 06 (error-envelope-visual-foundation) — EXECUTING
Plan: 8 of 8
Status: Phase complete — ready for verification
Last activity: 2026-04-17
Stopped at: Completed 06-08-PLAN.md — Phase 6 test gates in place

## Phase Map

| Phase | Name | Requirements | Depends on |
|-------|------|--------------|------------|
| 6 | Error Envelope & Visual Foundation | 16 (ERR-01..07, VISUAL-01..09) | Nothing beyond v1.0 |
| 7 | Client Snippets & Empty States | 17 (SNIPPET-01..09, EMPTY-01..08) | Phase 6 |
| 8 | Favorites, Saved Filters & Recents | 7 (FAV-01..07) | Phase 6 |
| 9 | Health & Status Dashboard | 9 (HEALTH-01..09) | Phase 6 |
| 10 | Repository Overview Pages | 8 (OVERVIEW-01..08) | Phases 6, 7, 9 |

## Scope reference

Scope is derived from `improvements.md` (in the repo root) — specifically
"Phase 1: Immediate Product Polish" plus the tightly-related UI sections.
Formal REQ-IDs live in `.planning/REQUIREMENTS.md`; phase breakdown in
`.planning/ROADMAP.md`.

**Explicitly deferred** to later milestones per `improvements.md`'s own
prioritization: retention/quotas, promotion/release pipeline, policy
engine, bulk administration, audit enrichment, notification hooks,
backup/recovery UX, air-gap export-import, HA, SBOM/provenance,
scoped tokens, LDAP/OIDC.

## Accumulated Context

### Decisions for v1.1

- **[06-01] oapi-codegen -generate types,skip-prune** — shared `components.schemas` (`ApiErrorEnvelope`, `ApiErrorClass`) and shared `components.responses` must survive regeneration even before plan 02 wires the `$ref`s. Without this, the generator prunes unreferenced schemas and `types_gen.go` loses them.
- **[06-01] httperr.Envelope is a hand-written mirror, not an alias** — keeps `internal/httperr` dependency-free of `internal/api`. Struct tags and shape asserted by `TestEnvelope_JSONMarshal` — any drift from generated `ApiErrorEnvelope` shows at test time.
- **[06-01] httperr.Internal() = ClassTransient + HTTP 500** — generic "An internal error occurred." message; cause logged via slog under incident_id and never serialized. Enforces ERR-03 at library boundary so handler call sites cannot accidentally leak.
- **[06-02] Use `context.WithValue(ctx, chimw.RequestIDKey, idStr)` for incident-ID injection** — chi v5.2.5 does NOT export a `WithRequestID` helper (confirmed by reading `vendor/github.com/go-chi/chi/v5/middleware/request_id.go`). The direct `context.WithValue` path is idiomatic and mirrors chi's own `RequestID` middleware at line 75 of that file.
- **[06-02] OpenAPI `default:` response `$ref` for envelope shape** — chose one `default: $ref: '#/components/responses/ValidationError'` per operation over exhaustive per-status enumeration. The v1.0 spec had only one inline 4xx block in 2666 lines; per-operation defaults document the envelope shape broadly (72 `$ref`s across 74 ops) without adding ~300 lines of repetitive YAML. Later plans can refine specific ops to enumerate 401/403/404 where deterministic.
- **[06-02] 302 handler call sites widened mechanically via sed** — `writeJSONError(w, ` → `writeJSONError(w, r, ` across 23 handler files. Every handler had `r *http.Request` in scope; zero refactoring needed. Semantic-free migration preserves the v1.0 call convention while enabling envelope+incident_id correlation.
- **[06-02] auth/middleware/deps.go still emits legacy `{error: ...}` shape** — out of scope for plan 06-02's file list. Deferred to plan 06-04 (integration audit / threat T-06-02-04). This is why `admin_phase1_test.go:605,759` still pass asserting `body["error"] == "password-change-required"` — the MCP 403 path is emitted by the middleware, not through `writeJSONError`.
- **[06-03] ApiError keeps backwards-compat `.code` / `.detail` getters** reading from the envelope so the 14+ pages still using `err.detail` render unchanged. Plans 06-05+ migrate those call sites to `ErrorEnvelopeRenderer` incrementally without breaking intervening commits.
- **[06-03] Dev-only `/api/v1/_dev/error/:class` routes live in `internal/api/dev_error_routes.go`** behind an `OMNIREPO_DEV` env-var gate. Wired from `app.go` (not `httpx.New`) because chi v5 panics if `Use` follows a route — `httpx` exports a named `MountDevErrorRoutes(r, fn)` helper that `app.Run` calls between the last `router.Use` and the first `router.Get`. This also keeps `httpx` free of an `internal/api` import (api → httpx already exists in `sync_actions.go`).
- **[06-03] Dev-only React surfaces gated by `import.meta.env.DEV` at module scope** (`const X = import.meta.env.DEV ? lazy(...) : null`) so Vite statically eliminates the branch. Acceptance gate: `grep web/dist/assets/*.js` for the component name must return zero — verified for `ErrorClassStoryPage`.
- **[06-03] Canned validation envelope ships BOTH `details.field` and `details.fields`** on the same response so `useApiError`'s dual-path normalisation is exercised on a single wire fixture. Plan 06-04 + plan 06-08 e2e tests get deterministic input for both the single-field and multi-field code paths.
- **[06-04] ZERO legacy {error, detail} emitters remain on /api/v1** — plan 04 migrated `internal/auth/middleware/deps.go` (writeJSON401/writeJSON401Basic/writeJSON403) + `internal/httpx/spa.go` writeAPINotFound (SPA 404 for /api/*) + `internal/httpx/middleware_maintenance.go` MaintenanceMode in-scope. Every /api/v1 error response now ships ApiErrorEnvelope regardless of which middleware or handler fired it; `TestEnvelope_NoInternalLeakage_AcrossHandlers` is a meaningful whole-surface gate.
- **[06-04] writeJSONError default-message-per-status fallback** — ~100 call sites passing `""` for `detail` on 500 paths would ship empty `message` fields violating the OpenAPI schema's required non-empty string. New `defaultMessageForStatus(status)` helper supplies static developer-authored sentences (never interpolated, ERR-03 safe) so every wire envelope has a non-empty message. Alternative of editing each call site would have been ~100 one-line changes with zero semantic value.
- **[06-04] Tightened normalizeLegacyCode passthrough** — old `containsDot` gate let "errors.go:123" / "/home/.../foo.db" pass through verbatim, violating both the envelope code regex AND ERR-03. New `codeShapeRegex` gate requires the full wire pattern for passthrough; non-matching inputs sanitize through `legacy.*` prefix.
- **[06-04] Three-flag dev-surface opt-in for Playwright e2e** — `OMNIREPO_DEV=1` (backend canned routes) + `OMNIREPO_DEV_PROXY=0` (keep embedded SPA instead of proxying to Vite) + `VITE_OMNIREPO_DEV=true` (build-time include of story page route). Independent flags so regular production builds stay completely free of dev surfaces (T-06-03-04 tree-shake invariant preserved), and the Playwright suite runs against a standalone Go binary + embedded bundle without a Vite sidecar.
- **[06-05] Actual `%v`-leak count was 59 in 14 files (raw/rpm/deb/pypi/helm), not the ~206 estimate** from 06-RESEARCH.md Q2 (the estimate used a broader `http.Error` grep — the `%v`-interpolation subset is ~1/3 of that). OCI/S3/Git handlers had ZERO leaks to redact (they delegate to library handlers emitting protocol-native errors: go-containerregistry, gofakes3, go-git v6). Acceptance-criterion threshold `slog.ErrorContext >= 100` was proportional to 206 — the underlying invariant ("every former `%v` leak paired with a log call") holds at 59/59.
- **[06-05] Dual-gate ERR-03 regression prevention** — in-process Go test `internal/protocol/protocoltest/TestNoPercentVLeakInHTTPError` runs under `go test ./...`; Makefile `lint-protocol-redaction` (wired as `test:` prerequisite) runs under `make test`. Identical grep pattern + `*_test.go` exclude rules so both fail/pass in lockstep. Future changes introducing new `%v` leaks fail both workflows simultaneously.
- **[06-05] Canonical protocol redaction shape** — `slog.ErrorContext(ctx, "<pkg>.<handler>.<op>_failed", slog.String("incident_id", chimw.GetReqID(ctx)), slog.String("<key>", <val>), slog.Any("err", err))` paired with `http.Error(w, "<generic>", status)`. Generic client messages: `"storage error"` for IO/tx, `"invalid multipart body"` for pypi multipart parse. Replaces the 1-line `http.Error(w, fmt.Sprintf("<op>: %v", err), status)` anti-pattern.
- **[06-06] Status tokens as 6×3 triples in :root + .dark (mirrored)** — `--status-{variant}`, `--status-{variant}-foreground`, `--status-{variant}-border` for healthy/warning/failure/disabled/maintenance/neutral. Dark tokens mirror light values verbatim in v1.1 (no dark theme activated). Tailwind 4 `@theme inline` exposes them as `bg-status-*` / `text-status-*-foreground` / `border-status-*-border` utilities. Downstream phases MUST use only these tokens; raw Tailwind palette is forbidden in new code per UI-SPEC §Color Forbidden list.
- **[06-06] Skeleton variants attach role="status" aria-label="Loading" only to the outer container** — inner Skeleton bars are decorative divs. T-06-06-03 mitigation: SR announces the surface once, not per bar.
- **[06-06] CopyInline uses 8px inset (right-2 top-2)** — new placement per UI-SPEC §Spacing Exceptions. The 6px (right-1.5 top-1.5) inset is grandfathered to the two v1.0 files where it already appears (SnippetPanel, OneTimeReveal). Plan 06-08 greps new files for the 6px classes and fails.
- **[06-06] CopyButton aria-live upgrade is purely additive** — wrap existing Tooltip return in a fragment, append `<span aria-live="polite" aria-atomic="true" className="sr-only">{copied ? 'Copied to clipboard' : ''}</span>`. Props signature unchanged; all three existing callers (SnippetPanel, OneTimeReveal, ErrorEnvelope) keep working.
- **[06-06] @fontsource-variable/geist purged** — vestigial import in index.css + dependency in package.json; zero components referenced Geist. Inter via self-hosted .woff2 remains the single UI typeface.
- **[06-06] PrimitivesStoryPage added to plan scope despite being out of `files_modified`** — the plan objective calls for "at least a spot Playwright visual verification"; no production consumer exists for these primitives until plan 06-07. Story page is dev-only, tree-shaken from production via the `DEV_ROUTES_ENABLED` gate (same pattern as ErrorClassStoryPage in 06-03). Provides a living reference for intended primitive usage plus a Playwright surface for 06-08's visual regression tests.
- **[06-07] Sticky-first-column centralized in shared DataTable via `stickyFirstColumn?: boolean` opt-in prop** — 3 admin pages (UsersPage/AuditPage/TrashPage) + 5 repo pages (Apt/Docker/Helm/Pypi/RpmRepoPage) all with 6+ columns flip it on; narrow tables (<6 cols: RawRepoPage, ProfilePage API-key/S3-key) leave it off. First-column sticky class is MERGED (not replaced) with any column-level className so layout hints (w-10, text-right, hidden lg:table-cell) survive. Avoids 8 × per-file wrapper drift.
- **[06-07] ProjectsPage migrated from card grid to 6-column sticky-first-column table** — Name/Description/Members/Repos/Size/Created. Plan must_haves required SkeletonTable + overflow-x-auto + sticky left-0 + bg-card all in ProjectsPage.tsx — card grid couldn't honour that honestly. Row click preserved via TableRow onClick; projects.spec.ts e2e asserts text visibility only, no regression. Rule 3 auto-fix (Blocking) committed in `a17565e`.
- **[06-07] DashboardPage uses a two-tier loading strategy** — full-page Skeleton* layout when `isLoading && storageLoading` (both cold), per-slice SkeletonCard/SkeletonMetric fallback once either resolves. AND-gate avoids flash-of-mixed-state when /api/v1/dashboard and /api/v1/dashboard/storage return at different speeds.
- **[06-07] StatusBadgeStoryPage is the third dev-only `/_dev/` page sharing the DEV_ROUTES_ENABLED gate** — guard extended to require all three story pages to resolve (`ErrorClassStoryPage && PrimitivesStoryPage && StatusBadgeStoryPage`) so a broken lazy-chunk never registers a partial dev surface. Production tree-shake verified: zero StatusBadgeStoryPage matches in web/dist/assets/*.js.
- **[06-07] Stale `.js` tsc -b emissions cleaned reactively; root fix (noEmit: true in tsconfig) deferred to 06-08** per the phase-06-07 prompt's explicit directive.
- **[06-08] Rule-1 auto-fix: --status-disabled-foreground darkened from oklch(0.55 0 0) to oklch(0.5 0 0)** in :root + .dark so the disabled token passes WCAG AA (was 4.45:1, now 5.50:1). scripts/check-contrast.mjs would otherwise ship with a failing gate; shipping the gate red would defeat its purpose. All 6 statuses now PASS AA text-on-fill.
- **[06-08] sidebar.tsx added to lint-spacing-carveout --exclude list** beyond the plan's required two files. UI-SPEC §Spacing grandfathers SnippetPanel + OneTimeReveal; shadcn-generated ui/sidebar.tsx (plan 05-02) also pre-Phase-6 with `top-1.5` / `right-1.5` as generated menu chrome. Three-file carve-out is hard-coded in the target with inline rationale.
- **[06-08] @axe-core/playwright in devDependencies ONLY** — Makefile `lint-axe-devdep` enforces on every `make test`. MPL-2.0 file-level copyleft compatible with Apache-2.0 runtime posture only if the MPL code never ships into the runtime artifact.
- **[06-08] noEmit:true landed in web/tsconfig.json** — root fix for the stale-.js Vite resolver bug flagged by 06-06/06-07 deviations. `tsc -b` now silently type-checks; Vite transpiles TS itself; zero .js leakage into web/src. `npm run build` still produces a clean `web/dist/`.
- **[06-08] Typography allowlist uses basename-based `--exclude`** with all 49 entries verified unique in the tree. Every Phase-6-created file (StatusBadge, 4 Skeleton*, CopyInline, ErrorEnvelope, useApiError, 3 story pages) is NOT on the allowlist — confirming 06-06/07's claim that new Phase-6 files ship clean of forbidden weight/size classes. lint-typography re-verifies on every `make test`.
- **[06-08] Every VISUAL-0N requirement now has at least one automated test gate.** Five hard lint gates in `make test` (lint-protocol-redaction + check-contrast + lint-typography + lint-spacing-carveout + lint-axe-devdep) plus 3 new Playwright specs (visual-foundation snapshot, responsive 1366×768 across 6 admin routes, a11y-audit via axe-core across 5 pages). Plans 7–10 inherit all gates automatically.
- **Phases continue numbering from v1.0** — v1.1 starts at Phase 6, not Phase 1. Preserves traceability across milestones in the same `.planning/` tree.
- **ERR envelope lands in Phase 6 as a foundation** — every SNIPPET/HEALTH/OVERVIEW surface renders its errors through the new envelope; putting ERR late would force rework across phases 7–10.
- **VISUAL is not a trailing-polish phase** — the design-system primitives (status tokens, skeletons, badges, copy-to-clipboard, button hierarchy) ship alongside ERR in Phase 6 so every later UI phase consumes shared components instead of re-implementing them.
- **FAV lives in its own phase (8)** — schema migration + server-side persistence + nav surfacing is independent enough to parallelize after Phase 6 lands, rather than bolting it onto an already-large foundation phase.
- **OVERVIEW (Phase 10) depends on SNIPPET (Phase 7) and HEALTH (Phase 9)** — OVERVIEW-02 reuses snippet components, and OVERVIEW's scan/sync summary cards share patterns with HEALTH cards. Scheduling it last avoids duplicate component drift.

### Decisions carried forward from v1.0

- Single Go binary, local filesystem only, zero outbound at runtime.
- `make grep-cdn` stays green — no runtime CDN.
- Stack frozen at Go 1.25 + React 19 + Vite + Tailwind 4 for v1.1.
- `oapi-codegen/v2` types-only generation; chi routes hand-written.
- modernc.org/sqlite + argon2id + Trivy-as-subprocess remain.
- All UI assets bundled via `//go:embed`.

### Todos

- Run `/gsd-plan-phase 6` to decompose Phase 6 into plans. ✅ Plans generated; now executing.
- Execute plan 06-02 (envelope wire-up + panic recovery). ✅ Shipped; 302 call sites migrated, 72 openapi $refs, middleware chain updated.
- Execute plan 06-03 (UI envelope layer + story page). ✅ Shipped; ApiError → envelope with compat getters, useApiError hook + ErrorEnvelopeRenderer live, dev-only `/api/v1/_dev/error/:class` + `/_dev/error-class-story` wired and Playwright-verified.
- Execute plan 06-04 next (integration tests + envelope audit across the handler surface). ✅ Shipped; 20 Go tests (6 unit + 14 integration) + 9 Playwright scenarios; auth middleware + SPA 404 + maintenance middleware migrated to envelope shape — ZERO legacy emitters remain on /api/v1.
- Execute plan 06-05 (protocol redaction). ✅ Shipped; 59 `%v` leaks redacted across 14 files (raw/rpm/deb/pypi/helm); protocoltest + Makefile `lint-protocol-redaction` gate prevent regression; OCI/S3/Git were already clean.
- Execute plan 06-06 (visual foundation — status tokens, StatusBadge, Skeleton). ✅ Shipped; 6 status-token triples in :root + .dark + @theme inline; StatusBadge (6 variants × 2 sizes + iconOnly); 4 Skeleton variants (Card/Table/Detail/Metric); CopyInline with optional masking; CopyButton aria-live upgrade; reduced-motion rule; Geist purge; dev-only /_dev/primitives-story verified via Playwright in both light and dark.
- Execute plan 06-07 (apply primitives to canonical pages + sticky-first-column admin tables + StatusBadge story page). ✅ Shipped; DashboardPage consumes SkeletonCard + SkeletonMetric (7 role=status regions on cold load); ProjectsPage migrated to sticky-first-column table with SkeletonTable on load; DataTable grew `stickyFirstColumn` prop enabled on 3 admin pages (Users/Audit/Trash) + 5 repo pages (Apt/Docker/Helm/Pypi/Rpm); /_dev/status-badge-story renders 24-variant matrix, production tree-shake verified; Playwright drove dashboard + projects (loaded + loading) + admin/users + admin/audit at 1366×768 with zero page-horizontal-scroll and zero console errors.
- Execute plan 06-08 (Phase 6 test gates). ✅ Shipped; 5 hard lint gates wired into `make test` (lint-protocol-redaction + check-contrast + lint-typography + lint-spacing-carveout + lint-axe-devdep, all clean); 3 new Playwright specs (visual-foundation snapshot of 24-variant StatusBadge matrix + responsive 1366×768 across 6 admin routes + a11y-audit via axe-core across 5 pages), 13/13 pass in 7.6s; @axe-core/playwright in devDeps only; tsconfig noEmit:true deferred-fix landed; --status-disabled-foreground Rule-1 auto-fix so every status passes WCAG AA. Full `make test` green in ~30s. Phase 6 complete — every ERR-01..07 + VISUAL-01..09 requirement now has at least one automated test gate.
- Phase 6 verification (post-execution). Run `codex:rescue` / Codex review flow per global CLAUDE.md. Transition to Phase 7 (Client Snippets & Empty States).

### Blockers

(none)

### Performance Metrics

| Phase | Plan | Duration | Tasks | Files |
|-------|------|----------|-------|-------|
| 06    | 01   | ~5 min   | 3     | 7     |
| 06    | 02   | ~25 min  | 3     | 26    |
| 06    | 03   | ~30 min  | 3     | 9     |
| 06    | 04   | ~25 min  | 2     | 17    |
| 06    | 05   | 11 min   | 2     | 15    |
| 06    | 06   | ~40 min  | 3     | 12    |
| 06    | 07   | ~45 min  | 3     | 12    |
| Phase 06 P08 | ~35 min | 3 tasks | 11 files |

### Research Flags

- **v1.1 scope source is `improvements.md`** — research step skipped
  because the document is already a researched product-direction brief.
  If phase planning surfaces unknowns, `/gsd-research-phase` or the
  research step inside `/gsd-plan-phase` is still available.

- **Error envelope shape (Phase 6)** — worth a short spike inside plan-phase
  to confirm the envelope is compatible with existing OpenAPI 3.1 components
  and the oapi-codegen types pipeline.

## Session Continuity

- **Next action**: Execute plan 06-08 (Phase 6 test gates — check-contrast.mjs WCAG gate, lint-typography + lint-spacing-carveout greps, visual-foundation/responsive/a11y-audit Playwright specs, @axe-core devDep gate, and the deferred `tsconfig noEmit: true` infra fix inherited from 06-06/06-07).
- **Last session:** 2026-04-17T14:13:23.690Z
- **Artifacts on disk**:
  - `.planning/PROJECT.md` (Current Milestone: v1.1)
  - `.planning/REQUIREMENTS.md` (57 v1.1 REQs, traceability populated)
  - `.planning/ROADMAP.md` (v1.1 roadmap: phases 6–10)
  - `.planning/STATE.md` (this file)
  - `.planning/milestones/v1.0-ROADMAP.md` + `v1.0-REQUIREMENTS.md` (archived)
  - `.planning/v1.0-MILESTONE-AUDIT.md` (v1.0 audit report)
  - `.planning/config.json` (granularity=coarse, parallelization=true, model_profile=quality)
  - `improvements.md` (repo root — v2.0 product-direction brief; v1.1 scope source)

---
*v1.1 milestone roadmap approved — ready to plan Phase 6*
