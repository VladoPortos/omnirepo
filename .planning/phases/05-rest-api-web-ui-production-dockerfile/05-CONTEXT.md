# Phase 5: REST API + Web UI + Production Dockerfile - Context

**Gathered:** 2026-04-16
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 5 delivers the complete product UX surface — everything a super-admin or project member interacts with through a browser — plus the production container image:

- **REST API** at `/api/v1/...` — hand-written chi routes typed from `oapi-codegen/v2` against a committed OpenAPI 3.1 spec. Swagger UI at `/api/docs` (bundled, no CDN). Every endpoint except `auth/login`, `/healthz`, `/readyz` requires auth. List endpoints paginate via `?limit=&cursor=`. Absorbs Phase 1's minimal admin REST surface (`admin_phase1.go`) into the full spec.
- **React 19 SPA** — embedded via `//go:embed web/dist/*`, served at `/` with client-side-routing fallback. Screens: login + forced-password-change, dashboard, projects, per-type repo detail, global search, profile, admin (users, audit, TLS, Trivy DB, GC, trash, maintenance). Dark mode default. Self-hosted fonts (Inter, JetBrains Mono), lucide-react icons, dicebear avatars. Zero runtime CDN.
- **Production Dockerfile** — multi-stage: node → go → trivy → alpine:3.21 (pinned by digest). Trivy binary + baked DB. Non-root UID 1000. linux/amd64 only. HEALTHCHECK. Go binary handles first-boot Trivy DB seeding.
- **Trivy DB admin** — upload tarball, online-pull button, status widget (version, age, source, warning when stale).
- **Global search** — dedicated `/search` page with clickable filters (type, severity, project) backed by FTS5 UNION across `repos_fts`, `artifacts_fts`, `cves_fts`, `rpm_fts`, `deb_fts`, `pypi_fts`, `helm_fts`. Plus per-repo inline search bar for filtering within artifact/file lists.
- **Sync job log viewer** — SYNC-06: `sync_jobs.log` field viewable in the UI.
- **Maintenance mode** — admin toggle returning 503 on writes; sticky global banner when active.
- **Playwright E2E** — covers login, forced password change, project + repo create, upload, scan view, search, profile API key reveal, admin maintenance toggle, TLS cert upload, GC trigger, trash restore.
- **Air-gap gates** — grep-cdn gate on `web/dist/`, full Playwright suite under `--network=none`.

The phase does NOT ship: Git web editing/commit from UI (deferred to v1.1), SSH Git, LDAP/OIDC, webhooks, cron sync, storage quotas, Prometheus metrics, or any v2 features.

</domain>

<decisions>
## Implementation Decisions

### UI shell & navigation

- **D-01** **Collapsible sidebar layout.** Left sidebar with icons + labels, collapsible to icon-only. Logo at top, nav links (Dashboard, Projects, Search, Admin), user avatar + menu at bottom. Artifactory-inspired — familiar for artifact repository users. Content fills the remaining space. Breadcrumbs above content for location context.
- **D-02** **Modern, animated feel.** Polished transitions throughout: page transitions (fade + slide-up, 200ms), sidebar collapse animation (300ms), card hover effects (translateY(-2px) + shadow lift), skeleton loading shimmer states, toast notifications (slide-in from top-right), modals (fade + scale-in, 150ms), tab underline slide animation, button subtle press effect. Library: **framer-motion** (~15 KB gzip). Not full motion design — polished but not distracting.
- **D-03** **Dashboard: overview cards + activity feed.** Top row: storage gauge (used/total), repo count, user count, scan findings summary (critical/high counts). Below: two-column layout — left: recent audit activity feed (scrollable, last 20 events), right: high-severity scan findings with links to affected repos. Quick-action buttons: Create Project, Upload Artifact.
- **D-04** **Project browsing: list → tabs by type.** Projects page shows project cards/list. Click a project → project detail with tabs: Overview (members, activity, storage), Docker, RPM, APT, PyPI, Helm, Git, RAW, S3. Each tab shows repos of that type with per-type columns. Click a repo → repo detail screen. Tabs with zero repos show an empty state with "Create first repo" CTA.
- **D-05** **Login: centered card.** Full-page dark background, centered card with OmniRepo logo/name, login + password fields, Sign In button. No sidebar visible until after login. Forced-password-change: same centered card layout with current + new + confirm fields and a clear banner explaining why. Offline-friendly error states (no network toast, not a redirect).
- **D-06** **Global search: dedicated page with clickable filters.** Full-page `/search` with dropdown/chip filters for type (repo/artifact/CVE), severity, project. All filters are clickable/selectable — no query syntax needed. Results displayed as a ranked list with type badges linking to source entities. **PLUS** per-repo inline search bar in every repo's artifact/file list tab that filters the list client-side as you type.
- **D-07** **Theme toggle: user menu dropdown.** Sun/moon icon in user avatar dropdown at sidebar bottom. Click toggles between dark and light. Preference saved in `localStorage`. Dark mode is the default for new/anonymous users. Smooth CSS transition animation (~200ms) on toggle.
- **D-08** **Admin section: dedicated sub-pages.** Admin nav item in sidebar expands to show sub-pages: Users, Audit, TLS Certs, Trivy DB, GC, Trash, Maintenance. Each is its own page/route. Super-admin-only — non-admin users don't see the Admin section at all.

