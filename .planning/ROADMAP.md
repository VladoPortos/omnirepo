# OmniRepo Roadmap

## Shipped milestones

- **v1.0 — MVP** (shipped 2026-04-17) — 5 phases, 52 plans, 175 requirements. Single Go binary serving OCI, RPM, APT, PyPI, Helm, RAW, S3 (SigV4), and Git on one port with embedded React SPA, Trivy scanning, and a hard no-outbound-network invariant. See [`milestones/v1.0-ROADMAP.md`](milestones/v1.0-ROADMAP.md).

## Active milestone

**v1.1 — Immediate Product Polish** (scoped 2026-04-17, rescoped 2026-04-17)
— 2 phases, 33 requirements across 4 categories (SNIPPET, EMPTY, ERR, VISUAL).
UI/UX quality-of-life pass on top of shipped v1.0 protocol surfaces. No core
protocol reworks; no new backend surfaces beyond what Phase 6 already shipped.
Invariants carried forward: single Go binary, local FS only, zero outbound at
runtime, `make grep-cdn` green, stack frozen at Go 1.25 + React 19 + Vite +
Tailwind 4.

**Rescoped 2026-04-17:** Phases 8 (FAV), 9 (HEALTH), 10 (OVERVIEW) were dropped
from v1.1 and deferred to v1.2. v1.1 ships after Phase 7. See
REQUIREMENTS.md "Deferred to v1.2" section for the REQ list that moves.

### Phases

- [x] **Phase 6: Error Envelope & Visual Foundation** — Stable error contract and shared design-system primitives every later v1.1 phase consumes. ✅ Shipped 2026-04-17.
- [ ] **Phase 7: Snippet Polish, Dashboard Cards & Empty States** — Accuracy pass on existing per-protocol client snippets, additive summary cards on the existing Dashboard using already-available signal, context-aware empty states on previously-blank surfaces, plus walkthrough micro-fixes surfaced during UI screen-driving.

### Phase Details

#### Phase 6: Error Envelope & Visual Foundation
**Goal**: Establish the stable `{code, message, hint?, class, incident_id?}` API error envelope and the shared visual-language primitives (status colors, badges, skeletons, copy-to-clipboard, button hierarchy, responsive admin layouts) that every subsequent v1.1 phase renders on top of.
**Depends on**: Nothing beyond v1.0
**Requirements**: ERR-01, ERR-02, ERR-03, ERR-04, ERR-05, ERR-06, ERR-07, VISUAL-01, VISUAL-02, VISUAL-03, VISUAL-04, VISUAL-05, VISUAL-06, VISUAL-07, VISUAL-08, VISUAL-09
**Success Criteria** (what must be TRUE):
  1. Every `/api/v1/*` and `/api/admin/*` error response returns the new envelope with a valid `class` enum and never leaks an internal filesystem path, driver message, or stack trace (verified by a Go integration test that forces failure in each handler class).
  2. The UI surfaces API errors through a single shared renderer: validation errors highlight offending fields, transient errors show a Retry button, operator-action-required errors deep-link to the relevant admin page, and each error displays its `incident_id` when present.
  3. A single source-of-truth set of status tokens (`healthy / warning / failure / disabled / maintenance`) is applied via shared components so a "stale" badge looks identical wherever it appears; Playwright snapshot tests cover the badge set.
  4. All known-shape surfaces (tables, cards, detail panels) render skeleton placeholders during load; destructive vs primary buttons are visually distinct; URLs, commands, digests, and shown-once API keys expose a copy-to-clipboard affordance with visible confirmation.
  5. The admin UI renders without horizontal scroll at 1366×768 and 1440×900, and text/status color pairs pass automated WCAG AA contrast checks on the default theme.
