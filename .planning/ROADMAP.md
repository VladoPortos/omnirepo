# OmniRepo Roadmap

## Shipped milestones

- **v1.0 — MVP** (shipped 2026-04-17) — 5 phases, 52 plans, 175 requirements. Single Go binary serving OCI, RPM, APT, PyPI, Helm, RAW, S3 (SigV4), and Git on one port with embedded React SPA, Trivy scanning, and a hard no-outbound-network invariant. See [`milestones/v1.0-ROADMAP.md`](milestones/v1.0-ROADMAP.md).

## Active milestone

**v1.1 — Immediate Product Polish** (scoped 2026-04-17) — 5 phases, 57 requirements across 7 categories (SNIPPET, EMPTY, HEALTH, ERR, FAV, OVERVIEW, VISUAL). UI/UX quality-of-life pass on top of shipped v1.0 protocol surfaces. No core protocol reworks; additive backend endpoints only where needed to power new UI. Invariants carried forward: single Go binary, local FS only, zero outbound at runtime, `make grep-cdn` green, stack frozen at Go 1.25 + React 19 + Vite + Tailwind 4.

### Phases

- [ ] **Phase 6: Error Envelope & Visual Foundation** — Stable error contract and shared design-system primitives every later v1.1 phase consumes.
- [ ] **Phase 7: Client Snippets & Empty States** — Copyable per-protocol client configuration and guided empty-state guidance replace blank screens across the UI.
- [ ] **Phase 8: Favorites, Saved Filters & Recents** — Per-user favorites, named saved filters, and recently-visited items persisted server-side across sessions.
- [ ] **Phase 9: Health & Status Dashboard** — Admin-facing health surface with disk/DB/jobs/Trivy/TLS/tasks metrics backed by new `/api/v1/admin/health/*` endpoints.
- [ ] **Phase 10: Repository Overview Pages** — Default-landing control-center Overview tab on every repo type reusing snippets, scan, sync, visibility, and audit summaries.

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

#### Phase 7: Client Snippets & Empty States
**Goal**: Every repo detail page offers copyable, pre-filled client configuration for its protocol, and every previously-blank surface (project with no repos, repo with no artifacts, empty trash, empty search, unconfigured TLS, unscannned repo, missing members, empty favorites/filters/recents) shows guided next-step content.
**Depends on**: Phase 6
**Requirements**: SNIPPET-01, SNIPPET-02, SNIPPET-03, SNIPPET-04, SNIPPET-05, SNIPPET-06, SNIPPET-07, SNIPPET-08, SNIPPET-09, EMPTY-01, EMPTY-02, EMPTY-03, EMPTY-04, EMPTY-05, EMPTY-06, EMPTY-07, EMPTY-08
**Success Criteria** (what must be TRUE):
  1. On each repo-type detail page (Docker, PyPI, APT, RPM, Helm, Git, S3, RAW), a user can click a snippet block and confirm — via visible feedback — that the pre-filled command (`docker login`/pull/push, `pip` + `.pypirc`, APT `sources.list`, RPM `.repo`, Helm `repo add`/push/pull, Git clone, `aws configure` + CLI/SDK, `curl -u … -T file URL`) lands on the clipboard with this instance's real URL, repo name, and auth hints.
  2. A project with zero repos, a project with zero additional members, a repo with zero artifacts, a scan-capable repo that has never been scanned, an admin account with no uploaded TLS cert, and an empty trash each render a typed empty state with an explanatory headline and a single primary CTA pointing at the correct action.
  3. A repo-with-zero-artifacts empty state inlines the SNIPPET component for that protocol so a new user can copy upload instructions without leaving the page.
  4. Search with no results, and any favorites/saved-filters/recents surface with no items, render guidance text (with example queries for search) rather than a blank region.
  5. Playwright coverage asserts every empty state by deterministically provisioning the "zero" precondition (new project, wiped repo, disabled TLS, empty trash, no-hits search) and verifying headline + CTA selectors.
**Plans**: TBD
**UI hint**: yes

#### Phase 8: Favorites, Saved Filters & Recents
**Goal**: Each authenticated user can pin favorite projects and repos, save named filters on every filterable table, and see their recently-visited items — with all of it persisted server-side, reorderable, renameable, deletable, and surviving a browser-data reset.
**Depends on**: Phase 6
**Requirements**: FAV-01, FAV-02, FAV-03, FAV-04, FAV-05, FAV-06, FAV-07
**Success Criteria** (what must be TRUE):
  1. A new SQLite migration adds per-user tables for favorites, saved filters, and recently-visited items; the migration runner and `sqlitetest` fixtures pass with the existing `BEGIN IMMEDIATE` writer discipline.
  2. After pinning a project and a repo, saving a named filter on at least two filterable tables (projects list, repos list, artifact list, audit log, search), and navigating through five repos, the user logs out, clears browser storage, logs back in, and sees the same pins, saved filters, and recents restored.
  3. The sidebar/top-nav surfaces favorite projects and repos together, respecting a user-chosen order set via drag-and-drop or explicit reorder controls; order persists across sessions.
  4. Saved filters can be renamed and deleted from the UI; the deletion removes the server-side row and immediately updates the filter picker without a page reload.
  5. Recently-visited projects and repos show at most the configured last-N entries per user, deduplicated on revisit, and degrade gracefully when a visited target has been deleted (shows a disabled entry with explanation, not a broken link).
