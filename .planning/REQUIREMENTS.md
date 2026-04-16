# Requirements: OmniRepo

**Defined:** 2026-04-14
**Core Value:** A single container that hosts every artifact type a corporate team produces or consumes — Docker images, Linux packages, Python wheels, Helm charts, raw blobs, S3 objects, Git repos — with vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.

## v1 Requirements

### Foundation (FOUND)

- [x] **FOUND-01**: System builds as a single Go binary with embedded SPA via `//go:embed`
- [x] **FOUND-02**: System runs in a Docker container; non-root UID 1000; volume `/var/lib/omnirepo/`
- [x] **FOUND-03**: System reads YAML config via koanf with environment-variable overrides
- [x] **FOUND-04**: System listens on HTTP and HTTPS on the same binary, configurable ports (default 8080 / 8443)
- [x] **FOUND-05**: System generates a self-signed TLS certificate on first boot (2-year validity, hostnames from config + container hostname + `localhost`)
- [x] **FOUND-06**: System exposes `/healthz` and `/readyz` endpoints
- [x] **FOUND-07**: System opens a SQLite metadata DB at `/var/lib/omnirepo/db/omnirepo.sqlite` in WAL mode with reader/writer split (writer pool size 1, `BEGIN IMMEDIATE`)
- [x] **FOUND-08**: System runs database migrations on startup; failure aborts boot with a clear message
- [x] **FOUND-09**: System owns one persistent root directory `/var/lib/omnirepo/` with subdirectories: `config/`, `certs/`, `db/`, `blobs/`, `repos/`, `s3/`, `trash/`, `trivy/`, `sboms/`, `logs/`, `tmp/`
- [x] **FOUND-10**: System routes HTTP requests via a chi router; reserved root prefixes (`v2`, `s3`, `git`, `api`, `ui`, `assets`, `static`, `login`, `logout`, `healthz`, `readyz`) are forbidden as project names
- [x] **FOUND-11**: System provides a content-addressed blob store (`/var/lib/omnirepo/blobs/sha256/<xx>/<full-digest>`) with atomic temp-file → fsync → rename writes
- [x] **FOUND-12**: System provides a path-addressed file store under `repos/<project>/<type>/<repo>/...` with atomic writes
- [x] **FOUND-13**: System soft-deletes content into `/var/lib/omnirepo/trash/<timestamp>-<id>/` with a configurable retention window (default 7 days)
- [x] **FOUND-14**: System provides a per-repo mutex map (`internal/storage/locks.go`) shared by APT, RPM, Helm, and any future protocol that regenerates metadata

### Tenancy & Identity (TEN)

- [x] **TEN-01**: System has exactly one global super-admin account; the super-admin can perform any action
- [x] **TEN-02**: User can log in with login + password (argon2id verification); session token issued in `Secure; HttpOnly; SameSite=Lax` cookie with 12h sliding / 7d hard cap
- [x] **TEN-03**: User can log out from any page; the session is deleted server-side
- [x] **TEN-04**: User can change own password from the profile screen (current password required)
- [x] **TEN-05**: User can edit own email and avatar seed from the profile screen
- [x] **TEN-06**: User can delete own account (must be logged in as themselves)
- [x] **TEN-07**: Avatar is rendered client-side from a stored seed using dicebear; no network requests
- [x] **TEN-08**: Super-admin can create users from the admin UI; the system generates a one-time 16-character password, displays it once, and sets `must_change_password = true`
- [x] **TEN-09**: Super-admin can edit any user (email, super-admin flag, project memberships, force password reset)
- [x] **TEN-10**: Super-admin can delete any user
- [x] **TEN-11**: Project member can create a new user and assign them to projects the creator already belongs to
- [x] **TEN-12**: First-login flow forces redirect to a change-password screen when `must_change_password = true`; no other navigation allowed until password changed
- [x] **TEN-13**: `must_change_password` is enforced by the policy engine on every auth surface (REST, Docker, Git, S3, package repos), not only on UI login; offending requests return `403 password-change-required`
- [x] **TEN-14**: System has a single policy engine `auth.Can(actor, action, target)`; super-admin allows everything, project actions check membership, self-actions check identity
- [x] **TEN-15**: Super-admin can create a project (URL-safe slug, unique globally, optional markdown description)
- [x] **TEN-16**: Super-admin can delete a project (soft-delete; data moves to trash for retention window)
- [x] **TEN-17**: Project members can add or remove other users in their projects (flat membership = full access; users may be in multiple projects)

### API Keys (KEY)

