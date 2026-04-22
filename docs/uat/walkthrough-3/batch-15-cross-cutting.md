# Batch 15 — Cross-cutting: search, dashboard, API docs, error envelopes, a11y, console cleanliness

**Status:** ⬜ Not started
**Prereqs:** Batches 01–14 ✅ — this is the final sweep and requires a realistic data set.

## Pre-flight

- [ ] All prior batches' state intact (users, projects, repos, artifacts, syncs, trash items, audit entries)
- [ ] Logged in as superadmin (for full dashboard visibility)
- [ ] Server log tail open

## Dashboard test cases

### 15.1 Dashboard loads
- [ ] `/` → shows all cards: Projects, Repos, Artifacts, Storage, Jobs, High-Sev Findings, DB Health, Audit activity
- [ ] Every count matches reality (cross-check with `/projects` list, `/admin/audit`, etc.)
- [ ] Console + network clean

### 15.2 Deleted items excluded from counts (WALKTHROUGH-2 F-4 regression)
- [ ] Soft-deleted projects/repos NOT counted in dashboard tiles
- [ ] After restoring an item, counts update

### 15.3 Storage drill-down modal
- [ ] Click Storage card → StorageDetailModal
- [ ] **Expected (WALKTHROUGH-2 recovery):** per-project/repo/bucket breakdown, sorted; totals reconcile
- [ ] Console clean

### 15.4 Jobs card
- [ ] Trigger a mirror sync; dashboard Jobs card updates live (or on poll)
- [ ] Completed jobs archived / moved to history

### 15.5 High-Sev Findings card
- [ ] Shows deduped CVE entries (WALKTHROUGH-2 F-3)
- [ ] Occurrence badge reflects count
- [ ] Click → search with severity=high pre-filter

### 15.6 DB Health card
- [ ] Last-check timestamp, status indicator
- [ ] If the DBHEALTH spec exists (it does, per Batch 14), the card is in sync with `/admin/db/health`

## Search test cases

### 15.7 Global search UI
- [ ] `/search` renders search box, result tabs (All / Repos / Artifacts / CVEs)
- [ ] Empty query state suggests popular terms or leaves blank — document
- [ ] Console clean

### 15.8 Freeform query
- [ ] Search `requests` (from PyPI in Batch 07)
- [ ] **Expected:** mixed results — pypi artifact + CVEs for requests
- [ ] Counts match FTS5 expectations

### 15.9 Kind filter
- [ ] Click Repos / Artifacts / CVEs buttons
- [ ] **Expected:** narrows results to that kind

### 15.10 Severity filter (WALKTHROUGH-2 F-13 regression)
- [ ] Set severity=high
- [ ] **Expected:** only HIGH CVE results; case-insensitive backend predicate holds
- [ ] Deep-link `/search?q=requests&kind=cve&severity=high` preloads chips + triggers correct call

### 15.11 Project-scoped search
- [ ] Search `acme` → results show acme repos and artifacts
- [ ] Results for `closed` project exclude trashed items (or label them — document)

### 15.12 Performance smoke
- [ ] With current data volume, search response is subjectively snappy (<500 ms)
- [ ] Backend log shows no slow-query warnings

## API docs test cases

### 15.13 Swagger UI loads
- [ ] `/api/docs` → redirects to `/swagger/index.html` (or renders inline)
- [ ] UI loads, no CDN fetches (air-gap)
- [ ] Console clean

### 15.14 OpenAPI spec completeness
- [ ] `/api/v1/openapi.yaml` downloads
- [ ] Spot-check: endpoints from every major area (auth, admin, projects, repos, mirrors, scans, s3-buckets, upstream-creds, git-refs) documented with types
- [ ] GitRef schema matches the backend shape (`sha` not `target`, short ref names — F-9 + F-10 regression)

### 15.15 Try-it-out (Swagger)
- [ ] From Swagger UI, execute `/api/v1/me` with current session
- [ ] **Expected:** response visible; works without leaving the app
- [ ] If auth is challenging for Swagger, document the expected flow (cookie vs API key)

