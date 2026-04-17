# Phase 7: Snippet Polish, Dashboard Cards & Empty States - Context

**Gathered:** 2026-04-17
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 7 delivers the v1.1 shipping polish across four tracks:

1. **Snippet audit + completion** — `web/src/lib/snippets.ts` per-protocol generators
   and `SnippetPanel.tsx` UI get correctness and completeness fixes, plus **one
   backend feature expansion**: OCI→traditional Helm chart mirroring so native
   `helm push oci://…` surfaces in `helm repo add` + `index.yaml`.
2. **Dashboard composition cards** — additive cards on the existing
   `DashboardPage.tsx` using a mix of already-shipped v1.0 endpoints and
   **new read-only admin endpoints** (scope relaxed — see D-07). 3 user-visible
   cards + 3 admin-only cards, rendered via Phase 6 primitives
   (StatusBadge + SkeletonCard).
3. **Shared EmptyState component** covering EMPTY-01..06 and EMPTY-08
   (EMPTY-07 deferred to v1.2 with FAV). Replaces ad-hoc inline empty-state
   strings across DashboardPage / ProjectsPage / SearchPage / ProjectDetailPage,
   plus new call sites for previously-blank surfaces.
4. **Walkthrough micro-fixes** — three concrete items from NEXT-SESSION-ISSUES.md
   folded in as atomic commits with tests.

The v1.1 "tight polish milestone" framing is preserved — this phase does NOT
spawn HEALTH / FAV / OVERVIEW work (those remain deferred to v1.2). It DOES
spend scope on two bounded protocol-surface enhancements (Helm mirror +
read-only dashboard endpoints) because both deliver compound UX value without
reshaping v1.0 semantics.

</domain>

<decisions>
## Implementation Decisions

### Snippet placeholder conventions

- **S-01:** APT snippet emits BOTH signing-key variants side by side — modern
  (`signed-by=/etc/apt/keyrings/omnirepo-<repo>.asc`) and legacy
  (`/etc/apt/trusted.gpg.d/omnirepo-<repo>.asc`). Each is a distinct labeled
  snippet inside the Sheet so Debian 12+/Ubuntu 22.04+ users paste the modern
  form and older hosts paste the legacy form. Neither form is "primary" —
  both are valid.
- **S-02:** APT `deb` line keeps the literal `stable main` from v1.0 (not
  `<suite> <component>` placeholders). Preserves copy-paste-and-run
  ergonomics for the 90% single-suite case. **Note:** this deviates from
  ROADMAP success criterion 1's verbatim "suite + component placeholders"
  phrasing — downstream acceptance test must assert the literal `stable main`,
  not placeholder text.
- **S-03:** Helm snippet emits BOTH traditional and OCI flows in the Sheet:
  - Traditional: `helm repo add <name> https://<host>/<project>/helm/<repo>/`
    then `helm pull <name>/<chart>`
  - OCI: `helm push chart.tgz oci://<host>/<project>/helm/<repo>` then
    `helm pull oci://<host>/<project>/helm/<repo>/<chart>:<version>`
- **S-03b:** **Scope expansion (backend):** add a forward-only mirror so OCI
  pushes land in the traditional index.yaml. On OCI `manifestPut` where the
  manifest's referenced `config` mediaType == `application/vnd.cncf.helm.config.v1+json`,
  extract the chart `.tgz` layer, write it to
  `/var/lib/omnirepo/helm/<project>/<repo>/charts/<chart>-<version>.tgz`,
  and trigger `helm.regen.IndexRebuild(project, repo)`. **Reverse direction
  (traditional PUT → OCI manifest synthesis) is deferred to v1.2.** Acceptance
  test: push via `helm push oci://…`, then `helm repo add` + `helm search repo`
  must list the chart. `manifestMediaType(r)` at
  `internal/protocol/oci/manifests.go:111` already honors client
  Content-Type verbatim — no mediaType whitelist fix is needed.
