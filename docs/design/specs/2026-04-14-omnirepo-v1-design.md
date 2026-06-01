# OmniRepo v1 — Design Spec

**Date:** 2026-04-14
**Status:** Approved (design phase complete; ready for implementation planning)

---

## 1. Purpose

OmniRepo is an internal, self-hosted artifact repository server — a simpler, focused alternative to Artifactory / Nexus for small-to-mid corporate environments. It serves multiple package ecosystems and Git hosting on one port from one container, backed by local filesystem storage, with built-in users (no LDAP), UI, REST API, and Trivy-powered vulnerability scanning.

**Explicitly not v1:** upstream/proxy repos, scheduled sync, webhooks, hard quotas, Prometheus metrics, SSH Git, 2FA, scoped PATs, retention policies, email, LDAP/OIDC, login lockout, artifact signing, cross-instance replication.

## 2. Hard constraints

- **Language:** Go (modern, modular monolith).
- **Deployment:** Docker container (also runs locally during dev without Docker).
- **Persistence:** one directory on a persistent volume — `/var/lib/omnirepo/`.
- **Protocols:** HTTP + HTTPS on same binary; self-signed cert generated on first boot; replaceable via UI cert upload with hot TLS reload.
- **Runtime air-gap:** once the container is built, it must boot and operate with **zero outbound network calls**. Build time has full network access.
- **No external CDN at runtime:** fonts, icons, Swagger UI, all JS/CSS bundled into the binary.
- **No telemetry, update checks, or error reporting** to external services.
- **Tests:** every feature ships with tests; `make test` is the merge gate. UI verified via Playwright by the developer before declaring done.

## 3. Architecture

One Go process, one HTTP server (two listeners: 8080 + 8443), chi-mounted protocol handlers. External binaries (`trivy`, optional `git`) invoked as subprocesses. All mutable state under one persistent volume.

```
┌─────────────────── OmniRepo container ─────────────────────┐
│                                                            │
│   omnirepo (Go)                                            │
│     chi HTTP(S) on :8080 / :8443                           │
│       /v2/...            OCI handler                       │
│       /s3/...            gofakes3 handler + SigV4 mw       │
│       /git/...           go-git v6 backend/http            │
│       /<proj>/rpm/...    RPM handler                       │
│       /<proj>/deb/...    APT handler                       │
│       /<proj>/pypi/...   PyPI handler                      │
│       /<proj>/helm/...   Helm handler                      │
│       /<proj>/raw/...    RAW handler                       │
│       /api/v1/...        REST API                          │
│       /api/docs          Swagger UI (bundled)              │
│       /                  React SPA (embedded)              │
│                                                            │
│     Shared: auth, metadata (SQLite), storage (local FS),   │
│             audit, scan (→ trivy subproc), search          │
│                                                            │
│   trivy (binary, bundled)   git (optional fallback)        │
│                                                            │
│   Persistent volume: /var/lib/omnirepo/                    │
└────────────────────────────────────────────────────────────┘
```

## 4. URL layout

**Reserved root prefixes** (forbidden as project names): `v2`, `s3`, `git`, `api`, `ui`, `assets`, `static`, `login`, `logout`, `healthz`, `readyz`.

| Protocol | Path | Example |
|---|---|---|
| Docker/OCI | `/v2/<project>/<repo>/<image>/...` | `/v2/acme/oracle/nginx/manifests/1.25` |
| S3 | `/s3/<bucket>/<key>` (+ virtual-host style) | `/s3/builds/artifact.tgz` |
| Git | `/git/<project>/<repo>.git/...` | `/git/acme/infra.git/info/refs` |
| REST API | `/api/v1/...` | `/api/v1/projects/acme/repos` |
| Swagger | `/api/docs` | |
| RPM | `/<project>/rpm/<repo>/...` | `/acme/rpm/oracle/repodata/repomd.xml` |
| APT | `/<project>/deb/<repo>/...` | `/acme/deb/internal/dists/stable/Release` |
| PyPI | `/<project>/pypi/<repo>/simple/...` | `/acme/pypi/wheels/simple/requests/` |
| Helm | `/<project>/helm/<repo>/...` | `/acme/helm/charts/index.yaml` |
| RAW | `/<project>/raw/<repo>/<path>` | `/acme/raw/docs/readme.pdf` |
| UI | `/` (SPA with client-side routing) | |