### Repo detail screens

- **D-09** **Per-type completely custom layouts.** Each repo type (Docker, RPM, APT, PyPI, Helm, Git, RAW, S3) gets its own fully custom page layout. Common elements shared: repo name breadcrumb, size stat, description/README display, settings gear icon, "Copy snippet" button. But the primary content area is protocol-specific.
- **D-10** **Docker repo detail: tag list with scan badges.** Sortable/filterable tag list: tag name, image size, scan status badge (✔/⚠/✖ with color), push date, digest preview (truncated sha256). Click tag → layer breakdown + full scan report. Actions bar: Pull External, Promote/Retag, Copy pull command. Per-tag actions: Rescan, Delete, Copy `docker pull` command. Cosign signed/unsigned badge per tag.
- **D-11** **Git repo detail: full repository browser.** Branch/tag selector dropdown at top. Tabs: Files, Commits, Refs (branches + tags). Files tab: file tree of the selected ref with file icons, sizes, last commit message per file (GitHub-style). Click a file → syntax-highlighted view (read-only). Commits tab: scrollable commit history with author, message, date, SHA; click a commit → diff view (added/removed lines per file). Blame tab: per-file view showing which commit last modified each line. Branch comparison: select two refs to see the diff. Clone URL with copy button. No web editing/commit in v1.
- **D-12** **Package repo detail (RPM, APT, PyPI, Helm): shared table layout with per-type columns.** All four use a sortable/filterable table. Shared columns: Name, Version, Size, Upload Date, Scan Status. Type-specific: RPM adds Arch + Release; APT adds Suite + Component + Arch with filter dropdowns for suite/component; PyPI groups by normalized project name (expand to see versions/files), adds Requires-Python; Helm groups by chart name (expand versions), adds App Version. Click row → detail panel with metadata, scan results, download link. Actions bar: Upload dropzone, Sync from URL, Copy snippet.
- **D-13** **S3 bucket detail: file manager with prefix navigation.** Folder-like prefix drill-down. Columns: Key (with folder/file icon), Size, Last Modified, ETag. Click a "folder" (common prefix) to navigate deeper. Actions: Upload file(s) via dropzone, Download, Delete. Bucket stats at top (object count, total size). Search bar filters by prefix. Copy snippet for `aws --endpoint-url`.
- **D-14** **RAW repo detail: file browser.** Similar to S3 — directory tree navigation with file listing. Upload dropzone. Download links. Size + content-type shown per file. Directory listing when path resolves to a directory.
- **D-15** **Upload experience: drag-and-drop dropzone + CLI snippet.** Every repo detail (except Git = CLI instructions only) has a dropzone area: drag files or click to browse. Shows upload progress with animated bar. After upload: success toast with scan-enqueue status. Collapsible "CLI upload" section below with copy-to-clipboard commands per protocol (docker push, twine upload, helm push, curl PUT, aws s3 cp).
- **D-16** **"Use this repo" snippets: protocol-aware snippet panel.** Prominent "Copy snippet" button on each repo detail. Click opens a slide-out panel/modal with pre-filled commands for that repo type. Auto-fills hostname (from `server.external_hostnames` config or current URL), project, repo name. One-click copy per line. Docker: login + pull + push. RPM: .repo file content for dnf. APT: sources.list line + signing key import. PyPI: pip --index-url. Helm: helm repo add + pull. Git: git clone. S3: aws configure + aws s3 cp. RAW: curl PUT/GET.
- **D-17** **Scan results: severity summary + expandable CVE list.** Scan tab shows: last scan date, Trivy DB version used, severity donut/bar chart (critical/high/medium/low/unknown counts). Below: filterable CVE table with columns: CVE ID (linkable), Severity (color-coded badge), Package, Installed Version, Fixed Version. Click CVE ID → expandable row with title + description text. Actions: Rescan button, Download SBOM (CycloneDX or SPDX format selector).
- **D-18** **README/description editor: Milkdown WYSIWYG.** WYSIWYG markdown editor using **Milkdown** (ProseMirror-based, MIT, ~30 KB gzip). Inline editing of rendered markdown — what you see is the rendered output with in-place editing. Outputs markdown stored as-is in the DB (`description_md` column). Toolbar with bold/italic/headings/code/link/list. No separate preview pane needed — Milkdown renders inline.

