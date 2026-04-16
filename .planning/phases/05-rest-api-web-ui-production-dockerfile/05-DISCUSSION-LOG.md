# Phase 5: REST API + Web UI + Production Dockerfile - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-04-16
**Phase:** 05-rest-api-web-ui-production-dockerfile
**Areas discussed:** UI layout & navigation, Repo detail screens, Admin & operations UX, Production Dockerfile

---

## UI layout & navigation

| Option | Description | Selected |
|--------|-------------|----------|
| Collapsible sidebar | Left sidebar with icons+labels, collapsible to icon-only. Artifactory-inspired. | ✓ |
| Top navbar + breadcrumbs | Horizontal nav bar, GitHub-style. | |
| Hybrid sidebar + top bar | Thin icon sidebar + top bar with breadcrumbs. | |

**User's choice:** Collapsible sidebar
**Notes:** "Take inspiration from Artifactory so the layout is similar and users can get into it faster. Our UI needs to feel modern, animated and in 2026 no old shitty static app."

---

| Option | Description | Selected |
|--------|-------------|----------|
| Overview cards + activity feed | Storage/repo/user/scan cards + two-column activity + scan findings | ✓ |
| Minimal status page | Sparse, health-focused | |
| Project-centric grid | Dashboard IS the projects list | |

**User's choice:** Overview cards + activity feed

---

| Option | Description | Selected |
|--------|-------------|----------|
| Project list → tabs by type | Project detail with per-type tabs (Docker, RPM, etc.) | ✓ |
| Flat repo list with filters | All repos in one list with type filter | |
| Tree view | File-explorer-style expandable tree | |

**User's choice:** Project list → tabs by type

---

| Option | Description | Selected |
|--------|-------------|----------|
| Polished transitions | Page fade+slide, skeleton loading, framer-motion (~15 KB) | ✓ |
| Micro-interactions only | CSS-only hover/press effects, no page transitions | |
| Full motion design | Route transitions, parallax, animated charts | |

**User's choice:** Polished transitions

---

| Option | Description | Selected |
|--------|-------------|----------|
| Centered card | Full-page dark bg, centered login card | ✓ |
| Split screen | Left branding panel, right form | |
| Full-page form | No card, fields directly on page | |

**User's choice:** Centered card

---

| Option | Description | Selected |
|--------|-------------|----------|
| Command palette + results page | Ctrl+K overlay + full /search page | |
| Search bar in sidebar | Always-visible search in sidebar | |
| Dedicated search page only | Full-page /search with clickable filters | ✓ |

**User's choice:** Dedicated search page only
**Notes:** "Dedicated search page with all the filters and nice clicky stuff so we don't have to know any commands or filter strings. Everything is clickable, selectable. However, there needs to be a search bar per repository — if we are in a tab for repo where there is a list of files, I need to be able to quickly type file name and it should filter out."

---

| Option | Description | Selected |
|--------|-------------|----------|
| Toggle in user menu | Sun/moon in user avatar dropdown | ✓ |
| Toggle in sidebar footer | Always-visible toggle at sidebar bottom | |
| System preference only | Follow OS setting, no manual toggle | |

**User's choice:** Toggle in user menu

---

## Repo detail screens

| Option | Description | Selected |
|--------|-------------|----------|
| Shared shell + per-type tabs | Common header with protocol-specific tabs | |
| Unified file browser | Same file-browser UI for all types | |
| Per-type completely custom | Fully custom page per protocol | ✓ |

**User's choice:** Per-type completely custom
**Notes:** "Especially for Docker, it definitely needs different layout. Same for Git. We need to discuss the Git a little bit more because I'm expecting to have Git repos under a project and I need a view at least close to what GitHub have so I can see commits, be able to do basic functionality and view files."

---

| Option | Description | Selected |
|--------|-------------|----------|
| Tag list with scan badges | Sortable/filterable tag list with scan status | ✓ |
| Image grid with thumbnails | Visual grid of image cards | |
| Split: tags left, detail right | Master-detail layout | |

**User's choice:** Tag list with scan badges (Docker)

---

| Option | Description | Selected |
|--------|-------------|----------|
| File tree + commit log + README | Basic file browser + commits | |
| Refs only (minimal) | Just branches, tags, clone URL | |
| Full repository browser | File tree, commits, diffs, blame, branch comparison | ✓ |

**User's choice:** Full repository browser (Git)

Git features selected (all):
- ✓ File tree + file viewer (syntax highlighted)
- ✓ Commit log + single-commit diff
- ✓ Blame view
- ✓ Branch comparison / diff between refs

**Notes:** "Would be nice if we could also view files individually and edit them and delete them and be able to commit changes directly from UI." — Noted as deferred to v1.1.

---

| Option | Description | Selected |
|--------|-------------|----------|
| Drag-and-drop dropzone + CLI snippet | Dropzone area + collapsible CLI commands | ✓ |
| CLI instructions only | No browser upload | |
| Multi-file upload wizard | Step-by-step guided upload | |

**User's choice:** Drag-and-drop dropzone + CLI snippet

---

| Option | Description | Selected |
|--------|-------------|----------|
| Protocol-aware snippet panel | Prominent button → panel with pre-filled commands | ✓ |
| Inline at top of page | Single-line copyable URL at top | |
| You decide | Claude picks | |

**User's choice:** Protocol-aware snippet panel

---

| Option | Description | Selected |
|--------|-------------|----------|
| Severity summary + expandable CVE list | Donut chart + filterable CVE table + SBOM download | ✓ |
| Simple list only | Plain text list | |
| You decide | Claude designs | |

