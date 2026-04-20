# OmniRepo Roadmap

## Shipped milestones

- **v1.0 — MVP** (shipped 2026-04-17) — 5 phases, 52 plans, 175 requirements. Single Go binary serving OCI, RPM, APT, PyPI, Helm, RAW, S3 (SigV4), and Git on one port with embedded React SPA, Trivy scanning, and a hard no-outbound-network invariant. See [`milestones/v1.0-ROADMAP.md`](milestones/v1.0-ROADMAP.md).

## Active milestone

**v1.1 — Immediate Product Polish** (scoped 2026-04-17, rescoped 2026-04-17, re-extended 2026-04-19)
— 3 phases, 33 requirements across 4 categories (SNIPPET, EMPTY, ERR, VISUAL)
plus one net-new scope addition (MIRROR — REQ IDs TBD at phase plan time).
UI/UX quality-of-life pass on top of shipped v1.0 protocol surfaces, plus
UI wiring for already-shipped upstream-mirror backend before public release.
Invariants carried forward: single Go binary, local FS only, zero outbound
at runtime except explicit user-triggered syncs, `make grep-cdn` green,
stack frozen at Go 1.25 + React 19 + Vite + Tailwind 4.

**Rescoped 2026-04-17:** Phases 8 (FAV), 9 (HEALTH), 10 (OVERVIEW) were dropped
from v1.1 and deferred to v1.2. See REQUIREMENTS.md "Deferred to v1.2" section
for the REQ list that moves.

**Re-extended 2026-04-19:** Phase 8 (Upstream Mirror & Docker Clone) added
as a pre-public-release scope addition. Spec:
`docs/superpowers/specs/2026-04-19-upstream-mirror-design.md`.
Names "Phase 8/9/10 (deferred)" below refer to historical v1.1 scope before
the 2026-04-17 rescope; v1.2 will re-number its own phases fresh.

### Phases

- [x] **Phase 6: Error Envelope & Visual Foundation** — Stable error contract and shared design-system primitives every later v1.1 phase consumes. ✅ Shipped 2026-04-17.
- [ ] **Phase 7: Snippet Polish, Dashboard Cards & Empty States** — Accuracy pass on existing per-protocol client snippets, additive summary cards on the existing Dashboard using already-available signal, context-aware empty states on previously-blank surfaces, plus walkthrough micro-fixes surfaced during UI screen-driving.
- [ ] **Phase 8: Upstream Mirror & Docker Clone** — UI for already-shipped upstream-mirror backend. Adds per-repo is_mirror flag at creation (APT/RPM/PyPI/Helm), Docker per-click clone modal with live progress bar, upstream-credentials CRUD UI, upload-block for mirror repos, scan_on_sync toggle (default OFF).

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
- [x] 07-01-PLAN.md — Doc edits: move EMPTY-07 to v1.2 deferred + rewrite ROADMAP SC #2 (D-07)
- [x] 07-02-PLAN.md — EmptyState + SnippetList primitives (extract from SnippetPanel)
- [x] 07-03-PLAN.md — snippets.ts rewrite (S-01..09) + unit tests + CopyButton aria-live e2e
- [x] 07-04-PLAN.md — Helm OCI→traditional chart mirror (MirrorToTraditional + OCI post-commit hook)
- [x] 07-05-PLAN.md — /admin/jobs/summary endpoint + dashboard-thresholds utility + TanStack hook
- [x] 07-06-PLAN.md — W-02 ref-counted repoSizeExpr + W-03 DEB Release-file pool-path reader
- [x] 07-07-PLAN.md — DashboardPage Composition row (6 cards) + D-05 string migrations
- [x] 07-08-PLAN.md — EmptyState wiring across 13 call sites (EMPTY-01..06, 08) + Playwright spec
- [x] 07-09-PLAN.md — Codex rescue sweep (W-01) + findings triage + phase closure
**UI hint**: yes