- **S-04:** S3 snippet surfaces explicit `<region>` placeholder +
  leading comment `# Access key & secret: create one at /profile → S3 Keys`.
  Does NOT hardcode `us-east-1`; does NOT expand into a full `~/.aws/config`
  stanza.
- **S-05:** Git snippet emits TWO labeled snippets: "Clone" (bare HTTPS URL)
  and "Authenticate" (`git config credential.helper store` form OR
  `git -c http.extraHeader='Authorization: Bearer <api-key>'` — planner
  picks the simpler of the two after verifying against our auth middleware).
  NO inline userinfo URLs (`https://user:key@host/…`) — credential leakage.
- **S-06:** RAW snippet surfaces `-u <user>:<api-key>` on BOTH upload and
  download with leading comment. Symmetric form works for both public and
  private repos without the user having to discover the auth requirement
  on a 401.
- **S-07:** PyPI snippet adds a `.pypirc` block alongside existing
  `pip install --index-url` and `twine upload` commands. Satisfies REQ-02
  wording verbatim. Shape: `[omnirepo]\nrepository = https://<host>/<project>/pypi/<repo>/legacy/\nusername = <user>\npassword = <api-key>`.
- **S-08:** Docker snippet (login/pull/push) ships as-is — no correctness
  gaps found against SNIPPET-01.
- **S-09:** RPM snippet (dnf `.repo` stanza with `gpgcheck=1`) ships as-is —
  no correctness gaps found against SNIPPET-04.
- **S-10:** SNIPPET-09 (one-click copy with visible confirmation) is
  satisfied by Phase 6's `CopyButton` (aria-live polite + "Copied to
  clipboard" sr-only). New Playwright spec asserts clipboard write +
  aria-live content on a snippet copy — no component change required.
- **S-11:** SnippetPanel container stays as a shadcn Sheet (slide-out right).
  No Phase 6 primitive supersedes it; EMPTY-03's inline-snippet variant is a
  separate EmptyState children-slot render, not a replacement for the Sheet.

### Dashboard composition cards

- **D-01:** New **Composition row** inserted at row 2 of `DashboardPage.tsx`,
  ABOVE the existing Storage section. 3-col grid on xl, 2-col on md/1366×768,
  1-col on sm. Admin-only cards conditionally render after user-visible cards
  in DOM order. Cold-load uses `SkeletonCard` (Phase 6 primitive).
- **D-02:** Thresholds are hard-coded defaults, overridable via existing
  `settings` table rows. Defaults:
  - **Storage:** `healthy <70%`, `warning 70–90%`, `failure >90%` (of
    `storage_total_bytes`)
  - **TLS cert:** `warning <30 days`, `failure <14 days` to `NotAfter`
  - **Trivy DB:** `warning >7 days`, `failure >30 days` since last update
- **D-03:** Scan Findings Trend card compares counts by counting `scan.*`
  audit events in the last 7 days versus days 7–14. Uses existing
  `/api/v1/admin/audit` paginated endpoint (or embeds summary in dashboard
  response for non-admins — planner picks). Does NOT require a `first_seen_at`
  column migration on `vulnerabilities` (that remains v1.2 work).