- [x] **KEY-01**: Token format `omr_<kind>_<28 base62>` (kind: `u` for user, `p` for project)
- [x] **KEY-02**: Stored as SHA-256 hash plus first 8-character prefix; plaintext revealed exactly once at creation
- [x] **KEY-03**: User can create, name, list, and revoke own API keys from the profile screen
- [x] **KEY-04**: Project members can create, name, list, and revoke project API keys for their projects
- [x] **KEY-05**: API keys authenticate via `Authorization: Bearer omr_...` for the REST API
- [x] **KEY-06**: API keys also authenticate as the password in HTTP Basic for Docker (`/v2/token`) and Git (HTTPS push/clone)
- [x] **KEY-07**: API keys are full-power within the owner's reach (no scoped tokens in v1)
- [x] **KEY-08**: System tracks `last_used_at` per key and surfaces it in the UI

### S3 Auth Credentials (S3K)

- [x] **S3K-01**: User can create dedicated S3 access-key/secret pairs from the profile screen
- [x] **S3K-02**: Project members can create dedicated S3 access-key/secret pairs scoped to their projects
- [x] **S3K-03**: S3 secret is AES-GCM encrypted at rest with a per-install key stored in `settings`; SigV4 verification recomputes HMAC using the decrypted secret
- [x] **S3K-04**: SigV4 middleware rejects requests with clock skew beyond a configurable window (default 15 minutes), echoing server time in `x-amz-date`-aware error
- [x] **S3K-05**: Wrong signature returns `403 SignatureDoesNotMatch`; missing credential returns `403 InvalidAccessKeyId`

### Bootstrap (BOOT)

- [x] **BOOT-01**: On first boot (empty DB), system reads `/var/lib/omnirepo/config/bootstrap.json` and seeds super-admin, users, projects, memberships, repos, and API keys
- [x] **BOOT-02**: Bootstrap JSON carries cleartext passwords and API keys; passwords are hashed and tokens are hashed during ingest; plaintext is never persisted server-side after seeding
- [x] **BOOT-03**: Bootstrapped users do NOT get the one-time-password flow; their bootstrap-supplied password stands as-is and `must_change_password` is `false`
- [x] **BOOT-04**: After the first successful boot, the bootstrap JSON file is ignored regardless of presence
- [x] **BOOT-05**: Bootstrap failures abort startup with a clear, line-pointing error and rollback any partial inserts

### Repositories (REPO)

- [x] **REPO-01**: Repository identity is `(project, type, name)`; name is unique within `(project, type)`
- [x] **REPO-02**: Supported types: `rpm`, `deb`, `pypi`, `docker`, `helm`, `git`, `raw`
- [x] **REPO-03**: S3 buckets exist as a separate, globally-named entity owned by a project
- [x] **REPO-04**: Project members can create a repo (name + type) within their projects
- [ ] **REPO-05**: Project members can edit per-repo description (markdown README), `auto_scan` flag, `block_on_severity` enum, and `public_read` flag
- [x] **REPO-06**: Project members can soft-delete a repo; the repo and its contents move to trash with retention
- [ ] **REPO-07**: Project members can wipe a repo's contents (delete all artifacts and metadata rows; keep the repo row); audit event emitted
- [x] **REPO-08**: System tracks `size_bytes` per repo and updates it on upload/delete; per-project total and server-wide free space available via API and UI
- [ ] **REPO-09**: `public_read` flag, when true, allows anonymous GET requests to the repo's read paths; writes and deletes still require auth and project membership

### OCI / Docker Protocol (OCI)

- [ ] **OCI-01**: Docker registry served at `/v2/<project>/<repo>/<image>/...` per OCI Distribution spec
- [ ] **OCI-02**: `docker login` flow returns `401 WWW-Authenticate: Bearer realm="<host>/v2/token",service="omnirepo"`; client exchanges Basic creds at `/v2/token` for a short-lived HMAC-JWT (default 60 minutes, configurable in `settings`)
- [ ] **OCI-03**: Docker push supports chunked blob upload (POST → PATCH ... → PUT), monolithic upload (POST + body), and cross-repo blob mount (`?from=<repo>`)
- [ ] **OCI-04**: Docker pull supports manifest GET (with `Accept` content negotiation for OCI/Docker manifest list types), blob GET (range supported), `HEAD` for manifest and blob existence
- [ ] **OCI-05**: Tag list, tag delete, manifest delete, and `/v2/_catalog` (scoped per-project, gated by membership) implemented
- [ ] **OCI-06**: Each blob digest is stored once in the CAS; per-repo `docker_manifests` rows reference the shared blobs; `docker_blobs.ref_count` is incremented in the same transaction as a manifest insert and decremented on delete
- [ ] **OCI-07**: `docker-content-digest` response header is set on every applicable response
- [ ] **OCI-08**: Project members can pull a Docker image from an external registry into one of their repos with optional retag (e.g. `docker.io/nginx:1.25` → `dxc/oracle/nginx:1.25-internal`); supports anonymous and Basic-auth upstream
- [ ] **OCI-09**: Project members can promote (retag) a Docker image from one local Docker repo to another in the same or another project they belong to; no blob copy
- [ ] **OCI-10**: Cosign signature verification displays a signed/unsigned badge on tag list views (read-only; OmniRepo does not sign)