## 5. Tenancy & identity

- **Super-admin**: one global account, can do anything.
- **Project**: the tenancy unit. Name is a URL-safe slug, unique globally.
- **User**: belongs to zero or more projects. Fields: login (unique), email, password hash (argon2id), avatar seed, `must_change_password` flag.
- **Flat project membership**: any user in a project has full read/write/admin power over that project's repos and membership.
- **Creation rights**:
  - Super-admin creates anyone and any project.
  - A project member can create a new user and add them to any project *the creator is already in*.
- **Deletion rights**:
  - Super-admin can delete any user/project/repo.
  - A user can delete their own account (must be logged in as themselves).
  - Project members cannot delete other users.
- **No self-registration**. No LDAP/OIDC/2FA/SSH keys/scoped PATs in v1.

### 5.1 Bootstrap JSON (first-run only)

On first boot, if the metadata DB is empty, the server consumes `/var/lib/omnirepo/config/bootstrap.json` to seed super-admin, users, projects, memberships, repos, and API keys. After the first successful boot, the file is ignored (its presence or absence no longer matters).

- JSON carries **cleartext** passwords and API keys. Super-admin owns distribution.
- Bootstrapped passwords are honored as-is (no forced change for those users).
- Passwords and API keys are hashed into the DB on import; plaintext is never persisted server-side after import.

### 5.2 One-time password flow (UI-created users)

When a user is created through UI or API (not bootstrap), the server generates a random 16-char password, returns it **once** in the API response (displayed once in the UI), and sets `must_change_password = true`. First UI login forces a redirect to a change-password screen before any other action.

### 5.3 API keys

- Token format: `omr_<kind>_<28 base62 chars>` (`omr_u_...` user, `omr_p_...` project).
- Stored as SHA-256 hash + short prefix for display. Plaintext revealed once at creation.
- Full powers within the owner's reach (no scopes in v1).
- Usable as:
  - `Authorization: Bearer omr_...` for REST API.
  - HTTP Basic password (`<login-or-project>:<key>`) for Git and Docker.
  - S3 SigV4: the API-key prefix functions as access-key-id; the full key as secret. (Or optional S3-specific key pairs issued via the same UI.)
- Docker `/v2/token` exchange issues a short-lived (15 min) HMAC-JWT on demand.

### 5.4 Policy engine

Single function consulted by every handler:

```
Can(actor, action, target):
  super-admin                   → allow all
  action on project P           → actor ∈ members(P) OR super-admin
  action on user self           → always OK for profile/password/delete-self
  action on other user          → super-admin only
  action on repo in project P   → same as project P
```

## 6. Repositories

**Identity:** `(project, type, name)` — name unique within project+type.

**Types:** `rpm`, `deb`, `pypi`, `docker`, `helm`, `git`, `raw`, plus standalone **S3 buckets** (globally-named per S3 convention).

**Per-repo attributes:** description (markdown README, editable in UI), size in bytes (cached), created-at, soft-deleted-at.

**Operations:**
- Create (project member or super-admin).
- Upload artifacts → server parses, generates/updates protocol-specific metadata (repodata, Packages/Release, simple index, Helm index.yaml).
- Delete → soft-delete to trash; GC hard-deletes after retention (default 7 days).
- **Wipe contents** (keep repo, drop all artifacts) — explicit admin action.
- Sync from external (manual, per-repo button).

## 7. Data root layout