- **D-04:** Final additive card inventory (6 total):
  - **C-1 Storage Status (user-visible)** — used/total bytes + % + top-3
    repos + StatusBadge (healthy/warning/failure). Data: existing
    `/api/v1/dashboard` + `/api/v1/dashboard/storage`.
  - **C-2 Recent Failures (user-visible)** — count + latest 3 audit events
    whose `event_kind` ends in `.failed` or contains `error` in last 24h.
    Data: `dashboard.recent_activity[]`, already per-actor scoped.
    StatusBadge: `healthy` if 0, `warning` if 1–5, `failure` if >5.
  - **C-3 Scan Findings Trend (user-visible)** — current
    CRITICAL/HIGH counts + 7d delta via audit `scan.*` events. StatusBadge:
    healthy if 0 critical, warning if 1–5, failure if >5. Data: existing
    `/dashboard.scan_findings` + audit events query.
  - **C-4 Background Jobs (admin-only)** — running / queued / failed_last_24h
    / last_completed_at / last_failed_at. Data: **new endpoint**
    `GET /api/v1/admin/jobs/summary` (super-admin gate = `ActionTriggerGC`).
  - **C-5 TLS Cert Expiry (admin-only)** — days remaining on the currently
    active cert with threshold-driven StatusBadge. Data: existing
    `GET /api/v1/admin/tls/current`.
  - **C-6 Trivy DB Freshness (admin-only)** — age of the Trivy DB with
    threshold-driven StatusBadge. Data: existing
    `GET /api/v1/admin/trivy/db/status`.
- **D-05:** The ad-hoc "no recent activity" / "no storage data" /
  "no high-severity findings — looking good!" inline strings in
  `DashboardPage.tsx` migrate to the new `EmptyState` component. The
  positive-framing "looking good!" message becomes an inline `StatusBadge`
  variant=healthy treatment instead of an empty-state.
- **D-06:** Jobs summary endpoint shape (MANDATORY — locked):
  ```
  GET /api/v1/admin/jobs/summary  (super-admin only)
  {
    "running": int,
    "queued": int,
    "failed_last_24h": int,
    "last_completed_at": "RFC3339 | null",
    "last_failed_at":    "RFC3339 | null"
  }
  ```
  ~30 LOC reading `internal/jobs` pool state. One handler test asserting
  shape + auth gate.
- **D-07:** **ROADMAP success criterion #2 relaxed.** The "Zero new
  `/api/v1/admin/health/*` routes; all endpoints must already be shipped
  in v1.0" constraint is rewritten to: "No new routes under
  `/api/v1/admin/health/*` (those belong to the deferred v1.2 Health page).
  New read-only admin endpoints are permitted when they deliver first-glance
  dashboard value and can be shipped without schema changes." The jobs
  summary endpoint (D-06) is permitted under this. ROADMAP.md must be
  edited during planning to reflect this.

### EmptyState component

- **E-01:** Component lives at `web/src/components/common/EmptyState.tsx`.
  Props:
  ```typescript
  interface EmptyStateProps {
    icon?: LucideIcon;
    title: string;
    description?: string;
    primaryCTA?: { label: string; to?: string; onClick?: () => void; disabled?: boolean };
    children?: React.ReactNode;  // for EMPTY-03 inline-snippet variant only
  }
  ```
  Single primary CTA (no secondary). `children` exists ONLY to serve
  EMPTY-03's inline-snippet requirement — all other sites use the props API.
- **E-02:** Layout is borderless, centered, muted-foreground. Icon size-12
  in muted-foreground; title `text-lg font-semibold`; description
  `text-sm text-muted-foreground`; CTA rendered via shadcn `Button`. No
  `Card` wrapper — callers wrap in Card/CardContent if the surface requires
  it. Padding `py-12 px-6`.
- **E-03:** EMPTY-03 (zero-artifacts repo) renders
  `<EmptyState icon={Terminal} title="No artifacts yet" description="Upload your first artifact using the snippet below.">` plus the FULL per-protocol snippet list
  (label + `<pre>` + `CopyButton`) inlined via the `children` slot. The
  component reuses the body-rendering logic from `SnippetPanel.tsx` (extract
  the `<ScrollArea>…</ScrollArea>` content into a shared `SnippetList`
  sub-component so both the Sheet and the EmptyState consume it).
- **E-04:** **EMPTY-07 is deferred to v1.2** alongside FAV-01..07. v1.1 does
  not ship an empty state for "no saved filters/favorites/recents" because
  none of those surfaces exist yet. `REQUIREMENTS.md` must be edited during
  planning to move EMPTY-07 to the "Deferred to v1.2" section.