**Plans**: TBD
**UI hint**: yes

#### Phase 9: Health & Status Dashboard
**Goal**: An admin can answer "is OmniRepo healthy right now?" from a single dedicated page backed by JSON endpoints — disk usage, DB size and growth, background-job status, Trivy DB freshness, TLS certificate expiry, and recent long-running task history — without reading logs.
**Depends on**: Phase 6
**Requirements**: HEALTH-01, HEALTH-02, HEALTH-03, HEALTH-04, HEALTH-05, HEALTH-06, HEALTH-07, HEALTH-08, HEALTH-09
**Success Criteria** (what must be TRUE):
  1. Admin REST endpoints under `/api/v1/admin/health/*` (disk, db, jobs, trivy, tls, tasks, summary) return the documented shapes against both a seeded test fixture and a freshly-booted instance; integration tests assert each endpoint's schema and that non-admins receive a permission-class error envelope.
  2. An admin opens the Health page from the sidebar and sees cards for disk usage (used/free/total with warning band at configurable threshold), SQLite DB size with 7-day and 30-day growth, background-job counts (running/queued/failed), Trivy DB freshness, TLS cert days-remaining, and a table of the last 20 long-running tasks with duration and failure reason.
  3. The warning thresholds for disk free space and TLS days-remaining are driven by settings so a test can flip a threshold and observe the card transition between healthy and warning states.
  4. The Health page supports one-click manual refresh and an optional visible auto-refresh interval that respects user session activity (no hidden background polling).
  5. The dashboard uses the Phase 6 design-system badges and skeleton loaders, and surfaces any underlying metric-collection failure through the Phase 6 error envelope (including a link to relevant admin remediation for operator-action-required cases such as missing Trivy DB).
**Plans**: TBD
**UI hint**: yes

#### Phase 10: Repository Overview Pages
**Goal**: Every repo detail page has an "Overview" tab as its default landing view, presenting a control-center layout with copyable snippets, latest artifacts, recent uploads, sync and scan status summaries, visibility/policy placeholders, and last-modified actors — the single page a user opens first to understand a repo.
**Depends on**: Phase 6, Phase 7, Phase 9
**Requirements**: OVERVIEW-01, OVERVIEW-02, OVERVIEW-03, OVERVIEW-04, OVERVIEW-05, OVERVIEW-06, OVERVIEW-07, OVERVIEW-08
**Success Criteria** (what must be TRUE):
  1. Navigating to any repo of any supported type (OCI, RPM, APT, PyPI, Helm, Git, RAW, plus S3 buckets) lands on an Overview tab by default; browser back/forward preserves the tab state and the Overview route is deep-linkable.
  2. The Overview reuses Phase 7 SNIPPET components for this repo's protocol, so snippet fixes land in exactly one place and propagate to both the repo detail surface and the Overview tab.
  3. The Overview displays the latest 5–10 artifacts with timestamp and actor, a separate "recent uploads" card scoped by recency window, a sync-status card (populated for PyPI/Helm/RPM/APT; hidden for non-syncable types), a scan-status summary with severity counts and a stale-state "Run scan" CTA, a visibility/policy panel showing the current `public_read` flag plus a v2.0-immutability placeholder row, and a last-modified-actors card linking to the audit log filtered to this repo.
  4. All Overview cards render with Phase 6 skeletons and status badges, and fall back gracefully — via the Phase 6 error envelope — when an underlying data source (scan, sync, audit) returns an error instead of blanking the card silently.
  5. Playwright walk-throughs open one repo of each supported type, confirm the Overview tab is the landing view, and screenshot-verify each card renders its expected content against a seeded fixture.
**Plans**: TBD
**UI hint**: yes

### Progress

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 6. Error Envelope & Visual Foundation | 0/8 | Not started | — |
| 7. Client Snippets & Empty States | 0/0 | Not started | — |
| 8. Favorites, Saved Filters & Recents | 0/0 | Not started | — |
| 9. Health & Status Dashboard | 0/0 | Not started | — |
| 10. Repository Overview Pages | 0/0 | Not started | — |

## Backlog

_Forward-looking ideas not yet scheduled into a milestone live in
`NEXT-SESSION-ISSUES.md` at the repo root._ Current entries (carried from v1.0
closing audit):

- Docker shared-blob storage overestimate — revisit when billing/quota work begins.
- DEB `resolveDebPoolPath` assumes standard Debian pool layout; exotic layouts may 404.
- Codex rescue pass across the 2026-04-17 shipping batch (S3 bucket REST, admin GC status, UI rewrite).