```
/var/lib/omnirepo/
├── config/
│   ├── omnirepo.yaml              # runtime config
│   └── bootstrap.json             # first-run only
├── certs/
│   ├── server.crt  server.key     # active TLS pair
│   └── uploaded/                  # history of UI-uploaded certs
├── db/
│   └── omnirepo.sqlite (+ -wal, -shm)
├── blobs/
│   └── sha256/<xx>/<full-digest>  # content-addressed (OCI layers, de-duped)
├── repos/
│   └── <project>/
│       ├── rpm/<repo>/            # .rpm files + repodata/
│       ├── deb/<repo>/            # pool/ + dists/
│       ├── pypi/<repo>/           # wheels + simple/
│       ├── helm/<repo>/           # .tgz + index.yaml
│       ├── raw/<repo>/            # arbitrary tree
│       ├── docker/<repo>/         # manifest metadata (layers in /blobs/)
│       └── git/<repo>.git/        # bare git repo
├── s3/
│   └── <bucket>/                  # flat key tree
├── trash/<timestamp>-<repo-id>/   # soft-deleted content awaiting GC
├── trivy/
│   ├── db/                        # active vuln DB
│   └── cache/
├── sboms/<scan-id>.json
├── logs/
│   ├── audit.log                  # mirrored tail of audit_log table
│   └── app.log                    # optional; stdout is primary
└── tmp/                           # staging area for atomic uploads
```

## 8. Data model (SQLite via `modernc.org/sqlite`)

### Identity & access
- `users` — id, login (unique), email, password_hash, avatar_seed, is_super_admin, must_change_password, created_at, deleted_at.
- `sessions` — token_hash, user_id, issued_at, expires_at, last_seen_at, ip, user_agent.
- `projects` — id, name (unique), description_md, created_at, deleted_at.
- `project_members` — project_id, user_id, added_by, added_at.
- `api_keys` — id, owner_kind (`user`|`project`), owner_id, name, prefix, hash, created_by, created_at, last_used_at, revoked_at.

### Repositories
- `repos` — id, project_id, type, name, description_md, size_bytes, auto_scan (bool), created_at, deleted_at. Unique on (project_id, type, name).
- `s3_buckets` — id, name (unique global), owner_project_id, created_at, deleted_at.

### Artifact metadata (per type, all FK `repos.id`)
- `rpm_packages` — nevra, arch, epoch, version, release, file_path, sha256, size, uploaded_by, uploaded_at, scan_id.
- `deb_packages` — package, version, arch, component, file_path, sha256, size, uploaded_by, uploaded_at, scan_id.
- `pypi_files` — project_normalized, filename, sha256, size, uploaded_by, uploaded_at, scan_id.
- `helm_charts` — name, version, digest, uploaded_by, uploaded_at, scan_id.
- `docker_manifests` — image, tag, manifest_digest, media_type, config_digest, size, uploaded_by, uploaded_at, scan_id.
- `docker_blobs` — digest (PK), size, ref_count.
- `raw_files` — path (relative), sha256, size, uploaded_by, uploaded_at, scan_id.
- `git_refs` — ref_name, commit_sha, last_updated. Authoritative data is the on-disk bare repo; this table is a denormalized mirror for search/UI.

### Security findings
- `scans` — id, target_kind, target_id, trivy_version, db_version, status (`pending`|`running`|`ok`|`failed`), started_at, finished_at.
- `vulnerabilities` — scan_id, cve_id, severity, package, installed_version, fixed_version, title, description.
- `sboms` — id, target_kind, target_id, format (`cyclonedx`|`spdx`), file_path.

### Ops
- `audit_log` — id, actor_user_id, actor_api_key_id, event_kind, target_kind, target_id, ip, user_agent, outcome, details_json, occurred_at.
- `sync_jobs` — id, repo_id, source_url, kind (`repo`|`docker-image`), status, started_at, finished_at, log.
- `settings` — key, value (maintenance flag, active TLS cert id, Docker-token HMAC secret, etc.).

### Search
- FTS5 virtual tables backing global search:
  - `repos_fts(name, description_md, project_name)`
  - `artifacts_fts(repo_id, artifact_name, version, tags)`
  - `cves_fts(cve_id, package, title, description)`

## 9. Go module layout