- **E-05:** EMPTY-04 (never-scanned repo) primary CTA `Run first scan`
  POSTs to the existing per-repo scan-trigger endpoint (planner verifies
  exact path; likely `POST /api/v1/projects/<p>/<type>/<r>/scan`). Renders
  success/failure envelope inline via the Phase 6 `ErrorEnvelopeRenderer`.
  Users without scan permission see a disabled button with tooltip
  "Requires maintainer role on this repo" — pulled from the actor's
  project-role resolution.
- **E-06:** Three ad-hoc DashboardPage strings migrate to EmptyState
  instances (per D-05). Exception: the scan-findings "looking good!"
  positive-signal switches to a compact `StatusBadge variant="healthy"`
  inline treatment instead of an empty-state, because "nothing here" is the
  wrong semantic framing for "zero vulnerabilities is the goal state."
- **E-07:** EMPTY-08 (no-results search) shows a static 3-example list as
  clickable chips that pre-fill the search input:
  - `openssl` (package name)
  - `CVE-2024-` (CVE ID prefix)
  - `myorg/docker/alpine` (full path)
  Plus a "Clear filters" secondary action (exception to the E-01 single-CTA
  rule because the ONE valid action on no-results search is literally
  clearing filters — if the primary slot is a filter-clear, the examples
  need another surface. Planner picks: examples as clickable chips in
  description region, single primary CTA = "Clear filters").
- **E-08:** Every EmptyState instance renders with
  `data-testid="empty-state"`, `role="status"`, and `aria-label={title}`.
  Playwright assertion helper:
  ```typescript
  async function assertEmptyState(page, title, ctaLabel?) {
    const es = page.getByTestId('empty-state');
    await expect(es).toBeVisible();
    await expect(es.getByRole('status')).toContainText(title);
    if (ctaLabel) await expect(es.getByRole('button', { name: ctaLabel })).toBeEnabled();
  }
  ```

### Walkthrough micro-fixes

- **W-01:** Codex rescue pass across the S3 + admin shipping batch from
  2026-04-17: `internal/api/s3_buckets.go`, `internal/api/admin_gc.go`,
  `web/src/pages/repo/S3BucketPage.tsx`, plus the Phase 5 dashboard
  extensions. Invoke via
  `Agent(subagent_type="codex:codex-rescue", ...)` with 15-minute hard
  time-box. Apply any blocker/real-issue findings as atomic commits; ignore
  minor/noise.
- **W-02:** Docker storage overestimate — rewrite `repoSizeExpr` in
  `internal/api/dashboard.go` to ref-count shared blobs by dividing each
  blob's `size_bytes` by the distinct-repo count of manifests referencing
  it. Target: accurate per-repo byte totals when a blob is shared across
  projects. ~80 LOC SQL rewrite plus test updates for dashboard tests that
  currently assert the over-counted totals. Breaking change for any
  external tooling reading `/dashboard.storage_used_bytes` expecting
  over-counted values (likely none).
- **W-03:** DEB pool-path reconstruction — replace the "infer from
  filename" heuristic in `internal/protocol/deb/resolveDebPoolPath` with
  logic that reads `dists/<suite>/Release` and honors whatever
  pool-layout hints it carries. Falls back to current filename-based
  inference when the Release file is silent. Ships with one integration
  test using a fixture repo that uses a non-standard pool layout.

### Claude's Discretion

- Card-ordering within the new Composition row (which of Storage / Failures
  / Scan Trend sits leftmost) — visual design call during implementation.
- Exact SQL shape of the audit-events-for-failures query on C-2 — backend
  detail; planner selects based on existing query conventions in
  `internal/api/dashboard.go`.
- Which of the two Git auth forms (credential.helper vs http.extraHeader)
  lands in S-05 — planner picks after verifying both work against our
  `BasicOrAPIKey` middleware in `internal/auth/`.