### S3 Protocol (S3)

- [x] **S3-01**: S3 API served at `/s3/<bucket>/<key>` (path-style) and at `<bucket>.<host>/...` (virtual-host style via `Host` header routing)
- [x] **S3-02**: Operations supported: `CreateBucket`, `ListBuckets`, `HeadBucket`, `DeleteBucket`, `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`, `ListObjectsV1`, `ListObjectsV2`, multipart upload (`CreateMultipartUpload`, `UploadPart`, `CompleteMultipartUpload`, `AbortMultipartUpload`)
- [x] **S3-03**: SigV4 middleware validates every request; failures return AWS-shape XML errors (`SignatureDoesNotMatch`, `InvalidAccessKeyId`, `RequestTimeTooSkewed`)
- [x] **S3-04**: Bucket contents stored at `/var/lib/omnirepo/s3/<bucket>/<key>` (flat keyspace materialized as a directory tree)
- [x] **S3-05**: No versioning, no object lock, no website hosting in v1
- [ ] **S3-06**: Conformance verified against `aws-sdk-go-v2`; positive and negative test cases covered

### Git Protocol (GIT)

- [x] **GIT-01**: Git Smart HTTP served at `/git/<project>/<repo>.git/...` via go-git v6 `backend` package (with sosedoff/gitkit fallback behind a config flag)
- [x] **GIT-02**: HTTP Basic auth (login + password OR `<login>:<api-key>` OR `project:<project>:<project-api-key>`) verified before reaching the Git backend
- [x] **GIT-03**: `info/refs`, `git-upload-pack`, and `git-receive-pack` endpoints implemented
- [x] **GIT-04**: Bare repos created at `/var/lib/omnirepo/repos/<project>/git/<repo>.git/`
- [x] **GIT-05**: System enforces a configurable per-repo push size limit (default 500 MB); requests exceeding the cap are rejected with a clear error
- [x] **GIT-06**: System maintains a denormalized `git_refs` table mirror (ref name → commit SHA) for search/UI; authoritative state is the on-disk bare repo
- [ ] **GIT-07**: Conformance verified by `git clone`, `git push`, `git fetch` against a real `git` CLI

### RPM Protocol (RPM)

- [x] **RPM-01**: RPM repo served at `/<project>/rpm/<repo>/...`
- [x] **RPM-02**: Project members can upload `.rpm` files via REST API (multipart) or UI dropzone; system parses RPM headers via `cavaliergopher/rpm` and stores the file under `repos/<project>/rpm/<repo>/`
- [x] **RPM-03**: System regenerates `repodata/repomd.xml`, `primary.xml.gz`, `filelists.xml.gz`, and `other.xml.gz` after every upload/delete; metadata files are content-hash-named for atomic swap; the per-repo mutex serializes regeneration
- [x] **RPM-04**: System signs `repomd.xml` to produce `repomd.xml.asc` using a per-repo or per-project GPG key (ProtonMail/go-crypto); key generated on first use
- [x] **RPM-05**: System exposes the public signing key at `/<project>/rpm/<repo>/public-key.asc`
- [x] **RPM-06**: Conformance verified via `dnf install` against the served repo (Docker-in-Docker test container)

### APT Protocol (APT)

- [x] **APT-01**: APT repo served at `/<project>/deb/<repo>/...`
- [x] **APT-02**: Project members can upload `.deb` files; system parses `control` from the inner `control.tar.gz` (`ar` archive) and stores the file under `repos/<project>/deb/<repo>/pool/<...>`
- [x] **APT-03**: Repo carries an explicit (suite, component, architecture) data model (default suite `stable`, default component `main`); upload selects suite + component, `arch` derived from the package
- [x] **APT-04**: System regenerates `dists/<suite>/<component>/binary-<arch>/Packages` (+ `.gz`), `dists/<suite>/Release`, and `dists/<suite>/InRelease` after every upload/delete; `InRelease` clearsigned with the repo's GPG key; per-repo mutex serializes regeneration
- [x] **APT-05**: System exposes the public signing key at `/<project>/deb/<repo>/public-key.asc`
- [x] **APT-06**: Conformance verified via `apt-get update` + `apt-get install` (Docker-in-Docker)

### PyPI Protocol (PYPI)

- [x] **PYPI-01**: PyPI repo served at `/<project>/pypi/<repo>/...`
- [x] **PYPI-02**: PEP 503 HTML simple index at `/simple/` and `/simple/<project-normalized>/`
- [x] **PYPI-03**: PEP 691 JSON simple index served via content negotiation (`Accept: application/vnd.pypi.simple.v1+json`)
- [x] **PYPI-04**: Single canonical `Normalize(name)` function applied everywhere: lowercase + replace `[-_.]+` with `-`
- [x] **PYPI-05**: Project members can upload wheels and sdists via the standard `twine upload` flow (multipart POST)
- [x] **PYPI-06**: Conformance verified via `pip install --index-url ...` and `uv pip install --index-url ...`