```
omnirepo/
├── cmd/omnirepo/main.go
├── internal/
│   ├── app/           # wire, server, bootstrap
│   ├── config/        # koanf (YAML + env)
│   ├── httpx/         # chi assembly + middleware
│   ├── auth/          # users, sessions, api keys, policy
│   ├── metadata/      # sqlite access + migrations
│   ├── storage/       # cas, pathstore, atomic, trash
│   ├── protocol/
│   │   ├── oci/  s3/  git/  rpm/  apt/  pypi/  helm/  raw/
│   ├── sync/          # repo sync + docker pull + promote
│   ├── scan/          # trivy subprocess driver, sbom
│   ├── audit/
│   ├── search/
│   ├── tls/           # self-signed gen + hot reload
│   ├── api/           # REST handlers + openapi.yaml
│   └── web/           # //go:embed dist/*
├── web/               # React + Vite source
├── deployments/
│   └── Dockerfile  docker-compose.yaml
├── docs/design/specs/
├── go.mod  go.sum
└── Makefile
```

**Hygiene rules:**
1. Each protocol package exports exactly one constructor `New(deps) (chi.Router, error)`; `app/wire.go` is the only assembly point.
2. `storage`, `metadata`, `audit`, `scan` expose interfaces consumed by higher layers; concrete impls live in the same package. Tests use fakes.

## 10. Key library choices

| Subsystem | Library | License |
|---|---|---|
| Routing | `go-chi/chi` | MIT |
| SQLite | `modernc.org/sqlite` | BSD |
| OCI registry | `google/go-containerregistry/pkg/registry` | Apache 2.0 |
| RPM parsing | `cavaliergopher/rpm` | BSD |
| APT GPG | `ProtonMail/go-crypto` | BSD |
| PyPI | stdlib `html/template` | — |
| Helm | `helm.sh/helm/v3/pkg/repo` | Apache 2.0 |
| S3 API | `johannesboyne/gofakes3` | MIT |
| Git HTTP | `go-git/go-git/v6/backend/http` (fallback: `sosedoff/gitkit` shelling to `git`) | Apache 2.0 / MIT |
| Vuln scan | `trivy` (subprocess, bundled) | Apache 2.0 |
| Config | `knadh/koanf` | MIT |
| Password hash | `golang.org/x/crypto/argon2` | BSD |
| JWT (Docker token) | `golang-jwt/jwt/v5` | MIT |
| OpenAPI types | `oapi-codegen` (types only; routes written by hand) | Apache 2.0 |

UI: React 18 + TypeScript + Vite + TailwindCSS + shadcn/ui + TanStack Query + React Router + `lucide-react` + `@dicebear/core` + bundled `swagger-ui-dist`. Fonts: Inter + JetBrains Mono as local `.woff2`.

## 11. Auth flows

- **UI session**: `POST /api/v1/auth/login` → argon2id verify → opaque token in `sessions` table → `Secure; HttpOnly; SameSite=Lax` cookie. 12h sliding / 7d hard cap.
- **API key**: `Authorization: Bearer omr_...`, verified by SHA-256 hash lookup.
- **Docker**: standard `/v2/token` flow; HMAC-JWT issued after Basic auth; 15 min TTL.
- **Git HTTPS**: Basic auth, verified in middleware before reaching the go-git backend.
- **S3 SigV4**: access-key-id = API key prefix (or dedicated S3 key); canonical signing verified by recomputing HMAC with stored full key; actor identified, policy engine consulted.

## 12. Background jobs

Single in-process worker pool (default size 4). Jobs are rows in SQLite tables with `status` columns; dispatcher polls. On boot, any `running` rows older than 10 min flip to `pending`.

**Job kinds:**
1. **Sync repo** (RPM/DEB/PyPI/Helm) — mirror upstream metadata, download referenced files, regenerate local metadata (re-signed for APT).
2. **Sync Docker image** — pull `<registry>/<image>:<tag>` using `go-containerregistry`, write layers into CAS, insert manifest rows into target repo with optional retag.
3. **Promote Docker image** — metadata-only retag between two local Docker repos; layers already in CAS.
4. **Scan** — Trivy subprocess, JSON output parsed into `scans` + `vulnerabilities`. Docker uses `trivy image --input <oci-layout-dir>` from a temp materialization of CAS; RPM/DEB/PyPI/RAW use `trivy rootfs`/`fs` against an extracted temp dir.
5. **SBOM generate** — `trivy sbom --format cyclonedx`.
6. **GC sweep** (admin-triggered) — mark referenced blobs/files via metadata walk; sweep moves unreferenced into `trash/`; hard-delete after retention on next GC run.
7. **Wipe repo** (admin-triggered) — keep repo row, delete all artifacts + metadata rows + files. Audit event.

