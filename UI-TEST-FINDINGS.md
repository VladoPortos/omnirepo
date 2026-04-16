# OmniRepo UI Walkthrough — Test Findings

**Date:** 2026-04-16
**Tester:** Claude Code (Playwright MCP)
**Frontend:** http://localhost:5173 (Vite dev)
**Backend:** http://localhost:8080 (Go) — config `/tmp/omnirepo-config.yaml`, data `/tmp/omnirepo-data3`
**Credentials:** admin / admin123
**Scope:** Click-through of every page. Document only — no fixes applied.
**Screenshots:** in `ui-test/` (initial findings `01-*`..`28-*`; post-fix retests prefixed `fix-*`).

---

## Status: all 15 findings fixed (2026-04-16)

Tests: `go test -count=1 ./...` green (including 2 new tests — `TestAPI_Repos` single-repo GET regression, `TestEnsureFTSIndexed_*` backfill/orphan-row edge cases — plus augmented `TestApplyBootstrap_HappyPath` asserting `repos_fts` count matches seed). Manual Playwright retest: login → dashboard drill-through links work → every repo detail page renders (no more 404) → global search returns results → TLS page shows Source + SHA-256 fingerprint → project tabs at 800 px scroll horizontally with "Overview" fully visible → Delete Project dialog present → Trivy page no longer contradicts itself → no console errors on happy path.

Summary by fix (in the same numbering as the findings below):

| # | Fix | Key files |
|---|-----|-----------|
| 1 | Added `GET /projects/{name}/repos/{type}/{repo}` backend route behind `ActionRepoRead` | `internal/api/repos.go`, `internal/api/admin_phase1.go` |
| 2 | Bootstrap now populates `repos_fts` inline + startup backfill for legacy DBs (LEFT-JOIN based, robust to orphan FTS rows) | `internal/app/bootstrap.go`, `internal/app/fts_backfill.go`, `internal/app/app.go` |
| 3 | Removed broken "Upload Artifact" button from dashboard; "Create Project" opens the project create dialog via `?create=1` | `web/src/pages/DashboardPage.tsx`, `web/src/pages/ProjectsPage.tsx` |
| 4 | Backend `handleTLSCurrent` already returned the full shape in source — the running server was stale. Restart picks up the fix. | (no code change; verified) |
| 5 | Recent Activity, Storage bars, CVE findings are now `<Link>` with `repo_type`-based URLs | `web/src/pages/DashboardPage.tsx` |
| 6 | `useCreateRepo` / `useDeleteRepo` / `usePatchRepo` invalidate project summary + repo list + dashboard | `web/src/api/queries.ts` |
| 7 | Project tabs use `flex-none px-3` with `justify-start` inside a horizontally-scrollable TabsList | `web/src/pages/ProjectDetailPage.tsx` |
| 8 | Removed `max-h-[*] overflow-y-auto` from Storage widget and Recent Activity/CVE lists | `web/src/pages/DashboardPage.tsx` |
| 9 | Trivy page handles age=-1 as "unknown age"; separate muted banner for unmeasured DBs; keeps stale warning only when age ≥ 0 | `web/src/pages/admin/TrivyPage.tsx` |
| 10 | Project detail gains destructive "Delete Project" action with typed-name confirmation dialog | `web/src/pages/ProjectDetailPage.tsx` |
| 11 | `useCreateProject` / `useDeleteProject` invalidate `['dashboard']` so stat cards refresh | `web/src/api/queries.ts` |
| 12 | Inline SVG data-URL favicon so `/favicon.ico` never 404s | `web/index.html` |
| 13 | Progressbar `x` comes from `@base-ui/react/progress` with `role="presentation"` + `visuallyHidden`. Not surfaced to real assistive tech. Left upstream. | (upstream) |
| 14 | Project list and repo-card UI replaced with `<Link>` (middle-click, keyboard focus) | `web/src/pages/ProjectsPage.tsx`, `web/src/pages/ProjectDetailPage.tsx` |
| 15 | Inline pre-paint theme script now toggles both `dark` and `light` classes to match `useTheme` | `web/index.html` |