### Helm Protocol (HELM)

- [x] **HELM-01**: Helm repo served at `/<project>/helm/<repo>/...`
- [x] **HELM-02**: Project members can upload `.tgz` chart archives; system parses `Chart.yaml` and stores the archive under `repos/<project>/helm/<repo>/`
- [x] **HELM-03**: System regenerates `index.yaml` from disk on every upload/delete via `helm.sh/helm/v3/pkg/repo`; per-repo mutex serializes regeneration
- [x] **HELM-04**: System pass-through-serves provenance `.prov` files when uploaded
- [x] **HELM-05**: Conformance verified via `helm repo add` + `helm pull` + `helm install --dry-run`

### RAW Protocol (RAW)

- [ ] **RAW-01**: RAW repo served at `/<project>/raw/<repo>/<path>` as a pass-through file store
- [ ] **RAW-02**: Project members can upload arbitrary files at any subpath via REST API or UI dropzone
- [ ] **RAW-03**: GET returns the file with correct `Content-Type` (best-effort via `mime.TypeByExtension` plus magic-number fallback) and `Content-Length`
- [ ] **RAW-04**: PUT to an existing path overwrites atomically; DELETE removes the file
- [ ] **RAW-05**: Directory listings (HTML or JSON via `Accept`) returned for collection paths

### Sync Jobs (SYNC)

- [ ] **SYNC-01**: System runs an in-process job dispatcher with two pools: a sync pool (default 4 workers) and a scan pool (default 2 workers)
- [ ] **SYNC-02**: Job rows live in `sync_jobs` and `scans` tables with `status`, `attempts`, `last_error`, `next_run_at` columns; dispatcher leases pending rows and updates status atomically
- [ ] **SYNC-03**: On boot, any `running` job older than 10 minutes is reset to `pending` for retry
- [ ] **SYNC-04**: Failed jobs retry with exponential backoff (1m, 5m, 30m, fail at 5 attempts)
- [x] **SYNC-05**: Project members can trigger a one-shot "sync from external URL" against an RPM, DEB, PyPI, or Helm repo; sync downloads the upstream index, fetches missing files (idempotent by checksum), and regenerates local metadata signed with the local key
- [ ] **SYNC-06**: Sync job log is captured in the `sync_jobs.log` field and viewable in the UI

### Vulnerability Scanning & SBOM (SCAN)

- [ ] **SCAN-01**: System invokes Trivy as a subprocess for every scan job; Trivy binary and DB are bundled in the container image
- [ ] **SCAN-02**: Trivy is always invoked with `--cache-dir /var/lib/omnirepo/trivy/cache --offline-scan --skip-db-update` (no automatic network)
- [ ] **SCAN-03**: System auto-scans on upload by default (per-repo `auto_scan` toggle, default on)
- [ ] **SCAN-04**: Project members can manually rescan any artifact via UI button or REST endpoint
- [ ] **SCAN-05**: Docker scans materialize the manifest + layers into a temporary OCI layout under `tmp/` and run `trivy image --input <dir>`; RPM/DEB/PyPI/RAW use `trivy rootfs` or `trivy fs` against an extracted tmp tree
- [ ] **SCAN-06**: Scan results are parsed via tolerant JSON decoding and inserted into `scans` and `vulnerabilities` rows; snapshot tests guard the parser against Trivy schema drift
- [ ] **SCAN-07**: Per-repo `block_on_severity` enum (`none|low|medium|high|critical`) blocks downloads of artifacts whose latest scan reports findings at or above the threshold; clients receive `403` with a clear message; `none` = advisory-only
- [ ] **SCAN-08**: System generates SBOM (CycloneDX by default; SPDX selectable) for Docker images and language packages on demand and stores them under `/var/lib/omnirepo/sboms/<scan-id>.json`
- [ ] **SCAN-09**: Super-admin can upload a Trivy DB tarball via UI (or `POST /api/v1/admin/trivy/db`); the system atomically swaps it into `/var/lib/omnirepo/trivy/db/`
- [ ] **SCAN-10**: Super-admin can trigger an online Trivy DB pull via UI button (or `POST /api/v1/admin/trivy/db/pull`); the request returns a clear error if the network is unavailable
- [ ] **SCAN-11**: Status page shows the active Trivy DB version, age, source (`baked-in`, `uploaded`, `online-pulled`), and a warning when the DB is older than a configurable threshold (default 7 days)
- [ ] **SCAN-12**: System tracks `blob_uploads` in-flight registry; GC excludes any digest in `blob_uploads` and any digest with `last_touched_at < now - 1h`

### Operations (OPS)