**Scan triggers:**
- Auto-scan on upload (per-repo setting, default on).
- Manual rescan button.
- Scheduled rescan deferred.

## 13. Trivy DB — air-gap handling

- **Build time** (Dockerfile stage): `trivy --download-db-only --cache-dir /opt/trivy-db`. DB baked into image.
- **First container start**: if `/var/lib/omnirepo/trivy/db/` empty, seed from `/opt/trivy-db/`.
- **Runtime**: always invoke Trivy with `--skip-db-update --offline-scan`. No automatic network calls.
- **Updates**, via admin UI/API only:
  1. **Upload tarball** — classic air-gap path. Admin exports from a connected helper, uploads; atomic swap.
  2. **Pull latest online** — one-shot `trivy --download-db-only`. Requires network; fails gracefully if absent.
- Status page shows DB version, age, source (`baked-in <date>` / `uploaded <date>` / `online-pulled <date>`), warning threshold configurable.

## 14. TLS

- First boot generates 2-year self-signed cert covering `localhost`, container hostname, and any hostnames in config. Written to `certs/server.crt|key`.
- UI upload screen accepts PEM cert + key, validates the pair + hostname sanity, atomically swaps files.
- `http.Server` uses `tls.Config.GetCertificate` pointing to an atomic cert holder — reload is instant, no restart.

## 15. Runtime air-gap invariants

Every PR must preserve:

1. No external CDN references in the built SPA (`grep -r "https://" web/dist/` returns only self-references).
2. Self-hosted fonts (`.woff2` in bundle), self-hosted icons (tree-shaken SVG via `lucide-react`), bundled Swagger UI from `swagger-ui-dist` npm package.
3. No runtime telemetry, no update checks, no external error reporting.
4. Trivy runs with `--skip-db-update --offline-scan` by default.
5. Sync-from-external and Docker-pull-external are user-triggered only; if offline they fail with a clear UI error.

Optional "is the internet reachable?" admin tool (single button, `HEAD https://ghcr.io`) to help users decide whether to try the online-pull actions. Never automatic.

## 16. REST API (OpenAPI 3.1)

Hand-written `internal/api/openapi.yaml`. Type generation via `oapi-codegen` (types only; routes written by hand for control). Swagger UI served from bundled assets at `/api/docs`. All endpoints require auth. Pagination via `?limit=&cursor=`. Upload size cap configurable; `413` on overflow. Streaming uploads for large blobs.

Representative endpoints:

- `POST /api/v1/auth/login`, `POST /api/v1/auth/logout`, `POST /api/v1/auth/change-password`
- `GET/POST /api/v1/projects`, `GET/PATCH/DELETE /api/v1/projects/{name}`
- `GET/POST /api/v1/projects/{p}/members`
- `GET/POST /api/v1/projects/{p}/repos`, `PATCH/DELETE /api/v1/projects/{p}/repos/{r}`
- `POST /api/v1/projects/{p}/repos/{r}/upload` (multipart)
- `POST /api/v1/projects/{p}/repos/{r}/sync` (body: source URL + optional creds)
- `POST /api/v1/projects/{p}/repos/{r}/wipe`
- `POST /api/v1/docker/pull` (source ref → target repo + retag)
- `POST /api/v1/docker/promote`
- `POST /api/v1/scans`, `GET /api/v1/scans/{id}`
- `GET /api/v1/sboms/{id}` (download)
- `GET /api/v1/search?q=&kind=&severity=`
- `GET /api/v1/audit?from=&to=&actor=`
- `GET/POST/DELETE /api/v1/users/me/api-keys`
- Admin: `POST /api/v1/admin/users`, `DELETE /api/v1/admin/users/{id}`, `POST /api/v1/admin/tls/upload`, `POST /api/v1/admin/trivy/db` (upload tarball), `POST /api/v1/admin/trivy/db/pull`, `POST /api/v1/admin/gc`, `POST /api/v1/admin/maintenance`, `GET /api/v1/admin/trash`, `POST /api/v1/admin/trash/{id}/restore`.