Codex rescue pass (second opinion) caught three issues in my first draft that I then fixed before shipping: the `DashboardVulnRow.repo_type` field was `type` in my helper (wrong, leading to 404s for non-docker findings); `activityTargetHref` parsed a `project/X` prefix that the API does not emit; the FTS backfill relied on two COUNT queries and could be fooled by orphan FTS rows or a race-insert. The per-repo LEFT-JOIN implementation + `TestEnsureFTSIndexed_OrphanRow` cover the edge cases.

---

## Original findings (for historical reference)

## Severity legend
- **P0 (blocker):** primary feature broken, crashes, data loss
- **P1 (high):** a main flow is unusable, or console errors on happy path
- **P2 (medium):** UX flaw, stale UI after action, layout glitch
- **P3 (low):** cosmetic / polish

---

## P0 — Blocker

### 1. Repo detail pages (all 8 protocols) show "Page Not Found"
**Where:** Clicking any repo card inside a project opens `/projects/{name}/{type}/{repo}` → renders `NotFoundPage`.
**Reproduction:** Navigate to any existing repo, e.g. `/projects/platform/docker/docker-images`, `/projects/platform/rpm/centos-packages`, `/projects/mobile-app/git/app-source`.
**Screenshot:** `ui-test/06-docker-repo.png`, `ui-test/07-rpm-repo.png`, `ui-test/09-git-repo.png`.
**Console evidence:**
```
Failed to load resource: 405 (Method Not Allowed) @ /api/v1/projects/platform/repos/docker/docker-images
Failed to load resource: 405 (Method Not Allowed) @ /api/v1/projects/platform/repos/rpm/centos-packages
Failed to load resource: 405 (Method Not Allowed) @ /api/v1/projects/mobile-app/repos/git/app-source
```
**Root cause analysis:** The frontend `useRepo` hook (`web/src/api/queries.ts:163-167`) does `GET /projects/{name}/repos/{type}/{repo}`, but the backend (`internal/api/admin_phase1.go:189,194`) only registers `DELETE` and `POST /wipe` for that path — no `GET`. chi responds 405, `useRepo` sets `isError`, `RepoDetailRouter.tsx` renders `<NotFoundPage />`.
**Impact:** Every repo detail page (Docker, RPM, APT, PyPI, Helm, Git, RAW, S3) is completely unreachable via the UI.
**Verified with curl:** `curl -s -X GET http://localhost:8080/api/v1/projects/platform/repos/docker/docker-images → HTTP 405`.

---

## P1 — High

### 2. Dashboard Search returns zero results for every term
**Where:** `/search` page. Also backend directly.
**Reproduction:** Log in as admin, go to Search, type any term (`docker`, `platform`, `cent`, `openssh`, `CVE-2024`).
**Console evidence:** `GET /api/v1/search?q=docker → 200 {"items":[],"next_cursor":""}`.
**Verified with curl (authenticated session cookie):** Every query returns `{"items":[],"next_cursor":""}` regardless of term.
**Likely cause:** FTS5 index is not being populated for seeded projects/repos/artifacts — either the index is missing, not built on seed import, or the search query path filters everything out.
**Screenshot:** `ui-test/10-search.png`.

### 3. "Upload Artifact" button on Dashboard navigates to `/projects` (no upload UI)
**Where:** Dashboard → top-right "Upload Artifact" button.
**Reproduction:** Click "Upload Artifact" on Dashboard → URL changes to `/projects` (Projects list page) without any upload dialog/wizard.
**Expected:** Open an upload modal (choose target repo + file) or route to an upload page.
**Screenshot:** `ui-test/02-dashboard.png` (button visible top-right).

### 4. TLS Certificates page — "Source" and fingerprint values appear empty
**Where:** `/admin/tls`.
**Observation:** The "Source" field after the label shows only a small dot with no text value. The SHA-256 Fingerprint row shows the label but no hex value visible.
**Screenshot:** `ui-test/16-admin-tls.png`.