### Admin & operations UX

- **D-19** **Audit log: filterable table with detail drawer.** Full-width table: Timestamp, Actor (avatar + name), Action (event kind), Target, Outcome (success/fail badge), IP address. Filter bar at top with dropdowns: Actor, Action type, Target kind, Date range picker, Outcome. Click a row → slide-out drawer on the right showing the full `details_json` in formatted JSON. Cursor-based pagination. Export button (CSV/JSON download).
- **D-20** **Trivy DB admin: status card + upload/pull actions.** DB status card showing: version, age (human-readable), source (`baked-in` / `uploaded` / `online-pulled`), warning banner (orange) when DB older than `scan.db_warn_age_days` (default 7). Below: two action cards side by side — "Upload DB" (drag-and-drop tarball with progress bar) and "Pull from Internet" (button with status spinner; clear error toast if network unavailable, matching air-gap expectations). History table of past DB updates (date, source, size).
- **D-21** **Trash viewer: table with restore/purge + bulk actions.** Table showing trashed items: Name, Type (repo/file), Original Location, Deleted By, Deleted At, Retention Countdown (human-readable "5d left"). Actions per row: Restore (moves back to original location), Purge (hard-delete immediately with confirmation dialog). Checkbox per row for bulk Restore/Purge. Header shows retention window config and total trash size.
- **D-22** **User management: CRUD table + modals.** Users table: Avatar (dicebear), Login, Email, Role (super-admin badge), Projects (tag chips), Created, Last Login. [+ Create User] button opens modal: login field, email field, project assignments (multi-select checkboxes). On submit: modal transitions to show the generated one-time password in a highlighted, copy-to-clipboard field with a warning "This password will not be shown again". Same one-time-reveal pattern as API key creation. Edit user: modal with editable fields + project reassignment + force-password-reset toggle. Delete: confirmation dialog.
- **D-23** **TLS cert admin: current cert info + upload + history.** Shows current certificate subject, issuer, expiry date, fingerprint. Upload form (PEM cert + key file inputs or paste). After upload: success toast confirming hot-swap complete. History list of previously uploaded certificates with timestamps, stored under `/var/lib/omnirepo/certs/uploaded/`.
- **D-24** **GC trigger: button + last run stats.** Simple page: "Trigger Garbage Collection" button with confirmation dialog. Last GC run stats (blobs deleted, bytes freed, trash entries deleted, duration). Status while running (spinner + "GC in progress" text).
- **D-25** **Maintenance mode: toggle + global sticky banner.** Admin page: toggle switch (ON/OFF) with confirmation dialog ("Are you sure? All write operations will return 503."). Shows who toggled and when. **When maintenance mode is ON:** persistent sticky warning banner at the very top of every page (orange/amber): "⚠ Maintenance mode active — write operations are disabled. [Disable]" (Disable button only visible to super-admin). Banner is not dismissible — stays until maintenance is turned off.
- **D-26** **Profile: full self-service panel.** Sections: Personal Info (edit email, avatar seed preview with regenerate button), Change Password (current + new + confirm), API Keys (list with create/revoke, one-time reveal on create in modal), S3 Keys (list per project with create/revoke, one-time secret reveal), My Projects (read-only list of project memberships). Delete Account button at bottom with confirmation dialog.
- **D-27** **Per-project activity feed.** Project detail Overview tab shows recent audit events scoped to that project — derived from audit_log where `target_kind` matches project or project's repos. Same table format as admin audit log but pre-filtered, read-only, no export. Shows last 50 events.

### REST API