## 17. UI

Stack: React 18 + TypeScript + Vite + Tailwind + shadcn/ui + TanStack Query + React Router + `lucide-react` + `@dicebear/core`. All assets bundled; no runtime CDN.

Screens:

- **Login** + forced-password-change.
- **Dashboard**: storage used total + free, recent audit events, recent high-severity scan findings.
- **Projects** list → project detail (members, repos grouped by type, add repo, invite user).
- **Repo detail** (per type): file browser + upload dropzone, README editor (markdown preview), scan status + CVE list (filter), SBOM download, size/count stats, Sync-from-URL form, Wipe contents, Delete repo. Docker-specific: Pull external image, Promote tag, tag list with cosign badge.
- **Global search**: free-text across repos/filenames/tags/hashes/CVE IDs; type and severity filters.
- **Profile**: edit email, change password, manage own API keys (create → reveal once → revoke).
- **Admin** (super-admin only): users CRUD, full audit log with filters, TLS cert upload, Trivy DB status + update actions, maintenance mode toggle, GC trigger, trash viewer with restore.
- **Swagger UI** at `/api/docs`.

Dev mode: Vite dev server on :5173, Go reverse-proxies non-API requests to it when `OMNIREPO_DEV=1`. Prod: SPA embedded via `//go:embed web/dist/*`.

## 18. Test strategy

- **Unit**: every package with real logic uses Go table tests against in-memory SQLite + per-test `t.TempDir()`. Process boundaries (Trivy subprocess) mocked via a `Runner` interface.
- **Protocol conformance**: per protocol, spin up the full app on a random port and exercise it with the real client:
  - OCI: `crane push/pull`
  - S3: `aws-sdk-go-v2`
  - RPM: `dnf` (Docker-in-Docker) or parser-level assertions
  - APT: `apt-get update` (DinD)
  - PyPI: `pip install --index-url`
  - Helm: `helm repo add` + `helm pull`
  - Git: `git clone` / `git push` via `os/exec`
- **API**: table-driven against a running server; asserts status + response schema.
- **UI**: Playwright E2E run by the developer per the global rule; covers login, project/repo/upload/scan happy paths plus one error path per screen.
- **Benchmarks**: `make bench` runs a realistic push + sync + scan workload.

No feature merged without tests passing.

## 19. Docker image

Multi-stage:

1. `node:22-alpine` — install deps, `vite build`, output `web/dist/`.
2. `golang:1.24-alpine` — `go build -trimpath -ldflags="-s -w"` with the SPA embedded.
3. `aquasec/trivy:<pinned>` AS trivy-src — copy `trivy` binary and run `trivy --download-db-only --cache-dir /opt/trivy-db`.
4. `alpine:3.21` (pinned by digest) — copy omnirepo binary, trivy binary, `/opt/trivy-db/`, optional `git` (fallback), CA bundle.

Final image target: ~100-150 MB (Trivy alone ≈ 70 MB).

- `ENTRYPOINT ["/app/omnirepo"]`, `CMD ["serve", "--config", "/var/lib/omnirepo/config/omnirepo.yaml"]`
- `EXPOSE 8080 8443`
- `VOLUME /var/lib/omnirepo`
- Non-root user UID 1000.
- `HEALTHCHECK CMD wget -qO- http://127.0.0.1:8080/healthz`.

`docker-compose.yaml` for local dev brings up OmniRepo + throwaway client container for manual testing.

## 20. Build / dev commands (Makefile)

- `make dev` — Vite dev server + Go with `air` live reload.
- `make build` — production binary with embedded SPA.
- `make docker` — builds image.
- `make test` — unit + integration tests (requires DinD for full protocol suite, opt-out flag available).
- `make e2e` — Playwright suite.
- `make seed FILE=bootstrap.json` — loads seed on a fresh data root.
- `make vendor` — refreshes Go vendor dir for offline builds.
