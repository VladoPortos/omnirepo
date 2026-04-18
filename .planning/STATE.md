---
gsd_state_version: 1.0
milestone: v1.1
milestone_name: on 2026-04-17)
status: executing
stopped_at: Completed 07-05-PLAN.md
last_updated: "2026-04-18T00:55:52.610Z"
last_activity: 2026-04-18
progress:
  total_phases: 3
  completed_phases: 1
  total_plans: 17
  completed_plans: 13
  percent: 76
---

# STATE: OmniRepo

**Last updated:** 2026-04-17

## Project Reference

- **Core value**: A single container that hosts every artifact type a corporate team produces or consumes — Docker images, Linux packages, Python wheels, Helm charts, raw blobs, S3 objects, Git repos — with vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.
- **Current focus**: v1.1 "Immediate Product Polish" — UI/UX quality-of-life pass. **Rescoped 2026-04-17**: ships after 2 phases (Phase 6 done, Phase 7 tight polish). HEALTH / FAV / OVERVIEW deferred to v1.2. No core protocol reworks; no new backend endpoints beyond what Phase 6 already shipped.
- **Granularity**: tight (2 phases, numbered 6 and 7, continuing from v1.0's last phase 5)

## Current Position

Phase: 07 (snippet-polish-dashboard-cards-empty-states) — EXECUTING
Plan: 6 of 9
Status: Ready to execute
Last activity: 2026-04-18
Stopped at: Completed 07-05-PLAN.md

## Phase Map

| Phase | Name | Requirements | Depends on |
|-------|------|--------------|------------|
| 6 | Error Envelope & Visual Foundation | 16 (ERR-01..07, VISUAL-01..09) — ✅ DONE | Nothing beyond v1.0 |
| 7 | Snippet Polish, Dashboard Cards & Empty States | 17 (SNIPPET-01..09, EMPTY-01..08) | Phase 6 |

**Deferred to v1.2** (dropped from v1.1 2026-04-17): HEALTH-01..09, FAV-01..07, OVERVIEW-01..08. See `REQUIREMENTS.md` "Deferred to v1.2" section and `ROADMAP.md` "Deferred to v1.2" block.

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
- **[07-01] EMPTY-07 grouped under 'EMPTY — Context-aware empty states (deferred to v1.2)' sub-heading between FAV and OVERVIEW blocks in REQUIREMENTS.md** — v1.2 planner sees EMPTY-07 alongside the FAV cluster it depends on per E-04. Active v1.1 coverage recalculated to 32/32 REQs; Deferred to v1.2 now 25 REQs.
- **[07-01] ROADMAP Phase 7 SC #2 invariant preserved** — no routes under `/api/v1/admin/health/*` ship in Phase 7; those belong to the deferred v1.2 Health page. The one new read-only admin endpoint permitted by the rewrite (`GET /api/v1/admin/jobs/summary`) lives under `/api/v1/admin/` directly, super-admin gate `ActionTriggerGC`, shape locked at D-06 in `07-CONTEXT.md`. Cards count raised to 6 (3 user-visible + 3 admin-only) matching D-04 inventory.
- **[07-02] EmptyState + SnippetList shipped as wave-0 shared primitives.** `web/src/components/common/EmptyState.tsx` (E-01 props API — icon/title/description ReactNode/primaryCTA/children/className) and `web/src/components/common/SnippetList.tsx` (lifted SnippetPanel body, font-semibold header, 8px inset) now exist and are importable. SnippetPanel's Sheet shell delegates its body to `<SnippetList />`. No call sites migrated yet (plan 07-08's job); no snippets.ts rewrite (plan 07-03's job).
- **[07-02] base-ui render= prop — NOT shadcn asChild — for Button-as-Link.** Repo wraps `@base-ui/react/button`, not shadcn/Radix. EmptyState's `primaryCTA.to` path uses `<Button nativeButton={false} render={<Link to={...}>{label}</Link>} />` matching every existing Link-composed button in the codebase (DashboardPage/ProjectDetailPage/NotFoundPage/Breadcrumbs/Sidebar/ProfilePage). Plan's `<Button asChild>` action text was a Rule-1 bug fixed inline; plan's acceptance criteria guards against `TooltipTrigger[^>]*asChild` only, so the correction does not break any contract.
- **[07-02] CopyButton grew optional `aria-label` prop.** Typed as `'aria-label'?: string` so SnippetList can announce contextual labels per snippet (`Copy Pull`, `Copy .pypirc`, `Copy helm repo add (traditional)`). Backwards-compatible — falls back to the hardcoded `"Copy to clipboard"` when unset. Preferred over HTML-attribute passthrough because Button's underlying aria-label is hardcoded literal.
- **[07-02] SnippetPanel removed from `lint-spacing-carveout --exclude` list.** Body lift normalized inset to 8px (`right-2 top-2`) in SnippetList; SnippetPanel itself no longer contains the 6px classes. OneTimeReveal.tsx + shadcn-generated ui/sidebar.tsx remain the only grandfathered files. New SnippetList.tsx + EmptyState.tsx files use 8px inset and are NOT on the allowlist (per UI-SPEC line 532).
- **[07-02] EmptyState description typed as ReactNode, not string.** E-08 + UI-SPEC §EmptyState callsite wiring rule 2 requires embedding example chip buttons in the description region for EMPTY-08; locking the type signature here now avoids a 07-08 follow-up widening.
- **[07-02] EmptyState disabled CTA wraps Button in `<span className="inline-block">`.** Bare disabled Button has `pointer-events-none` which would swallow hover; span forwards pointer events so the TooltipTrigger fires on hover. Tooltip API is base-ui `render=` prop, NOT shadcn/Radix `asChild`.
- **[07-03] Git Authenticate form = `credential.helper store`, NOT `-c http.extraHeader=…`.** Both forms work against the BasicOrAPIKey middleware (API-key-as-password in any Basic-auth username field, verified in `internal/auth/middleware/basic_or_apikey.go`). Helper-store is simpler for users — one `git config` call, then `git push`/`fetch` prompts for user + key once and caches in `~/.git-credentials`. Avoids teaching the extraHeader mechanic.
- **[07-03] Vitest 4.1.4 added as devDep; `npm test` wires `vitest run`.** Tests colocated under `web/src/lib/__tests__/`; vitest config at `web/vitest.config.ts` uses node environment + `@/` alias matching Vite config. Peer-compatible with Vite 6.3.3. Unlocks pure-TS unit testing for `web/src/lib/*` (format helpers, query-key builders, etc.) without per-phase setup cost. First consumer is `snippets.test.ts` (9 shape tests).
- **[07-03] Defensive `default: return []` added to `getSnippets` switch.** The v1.0 implementation had no default branch, so passing an unknown RepoType returned `undefined` and would crash downstream `.map()` callers. Latent bug caught by the new "unknown RepoType" vitest case.
- **[07-03] APT dual-signing-key variants shipped as SEPARATE labeled entries, not a single dual-purpose block.** S-01 deprecation fix (Debian 12+ / Ubuntu 22.04+ no longer ship `apt-key`). Modern variant writes to `/etc/apt/keyrings`, legacy to `/etc/apt/trusted.gpg.d`. `apt source` line shows both `deb [signed-by=…]` and plain `deb` forms side-by-side inside one `<pre>` block (commented) so copy-paste ergonomics stay intact.
- **[07-03] Helm 4-entry snippet covers both traditional AND OCI flows.** `helm repo add (traditional)` + `helm pull (traditional)` + `helm push (OCI)` + `helm pull (OCI)`. OCI pushes will be server-side-mirrored to the traditional index in plan 07-04 or later (S-03b).
- **[07-03] Playwright webServer shell-syntax bug discovered (pre-existing, OUT-OF-SCOPE for 07-03).** `web/playwright.config.ts` `webServer.command` uses bash subshell syntax `(cd web && …)` which `/bin/sh` rejects with `Syntax error: "(" unexpected`. Reproduces on existing specs too (not new with 07-03). Logged to `.planning/phases/07-snippet-polish-dashboard-cards-empty-states/deferred-items.md`. Snippet-copy spec parses cleanly via `--list`; full-run verification deferred.
- **[07-04] OCI /v2 `resolveRepo` requireDocker gate relaxed to accept `type ∈ {docker, helm}`.** Without this change, `helm push oci://host/proj/helm/repo` 400s at the blob upload step before the mirror hook ever runs. Rule 3 (blocking) deviation absorbed inside Task 2. Existing docker-type tests continue to pass; helm-type integration tests (4 new) prove the new branch.
- **[07-04] Helm-mirror detection is mediaType-keyed, NEVER `len(layers)==1`.** Helm v3 supports provenance layers alongside the chart layer. Detection requires (a) `config.mediaType == application/vnd.cncf.helm.config.v1+json` AND (b) first layer with `mediaType == application/vnd.cncf.helm.chart.content.v1.tar+gzip`. `TestOCIManifestPut_MirrorsHelmWithProvenanceLayer` pushes provenance FIRST to prove selection is mediaType-driven.
- **[07-04] `oci.HelmMirrorHook` interface lives on the oci package; concrete `ociHelmMirrorAdapter` lives in `internal/app/phase3_helm.go`.** Keeps `oci` free of a `helm` import cycle while letting `app.Run` wire both CAS + helm.Mirror at construction time. New `wireHelmMirror` helper + compile-time guard `var _ oci.HelmMirrorHook = (*ociHelmMirrorAdapter)(nil)`.
- **[07-04] Forward-compat skip behavior.** Helm-config manifest with NO chart-content layer is a silent SKIP (debug-log, not warn). Could be a malformed push or a future Helm spec variant; either way the OCI push has already committed and the mirror is a pure side-effect. No warn-spam in operator logs.
- **[07-04] `helm.Mirror` constructed from the same deps as `helm.Handler` via the existing `wireHelm` path.** Both write paths share PathStore + coalescer + repos handles. A future reverse-mirror (traditional PUT → OCI manifest synthesis, deferred to v1.2) can plug into the same wiring harness.
- **[07-05] `/admin/jobs/summary` reuses `ActionTriggerGC` — no new policy action for a read-only summary.** Same gate `/admin/gc`, `/admin/trivy`, `/admin/tls` already use. D-06 explicitly calls this out; keeps the auth surface narrow.
- **[07-05] Three per-bucket `COUNT(*) WHERE status=...` queries instead of one FILTER aggregate.** Simpler to review, same perf on the small indexed `sync_jobs` table, side-steps the Assumption-A2 `SUM(CASE WHEN)` fallback the plan carried forward from RESEARCH. `last_completed_at` / `last_failed_at` use `ORDER BY updated_at DESC LIMIT 1`.
- **[07-05] `jobsVariant` returns ONLY `healthy` / `warning` / `failure`.** D-02 locks the variant set for the Jobs card; inventing `disabled` / `maintenance` returns here would expand the StatusBadge variant enum without a CONTEXT decision. Idle-never-run maps to healthy by design; a future phase that needs a fourth semantic state MUST add D-02b first.
- **[07-05] Six per-function typed overrides objects, not a single `DashboardThresholds` blob.** Each threshold function accepts its own shape (e.g. `StorageOverrides { warnRatio?, failRatio? }`) so admins can tune thresholds via the existing `settings` table without touching rendering code, and TypeScript narrows per-card so you can't accidentally pass Trivy overrides to `storageVariant`.
- **[07-05] `sync_jobs.status='pending'` exposed as `queued` on the wire.** Operator-friendly naming while preserving the schema. D-06 shape uses `queued` verbatim; the handler comment documents the mapping.

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
- Phase 6 verification (post-execution). Run `codex:rescue` / Codex review flow per global CLAUDE.md. Transition to Phase 7 (Client Snippets & Empty States). ✅ Shipped; Playwright walkthrough surfaced 5 findings (hydration warning, duplicate breadcrumb key, repo-validator wording, spurious project-bucket refetch, ERR-06 field highlight). All 5 resolved as atomic commits `2fd7ba5..0c79ef1`, Codex-reviewed and real-issue items absorbed (encodeURIComponent on route params + Playwright spec for aria-invalid + defensive setup-email/login bindings). Full `go test ./...` + `make test` + `npm run build` green. Phase 6 fully shipped.
- Transition to Phase 7 (Client Snippets & Empty States). Run `/gsd-plan-phase 7` to generate plans. ⚠️ SCOPE CHANGED 2026-04-17 session — rescope applied, see below.
- Execute plan 07-02 (wave-0 shared primitives: EmptyState + SnippetList). ✅ Shipped; `web/src/components/common/EmptyState.tsx` (new, 136 lines, E-01 props API + E-02 layout + E-08 a11y), `web/src/components/common/SnippetList.tsx` (new, 58 lines, lifted from SnippetPanel with font-medium→font-semibold + 6px→8px inset fixes), SnippetPanel refactored to delegate body to SnippetList, CopyButton grew optional `aria-label` prop for contextual per-snippet labels, Makefile `lint-spacing-carveout` dropped SnippetPanel.tsx from the exclude list. Two atomic commits `1c3674a` (Task 1) + `7ff48e6` (Task 2). All 5 Phase 6 lint gates + `npm run build` green.
- Execute plan 07-03 (snippet polish — getSnippets rewrite per S-01..S-09 + vitest scaffold + Playwright aria-live/clipboard spec). ✅ Shipped; `web/src/lib/snippets.ts` rewritten (docker/rpm unchanged, deb dual-signing+literal `stable main`, pypi `.pypirc`, helm 4-entry traditional+OCI, git Clone+Authenticate no-userinfo, raw `-u` on both, s3 `<region>`+credential comment); `web/vitest.config.ts` + `web/src/lib/__tests__/snippets.test.ts` (9 passing shape tests); `web/e2e/snippet-copy.spec.ts` asserts aria-live polite + clipboard round-trip. Three commits `bcd14b6` (RED tests) + `7f9e865` (GREEN impl) + `5b42059` (e2e spec). SNIPPET-01..09 now complete. Vitest 4.1.4 added as devDep. Full verification: `npm test` 9/9 green, Playwright `--list` green, make lint-spacing-carveout + lint-typography clean, `npm run build` green.
- Execute plan 07-04 (Helm OCI→traditional chart mirror — S-03b backend). ✅ Shipped; new `helm.Mirror` + `NewMirror` + `(*Mirror).MirrorToTraditional` (internal/protocol/helm/oci_mirror.go, 215 lines) mirrors OCI-pushed charts into `<dataRoot>/repos/<proj>/helm/<repo>/charts/<name>-<version>.tgz` with writer-tx (helm_charts upsert + FTS + metadata_state=dirty) + HI-02 rollback + regen coalescer kick; `oci.MediaTypeHelmChartConfigV1` + `oci.MediaTypeHelmChartContentV1` constants; `oci.HelmMirrorHook` interface; post-commit hook in `manifestPut` keyed on config mediaType + first-layer mediaType (NOT index); OCI `resolveRepo` relaxed to accept type=helm on /v2 (blocking deviation); `ociHelmMirrorAdapter` in `internal/app/phase3_helm.go` streams chart blob from OCI CAS into the mirror. Three commits `2d940e6` (RED), `6b9ad13` (GREEN Task 1), `72bff2d` (Task 2 full). Four helm-side integration tests + four OCI-side integration tests all green; full `go test ./...` + `make test` + `make lint-protocol-redaction` clean. SNIPPET-05 complete.
- Execute plan 07-05 (dashboard data sources — /admin/jobs/summary endpoint + threshold utilities). ✅ Shipped; new `internal/api/admin_jobs.go` (D-06 locked shape: running/queued/failed_last_24h/last_completed_at/last_failed_at) super-admin-gated via existing `ActionTriggerGC` + mounted next to `mountAdminGC` in `admin_phase1.go`; three handler tests green (200 super-admin/403 non-super/401 unauth); `web/src/lib/dashboard-thresholds.ts` ships six pure threshold functions (storage/failures/scanFindings/jobs/tls/trivyDB) mapping D-02 defaults to `StatusVariant` with per-function typed overrides; `jobsVariant` returns ONLY healthy/warning/failure (no new StatusBadge variants invented); 54 vitest boundary cases green; `useAdminJobsSummary(enabled)` TanStack hook + `AdminJobsSummary` interface appended to `queries.ts`. Four commits `2c16eb2` (RED Task 1), `f26f3e9` (GREEN Task 1), `84ddf51` (RED Task 2), `6fb0134` (GREEN Task 2). Full `go test ./internal/api/` + `npm run test` (63/63) + `npm run build` + `make lint-protocol-redaction` + `make lint-typography` + `make lint-spacing-carveout` clean. Plan 07-07 now has everything pre-built for the Composition row.

### Phase 7 rescope (2026-04-17 — APPLIED)

**User decision 2026-04-17:** v1.1 ships after one more tight polish phase.
Phases 8, 9, 10 were dropped from v1.1 and moved to a future v1.2 milestone
(user will plan v1.2 from scratch after v1.1 ships).

**Rescope applied 2026-04-17** to `.planning/ROADMAP.md` and
`.planning/REQUIREMENTS.md`. Phase 7 is now ready for `/gsd-plan-phase 7`
with the tight scope below.

**Phase 7 scope (APPLIED):**

- **Snippet audit** — verify `web/src/lib/snippets.ts` + `SnippetPanel` per repo type. Code-level verification 2026-04-17 confirmed real gaps: APT uses deprecated `apt-key add` and hard-codes `stable main`; Helm missing `helm push`; S3 missing region; Git/RAW missing auth hints. Accuracy/polish pass, not a rebuild.
- **Dashboard summary cards** — additive composition cards on existing `DashboardPage` using already-available v1.0 signal (audit log, storage endpoint, jobs endpoint, TLS admin endpoint, Trivy admin endpoint). Phase 6 primitives (StatusBadge + SkeletonCard) for rendering. **No new `/api/v1/admin/health/*` endpoints** — those are v1.2 Health page work.
- **EMPTY-01..08** — shared `EmptyState` component; code-level verification 2026-04-17 confirmed no shared component exists today and only 4 ad-hoc inline empty-state strings live across ProjectsPage/SearchPage/ProjectDetailPage/DashboardPage. EMPTY REQ wording preserved.
- **Walkthrough micro-fixes** — user names specific items at plan time; ship as atomic commits within the phase.

**Dropped from v1.1 → deferred to v1.2** (REQs preserved under REQUIREMENTS.md "Deferred to v1.2"):

- HEALTH-01..09 — dedicated Health/Status page + new `/api/v1/admin/health/*` endpoints.
- FAV-01..07 — favorites, saved filters, recently-visited (cross-session persistence).
- OVERVIEW-01..08 — repo overview control-center tab.

**Also parked for v1.2 (not yet REQs):**

- Avatar style picker — DiceBear is already in our deps (`@dicebear/core` + `@dicebear/collection` v9.2.2). Current UI uses only the `initials` collection. Swap to a user-choosable style. ~4-hour feature: picker in profile settings + seed column on users table + migration.
- Tamagotchi ASCII pet in bottom-right — backlog item 999.1 in ROADMAP.md. Fun/morale feature.

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
| Phase 07 P01 | ~3 min | 2 tasks | 2 files |
| Phase 07 P02 | ~4min | 2 tasks | 5 files |
| Phase 07 P03 | 5m21s | 2 tasks | 6 files |
| Phase 07 P04 | 11 min | 2 tasks | 10 files |
| Phase 07 P05 | 5m13s | 2 tasks | 6 files |

### Research Flags

- **v1.1 scope source is `improvements.md`** — research step skipped
  because the document is already a researched product-direction brief.
  If phase planning surfaces unknowns, `/gsd-research-phase` or the
  research step inside `/gsd-plan-phase` is still available.

- **Error envelope shape (Phase 6)** — worth a short spike inside plan-phase
  to confirm the envelope is compatible with existing OpenAPI 3.1 components
  and the oapi-codegen types pipeline.

## Session Continuity

- **Next action**: Run `/gsd-plan-phase 7` to generate plans for the rescoped Phase 7 (Snippet Polish, Dashboard Cards & Empty States). ROADMAP.md + REQUIREMENTS.md already reflect the tight scope.
- **Last session:** 2026-04-18T00:55:52.608Z
- **Artifacts on disk**:
  - `.planning/PROJECT.md` (Current Milestone: v1.1, Phase 6 progress paragraph added)
  - `.planning/REQUIREMENTS.md` (33 active v1.1 REQs + 24 deferred v1.2 REQs; traceability split by target milestone)
  - `.planning/ROADMAP.md` (v1.1 roadmap: phases 6–7; phases 8/9/10 in "Deferred to v1.2" block)
  - `.planning/STATE.md` (this file)
  - `.planning/milestones/v1.0-ROADMAP.md` + `v1.0-REQUIREMENTS.md` (archived)
  - `.planning/v1.0-MILESTONE-AUDIT.md` (v1.0 audit report)
  - `.planning/config.json` (granularity=coarse, parallelization=true, model_profile=quality)
  - `improvements.md` (repo root — v2.0 product-direction brief; v1.1 scope source)

---
*v1.1 rescoped 2026-04-17 — Phase 6 shipped; ready to plan Phase 7*
