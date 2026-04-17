---
gsd_state_version: 1.0
milestone: v1.1
milestone_name: v1.1-immediate-polish
status: executing
stopped_at: Completed 06-03-PLAN.md
last_updated: "2026-04-17T12:04:39.254Z"
last_activity: 2026-04-17 — Plan 06-03 completed (ApiError→envelope, useApiError hook + ErrorEnvelopeRenderer, dev-only story page live and Playwright-verified)
progress:
  total_phases: 5
  completed_phases: 0
  total_plans: 8
  completed_plans: 3
  percent: 38
---

# STATE: OmniRepo

**Last updated:** 2026-04-17

## Project Reference

- **Core value**: A single container that hosts every artifact type a corporate team produces or consumes — Docker images, Linux packages, Python wheels, Helm charts, raw blobs, S3 objects, Git repos — with vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.
- **Current focus**: v1.1 "Immediate Product Polish" — UI/UX quality-of-life pass (client snippets, empty states, health dashboard, failure messaging, saved filters, repo overview pages, visual-language polish). No core protocol reworks; additive backend endpoints only where needed to power the new UI.
- **Granularity**: coarse (5 phases, numbered 6 through 10, continuing from v1.0's last phase 5)

## Current Position

Phase: 06 (error-envelope-visual-foundation) — EXECUTING
Plan: 4 of 8
Status: Ready to execute
Last activity: 2026-04-17
Stopped at: Completed 06-03-PLAN.md

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
- Execute plan 06-04 next (integration tests + envelope audit across the handler surface).
- **Plan 06-04 follow-up:** migrate `internal/auth/middleware/deps.go` writeJSON401/writeJSON403 helpers to emit envelope shape, then update 3 legacy test assertions (admin_phase1_test.go:605,759; session_or_apikey_test.go:309).

### Blockers

(none)

### Performance Metrics

| Phase | Plan | Duration | Tasks | Files |
|-------|------|----------|-------|-------|
| 06    | 01   | ~5 min   | 3     | 7     |
| 06    | 02   | ~25 min  | 3     | 26    |
| 06    | 03   | ~30 min  | 3     | 9     |

### Research Flags

- **v1.1 scope source is `improvements.md`** — research step skipped
  because the document is already a researched product-direction brief.
  If phase planning surfaces unknowns, `/gsd-research-phase` or the
  research step inside `/gsd-plan-phase` is still available.

- **Error envelope shape (Phase 6)** — worth a short spike inside plan-phase
  to confirm the envelope is compatible with existing OpenAPI 3.1 components
  and the oapi-codegen types pipeline.

## Session Continuity

- **Next action**: Execute plan 06-04 — integration tests + envelope audit across the handler surface (including migrating `internal/auth/middleware/deps.go` writeJSON401/writeJSON403 helpers to the envelope shape so the three legacy test assertions flip to `body["code"]`).
- **Last session:** 2026-04-17T12:04:26.047Z
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