**Plans:** 8 plans
Plans:
- [x] 06-01-PLAN.md — OpenAPI schema + internal/httperr package (envelope types + Write helper + tests)
- [x] 06-02-PLAN.md — Wire writeJSONError to envelope + UUID v7 incident IDs + EnvelopeRecoverer + openapi.yaml $ref migration
- [x] 06-03-PLAN.md — UI ApiError migration + useApiError hook + ErrorEnvelopeRenderer + dev-only error routes + ErrorClassStoryPage
- [x] 06-04-PLAN.md — Wave 1 verification (Go integration tests + Playwright error-envelope e2e + legacy handler test updates)
- [x] 06-05-PLAN.md — Protocol handler redaction sweep (29 files in internal/protocol/** + Makefile lint-protocol-redaction)
- [x] 06-06-PLAN.md — Visual primitives (status tokens in index.css + StatusBadge + 4 Skeleton* + CopyInline + CopyButton aria-live + Geist cleanup + reduced-motion)
- [x] 06-07-PLAN.md — Canonical skeleton adoption (Dashboard/Projects) + sticky-first-column tables + StatusBadgeStoryPage
- [x] 06-08-PLAN.md — Test gates (check-contrast.mjs + lint-typography + lint-spacing-carveout + visual-foundation/responsive/a11y-audit Playwright specs + @axe-core devDep gate)
**UI hint**: yes

#### Phase 7: Snippet Polish, Dashboard Cards & Empty States
**Goal**: Tight polish phase that ships v1.1. Four tracks: (a) audit and fix the
existing per-protocol snippet generators already shipped in v1.0
(`web/src/lib/snippets.ts` + `SnippetPanel`) for correctness, placeholder
convention, and completeness — this is accuracy work, NOT a rebuild;
(b) add composition summary cards to the existing Dashboard using
already-available signal (scan findings, audit log, storage endpoint, jobs) —
NO new `/api/v1/admin/health/*` endpoints, those belong to the v1.2 HEALTH
page; (c) replace the handful of ad-hoc inline empty-state strings with a
shared `EmptyState` component covering the EMPTY-01..08 surfaces with
explanatory headline + single primary CTA; (d) walkthrough micro-fixes —
placeholder for items surfaced during UI screen-driving that don't warrant
their own phase.
**Depends on**: Phase 6
**Requirements**: SNIPPET-01, SNIPPET-02, SNIPPET-03, SNIPPET-04, SNIPPET-05, SNIPPET-06, SNIPPET-07, SNIPPET-08, SNIPPET-09, EMPTY-01, EMPTY-02, EMPTY-03, EMPTY-04, EMPTY-05, EMPTY-06, EMPTY-07, EMPTY-08
**Success Criteria** (what must be TRUE):
  1. Every snippet in `web/src/lib/snippets.ts` passes a correctness audit: APT snippet no longer uses deprecated `apt-key add` (switches to `signed-by=` or `/etc/apt/trusted.gpg.d/*.asc`) and exposes suite + component placeholders instead of hard-coded `stable main`; Helm snippet includes `helm repo add`, `helm push` (documented plugin path), and `helm pull`; S3 snippet surfaces endpoint URL + region + bucket + access-key reminder (currently region is missing); Git snippet includes an auth hint (HTTPS basic auth or API-key form); RAW snippet includes `-u user:key` auth form per REQ-08 wording. Each edit lands with one unit test that asserts the emitted string shape.
  2. The existing `DashboardPage` grows at least 6 additive composition cards rendered via Phase 6 primitives (StatusBadge + SkeletonCard): 3 user-visible (Storage / Recent Failures / Scan Findings Trend) + 3 admin-only (Background Jobs / TLS Cert Expiry / Trivy DB Freshness). No new routes under `/api/v1/admin/health/*` (those belong to the deferred v1.2 Health page). New read-only admin endpoints are permitted when they deliver first-glance dashboard value and can be shipped without schema changes. The phase ships exactly one such endpoint: `GET /api/v1/admin/jobs/summary` (super-admin gate `ActionTriggerGC`, shape locked at D-06).
  3. A shared `EmptyState` component replaces every ad-hoc inline empty-state text (currently 4 call sites: ProjectsPage, SearchPage, ProjectDetailPage, DashboardPage) plus the previously-blank surfaces for EMPTY-01..08: zero-repos project, zero-members project, zero-artifacts repo (inlines SNIPPET for that protocol), never-scanned repo, no-TLS-cert admin, empty trash, empty saved-filters/favorites/recents, no-results search. Every empty state has an explanatory headline + single primary CTA selector that Playwright asserts.
  4. Walkthrough micro-fixes (items the user names at plan time) ship as atomic commits within the phase; each one is test-covered (unit, integration, or Playwright as appropriate).
  5. Full `make test` + `go test ./...` + `npm run build` green; all Phase 6 lint gates (protocol-redaction / contrast / typography / spacing-carveout / axe-devdep) still pass; Phase 6 Playwright specs still pass alongside new snippet-audit and empty-state specs.
**Plans:** 9 plans
Plans:
- [ ] 07-01-PLAN.md — Doc edits: move EMPTY-07 to v1.2 deferred + rewrite ROADMAP SC #2 (D-07)
- [ ] 07-02-PLAN.md — EmptyState + SnippetList primitives (extract from SnippetPanel)
- [ ] 07-03-PLAN.md — snippets.ts rewrite (S-01..09) + unit tests + CopyButton aria-live e2e
- [ ] 07-04-PLAN.md — Helm OCI→traditional chart mirror (MirrorToTraditional + OCI post-commit hook)
- [ ] 07-05-PLAN.md — /admin/jobs/summary endpoint + dashboard-thresholds utility + TanStack hook
- [ ] 07-06-PLAN.md — W-02 ref-counted repoSizeExpr + W-03 DEB Release-file pool-path reader
- [ ] 07-07-PLAN.md — DashboardPage Composition row (6 cards) + D-05 string migrations
- [ ] 07-08-PLAN.md — EmptyState wiring across 13 call sites (EMPTY-01..06, 08) + Playwright spec
- [ ] 07-09-PLAN.md — Codex rescue sweep (W-01) + findings triage + phase closure
**UI hint**: yes

### Deferred to v1.2 (dropped from v1.1 on 2026-04-17)

The following phases were scoped into v1.1 on 2026-04-17 and then dropped the
same day when the user decided v1.1 should ship as a tight polish milestone.
Their REQs live in REQUIREMENTS.md under "Deferred to v1.2" and will be
re-planned against a fresh v1.2 ROADMAP.md when that milestone opens.

- **Phase 8 (deferred): Favorites, Saved Filters & Recents** — Per-user
  favorites, named saved filters, and recently-visited items persisted
  server-side across sessions. Covers FAV-01..07. New SQLite migration
  required; dedicated design work before implementation.
- **Phase 9 (deferred): Health & Status Dashboard** — Admin-facing Health
  page with disk/DB/jobs/Trivy/TLS/tasks metrics backed by new
  `/api/v1/admin/health/*` endpoints. Covers HEALTH-01..09. Phase 7's
  dashboard-cards track addresses the "I need a quick health glance"
  need via composition over existing signal; the dedicated page + new
  endpoints are v1.2 work.
- **Phase 10 (deferred): Repository Overview Pages** — Default-landing
  control-center Overview tab on every repo type reusing snippets, scan,
  sync, visibility, and audit summaries. Covers OVERVIEW-01..08.
  Depended on the (now-deferred) Phase 9 health page for its scan-status
  card patterns.

### Progress

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 6. Error Envelope & Visual Foundation | 8/8 | ✅ Shipped | 2026-04-17 |
| 7. Snippet Polish, Dashboard Cards & Empty States | 0/9 | Not started | — |

## Backlog

_Forward-looking ideas not yet scheduled into a milestone live in
`NEXT-SESSION-ISSUES.md` at the repo root._ Current entries (carried from v1.0
closing audit):

- Docker shared-blob storage overestimate — revisit when billing/quota work begins.
- DEB `resolveDebPoolPath` assumes standard Debian pool layout; exotic layouts may 404.
- Codex rescue pass across the 2026-04-17 shipping batch (S3 bucket REST, admin GC status, UI rewrite).

### Phase 999.1: Tamagotchi ASCII pet in corner reacts to system state (BACKLOG)

**Goal:** [Captured for future planning] ASCII character living in a dismissible bubble in the bottom-right of the page. Reacts to live system state: uploading → "working" animation, idle → "sleeping z z z", errors → "concerned", scans running → "excited". Speech bubbles for occasional messages. Toggle-able from profile settings so corporate users who dislike the feature can hide it. Hooks into existing dashboard signal streams (jobs, scans, uploads) — no new backend endpoints. Fun/morale feature; post-v1.1, likely v1.2 Phase 8-ish.
**Requirements:** TBD
**Plans:** 0 plans

Plans:
- [ ] TBD (promote with /gsd-review-backlog when ready)