- [x] **OPS-01**: System writes an append-only `audit_log` row for every state-changing action (login attempt, user create/update/delete, project CRUD, repo CRUD, upload, delete, sync, scan, GC, TLS swap, maintenance toggle); fields include actor, target, IP, user-agent, outcome, structured details JSON
- [x] **OPS-02**: Audit log mirrored to `/var/lib/omnirepo/logs/audit.log` (NDJSON) for tail-friendly inspection
- [ ] **OPS-03**: Super-admin can browse, filter (by actor, target kind, time range, outcome), and paginate the audit log in the admin UI
- [ ] **OPS-04**: Per-project activity feed surfaces relevant audit events on the project detail screen
- [ ] **OPS-05**: Super-admin can toggle maintenance mode; while active, all write paths return `503 maintenance` and reads continue to serve
- [ ] **OPS-06**: Super-admin can trigger garbage collection (mark + sweep); orphan blobs and trash entries past retention are hard-deleted; CAS refcounts cross-checked
- [ ] **OPS-07**: Super-admin can browse trash and restore soft-deleted repos or files
- [x] **OPS-08**: Super-admin can upload a new TLS certificate + private key (PEM) via UI; system validates the pair and atomically swaps them; running connections are unaffected; new connections use the new certificate (hot reload via `tls.Config.GetCertificate` and `atomic.Pointer`)
- [ ] **OPS-09**: Super-admin can browse historical uploaded certificates under `/var/lib/omnirepo/certs/uploaded/`

### REST API (API)

- [ ] **API-01**: REST API served at `/api/v1/...`; OpenAPI 3.1 spec hand-written and committed at `internal/api/openapi.yaml`; `oapi-codegen/v2` generates Go types only (chi routes are hand-written)
- [ ] **API-02**: Swagger UI bundled (no CDN) and served at `/api/docs`
- [ ] **API-03**: Every endpoint requires authentication except `/api/v1/auth/login`, `/healthz`, `/readyz`
- [ ] **API-04**: List endpoints paginate via `?limit=&cursor=` (cursor-based)
- [ ] **API-05**: Upload endpoints stream the request body to disk; configurable max size; over-cap requests return `413`
- [ ] **API-06**: Endpoints exist (at minimum) for: auth (login, logout, change-password), projects (CRUD), members (add, remove), repos (CRUD per type), uploads (multipart per type), repo wipe, sync, Docker pull-external, Docker promote, scans (start, get, list, SBOM download), search, audit, profile, own API keys, admin users, admin TLS upload, admin Trivy DB upload + pull, admin GC, admin maintenance, admin trash + restore

### Search (SRCH)

- [ ] **SRCH-01**: System maintains FTS5 virtual tables: `repos_fts(name, description_md, project_name)`, `artifacts_fts(repo_id, artifact_name, version, tags)`, `cves_fts(cve_id, package, title, description)`
- [ ] **SRCH-02**: FTS rows are inserted/updated/deleted in the same transaction as the underlying entity (no out-of-band reindex)
- [ ] **SRCH-03**: `GET /api/v1/search?q=&kind=&severity=&project=` returns ranked results across repos, artifacts, and CVEs with type and severity filters
- [ ] **SRCH-04**: Search supports filename, image tag, checksum exact match, CVE ID, and partial-prefix queries

### Web UI (UI)

- [ ] **UI-01**: SPA built with React 19, TypeScript, Vite 8, Tailwind CSS 4 (CSS-first config), shadcn/ui 4, TanStack Query, React Router 7
- [ ] **UI-02**: SPA embedded in the Go binary via `//go:embed web/dist/*`; served at `/` with client-side-routing fallback to `index.html`
- [ ] **UI-03**: Dev mode (`OMNIREPO_DEV=1`) reverse-proxies non-API requests to a Vite dev server on `:5173` for HMR
- [ ] **UI-04**: All UI assets bundled: `lucide-react` SVG icons, `@dicebear/core` avatars, `swagger-ui-dist`, self-hosted Inter and JetBrains Mono `.woff2`; zero external CDN references at runtime
- [ ] **UI-05**: Login screen + forced-password-change screen; offline-friendly error states
- [ ] **UI-06**: Dashboard shows total + free storage, recent audit events, recent high-severity scan findings
- [ ] **UI-07**: Projects list and project detail (members, repos grouped by type, add repo, invite/remove user)
- [ ] **UI-08**: Per-type repo detail screens (file browser + upload dropzone, README markdown editor + preview, scan results filter, SBOM download, size/count stats, sync-from-URL form, wipe contents, soft delete, public-read toggle, scan-severity gate, Docker-only: pull external + promote tag + cosign badge)
- [ ] **UI-09**: Global search screen with type and severity filters; results link to source entities
- [ ] **UI-10**: Profile screen (edit email, change password, manage own API keys with one-time reveal, manage own S3 keys)
- [ ] **UI-11**: Admin screens (super-admin only): users CRUD, full audit log with filters, TLS cert upload, Trivy DB status + upload + online-pull, maintenance mode toggle, GC trigger, trash viewer with restore
- [ ] **UI-12**: Every repo detail screen includes a copy-to-clipboard "use this repo" snippet (e.g. `helm repo add`, `pip install --index-url`, `docker login`, `aws --endpoint-url`, `git clone`)
- [ ] **UI-13**: Dark mode default; light theme available via toggle