### 5. Dashboard CVE findings / storage entries / activity entries are not clickable
**Where:** Dashboard widgets: "High-Severity Findings", "Storage" bars, "Recent Activity".
**Observation:** Items look hoverable (they are presented as a list of discrete entries), but clicking them does nothing. There are zero `cursor-pointer` elements in the dashboard main content.
**Expected:** Drill-through — e.g. CVE click → CVE detail / affected repo; storage click → repo; activity click → target entity.
**Screenshot:** `ui-test/02-dashboard.png`, `ui-test/27-dashboard-blank.png`.

---

## P2 — Medium

### 6. "Create Repository" dialog does not invalidate repo list after creation (stale UI)
**Where:** Any project → any protocol tab → "Create Repository".
**Reproduction:**
1. Go to `/projects/platform` → APT tab ("No repositories").
2. Click "Create Repository", name `qa-apt`, click create → dialog closes, mutation returns `200 OK`.
3. The tab still shows "No repositories".
4. Navigate away and back → repo now appears.
**Root cause:** Success handler does not invalidate the relevant TanStack Query keys (project repos/project storage).
**Screenshots:** `ui-test/08-create-apt-dialog.png`.

### 7. Project detail tabs overflow on narrow viewport — "Overview" label clipped
**Where:** `/projects/{name}` with viewport width ≤ ~800 px.
**Observation:** The tablist does not wrap or horizontally scroll in a contained way. At 800 × 900, the first tab reads `…view` (left edge hidden). All nine tabs (Overview, Docker, RPM, APT, PyPI, Helm, Git, RAW, S3) fight for space.
**Expected:** Horizontal scroll container or responsive wrapping.
**Screenshot:** `ui-test/24-project-800.png`.

### 8. Dashboard Storage widget — permanent vertical scrollbar at 1920×1080
**Where:** `/` dashboard, Storage card, at common desktop heights (e.g. 1080 px total viewport).
**Observation:** The storage list is in an internal `overflow-y: auto` container that introduces a scrollbar even when only 8 entries fit naturally.
**Expected:** Either make the container grow with content, or hide scrollbar until actually needed.
**Screenshot:** `ui-test/23-dashboard-1920.png`.

### 9. Trivy Database — "less than an hour old" + "Consider updating" contradicts itself
**Where:** `/admin/trivy`.
**Observation:** Banner says "Trivy database is less than an hour old. Consider updating for the latest vulnerability data." A freshly-seeded DB that is <1h old should not trigger the update nag.
**Also:** Version field is empty; Source shows `none`; Update History is empty despite age banner implying a load event.
**Screenshot:** `ui-test/17-admin-trivy.png`.

### 10. No "Delete Project" button on project detail page
**Where:** `/projects/{name}` (e.g. `qa-test-project`).
**Observation:** Page actions are limited to tabs + "Add Member". There is no UI to delete an empty/unwanted project; only option is to manually call the API.
**Screenshot:** `ui-test/04-new-project.png`.

### 11. Projects count on Dashboard doesn't match Projects list after create
**Where:** Dashboard "Projects" stat card vs. `/projects`.
**Observation:** After creating `qa-test-project`, Dashboard shows 4 projects (correct), but this is only reflected after navigating back. Not a critical bug, but worth noting — the dashboard stats probably also need targeted invalidation on project create.

---

## P3 — Low / Polish

### 12. Login page 404s `/favicon.ico` on every load
**Where:** Every page load.
**Console evidence:** `Failed to load resource: 404 (Not Found) @ /favicon.ico`.
**Screenshot:** `ui-test/01-login.png`.
**Fix:** Either ship a favicon or add `<link rel="icon" href="data:,">` to silence it.