#### Phase 8: Upstream Mirror & Docker Clone
**Goal**: Wire the UI for OmniRepo's already-shipped upstream-mirror backend (sync_handlers for APT/RPM/PyPI/Helm; pull-external for OCI; encrypted upstream credentials; jobs pool) so the v1.1 public release lets operators point a repo at an upstream archive (Ubuntu focal main/universe amd64 etc.) or clone individual Docker images on demand. Adds per-repo `is_mirror` flag at creation time (immutable URL, editable filter), Docker per-click clone modal with live byte-level progress, upstream-credentials CRUD UI on ProjectSettingsPage, upload-block middleware for mirror repos, and a new `scan_on_sync` per-repo flag (default OFF). No scheduler — external cron or UI button only. No drift-purge — accumulator semantics only. No Git mirror. Full design spec: `docs/superpowers/specs/2026-04-19-upstream-mirror-design.md`.
**Depends on**: Phase 7
**Requirements**: MIRROR-01..NN (IDs assigned at plan time — see phase 08-CONTEXT.md)
**Success Criteria** (what must be TRUE):
  1. A user can create an APT/RPM/PyPI/Helm repo with `is_mirror=true`, fill upstream URL + protocol-specific filters + optional credential + optional scan_on_sync flag, and the repo persists those fields. Every attempt to upload to a mirror repo returns 403 envelope `code="repo_is_mirror"` from every protocol's upload handler (verified per-protocol integration tests).
  2. The existing `POST /api/v1/projects/{name}/repos/{type}/{repo}/sync` endpoint, when called with empty body against a mirror repo, reads mirror config from the repo row and enqueues a sync_jobs row. Calls with body against a mirror repo return 400 `mirror_overrides_not_allowed`. A second sync on a repo with an in-flight sync returns 409 `sync_already_running`.
  3. The Docker repo page's "Pull External" dialog is rewritten to actually call `POST /pull-external` with source ref + optional retag + optional cred + optional scan override, and displays a live progress bar (layer N/M · bytes / total · pct) via polling `GET /api/v1/jobs/{id}` every 500 ms. Playwright asserts progress advances and success closes the modal.
  4. The 4 mirror-repo pages (AptRepoPage / RpmRepoPage / PypiRepoPage / HelmRepoPage) render a "Sync now" button visible only when `is_mirror=true`, which POSTs to `/sync` with empty body and surfaces the same job-progress affordance. RepoSettingsTab gains a "Mirror config" card showing URL (readonly), filter (editable), cred (editable), scan_on_sync toggle — all behind PATCH `/repos/{type}/{repo}` with URL-change rejection.
  5. ProjectSettingsPage gains an "Upstream credentials" tab using the already-mounted `/api/v1/projects/{name}/upstream-creds` CRUD. Secrets never echoed on response. Deleting a cred referenced by a mirror repo sets `mirror_cred_id=NULL` via ON DELETE SET NULL and the next sync fails with a clear "credential missing" envelope.
  6. `go test ./...` + `make test` + `npm run build` + `make grep-cdn` green; all Phase 6 lint gates still pass; Playwright e2e covers mirror creation, sync-now, Docker clone, and mirror-upload rejection. Codex rescue pass run per CLAUDE.md global rule.
**Plans:** 6 plans expected (1 per milestone M1–M6 from the design spec)
Plans:
- [x] 08-01-PLAN.md — M1: Backend foundation (schema + mirror-aware sync + upload-reject + concurrency guard) ✅ Shipped 2026-04-20
- [ ] 08-02-PLAN.md — M2: Progress tracking (writer helper + jobs endpoint + handler wraps across 5 protocols)
- [ ] 08-03-PLAN.md — M3: Docker clone modal with progress + retag + scan override
- [ ] 08-04-PLAN.md — M4: Mirror flag UI (CreateRepoDialog + 4 Sync Now buttons + RepoSettingsTab mirror card)
- [ ] 08-05-PLAN.md — M5: Upstream-credentials CRUD UI tab on ProjectSettingsPage
- [ ] 08-06-PLAN.md — M6: Integration tests (fake upstreams × 5) + Playwright e2e + Codex rescue
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
| 7. Snippet Polish, Dashboard Cards & Empty States | 1/9 | In Progress | — |
| 8. Upstream Mirror & Docker Clone | 0/6 | Not planned | — |

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
