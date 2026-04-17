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

## Next Milestone Goals

_Not yet scoped._ Candidates to triage during `/gsd-new-milestone`
questioning (derived from v1.0 close):

- **Billing / quota groundwork** — if introduced, the Docker shared-blob
  storage overestimate in `repoSizeExpr` needs closing.
- **Protocol hardening** — DEB `resolveDebPoolPath` edge cases for
  exotic pool layouts; real-world repos may trip this on day two.
- **Deferred v1 items** — the Out-of-Scope list below already flags
  upstream/proxy repos, scheduled sync, webhooks, SSH for Git, TOTP 2FA,
  scoped tokens, retention policies, email, LDAP/OIDC, artifact signing,
  cross-instance replication — any of these are live candidates for v1.1+.

Start the scoping pass with `/gsd-new-milestone` — it walks questioning →
research → requirements → roadmap and recreates a fresh
`.planning/REQUIREMENTS.md`.

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
*Last updated: 2026-04-17 after v1.0 milestone archival*