**User's choice:** Severity summary + expandable CVE list

---

| Option | Description | Selected |
|--------|-------------|----------|
| File manager with prefix navigation | Folder-like prefix drill-down for S3 | ✓ |
| Flat object list | All objects in one flat list | |
| You decide | Claude designs | |

**User's choice:** File manager with prefix navigation (S3)

---

| Option | Description | Selected |
|--------|-------------|----------|
| Side-by-side markdown editor | Split: textarea left, preview right | |
| Simple textarea | Just a textarea, preview after save | |
| WYSIWYG editor | Rich text editor generating markdown | ✓ |

**User's choice:** WYSIWYG editor

WYSIWYG library:

| Option | Description | Selected |
|--------|-------------|----------|
| Tiptap | Headless rich-text, ~40 KB, MIT | |
| Milkdown | Markdown-first WYSIWYG, ~30 KB, MIT | ✓ |
| BlockNote | Block-based Notion-like, ~60 KB, MIT | |

**User's choice:** Milkdown

---

| Option | Description | Selected |
|--------|-------------|----------|
| Shared table layout with per-type columns | Sortable table, shared + type-specific columns | ✓ |
| Card grid | Package cards in grid | |
| Per-type custom | Each protocol gets own layout | |

**User's choice:** Shared table layout with per-type columns (packages)
**Notes:** User asked about per-type quirks — confirmed that PyPI groups by project, Helm groups by chart, APT has suite/component filter, RPM groups by name. Shared table handles it with extra columns and grouping headers.

---

## Admin & operations UX

| Option | Description | Selected |
|--------|-------------|----------|
| Filterable table with detail drawer | Full-width table + filter bar + click-to-drawer | ✓ |
| Timeline/feed style | Chronological activity feed cards | |
| You decide | Claude designs | |

**User's choice:** Filterable table with detail drawer (audit log)

---

| Option | Description | Selected |
|--------|-------------|----------|
| Status card + upload/pull actions | DB status card + two action cards side by side | ✓ |
| Minimal: upload only | Just upload form + version display | |
| You decide | Claude designs | |

**User's choice:** Status card + upload/pull actions (Trivy DB)

---

| Option | Description | Selected |
|--------|-------------|----------|
| Table with restore/purge + bulk actions | Table with checkboxes, per-row and bulk actions | ✓ |
| Simple list | Plain list with restore buttons | |
| You decide | Claude designs | |

**User's choice:** Table with restore/purge + bulk actions (trash)

---

| Option | Description | Selected |
|--------|-------------|----------|
| Dedicated sub-pages under Admin | Each admin function gets its own page/route | ✓ |
| Single admin dashboard | All admin actions on one page with sections | |
| You decide | Claude organizes | |

**User's choice:** Dedicated sub-pages under Admin

---

| Option | Description | Selected |
|--------|-------------|----------|
| Modal form + one-time password reveal | Create in modal, password shown once | ✓ |
| Inline form in table | New user row appears in table | |
| You decide | Claude designs | |

**User's choice:** Modal form + one-time password reveal (user creation)

---

| Option | Description | Selected |
|--------|-------------|----------|
| Global warning banner + toggle | Sticky banner on every page when active | ✓ |
| Admin page only | Toggle only on admin page | |
| You decide | Claude designs | |

**User's choice:** Global warning banner + toggle (maintenance)
**Notes:** "Sticky bar on top of the page warning that this is going on."

---

| Option | Description | Selected |
|--------|-------------|----------|
| Full self-service panel | Email, avatar, password, API keys, S3 keys, projects, delete account | ✓ |
| Minimal: password + keys only | Just password change and API key management | |
| You decide | Claude designs | |

**User's choice:** Full self-service panel (profile)

---

## Production Dockerfile

| Option | Description | Selected |
|--------|-------------|----------|
| Alpine 3.21 pinned by digest | 4-stage build, ~650 MB total | ✓ |
| Distroless | Smallest image, no shell/git | |
| Debian slim | Larger, familiar, apt available | |

**User's choice:** Alpine 3.21 pinned by digest

---

| Option | Description | Selected |
|--------|-------------|----------|
| Download in build stage | Stage 3 runs trivy --download-db-only | ✓ |
| Pre-built DB artifact | DB downloaded separately, COPY'd | |
| No baked DB | Admin must upload before scans | |

**User's choice:** Download in build stage (Trivy DB baking)

---

| Option | Description | Selected |
|--------|-------------|----------|
| Docker Buildx manifest list | Multi-arch amd64 + arm64 | |
| amd64 only for v1 | Single arch, simpler CI | ✓ |
| Separate images per arch | Two tags, no manifest list | |

**User's choice:** amd64 only for v1
**Notes:** "No arm needed."

---

| Option | Description | Selected |
|--------|-------------|----------|
| Go binary handles everything | No shell entrypoint, binary handles DB seed | ✓ |
| Shell wrapper entrypoint | Shell script for first-boot tasks | |
| You decide | Claude picks | |

**User's choice:** Go binary handles everything (entrypoint)

---

## Claude's Discretion

- Component hierarchy and file organization under `web/src/`
- TanStack Query cache/stale times
- React Router data loaders vs TanStack Query
- shadcn/ui component selection
- framer-motion animation configs within specified ranges
- Git syntax highlighter choice (Shiki vs alternative)
- Git diff viewer implementation approach
- OpenAPI spec file organization
- Per-repo inline search implementation (client vs server filtering)

## Deferred Ideas

- Git web-based file editing + commit from UI (v1.1)
- arm64 Docker image (v1.1)
- Command palette Ctrl+K (v1.1 candidate)