### Build & Air-Gap Invariants (AIR)

- [ ] **AIR-01**: Multi-stage Dockerfile: stage 1 `node:22-alpine` builds the SPA, stage 2 `golang:1.25-alpine` builds the Go binary with embedded SPA, stage 3 copies Trivy binary from the pinned Trivy image, stage 4 assembles `alpine:3.21` (pinned by digest) with the Go binary, Trivy binary, baked Trivy DB at `/opt/trivy-db/`, optional `git` binary, CA bundle, non-root user UID 1000
- [ ] **AIR-02**: Image is built for `linux/amd64` and `linux/arm64`
- [ ] **AIR-03**: First container start seeds `/var/lib/omnirepo/trivy/db/` from `/opt/trivy-db/` if empty
- [x] **AIR-04**: Runtime makes zero outbound network calls except when explicitly invoked by the user (sync from external, pull external Docker image, online Trivy DB pull, optional internet-reachability check button)
- [x] **AIR-05**: CI invariant test boots the container with `--network=none` and exercises the UI via Playwright; failure blocks merge
- [x] **AIR-06**: Build-time grep gate: `grep -rEI 'https?://(?!localhost|127\.0\.0\.1)' web/dist/` returns only self-references; failure breaks the build
- [x] **AIR-07**: No telemetry, no automatic update checks, no external error-reporting integrations in any artifact

### Tests (TEST)

- [x] **TEST-01**: Unit tests cover every package with real logic using table-driven Go tests and `t.TempDir()`; SQLite-backed tests use the project's reader/writer split helper
- [ ] **TEST-02**: Per-protocol conformance tests boot the full app on a random port and exercise it with the real client: `crane` (OCI), `aws-sdk-go-v2` (S3), `dnf` (RPM, DinD), `apt-get` (APT, DinD), `pip` and `uv pip` (PyPI), `helm` (Helm), `git` CLI (Git)
- [ ] **TEST-03**: API tests exercise every REST endpoint against a running server, asserting status code and response schema against the OpenAPI types
- [ ] **TEST-04**: Playwright end-to-end suite covers login, forced password change, project + repo create, upload, scan view, search, profile API key reveal, admin maintenance toggle, TLS cert upload, GC trigger, trash restore
- [ ] **TEST-05**: Bench target (`make bench`) measures upload + sync + scan throughput on a representative dataset
- [x] **TEST-06**: SQLite contention bench: 16 concurrent uploads across protocols, zero `SQLITE_BUSY`
- [ ] **TEST-07**: go-git v6 memory bench: 200 MB synthetic repo clone, RSS < 3× repo size
- [x] **TEST-08**: Air-gap invariant test (`--network=none`) is part of the standard `make test` matrix
- [x] **TEST-09**: `make test` is the merge gate; no feature lands without tests passing

## v2 Requirements

Deferred to future release. Tracked but not in current roadmap.

### Tenancy

- **V2-LDAP-01**: LDAP / OIDC / SSO integration (corporate identity providers)
- **V2-RBAC-01**: Tiered project roles (admin / member / viewer)
- **V2-2FA-01**: TOTP 2FA for UI login
- **V2-SSH-01**: SSH protocol for Git with per-user SSH keys
- **V2-PAT-01**: Scoped personal access tokens with explicit grants

### Repositories

- **V2-PROXY-01**: Upstream / proxy / virtual repositories
- **V2-CRON-01**: Scheduled sync (cron-style)
- **V2-RETAIN-01**: Retention policies (keep last N versions, delete older than N days)
- **V2-QUOTA-01**: Hard storage quotas per project
- **V2-MIRROR-01**: Cross-instance OmniRepo replication

### Operations

- **V2-WEBHOOK-01**: Outbound webhooks with HMAC signing and retries
- **V2-METRICS-01**: Prometheus `/metrics` endpoint
- **V2-EMAIL-01**: SMTP integration for notifications
- **V2-RATELIMIT-01**: Login rate limiting and account lockout
- **V2-SIGN-01**: Artifact signing for Docker, RPM, and DEB (we currently only verify)

### Protocols

- **V2-LFS-01**: Git LFS support (workaround in v1: use a RAW repo)
- **V2-RPMMOD-01**: RPM modularity metadata (`modules.yaml.gz` for AppStream)
- **V2-COSIGN-01**: Cosign signing for Docker (we only verify in v1)

