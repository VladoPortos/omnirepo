# OmniRepo

## What This Is

OmniRepo is a self-hosted, internal artifact repository server — a focused,
simpler alternative to JFrog Artifactory or Sonatype Nexus for small-to-mid
corporate environments. From a single Docker container on one HTTP/HTTPS
port it serves OCI/Docker, RPM/YUM, APT/Debian, PyPI, Helm, RAW,
S3-compatible buckets, and Git hosting, with a built-in user/project
model, web UI, REST API, and Trivy-powered vulnerability scanning.
Designed to run in air-gapped corporate networks.

## Core Value

A single container that hosts every artifact type a corporate team
produces or consumes — Docker images, Linux packages, Python wheels,
Helm charts, raw blobs, S3 objects, Git repos — with vulnerability
scanning, project-scoped access control, and zero outbound network calls
at runtime.

## Current State

**Shipped milestone: v1.0** (tagged 2026-04-17)

All 175 v1 requirements delivered across five phases and 52 plans. The
shipped product matches the original "What This Is" above — every
protocol surface, every admin screen, the Trivy pipeline, the GC loop,
the Git backend with both gogit + gitkit paths, the S3 SigV4 verifier
with AES-GCM key storage, and the multi-stage Docker image with baked
Trivy DB. Full archive: `milestones/v1.0-ROADMAP.md` and
`milestones/v1.0-REQUIREMENTS.md`.

**v1.1 progress:** Phase 6 (Error Envelope & Visual Foundation) complete
2026-04-17 — every `/api/v1` handler emits the `ApiErrorEnvelope` shape
with UUID-v7 incident-id correlation, the UI renders all 4 error classes
through a shared component, protocol handlers no longer leak `%v`
internals, and the visual-foundation primitives (status tokens,
StatusBadge, Skeletons, CopyInline, a11y-upgraded CopyButton,
reduced-motion) ship with six automated regression gates covering
WCAG AA contrast, typography, spacing, responsive layout, and axe-core
a11y audits. 16/16 must-haves verified. ERR-01..07 + VISUAL-01..09
closed.

## Current Milestone: v1.1 Immediate Product Polish

**Goal:** Make OmniRepo easier to operate, faster to navigate, and clearer
under failure — a UI/UX-first pass that turns a functional v1.0 into a
product that feels deliberate in daily use. No core protocol reworks; only
additive backend endpoints where needed to power the new UI surfaces.

**Target features** (rescoped 2026-04-17 — v1.1 ships after 2 phases):

*Phase 6 (✅ shipped 2026-04-17):*

- Better failure messaging — stable `ApiErrorEnvelope` (`{code, message, hint?, class, incident_id?}`) across every `/api/v1` handler with UUID-v7 incident-id correlation; UI renders all 4 error classes through a shared component; protocol handlers no longer leak `%v` internals.
- UI visual language + polish — consistent color semantics for healthy / warning / failure / disabled / maintenance; loading skeletons (Card/Table/Detail/Metric); copy-to-clipboard affordances for URLs, commands, digests, and keys; status-badge consistency (StatusBadge); responsive for laptop-sized admin screens; WCAG AA contrast + axe-core a11y gates.

*Phase 7 (in progress — tight polish):*

- Client configuration snippets — accuracy pass on the existing per-protocol snippet generators (docker/pip/apt/rpm/helm/git/s3/raw) that shipped in v1.0; fix deprecated `apt-key`, complete Helm push, S3 region, Git/RAW auth hints.
- Dashboard summary cards — additive composition cards (health-summary / recent-failures / storage-growth / expiring-certs / queues / audit-highlights) on the existing `DashboardPage` using already-available v1.0 signal. No new backend endpoints.
- Context-aware empty states — shared `EmptyState` component covering EMPTY-01..08 (zero-repos project, zero-members project, zero-artifacts repo with inline snippet, never-scanned repo, no-TLS, empty trash, empty filters/favorites/recents, no-results search).
- Walkthrough micro-fixes — atomic-commit polish items surfaced during UI screen-driving.

**Deferred to v1.2** (dropped from v1.1 on 2026-04-17 — see
`.planning/REQUIREMENTS.md` "Deferred to v1.2" section for the 24 REQs):

- Health / status dashboard — dedicated admin Health page with disk /
  DB / jobs / Trivy / TLS / tasks cards backed by new
  `/api/v1/admin/health/*` endpoints (HEALTH-01..09). Phase 7's
  dashboard-cards track partially addresses the "quick health glance"
  need via composition; the dedicated page + new endpoints are v1.2.
- Saved filters, favorites, recents — per-user persistence with new
  SQLite migration (FAV-01..07).
- Better repository overview pages — default-landing control-center
  Overview tab reusing snippets + scan + sync + visibility summaries
  (OVERVIEW-01..08).

**Explicitly NOT in v1.1 or v1.2** (deferred further per the
`improvements.md` prioritization):

- Retention, quotas, capacity management
- Promotion / release pipeline model
- Policy engine, immutable releases, bulk administration
- Notification hooks, backup / recovery UX, audit-trail enrichment
- Job-model refactor (touched only as read-only visibility, not rebuilt)
- Air-gap export / import bundles, cross-instance replication
- HA direction, SBOM / provenance surfaces, scoped tokens, LDAP / OIDC