## Error envelope test cases

### 15.16 Dev error routes
- [ ] If the build was compiled with `OMNIREPO_DEV=1` and `VITE_OMNIREPO_DEV=true`, `/_dev/error-class-story` exists; walk through each envelope class (validation / auth / rate / transient / permanent / conflict)
- [ ] **Expected:** UI `ErrorEnvelopeRenderer` renders each correctly with incident ID, class, code, field details
- [ ] In a production build, these routes should be tree-shaken / 404 — verify by building without the dev flags

### 15.17 Real 4xx/5xx envelopes
- [ ] Trigger a 409 (create duplicate user), 400 (invalid input), 403 (forbidden page), 404 (missing repo), 429 (rate limit on db health), 503 (git mirror.not_yet_synced)
- [ ] **Expected:** each produces a structured envelope; UI displays class + code + optional incident ID + field-level details
- [ ] No raw stack traces leak to the UI

### 15.18 Incident ID is UUIDv7 and unique
- [ ] Capture 5 incident IDs from different envelopes
- [ ] **Expected:** all valid UUIDv7, all distinct, all correlate with audit/log rows on the server

## Accessibility test cases

### 15.19 Axe scan on core pages
- [ ] Run `npx playwright test a11y-audit` (or the existing axe spec in `web/e2e/a11y-audit.spec.ts`)
- [ ] **Expected:** no critical/serious violations on dashboard, projects list, project detail, repo detail (each protocol), profile, admin/users, admin/audit
- [ ] `make lint-axe-devdep` green

### 15.20 Keyboard navigation
- [ ] Tab through the dashboard and the CreateRepoDialog
- [ ] **Expected:** every interactive element reachable; focus ring visible; no keyboard traps
- [ ] Escape closes dialogs

### 15.21 Contrast / typography / spacing lints
- [ ] `make check-contrast && make lint-typography && make lint-spacing-carveout`
- [ ] All green

## Responsive design

### 15.22 Breakpoints
- [ ] Use Playwright `browser_resize` to 768, 1024, 1280, 1920 widths
- [ ] **Expected:** layouts adapt; sidebar collapses on narrow widths; tables allow horizontal scroll; no overflowing content
- [ ] Existing `web/e2e/responsive.spec.ts` still green

## Console + network final sweep

### 15.23 Visit every page with console capture
- [ ] Dashboard, Projects, Projects/:name (all tabs), each repo type detail page, scan report, search, profile (all tabs), admin/users, admin/audit, admin/trash, admin/gc, admin/tls, admin/trivy, admin/maintenance, Swagger UI
- [ ] After each: `browser_console_messages` → zero errors/warnings
- [ ] After each: `browser_network_requests` → zero 5xx; no unexpected 4xx; no outbound (non-localhost) traffic

### 15.24 Airgap CI gate
- [ ] `make test-airgap` → green
- [ ] Any network call during normal flows is a blocker finding

### 15.25 Error-log tail check
- [ ] `grep -E '(ERROR|panic|FATAL|level=error)' $OMNIREPO_DATA_ROOT/server.log | wc -l`
- [ ] **Expected:** 0 (or only lines produced by intentional negative cases that happen to log at ERROR — which is itself likely a finding, since handled errors should log WARN or lower)

## Full-suite green

### 15.26 Automated suites
- [ ] `make test` → green
- [ ] `make e2e` → green (all Playwright specs, not just the new ones)
- [ ] `make conformance-all` → green

## Findings

_(F-15.N)_

## Sign-off

- [ ] All cases passed
- [ ] All F-15.* closed
- [ ] `make test`, `make e2e`, `make test-airgap`, `make conformance-all` all green
- [ ] Codex final pass on full branch diff since start of walkthrough-3
- [ ] README.md batch 15 status flipped to ✅
- [ ] Release gate in README.md fully green ✅