## Out of Scope

| Feature | Reason |
|---------|--------|
| Self-registration of users | Admin-mediated user creation only; explicit user policy |
| S3-as-backend storage | Local filesystem is the only backing store; S3 is a served protocol, not a consumed one |
| Multi-process / multi-tenant database | Single SQLite file is sufficient for internal-tool scale |
| Non-Apache-2.0-compatible dependencies | Corporate license constraint (rules out MinIO AGPLv3, etc.) |
| Public-facing deployment hardening (DDoS, brute-force lockout) | Internal tool; threat model assumes corporate network |
| Multi-region / HA deployment | Single-instance deployment in v1 |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| FOUND-01 | Phase 1 | Complete |
| FOUND-02 | Phase 1 | Complete |
| FOUND-03 | Phase 1 | Complete |
| FOUND-04 | Phase 1 | Complete |
| FOUND-05 | Phase 1 | Complete |
| FOUND-06 | Phase 1 | Complete |
| FOUND-07 | Phase 1 | Complete |
| FOUND-08 | Phase 1 | Complete |
| FOUND-09 | Phase 1 | Complete |
| FOUND-10 | Phase 1 | Complete |
| FOUND-11 | Phase 1 | Complete |
| FOUND-12 | Phase 1 | Complete |
| FOUND-13 | Phase 1 | Complete |
| FOUND-14 | Phase 1 | Complete |
| TEN-01 | Phase 1 | Complete |
| TEN-02 | Phase 1 | Complete |
| TEN-03 | Phase 1 | Complete |
| TEN-04 | Phase 1 | Complete |
| TEN-05 | Phase 1 | Complete |
| TEN-06 | Phase 1 | Complete |
| TEN-07 | Phase 1 | Complete |
| TEN-08 | Phase 1 | Complete |
| TEN-09 | Phase 1 | Complete |
| TEN-10 | Phase 1 | Complete |
| TEN-11 | Phase 1 | Complete |
| TEN-12 | Phase 1 | Complete |
| TEN-13 | Phase 1 | Complete |
| TEN-14 | Phase 1 | Complete |
| TEN-15 | Phase 1 | Complete |
| TEN-16 | Phase 1 | Complete |
| TEN-17 | Phase 1 | Complete |
| KEY-01 | Phase 1 | Complete |
| KEY-02 | Phase 1 | Complete |
| KEY-03 | Phase 1 | Complete |
| KEY-04 | Phase 1 | Complete |
| KEY-05 | Phase 1 | Complete |
| KEY-06 | Phase 1 | Complete |
| KEY-07 | Phase 1 | Complete |
| KEY-08 | Phase 1 | Complete |
| S3K-01 | Phase 4 | Complete |
| S3K-02 | Phase 4 | Complete |
| S3K-03 | Phase 4 | Complete |
| S3K-04 | Phase 4 | Complete |
| S3K-05 | Phase 4 | Complete |
| BOOT-01 | Phase 1 | Complete |
| BOOT-02 | Phase 1 | Complete |
| BOOT-03 | Phase 1 | Complete |
| BOOT-04 | Phase 1 | Complete |
| BOOT-05 | Phase 1 | Complete |
| REPO-01 | Phase 1 | Complete |
| REPO-02 | Phase 1 | Complete |
| REPO-03 | Phase 1 | Complete |
| REPO-04 | Phase 1 | Complete |
| REPO-05 | Phase 2 | Pending |
| REPO-06 | Phase 1 | Complete |
| REPO-07 | Phase 2 | Pending |
| REPO-08 | Phase 1 | Complete |
| REPO-09 | Phase 2 | Pending |
| OCI-01 | Phase 2 | Pending |
| OCI-02 | Phase 2 | Pending |
| OCI-03 | Phase 2 | Pending |
| OCI-04 | Phase 2 | Pending |
| OCI-05 | Phase 2 | Pending |
| OCI-06 | Phase 2 | Pending |
| OCI-07 | Phase 2 | Pending |
| OCI-08 | Phase 2 | Pending |
| OCI-09 | Phase 2 | Pending |
| OCI-10 | Phase 2 | Pending |
| S3-01 | Phase 4 | Complete |
| S3-02 | Phase 4 | Complete |
| S3-03 | Phase 4 | Complete |
| S3-04 | Phase 4 | Complete |
| S3-05 | Phase 4 | Complete |
| S3-06 | Phase 4 | Pending |
| GIT-01 | Phase 4 | Complete |
| GIT-02 | Phase 4 | Complete |
| GIT-03 | Phase 4 | Complete |
| GIT-04 | Phase 4 | Complete |
| GIT-05 | Phase 4 | Complete |
| GIT-06 | Phase 4 | Complete |
| GIT-07 | Phase 4 | Pending |
| RPM-01 | Phase 3 | Complete |
| RPM-02 | Phase 3 | Complete |
| RPM-03 | Phase 3 | Complete |
| RPM-04 | Phase 3 | Complete |
| RPM-05 | Phase 3 | Complete |
| RPM-06 | Phase 3 | Complete |
| APT-01 | Phase 3 | Complete |
| APT-02 | Phase 3 | Complete |
| APT-03 | Phase 3 | Complete |
| APT-04 | Phase 3 | Complete |
| APT-05 | Phase 3 | Complete |
| APT-06 | Phase 3 | Complete |
| PYPI-01 | Phase 3 | Complete |
| PYPI-02 | Phase 3 | Complete |
| PYPI-03 | Phase 3 | Complete |
| PYPI-04 | Phase 3 | Complete |
| PYPI-05 | Phase 3 | Complete |
| PYPI-06 | Phase 3 | Complete |
| HELM-01 | Phase 3 | Complete |
| HELM-02 | Phase 3 | Complete |
| HELM-03 | Phase 3 | Complete |
| HELM-04 | Phase 3 | Complete |
| HELM-05 | Phase 3 | Complete |
| RAW-01 | Phase 2 | Pending |
| RAW-02 | Phase 2 | Pending |
| RAW-03 | Phase 2 | Pending |
| RAW-04 | Phase 2 | Pending |
| RAW-05 | Phase 2 | Pending |
| SYNC-01 | Phase 2 | Pending |
| SYNC-02 | Phase 2 | Pending |
| SYNC-03 | Phase 2 | Pending |
| SYNC-04 | Phase 2 | Pending |
| SYNC-05 | Phase 3 | Complete |
| SYNC-06 | Phase 5 | Pending |
| SCAN-01 | Phase 2 | Pending |
| SCAN-02 | Phase 2 | Pending |
| SCAN-03 | Phase 2 | Pending |
| SCAN-04 | Phase 2 | Pending |
| SCAN-05 | Phase 2 | Pending |
| SCAN-06 | Phase 2 | Pending |
| SCAN-07 | Phase 2 | Pending |
| SCAN-08 | Phase 2 | Pending |
| SCAN-09 | Phase 5 | Pending |
| SCAN-10 | Phase 5 | Pending |
| SCAN-11 | Phase 5 | Pending |
| SCAN-12 | Phase 2 | Pending |
| OPS-01 | Phase 1 | Complete |
| OPS-02 | Phase 1 | Complete |
| OPS-03 | Phase 5 | Pending |
| OPS-04 | Phase 5 | Pending |
| OPS-05 | Phase 5 | Pending |
| OPS-06 | Phase 2 | Pending |
| OPS-07 | Phase 5 | Pending |
| OPS-08 | Phase 1 | Complete |
| OPS-09 | Phase 5 | Pending |
| API-01 | Phase 5 | Pending |
| API-02 | Phase 5 | Pending |
| API-03 | Phase 5 | Pending |
| API-04 | Phase 5 | Pending |
| API-05 | Phase 5 | Pending |
| API-06 | Phase 5 | Pending |
| SRCH-01 | Phase 2 | Pending |
| SRCH-02 | Phase 3 | Pending |
| SRCH-03 | Phase 5 | Pending |
| SRCH-04 | Phase 5 | Pending |
| UI-01 | Phase 5 | Pending |
| UI-02 | Phase 5 | Pending |
| UI-03 | Phase 5 | Pending |
| UI-04 | Phase 5 | Pending |
| UI-05 | Phase 5 | Pending |
| UI-06 | Phase 5 | Pending |
| UI-07 | Phase 5 | Pending |
| UI-08 | Phase 5 | Pending |
| UI-09 | Phase 5 | Pending |
| UI-10 | Phase 5 | Pending |
| UI-11 | Phase 5 | Pending |
| UI-12 | Phase 5 | Pending |
| UI-13 | Phase 5 | Pending |
| AIR-01 | Phase 5 | Pending |
| AIR-02 | Phase 5 | Pending |
| AIR-03 | Phase 5 | Pending |
| AIR-04 | Phase 1 | Complete |
| AIR-05 | Phase 1 | Complete |
| AIR-06 | Phase 1 | Complete |
| AIR-07 | Phase 1 | Complete |
| TEST-01 | Phase 1 | Complete |
| TEST-02 | Phase 2 | Pending |
| TEST-03 | Phase 5 | Pending |
| TEST-04 | Phase 5 | Pending |
| TEST-05 | Phase 5 | Pending |
| TEST-06 | Phase 1 | Complete |
| TEST-07 | Phase 4 | Pending |
| TEST-08 | Phase 1 | Complete |
| TEST-09 | Phase 1 | Complete |

**Coverage:**
- v1 requirements: 167 total
- Mapped to phases: 167
- Unmapped: 0 ✓

---
*Requirements defined: 2026-04-14*
*Last updated: 2026-04-14 after initial definition*