- **D-28** **OpenAPI 3.1 spec at `internal/api/openapi.yaml`.** Hand-written, committed to the repo. `oapi-codegen/v2` generates Go types only (`-generate types`). Chi routes hand-written for full control over middleware ordering, auth handling, streaming uploads. Swagger UI bundled from `swagger-ui-dist` npm package, copied into `web/public/swagger/` at build time, served at `/api/docs` from embedded assets.
- **D-29** **Cursor-based pagination.** List endpoints use `?limit=<int>&cursor=<opaque>`. Cursor is a base64-encoded `(id, sort_value)` tuple. Response includes `next_cursor` (null if last page). Default limit = 50, max = 200.
- **D-30** **Upload streaming.** Upload endpoints (`PUT /<project>/<type>/<repo>/...`, `POST /api/v1/.../upload`) stream the request body directly to disk via the existing atomic temp+fsync+rename helpers. Configurable max upload size in config (`server.max_upload_bytes`, default 5 GiB). Over-cap requests return `413 Payload Too Large`.
- **D-31** **Phase 1 endpoint absorption.** `internal/api/admin_phase1.go` and `internal/api/types_phase1.go` are folded into the full API package. Existing endpoint paths stay compatible. Types are replaced by `oapi-codegen`-generated types from the OpenAPI spec.
- **D-32** **API endpoint inventory (API-06).** At minimum: auth (login, logout, change-password), projects (CRUD), members (add, remove), repos (CRUD per type), uploads (multipart per type), repo wipe, repo settings (PATCH description/auto_scan/block_on_severity/public_read), sync jobs (create, get, list with log), Docker pull-external, Docker promote, scans (start, get, list, SBOM download), search, audit log (list, filter), profile (get, update, change password), own API keys (CRUD), own S3 keys (CRUD), admin users (CRUD), admin TLS upload, admin Trivy DB (upload, pull, status), admin GC (trigger, status), admin maintenance (toggle, status), admin trash (list, restore, purge), settings (get/update for super-admin). Git-specific: list repos with refs, get file tree, get file content, get commit log, get commit diff, get blame, compare refs.
- **D-33** **Dev mode reverse proxy.** When `OMNIREPO_DEV=1`, the Go server reverse-proxies non-API requests to the Vite dev server on `:5173` for HMR. API routes (`/api/`, `/v2/`, `/s3/`, `/git/`, `/<project>/{rpm,deb,pypi,helm,raw}/...`) served directly by Go. This lets frontend dev iterate without rebuilding the Go binary.

### Frontend stack