**Invariants carried forward from v1.0:**

- Single Go binary, local filesystem only, zero outbound network at
  runtime.
- `make grep-cdn` stays green — no runtime CDN for fonts, icons, or
  scripts. Any new UI assets ship embedded via `//go:embed`.
- Stack stays on Go 1.25 + React 19 + Vite + Tailwind 4; no major
  framework upgrades in v1.1.

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd-complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

## Archived context (v1.0 scoping)

<details>
<summary>Expand to see the original v1.0 "Active requirements" block and Key Decisions journal that drove the first milestone.</summary>

### Original v1.0 "Active requirements" (all delivered)

Tenancy & identity
- [x] Single global super-admin account, full powers
- [x] Project as the tenancy unit; flat membership = full access to everything in the project
- [x] Users with login, email, password (argon2id), library-generated avatar; multi-project membership
- [x] One-time password generated at user creation, forced change on first login
- [x] Self-service password change and self-account-deletion; super-admin can delete anyone
- [x] Project members can create new users into projects they belong to
- [x] First-run JSON bootstrap (cleartext seed) for users, projects, repos, and API keys; ignored after first run
- [x] API keys per user and per project; full scope within owner reach; revealed once at creation
- [x] Single policy engine consulted by every handler (super-admin OR project membership)

Repositories
- [x] Repo identity = (project, type, name); multiple repos per type per project
- [x] Repo types: RPM, DEB, PyPI, Docker/OCI, Helm, Git, RAW
- [x] Standalone S3 buckets (globally named per S3 convention)
- [x] Per-repo markdown README/description, editable in UI
- [x] Per-repo size cached; per-project total and server-wide free space displayed
- [x] Manual upload triggers protocol-specific metadata generation
- [x] Soft-delete to trash for repos and files; admin-triggered GC hard-deletes after retention
- [x] Wipe-contents action keeps repo, drops all artifacts

Protocol surfaces, sync, security, operations, API/UI, tests, and
air-gap invariants all delivered as captured in
`milestones/v1.0-ROADMAP.md`.

### Original "Out of Scope" at v1.0 (still deferred)

- Upstream/proxy/virtual repositories
- Scheduled / cron sync
- Webhooks
- Hard storage quotas (usage shown, not enforced)
- Prometheus `/metrics` endpoint
- SSH protocol for Git
- TOTP 2FA
- Scoped personal access tokens
- Retention policies (manual cleanup via wipe + GC only)
- Email notifications
- LDAP / OIDC / SSO
- Login rate-limit / lockout
- Artifact signing (verification only)
- Cross-instance replication
- Self-registration of users (excluded)
- S3-as-backend (excluded — local FS only)
- Multi-tenant DB / multi-process backend (excluded)

### Key Decisions (v1.0)

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Modular monolith in Go (single binary) | Simpler ops than microservices; chi `Mount()` makes multi-protocol clean | Shipped — all protocols multiplex on one port |
| Local filesystem only, no cloud backends | User explicitly excluded cloud backends; S3 is served, not stored | Shipped |
| go-git v6 `backend/http` with gitkit fallback | Pure-Go Git hosting; v6 stable as of Feb 2026 | Both paths shipped; config-selectable |
| Trivy as bundled subprocess, not Go library | Avoids dep-tree explosion + unstable Trivy Go API | Shipped; baked DB in image |
| `modernc.org/sqlite` (pure Go, no CGo) | Cross-compile, single-binary, `FROM scratch` | Shipped |
| Built-in users only (no LDAP/OIDC in v1) | Internal tool, simpler ops | Shipped |
| Flat project membership (any member = full access) | Matches user tenancy needs; no role tiers | Shipped |
| One-time password + forced change on first login | No SMTP in v1 | Shipped |
| First-run JSON bootstrap with cleartext creds | Super-admin owns distribution; ignored after first run | Shipped |
| API keys revealed once, stored SHA-256 hashed | Standard token handling | Shipped |
| Reserved root prefixes (`v2`, `s3`, `git`, `api`, `ui`, `assets`, `static`, `login`, `logout`, `healthz`, `readyz`) | Avoids URL collisions | Enforced |
| OpenAPI 3.1 hand-written; `oapi-codegen` types only | Hand-written chi routes for control | Shipped |
| All UI assets bundled; zero runtime CDN | Air-gap requirement | Shipped; `make grep-cdn` gate green |
| Trivy DB baked at build, admin upload/online-pull | Air-gap runtime | Shipped |
| S3 bucket provisioning is REST/UI, not SigV4 CreateBucket | `gofakes3.CreateBucket` disabled in prod wiring; matches AWS console vs API mental model | Closed mid-audit as F-S3-A |
| React 19 + Vite 8 + Tailwind 4 + shadcn/ui (was React 18) | Starting new UI on stable-current versions avoids forced upgrade | Shipped on 19.2 / 8 / 4 |
| Dependency + license matrix lives in README.md | CLAUDE.md does not ship | Matrix inlined into README near the end |

</details>

---
*Last updated: 2026-04-17 — v1.1 "Immediate Product Polish" milestone scoped (post-v1.0)*