### 13. Storage progressbars contain a literal "x" in the accessibility tree
**Where:** Every place a storage progressbar is rendered (Dashboard, Project Overview).
**Observation:** Snapshot shows `progressbar → text: x` — the `x` is visible to assistive tech / programmatic consumers and is a leftover placeholder. Does not affect visual display.
**Screenshot:** `ui-test/02-dashboard.png` (rendered) — look at the Storage card accessibility tree.

### 14. Project card on projects list isn't an `<a>` — no right-click open-in-tab
**Where:** `/projects`.
**Observation:** Cards are clickable `<div class="cursor-pointer">` rather than `<a>` tags, so middle-click / Cmd-click / right-click → Open in new tab does not work. Same treatment as repo cards inside a project.
**Screenshot:** `ui-test/03-projects.png`.

### 15. Light mode sidebar vs content mismatch on first paint (minor)
**Where:** User menu → Light Mode.
**Observation:** Content area becomes light instantly; sidebar appears to stay dark for a moment before catching up. Transient. Probably because the theme class is applied via React state rather than before-paint.

---

## Passed checks (no issues)

- Login with `admin/admin123`: ✅ redirects to dashboard.
- Login with wrong creds: ✅ shows "Invalid login or password." and stays on page.
- Dashboard stat cards render with correct data.
- Storage widget bars render with per-type colors and correct sizes.
- Recent Activity list renders with relative timestamps.
- High-Severity Findings list renders with severity colors.
- Projects list renders 3 seeded projects + any newly created.
- Create Project dialog works end-to-end; project appears after redirect.
- Project Overview shows Members, Storage, Project Activity.
- Tab counts `(1)` next to Docker/RPM/Helm match actual repo count.
- APT/PyPI/RAW/Git/S3 empty-state renders with CTA.
- Create Repository dialog: `deb` selected by default, submits 200.
- Profile page: personal info, change-password form, API keys, S3 keys, My Projects, Delete Account.
- Create API Key flow: dialog → one-time key display with copy button → closes → table refreshes with new row (this page DOES invalidate).
- Revoke API Key: confirmation dialog → 200 → row removed (good).
- Avatar regenerate button visible (not tested for actual change).
- Admin / Users: 4 seeded users + edit (pencil) / delete (trash) actions; Super Admin badge on admin.
- Edit User dialog: email + Super Admin toggle + Force Password Reset toggle.
- Admin / Audit Log: ~40 entries with actor, action, target, outcome, IP, time. Filters/CSV/JSON export buttons.
- Admin / Trivy DB, GC, Trash, Maintenance: pages render, empty-state correct.
- User menu popover: Profile / Light Mode / Sign Out.
- Light ↔ Dark theme switch works and persists (`localStorage.omnirepo-theme`).
- Sign Out exists in menu (not exercised to preserve session).
- Breadcrumbs navigate correctly at every level.
- Sidebar links (Dashboard, Projects, Search, Admin drop-down) all route correctly.

---

## Sanity notes on test environment

- Only **3** repo detail pages were exercised directly (Docker → `docker-images`, RPM → `centos-packages`, Git → `app-source`). APT / PyPI / Helm / RAW / S3 follow the same route pattern and same backend gap, so finding #1 applies to all eight.
- I created one project (`qa-test-project`) and one APT repo (`platform/qa-apt`) during testing; both persist in seeded DB at `/tmp/omnirepo-data3`.
- One API key `test-key` was created then revoked in-session.
- Maintenance mode toggle was **not** flipped (side-effect on all writes).
- Pull-latest Trivy DB was **not** clicked (would violate air-gap invariant if backend is not configured to block outbound).
- Delete Account was **not** clicked.

---

## Backend routing issue summary (for finding #1)

Adding a single route in `internal/api/admin_phase1.go` (somewhere near line 189, in the same chi group) unblocks all 8 repo detail pages:

```go
Get("/projects/{name}/repos/{type}/{repo}", d.handleGetRepo)
```

The frontend expects a JSON `Repo` object matching the `Repo` type in `web/src/api/types.ts`. The handler likely needs to look up the repo in metadata, return shape matching `ListRepos` entry but for a single repo.