- **D-34** **Stack:** React 19 + TypeScript 6 + Vite 8 + Tailwind CSS 4 (CSS-first config, `@tailwindcss/vite` plugin) + shadcn/ui 4 + TanStack Query 5 + React Router 7 + lucide-react + @dicebear/core + framer-motion + Milkdown (README editor) + swagger-ui-dist.
- **D-35** **Fonts:** Inter (body) + JetBrains Mono (code/monospace). Self-hosted `.woff2` files in `web/src/assets/fonts/`, referenced via `@font-face` with `font-display: swap`. No Google Fonts CDN.
- **D-36** **Query keys:** Structured as `['projects', projectName, 'repos', repoName, ...]` per TanStack Query conventions. Mutation invalidation keys derived from the same structure.
- **D-37** **Router:** React Router 7 with `createBrowserRouter` data-router API. Route-based code splitting for admin pages (lazy imports). Go server returns `index.html` for all non-API, non-asset paths (SPA fallback).
- **D-38** **Git file viewer: syntax highlighting.** Read-only file viewer for Git repos uses a lightweight syntax highlighter. Candidates: **Shiki** (VS Code's highlighter, WASM-based, supports 100+ languages, ~150 KB gzip for core + commonly used grammars, loaded on demand) or a simpler alternative. Planner picks — must be self-hosted, no CDN.
- **D-39** **Git diff viewer.** Commit diff and branch comparison use a split-pane diff component. Candidate: **react-diff-viewer-continued** (MIT, ~20 KB) or hand-rolled with `diff` output from go-git. Planner picks implementation approach.

### Production Dockerfile

- **D-40** **4-stage multi-stage build.** Stage 1: `node:22-alpine` — `npm ci && npm run build`, outputs `web/dist/`. Stage 2: `golang:1.25-alpine` — copies `web/dist/` into the Go source tree, `go build -mod=vendor -trimpath -ldflags="-s -w"`, outputs the binary. Stage 3: `aquasec/trivy:0.69.3` (pinned) — `trivy image --download-db-only` to fetch the latest DB at build time. Stage 4: `alpine:3.21` pinned by SHA256 digest — copies Go binary, Trivy binary, baked Trivy DB to `/opt/trivy-db/`, installs `git` + `ca-certificates` via apk, creates non-root user UID 1000.
- **D-41** **linux/amd64 only for v1.** No arm64 build. Simplifies CI and avoids cross-compile edge cases. arm64 is a v1.1 addition.
- **D-42** **Go binary handles first-boot Trivy DB seed.** No shell entrypoint script. The `serve` command checks on startup: if `/var/lib/omnirepo/trivy/db/` is empty, copies from `/opt/trivy-db/`. Single-process container.
- **D-43** **HEALTHCHECK instruction.** `HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -qO- http://localhost:8080/healthz || exit 1`. Uses wget (available in Alpine).
- **D-44** **Volume and ports.** `VOLUME ["/var/lib/omnirepo"]`. `EXPOSE 8080 8443`. `ENTRYPOINT ["omnirepo", "serve"]`.
- **D-45** **Build flags.** `go build -mod=vendor -trimpath -ldflags="-s -w -X main.version=${VERSION}"`. Version injected from git tag or build arg.

### Search

- **D-46** **Search API: `GET /api/v1/search?q=&kind=&severity=&project=`.** Returns ranked results across repos, artifacts, and CVEs from FTS5 UNION ALL query across `repos_fts`, `artifacts_fts`, `cves_fts`, `rpm_fts`, `deb_fts`, `pypi_fts`, `helm_fts`. Supports: filename exact match, image tag, checksum exact match, CVE ID exact match, and prefix queries. Results include type badge, severity (for CVE results), link to source entity.
- **D-47** **Search UI: dedicated page with clickable filters.** Filter chips/dropdowns: Kind (All / Repos / Artifacts / CVEs), Severity (for CVE results: Critical / High / Medium / Low), Project (dropdown of user's projects). All filters are clickable — no query syntax to learn. Result cards show: type icon, name, location (project/repo), relevance snippet, and link to the entity detail page. Pagination with cursor.

### Playwright E2E

- **D-48** **E2E suite covers the golden path.** Playwright tests (TEST-04) cover: login → forced password change → dashboard → create project → create one repo of each type → upload an artifact via dropzone → view scan results → copy the "use this repo" snippet → log out. Plus: profile API key reveal, admin maintenance toggle, TLS cert upload, GC trigger, trash restore. Air-gap: full suite runs under `--network=none`. Dark mode is the default theme verified.
- **D-49** **Air-gap grep gate.** `grep -rEI 'https?://(?!localhost|127\.0\.0\.1)' web/dist/` returns only self-references. Failure breaks the build. Applied after the SPA build in CI.

### Claude's Discretion

- Internal component hierarchy and file splits under `web/src/` (pages, components, hooks, lib).
- Exact TanStack Query cache times and stale-while-revalidate windows.
- Whether to use React Router 7's data loaders or stick with TanStack Query for data fetching (planner picks based on complexity tradeoff).
- shadcn/ui component selection — which components to generate via `npx shadcn@latest add`.
- Exact framer-motion animation configs (spring vs tween, exact durations) within the 200-300ms range specified.
- Swagger UI customization (theme, try-it-out enabled/disabled).
- Whether the Git file tree uses go-git tree-walking at the API level or returns pre-built JSON (planner picks).
- Exact OpenAPI 3.1 spec organization (one file vs `$ref` splits) — as long as it generates correct types.
- Per-repo inline search: whether to use client-side filtering or a server-side `?q=` param on list endpoints.
- Git syntax highlighter choice (Shiki vs alternative) — must be self-hosted.
- Git diff viewer implementation (library vs hand-rolled) — planner picks.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project context (always read)
- `.planning/PROJECT.md` — Vision, constraints, key decisions, technology stack (complete with pinned versions and rationale).
- `.planning/REQUIREMENTS.md` — Phase 5 requirements: SYNC-06, SCAN-09/10/11, OPS-03/04/05/07/09, API-01..06, SRCH-03/04, UI-01..13, AIR-01/02/03, TEST-03/04/05 (36 total).
- `.planning/ROADMAP.md` — Phase 5 section is authoritative for goal + success criteria.
- `.planning/STATE.md` — Current phase pointer, accumulated decisions.

### Prior phase outputs (heavy reuse)
- `.planning/phases/01-foundation/01-CONTEXT.md` — Actor/Can/middleware (D-16..D-21), storage primitives (D-28..D-32), audit (D-33..D-35), config (D-04..D-07), Phase 1 admin REST (D-36..D-37), reserved prefixes (D-26), session/API-key formats (D-17..D-18).
- `.planning/phases/02-oci-raw-scan-pipeline/02-CONTEXT.md` — OCI handler decisions (D-01..D-08), upstream creds (D-09..D-13), job runner (D-14..D-20), Trivy driver (D-21..D-26), RAW handler (D-27..D-31), public_read (D-32..D-33), repo edit/wipe (D-34..D-36), GC (D-37..D-39), FTS5 (D-40..D-41).
- `.planning/phases/03-package-repos-rpm-apt-pypi-helm/03-CONTEXT.md` — Signing keys (D-01..D-06), metadata regen (D-07..D-12), SYNC-05 (D-13..D-19), upload endpoints (D-20..D-22), APT model (D-23..D-25), DB schema (D-26), FTS5 (D-27..D-28).
- `.planning/phases/04-s3-git/04-CONTEXT.md` — S3 access keys (D-01..D-08), SigV4 (D-09..D-14), S3 handlers (D-15..D-17), multipart (D-18..D-22), Git backend (D-25..D-28), Git pipeline (D-29..D-32), git_refs (D-36..D-38).

### Existing code to read
- `internal/api/admin_phase1.go` — Phase 1 admin REST endpoints to be absorbed into full API.
- `internal/api/types_phase1.go` — Phase 1 hand-typed request/response shapes to be replaced by oapi-codegen types.
- `internal/api/*.go` — All existing API handlers (repos, scans, sync_actions, upstream_creds, s3_keys, admin_gc).
- `internal/httpx/router.go` — Chi router construction and middleware chain.
- `internal/httpx/middleware_maintenance.go` — Maintenance mode stub to be made functional.
- `internal/metadata/migrations/` — Migration numbering (last used: `019_`). Phase 5 may add migrations for any new tables.
- `Dockerfile` — Current 2-stage stub to be replaced with 4-stage production build.
- `Makefile` — Existing targets; Phase 5 adds `dev` (Go + Vite), `build` (SPA + Go), `docker`, `e2e`.
- `web/dist/` — Currently empty placeholder; will contain built SPA.

### Spec (single source of truth)
- `docs/superpowers/specs/2026-04-14-omnirepo-v1-design.md` — Read sections covering REST API, UI, Swagger, search, admin screens, Dockerfile, air-gap invariants, and Phase 5 success criteria.

### Research outputs
- `.planning/research/STACK.md` — Pinned frontend versions: React 19.2.5, Vite 8.0.8, Tailwind 4.2.2, TypeScript 6.0.2, shadcn@4.2.0, TanStack Query 5.99.x, React Router 7.14.x, lucide-react 1.8.0, @dicebear/core 9.4.2, swagger-ui-dist 5.32.3.
- `CLAUDE.md` §Technology Stack — Complete stack table with licenses and rationale for every dependency.
- `tools.md` — Original technology blueprint; consulted for library choices.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `internal/api/admin_phase1.go` — 14 REST endpoints already working. Phase 5 absorbs them; handler patterns (chi context params, JSON encode/decode, error responses) are the template for all new endpoints.
- `internal/api/types_phase1.go` — Request/response types. Replaced by `oapi-codegen` generated types but the shapes inform the OpenAPI spec.
- `internal/api/repos.go` — Repo CRUD endpoints already exist (PATCH, create, list).
- `internal/api/scans.go` — Scan REST surface (start, get, list) already exists.
- `internal/api/sync_actions.go` — Sync job endpoints already exist.
- `internal/api/upstream_creds.go` — Upstream creds CRUD already exists.
- `internal/api/s3_keys.go` — S3 access-key CRUD already exists.
- `internal/api/admin_gc.go` — GC trigger endpoint already exists.
- `internal/httpx/middleware_maintenance.go` — Maintenance mode middleware stub (no-op). Phase 5 makes it functional with a DB-backed toggle.
- `internal/httpx/anon_read.go` — Anonymous read middleware for public_read repos.
- `internal/auth/*` — Full auth substrate (Actor, Can, sessions, API keys, middleware).
- `internal/audit/*` — Audit logger and event kinds.
- `internal/metadata/*` — All DB repos (users, sessions, projects, members, repos, api_keys, s3_keys, settings, etc.).
- `internal/scan/runner.go` — Trivy Runner interface.
- `internal/jobs/*` — Two-pool job runner.
- `internal/protocol/{oci,raw,rpm,deb,pypi,helm,s3,git}/` — All protocol handlers mounted on chi.

### Established Patterns
- Atomic temp+fsync+rename for every mutable file.
- Writer pool size 1 + `BEGIN IMMEDIATE` for write transactions.
- `Actor` in `context.Context`; `auth.Can(actor, action, target)` at every auth gate.
- Audit row + NDJSON mirror per state-changing action.
- Chi middleware composition: per-mount auth → resolve → permission → handler.
- One-time secret reveal pattern (API keys, S3 keys): plaintext returned on creation, never again.

### Integration Points
- `internal/httpx/router.go` — Phase 5 adds: full `/api/v1/...` route tree (replacing Phase 1 minimal surface), `/api/docs` (Swagger UI), `/` SPA serving with fallback.
- `internal/api/` — Grows from ~15 endpoints to ~60+.
- `web/` — New directory: `web/src/` (React SPA source), `web/dist/` (built output, `//go:embed`).
- `Dockerfile` — Complete rewrite from 2-stage to 4-stage.
- `Makefile` — New targets: `dev`, `build` (frontend+backend), `docker`, `e2e`.
- `internal/metadata/migrations/` — Phase 5 may add migrations for maintenance state, Trivy DB metadata.
- `cmd/omnirepo/serve.go` — First-boot Trivy DB seeding logic.

</code_context>

<specifics>
## Specific Ideas

- **Artifactory-inspired layout** — User specifically referenced JFrog Artifactory as UX inspiration. The collapsible sidebar, project-scoped repo browsing, and protocol-type tabs mirror that familiar pattern.
- **"No old shitty static app"** — User explicitly wants a modern, animated feel. framer-motion provides the polished transitions (page, sidebar, cards, modals, tabs) without going overboard. Skeleton loading states replace spinner-only patterns.
- **Full Git browser** — User wants to see commits, diffs, blame, and branch comparison. This is the most ambitious UI feature in Phase 5. Implementation requires: go-git API endpoints for tree walking, commit log, diff generation, blame computation. File viewer needs syntax highlighting (Shiki or similar). Diff viewer needs a split-pane component. All read-only — no web editing in v1.
- **Per-repo inline search bars** — Not just global search. Each repo's artifact/file list has a search input that filters the displayed items. "If we are in a tab for repo where there is a list of files, I need to be able to quickly type file name and it should filter out."
- **Milkdown for README editing** — User chose Milkdown over Tiptap and side-by-side editors. Milkdown is markdown-first WYSIWYG with ProseMirror; lighter than Tiptap.
- **Sticky maintenance banner** — Not dismissible. Stays at the top of every page until maintenance mode is turned off. Only super-admin sees the Disable button in the banner.
- **amd64 only** — User confirmed no arm64 needed for v1. Simplifies the Docker build.
- **All UI filters are clickable** — No query syntax, no search language. Dropdowns, chips, checkboxes. "Everything is clickable, selectable."

</specifics>

<deferred>
## Deferred Ideas

- **Git web-based file editing + commit from UI** — User requested viewing, editing, deleting files, and committing directly from UI. Requires: go-git commit creation, conflict handling, code editor component (Monaco/CodeMirror). Significant scope — deferred to v1.1. v1 ships the full read-only Git browser.
- **arm64 Docker image** — User confirmed amd64 only for v1. Multi-arch (Buildx manifest list) is a v1.1 addition.
- **Command palette (Ctrl+K)** — Considered and rejected for v1. User prefers dedicated search page. Could be added later as a power-user feature.
- **Signing key rotation UI** — Phase 3 deferred idea; still deferred.
- **Prometheus /metrics** — Out of scope for v1.
- **Webhooks** — Out of scope for v1.
- **Storage quotas enforcement** — Usage displayed, enforcement deferred.
- **Git server-side hooks / push rules** — v2.

</deferred>

---

*Phase: 05-rest-api-web-ui-production-dockerfile*
*Context gathered: 2026-04-16*