- Exact file split between `SnippetPanel.tsx` and a new `SnippetList`
  sub-component (per E-03) — refactor shape the planner decides; the
  invariant is that the Sheet and the EmptyState render the same list body.
- Whether the dashboard thresholds use one aggregated settings key
  (`dashboard_thresholds` as a JSON blob) or six individual keys — shipping
  either way is fine; planner picks based on `internal/config` patterns.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Phase 7 scope source
- `.planning/ROADMAP.md` — Phase 7 goal + success criteria (NOTE: SC #2
  needs editing per D-07; SC #1 deviation acknowledged per S-02)
- `.planning/REQUIREMENTS.md` — SNIPPET-01..09, EMPTY-01..08 (NOTE:
  EMPTY-07 needs moving to "Deferred to v1.2" per E-04)
- `.planning/STATE.md` — project state + accumulated Phase 6 decisions

### Phase 6 foundation (must honor)
- `.planning/phases/06-error-envelope-visual-foundation/06-UI-SPEC.md` —
  status tokens (`--status-*`), StatusBadge API + variants, Skeleton*
  primitives, CopyButton aria-live contract, spacing carve-out list,
  typography allowlist
- `.planning/phases/06-error-envelope-visual-foundation/06-SUMMARY.md`
  files — what each Phase 6 plan actually shipped

### Snippet scope — current code
- `web/src/lib/snippets.ts` — `getSnippets(type, project, repo, host)` per-protocol generator
- `web/src/components/common/SnippetPanel.tsx` — Sheet UI + CopyButton wiring
- `web/src/components/common/CopyButton.tsx` — aria-live + clipboard API
- `web/src/api/types.ts` — `RepoType` union

### Helm OCI mirror — backend surface
- `internal/protocol/oci/handler.go:181-227` — `/v2/` route mount + URL parser
- `internal/protocol/oci/manifests.go:102-281` — `manifestPut` + `manifestMediaType`
- `internal/protocol/oci/mediatype.go` — existing mediaType constants
- `internal/protocol/helm/handler.go:104-148` — traditional `/<project>/helm/<repo>/` routes
- `internal/protocol/helm/put.go` — chart `.tgz` PUT handler + storage layout
- `internal/protocol/helm/regen.go` — `IndexRebuild` (idempotent index.yaml regen)

### Dashboard cards — backend surface
- `internal/api/dashboard.go` — `handleDashboard` + `handleStorageDetail` +
  `repoSizeExpr` (W-02 target)
- `internal/api/admin_trivy.go:43` — Trivy DB status handler (C-6)
- `internal/api/admin_tls_history.go:28` — TLS current/history handlers (C-5)
- `internal/api/admin_audit.go` — audit log handler (C-2, C-3)
- `internal/api/admin_gc.go:48` — GC status handler (pattern reference for C-4)
- `internal/auth/actions.go` — `ActionTriggerGC` super-admin gate
  (reuse for `/admin/jobs/summary`)
- `internal/jobs/` — pool state source for C-4 Jobs card

### Dashboard cards — frontend surface
- `web/src/pages/DashboardPage.tsx` — target for row-2 insert + ad-hoc
  string migration (D-05)
- `web/src/api/queries.ts:198` — existing TanStack query for
  `/dashboard` + `/dashboard/storage`; add queries for new endpoints
- `web/src/components/common/StatusBadge.tsx` — Phase 6 primitive (C-*)
- `web/src/components/common/SkeletonCard.tsx` — Phase 6 primitive (cold load)

### EmptyState — existing call sites (replacement targets)
- `web/src/pages/ProjectsPage.tsx:158` — "No projects yet" → EMPTY-01
  (generalized to cover zero-projects too)
- `web/src/pages/ProjectDetailPage.tsx:284` — "No members." → EMPTY-02
- `web/src/pages/ProjectDetailPage.tsx:341` — "No activity yet." → keep
  as inline paragraph (not an EMPTY REQ target)
- `web/src/pages/DashboardPage.tsx:280,303,402` — 3 ad-hoc strings (D-05)
- `web/src/pages/SearchPage.tsx:226` — "No results found" → EMPTY-08
- `web/src/pages/admin/TLSPage.tsx:186` — "No certificate information
  available." → EMPTY-05
- `web/src/pages/admin/TrashPage.tsx` (confirm path) — empty trash → EMPTY-06

### Test gates inherited from Phase 6 (MUST remain green)
- `make test` — full test + all lint gates
- `lint-protocol-redaction`, `check-contrast`, `lint-typography`,
  `lint-spacing-carveout`, `lint-axe-devdep` — all Phase 6 lint gates
- Playwright specs at 1366×768 across admin routes + axe-core a11y audit
- `@axe-core/playwright` stays in devDependencies only (MPL license invariant)

### Walkthrough micro-fix sources
- `NEXT-SESSION-ISSUES.md` — carries W-01, W-02, W-03 wording verbatim
- `internal/protocol/deb/resolveDebPoolPath` (location TBD by grep) — W-03 target
- Phase 5 dashboard commits (S3 + admin batch) — W-01 scope

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- **Phase 6 primitives** — StatusBadge (6 variants × 2 sizes),
  SkeletonCard/Metric/Table/Detail, CopyButton (aria-live), CopyInline,
  ErrorEnvelope renderer, useApiError hook. New dashboard cards and
  EmptyState consume these directly.
- **SnippetPanel body** — renderable content (snippet list + CopyButton per
  line) can be lifted into a shared `SnippetList` sub-component so the Sheet
  and the EMPTY-03 EmptyState consume the same body.
- **DataTable `emptyMessage` prop** — already exists at `web/src/components/common/DataTable.tsx`.
  Some call sites (ProfilePage API keys + S3 keys) use it. Phase 7 should
  decide whether DataTable's empty message path also migrates to EmptyState
  or stays as a simpler inline string (likely stays — empty tables rarely
  need a CTA).
- **`repoSizeExpr` pattern** — dashboard's SQL fragment for per-repo byte
  totals is the pattern Phase 7 inherits. W-02 rewrites it in place.

### Established Patterns

- **Dashboard scoping** — `visibleProjectIDs(ctx, Members, actor)` gives
  per-actor project ID list (super-admin, user, scoped-API-key all handled).
  Every new dashboard-adjacent query must use this pattern; no card surfaces
  cross-project data to a non-member actor.
- **Admin endpoint auth gate** — `RequireCan(ActionTriggerGC)` is the
  super-admin gate used by `/admin/trivy`, `/admin/tls`, `/admin/gc`,
  `/admin/audit`, `/admin/settings`. The new `/admin/jobs/summary`
  endpoint (D-06) reuses this exact gate.
- **Dev-only routes gate** — `DEV_ROUTES_ENABLED` + `OMNIREPO_DEV=1` +
  `VITE_OMNIREPO_DEV=true` tri-flag opt-in (Phase 6 pattern). Phase 7 adds
  no dev-only surfaces — story pages already cover the primitive set.
- **Playwright at 1366×768** — Phase 6 established the responsive gate
  viewport. New Phase 7 specs use the same viewport.
- **Status-token usage** — status-badged cards and EmptyStates reference
  `bg-status-<variant>` / `text-status-<variant>-foreground` /
  `border-status-<variant>-border` utilities ONLY; raw Tailwind palette
  in new Phase 7 code fails `lint-typography` (enforced).

### Integration Points

- **Helm OCI mirror (S-03b)** — hook point: `internal/protocol/oci/manifests.go:handleManifestPut`
  (lookup `manifestMediaType()` site). Detect Helm config mediaType by
  parsing the manifest body for `config.mediaType`, then call a new
  `helm.MirrorToTraditional(ctx, project, repo, chartTgzReader)` function
  that writes to `/var/lib/omnirepo/helm/<project>/<repo>/charts/` and
  calls `helm.IndexRebuild`.
- **Dashboard Composition row (D-01)** — insertion point:
  `DashboardPage.tsx` between existing Row 1 (metric tiles grid, line ~107)
  and Row 2 (Storage section, line ~208). Skeleton-on-cold-load block
  (line 67-90) also grows to match new row.
- **EmptyState wiring** — 7 call sites changed in Phase 7:
  ProjectsPage:158, ProjectDetailPage:284 (members), DashboardPage ×3
  (activity/storage/high-sev), SearchPage:226, TLSPage:186; plus ≥2
  new call sites for previously-blank surfaces (EMPTY-03 zero-artifacts
  repo pages ×7-per-protocol, EMPTY-04 never-scanned surface, EMPTY-06
  empty-trash).
- **ROADMAP + REQUIREMENTS edits** — during planning, two doc edits land:
  (a) ROADMAP.md Phase 7 SC #2 rewrite per D-07; (b) REQUIREMENTS.md
  EMPTY-07 move to "Deferred to v1.2" section per E-04.

</code_context>

<specifics>
## Specific Ideas

- **Helm OCI mirror is the hinge.** User explicitly stated: "We need both
  traditional and helm push and we need them to work so the cross path
  consistency needs to be fixed... if I use helm to list the repo, list the
  manifest in repo via plugin I need to still see them and it needs to
  regenerate and everything. So this is something we need to do probably as
  a first." — this elevates S-03b to a Phase 7 tentpole, potentially the
  first plan to land.
- **Dashboard endpoint latitude.** User rejected the "zero new endpoints"
  framing: "We don't have to force no new endpoints policy because most of
  these endpoints will be just reading information from database, not
  writing anything... I'm open to adding a new endpoints as necessary for
  better UI experience for that dashboard." → D-07 formalizes this.
- **Tight polish mandate preserved.** Even with the two scope expansions
  (Helm mirror + dashboard endpoints), Phase 7 remains a polish phase —
  NO new schema migrations, NO new user-visible pages beyond what the
  existing DashboardPage grows into, NO new auth surfaces, NO new protocol
  handlers.

</specifics>

<deferred>
## Deferred Ideas

### To v1.2 (new)

- **Bidirectional Helm OCI↔traditional symmetry** — reverse direction
  (traditional PUT → synthesize OCI manifest + blobs) deferred to v1.2.
  Asymmetric v1.1 ship: OCI-pushed charts appear in `index.yaml`; charts
  uploaded via traditional PUT do NOT appear under `oci://…`. Document in
  Phase 7 plan commit messages + README.

- **Storage-growth snapshots** — server-side daily snapshot table for
  real 7d/30d storage-delta tracking. Dashboard Storage Status card ships
  with static "% used" framing in v1.1; true growth framing arrives in v1.2.

- **first_seen_at on vulnerabilities** — column + backfill + write-path
  change to enable true "new CRITICAL/HIGH CVEs this week" count. v1.1
  Scan Findings Trend uses audit-event-count as a proxy (D-03).

- **Jobs page** — dedicated admin page with paginated job history and
  filters. v1.1 exposes only the summary endpoint (D-06). Full jobs page
  is v1.2 work alongside HEALTH.

### Carried forward from v1.0 (still deferred)

- DEB pool-path inference for exotic layouts — partially addressed by W-03
  (reads Release file where available) but the fully-general "any repo
  layout" solution still deferred.
- Docker storage per-repo attribution precision — W-02 ref-counts blobs
  (no longer over-counts), but does not yet split shared blob bytes across
  referencing repos. Billing-grade attribution remains v2.0.

### Reviewed Todos

None — `todo match-phase 7` returned zero matches.

</deferred>

---

*Phase: 07-snippet-polish-dashboard-cards-empty-states*
*Context gathered: 2026-04-17 via /gsd-discuss-phase*
