# OmniRepo

[![CI](https://github.com/VladoPortos/omnirepo/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/VladoPortos/omnirepo/actions/workflows/ci.yml)
[![CodeQL](https://github.com/VladoPortos/omnirepo/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/VladoPortos/omnirepo/actions/workflows/codeql.yml)
[![Trivy](https://github.com/VladoPortos/omnirepo/actions/workflows/trivy.yml/badge.svg?branch=main)](https://github.com/VladoPortos/omnirepo/actions/workflows/trivy.yml)
[![govulncheck](https://github.com/VladoPortos/omnirepo/actions/workflows/govulncheck.yml/badge.svg?branch=main)](https://github.com/VladoPortos/omnirepo/actions/workflows/govulncheck.yml)
[![Scorecard](https://github.com/VladoPortos/omnirepo/actions/workflows/scorecard.yml/badge.svg?branch=main)](https://github.com/VladoPortos/omnirepo/actions/workflows/scorecard.yml)
[![Dependabot](https://img.shields.io/badge/dependabot-enabled-025E8C?logo=dependabot)](https://github.com/VladoPortos/omnirepo/security/dependabot)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**A single self-hosted container that serves every artifact type your team produces or consumes — OCI images, RPM/APT/PyPI/Helm packages, Git repos, S3 buckets, and raw blobs — with built-in vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.**

Designed as a focused, simpler alternative to JFrog Artifactory or Sonatype Nexus for small-to-mid corporate and air-gapped environments.

---

## What OmniRepo is

One Go binary, one HTTP/HTTPS port, one mounted volume. Drop it on a host, point your build tools at it, and it speaks every major artifact protocol natively.

### Protocols served

| Protocol | Endpoint shape | Clients | Spec | Mirror |
|----------|----------------|---------|------|--------|
| **OCI/Docker Registry** | `/v2/...` | `docker`, `podman`, `crane`, `skopeo` | Distribution v2, Bearer JWT | Clone images from external registries (see "Clone an image") |
| **RPM/YUM repo** | `/<project>/rpm/<repo>/...` | `dnf`, `yum`, `zypper` | `repomd.xml` + signed `primary.xml.gz` | ✓ Mirror an upstream RPM repo with filters + credentials |
| **APT/Debian repo** | `/<project>/deb/<repo>/dists/<suite>/...` | `apt`, `apt-get` | `Release` + PGP `InRelease` | ✓ Mirror an upstream APT archive (per-suite / component / arch filter) |
| **PyPI** | `/<project>/pypi/<repo>/simple/...` | `pip`, `uv`, `twine` | PEP 503 + 691 + 694 uploads | ✓ Mirror a PEP 503 upstream (by project name) |
| **Helm** | `/<project>/helm/<repo>/index.yaml` | `helm` | Helm v3 `index.yaml` + `.tgz` + `.prov` | ✓ Mirror a Helm chart repo (HTTP `index.yaml`; `oci://` charts skipped gracefully) |
| **Raw blobs** | `/<project>/raw/<repo>/<path>` | `curl`, `wget`, any HTTP client | Plain HTTP with digest headers | — |
| **S3-compatible** | `/s3/<bucket>/<key>` with SigV4 | `aws-cli`, `s3cmd`, any AWS SDK | AWS SigV4 + gofakes3 backend | — |
| **Git hosting** | `/<project>/git/<repo>.git` | `git` CLI (Smart HTTP) | go-git v6 backend + gitkit fallback | — |

### Core features

- **Project-scoped access control** — flat user/project/member model with super-admin bypass. Every artifact lives under a project; project members get read+write, everyone else gets 403. Repos can opt into `public_read` for anonymous GET/HEAD.
- **API keys** — user-owned or project-owned, shown once at creation, hashed at rest, prefix-indexed for O(1) lookup.
- **Upstream mirror repos** — APT, RPM, PyPI, and Helm repos can be created as mirrors pointed at a real upstream (pypi.org, archive.ubuntu.com, charts.bitnami.com, etc.), with per-protocol allowlist filters (suite/component/arch for APT, project names for PyPI, chart names + globs for RPM/Helm), optional stored upstream credentials, and an in-UI **Sync now** button that streams byte-level progress and shows a `Sync complete · N files · X MB` confirmation pill when done. Native clients (`pip`, `apt`, `dnf`, `helm`) pull from OmniRepo's locally cached copy — no direct upstream fetch at install time.
- **Docker clone from external registries** — operators can pull a specific image tag (or digest) from `docker.io`, `quay.io`, or any public OCI registry and retag it into a local OmniRepo repo. Same sync-job progress UI as the package mirrors.
- **Vulnerability scanning** — embedded Trivy subprocess with an air-gapped baked DB. Per-repo `block_on_severity` gate can refuse pulls of artifacts whose latest scan found findings ≥ threshold. RPM cpio payloads and PyPI wheel transitive deps are unpacked before the scan so Trivy sees real filesystem entries. Scan-on-sync toggle per mirror repo.
- **Built-in web UI** — React 19 + Tailwind 4 + shadcn/ui + Radix, embedded in the Go binary via `//go:embed`. Zero runtime CDN. Includes a first-class S3 bucket browser (prefix drill-down, object table, delete flow), per-repo client snippets (ready-to-copy `docker pull`, `pip install`, `helm repo add`, `apt source.list` lines with your host pre-filled), dashboard composition cards, and shared empty-state surfaces for every "nothing here yet" scenario.
- **Structured error envelopes** — every `/api/v1` error response is a typed `ApiErrorEnvelope` with a UUID-v7 incident-id, error class (validation / auth / rate / transient / permanent), a machine-readable code, and optional field-level details. The UI renders them via a single `ErrorEnvelopeRenderer`.
- **Audit log** — every auth decision, upload, admin action, sync job, and settings change recorded in SQLite with an NDJSON file mirror.
- **Air-gap by default** — no outbound network calls without an explicit admin action. Trivy DB updates only via tarball upload or admin button. Mirror syncs only ever reach upstreams the operator configured. Enforced in CI by `make test-airgap` (boots the binary and verifies zero outbound network calls on its own).
- **Hot-reloadable TLS** — admin-uploaded certs swap into the live listener without restart.
- **Maintenance mode** — global read-only switch with an in-UI banner visible to all users.
- **Accessibility gates** — CI runs axe-core + WCAG AA contrast + typography + spacing + reduced-motion linters against every UI PR. No merge if any gate goes red.

---

## How it works

### Single binary, single port, single volume

Everything multiplexes on one HTTP/HTTPS port via a single `chi` router. Each protocol handler is embedded as an `http.Handler` — no sidecars, no reverse proxy required. All mutable state lives under one mounted volume (default `/var/lib/omnirepo`) holding:

```
/var/lib/omnirepo/
  db/omnirepo.sqlite      — metadata (users, projects, repos, scans, sync_jobs, audit log, mirror config, upstream creds)
  blobs/sha256/xx/...     — OCI content-addressed blob tree
  repos/<project>/...     — per-project artifact trees (rpm, deb, pypi, helm, raw, git)
  s3/<bucket>/...         — per-bucket S3 object storage (gofakes3 backend)
  sboms/                  — Trivy-produced SBOM JSON files
  trivy/db/               — baked Trivy vulnerability DB (updated via admin tarball upload)
  certs/server.{crt,key}  — live TLS pair (self-signed by default)
  certs/uploaded/<ts>/    — rollback archive of admin-uploaded certs
  tmp/                    — transient CAS staging (truncated on boot)
  logs/audit.log          — audit log NDJSON mirror (rotated per `log.audit_max_size_mb`)
  config/bootstrap.json   — first-boot seed (consumed once; see below)
```

### Tech stack

- **Go 1.25** — single static binary, pure-Go SQLite (`modernc.org/sqlite`), cross-compiled for `linux/amd64` + `linux/arm64`.
- **SQLite with FTS5** — one WAL-mode database for every metadata need (including full-text search across repos, artifacts, CVEs, and packages).
- **React 19 + Vite 8 + Tailwind 4 + shadcn/ui** — frontend built into `web/dist/` and embedded into the Go binary.
- **Apache-2.0-compatible dependencies only** — MinIO (AGPL) explicitly excluded; gofakes3 (MIT) used for S3 instead.

See the **[Dependencies, versions, and licenses](#dependencies-versions-and-licenses)** section below for the full matrix.

---

## Installation

### Docker (recommended)

Pre-built images are published to this repository's GitHub Container Registry:

```bash
docker run -d \
  --name omnirepo \
  -p 8080:8080 -p 8443:8443 \
  -v /srv/omnirepo:/var/lib/omnirepo \
  ghcr.io/vladoportos/omnirepo:latest
```

Available tags: `latest`, the pinned patch release (e.g. `1.0.0`), and the `1.0`
minor series (`linux/amd64`). Pin to a release tag for reproducible deploys:

```bash
docker pull ghcr.io/vladoportos/omnirepo:1.0.0
```

To build the image yourself instead, the `make docker` target tags it from
`git describe --tags` — `omnirepo:v1.0.0`, `omnirepo:v1.0.0-<N>-g<sha>` between
tags, or `omnirepo:dev` when no tags are reachable.

Browse to `https://<host>:8443/` (accepting the self-signed cert) or `http://<host>:8080/` in dev.

On first boot, OmniRepo:

1. Creates the data-root layout under `/var/lib/omnirepo/`.
2. Applies SQLite migrations.
3. If a `bootstrap.json` is present, ingests it (idempotent — skipped if any user already exists).
4. Generates a self-signed TLS cert.
5. Starts the HTTP + HTTPS listeners.

### Building from source

Requires Go 1.25 and Node 22+.

```bash
make vendor       # go mod vendor — one-time
make build-all    # builds frontend + go binary into bin/omnirepo
./bin/omnirepo serve --config /etc/omnirepo.yaml
```

### Dev mode (hot reload)

```bash
make dev
# Starts the Go server with OMNIREPO_DEV=1 + Vite dev server on :5173.
# Vite proxies /api, /v2, /s3, and /git to the Go server, so hot module
# reload works against the live backend for every protocol surface.
```

---

## Configuration

OmniRepo reads a YAML config (path via `--config` flag or `$OMNIREPO_CONFIG`). Everything has a sensible default; the file is optional for small deployments.

```yaml
server:
  http_port: 8080
  https_port: 8443
  hostname: omnirepo.example.com
  external_hostnames:        # used for WWW-Authenticate Bearer realm
    - omnirepo.example.com
    - artifacts.example.com
data_root: /var/lib/omnirepo
tls:
  cert_path: /var/lib/omnirepo/certs/server.crt
  key_path: /var/lib/omnirepo/certs/server.key
bootstrap:
  path: /var/lib/omnirepo/config/bootstrap.json
auth:
  session_ttl: 12h
  docker_jwt_ttl: 60m
  sigv4_skew: 15m
scan:
  auto_scan_default: true
  db_warn_age_days: 7
air_gap:
  allow_external_actions: true
log:
  level: info
  format: json
  audit_max_size_mb: 100
  audit_keep: 10
```

### First-boot bootstrap

Drop a `bootstrap.json` at the configured path to seed the super-admin (and optionally extra users, projects, repos, and API keys). Consumed exactly once; if any user row already exists, the whole file is skipped.

```json
{
  "schema_version": 1,
  "super_admin": {
    "login": "admin",
    "email": "admin@example.com",
    "password": "ChangeMe!",
    "must_change_password": true
  },
  "users": [
    {
      "login": "alice",
      "email": "alice@example.com",
      "password": "TempPass1!",
      "must_change_password": true
    }
  ],
  "projects": [
    { "name": "platform", "description_md": "Shared infra artifacts", "members": ["alice"] }
  ],
  "repos": [
    { "project": "platform", "type": "docker", "name": "images", "auto_scan": true, "public_read": false },
    { "project": "platform", "type": "pypi",   "name": "pkg",    "auto_scan": true, "block_on_severity": "HIGH" }
  ],
  "api_keys": [
    { "owner_kind": "user",    "owner": "alice",    "name": "ci",        "token": "omr_u_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" },
    { "owner_kind": "project", "owner": "platform", "name": "pipelines", "token": "omr_p_yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy" }
  ]
}
```

Field reference:

- **`super_admin`** (required): `login`, `email`, `password`, `must_change_password`.
- **`users`** (optional): additional users with the same four fields as `super_admin`.
- **`projects`** (optional): `name`, `description_md`, `members` (list of user logins that become project members).
- **`repos`** (optional): `project`, `type` (`docker`/`rpm`/`deb`/`pypi`/`helm`/`raw`/`git`), `name`, `description_md`, `auto_scan` (bool), `block_on_severity` (`LOW`/`MEDIUM`/`HIGH`/`CRITICAL`), `public_read` (bool).
- **`api_keys`** (optional): `owner_kind` (`user`/`project`), `owner` (user login or project name), `name`, `token` — the plaintext token string including its `omr_u_` / `omr_p_` prefix. Stored hashed at rest; plaintext is not recoverable after bootstrap consumption.

Mirror-repo configuration (`is_mirror`, `mirror_upstream_url`, `mirror_filter`,
`mirror_cred_id`, `scan_on_sync`) is **not** accepted in `bootstrap.json` —
create mirror repos via the REST API or the web UI after the first login so
upstream credentials can be encrypted with the runtime key.

---

## Getting started (5-minute tour)

### 1. Push an OCI image

```bash
docker login https://<host>:8443
# username: admin
# password: (from bootstrap)

docker tag nginx:latest <host>:8443/platform/docker/images/nginx:latest
docker push <host>:8443/platform/docker/images/nginx:latest
```

### 2. Publish a Python wheel

```ini
# ~/.pypirc
[omnirepo]
repository = https://<host>:8443/platform/pypi/pkg/legacy/
username = __token__
password = <api-key>
```

```bash
twine upload --repository omnirepo dist/*.whl
# Install side:
pip install --index-url https://<host>:8443/platform/pypi/pkg/simple/ mypackage
```

### 3. Serve an APT repo

Upload `.deb` files via the REST API, then on the client:

```bash
echo "deb https://<host>:8443/platform/deb/stable/ bookworm main" | \
  sudo tee /etc/apt/sources.list.d/omnirepo.list
sudo apt update
```

### 4. Push a Git repo

```bash
git remote add omnirepo https://<host>:8443/platform/git/myrepo.git
git push omnirepo main
# Project-owned API keys authenticate as user='project', password='<projname>:<key>'.
```

### 5. Create an S3 bucket

Buckets are project-scoped in OmniRepo, so bucket provisioning happens
through the REST API (or the **S3** tab on the project page in the web UI) —
the SigV4 protocol surface is only used for object operations. Two steps:

**a. Provision the bucket.** Open a project, switch to the **S3** tab,
and click **Create Bucket** — or hit the REST endpoint directly:

```bash
curl -X POST https://<host>:8443/api/v1/projects/platform/s3-buckets/ \
  -H 'Content-Type: application/json' \
  -b /tmp/omni.cookies \
  -d '{"name":"my-bucket"}'
```

**b. Mint an S3 access key** for that project (UI: project page → S3 Keys,
or `POST /api/v1/projects/<name>/s3-access-keys`). The secret is shown
exactly once.

**c. Use any AWS SDK / CLI for objects.** Path-style addressing, SigV4
required (no anonymous access):

```bash
aws configure set default.s3.signature_version s3v4
AWS_ACCESS_KEY_ID=AKIA... AWS_SECRET_ACCESS_KEY=... \
  aws --endpoint-url https://<host>:8443/s3 s3 cp file.bin s3://my-bucket/
aws --endpoint-url https://<host>:8443/s3 s3 ls s3://my-bucket/
```

The bucket detail page at `/projects/<project>/s3/<bucket>` lists every
object with prefix drill-down. Deleting a bucket via the UI requires it
to be empty; object deletes are synchronous (no trash path).

### 6. Mirror a package upstream (APT / RPM / PyPI / Helm)

Mirror repos proxy an upstream package index into OmniRepo's local cache
so native clients (`pip`, `apt`, `dnf`, `helm`) pull from your host, not
from the internet. Configured once, sync on demand, scan on sync.

**a. (Optional) Store upstream credentials** — only if the upstream
needs Basic auth. Username + password are encrypted at rest with the
runtime key. Host-bound so a leaked cred can't be redirected.

```bash
curl -u admin:<pw> -X POST \
  https://<host>:8443/api/v1/projects/platform/upstream-creds \
  -H 'Content-Type: application/json' \
  -d '{
    "kind":     "apt",
    "host":     "archive.ubuntu.com",
    "username": "mirror-bot",
    "password": "<secret>"
  }'
# → { "id": 7, "kind": "apt", "host": "archive.ubuntu.com", ... }
```

Valid `kind` values: `apt`, `rpm`, `pypi`, `helm`, `docker` (5 total —
`deb` is rejected with a 400 `validation.invalid_cred_kind` envelope
redirecting to `apt`). For token-based registries send `token` instead
of `username` + `password`.

Omit this step for public mirrors (`pypi.org`, `charts.bitnami.com`).

**b. Create the mirror repo** — set `is_mirror: true`, point at the
upstream URL, and supply a protocol-specific filter so you don't
accidentally sync the whole archive:

```bash
# APT mirror — pull Ubuntu focal main/amd64 only
curl -u admin:<pw> -X POST \
  https://<host>:8443/api/v1/projects/platform/repos \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "ubuntu",
    "type": "deb",
    "is_mirror": true,
    "mirror_upstream_url": "https://archive.ubuntu.com/ubuntu",
    "mirror_cred_id": 7,
    "mirror_filter": {
      "Suites":     ["focal"],
      "Components": ["main"],
      "Arches":     ["amd64"]
    },
    "scan_on_sync": false
  }'

# PyPI mirror — allowlist specific projects
curl -u admin:<pw> -X POST \
  https://<host>:8443/api/v1/projects/platform/repos \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "pkg",
    "type": "pypi",
    "is_mirror": true,
    "mirror_upstream_url": "https://pypi.org/simple/",
    "mirror_filter": { "Names": ["requests", "urllib3", "six"] },
    "scan_on_sync": true
  }'

# Helm mirror — chart-name filter
curl -u admin:<pw> -X POST \
  https://<host>:8443/api/v1/projects/platform/repos \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "charts",
    "type": "helm",
    "is_mirror": true,
    "mirror_upstream_url": "https://charts.bitnami.com/bitnami",
    "mirror_filter": { "Names": ["memcached", "redis"] }
  }'
```

Filter shapes per protocol: APT takes `Suites`, `Components`, `Arches`;
RPM takes `Names` + `Globs`; PyPI and Helm take `Names` (PEP 503 /
chart name) + optional `Globs`. Empty filter = sync everything (not
recommended for public upstreams).

**c. Trigger a sync** — POST with an empty body; the mirror config is
read from the repo row:

```bash
curl -u admin:<pw> -X POST \
  https://<host>:8443/api/v1/projects/platform/repos/deb/ubuntu/sync \
  -H 'Content-Type: application/json' \
  -d '{}'
# → 202 { "job_id": 42, "kind": "apt_sync" }
```

You can also click **Sync now** on the repo detail page. The UI shows
a byte-level progress line during the sync and a `Sync complete · N
files · X MB` confirmation pill when it finishes (8-second auto-dismiss).

**d. Poll progress** (for scripts):

```bash
curl -u admin:<pw> \
  https://<host>:8443/api/v1/projects/platform/repos/deb/ubuntu/sync-jobs/42
# → { "status": "running",
#     "progress_bytes": 8388608,  "total_bytes": 67108864,
#     "files_synced": 0,          "current_step": "pulling libc6_2.31-0ubuntu9.16_amd64.deb",
#     ... }
# → eventually { "status": "done",
#     "progress_bytes": 67108864, "total_bytes": 67108864,
#     "files_synced": 847,        "current_step": "done", ... }
```

**e. Point a client at OmniRepo.** Now `pip`, `apt`, `dnf`, `helm` pull
from your host instead of the public internet — use the auth shapes
from the [Authentication section](#authentication-for-protocol-endpoints)
below.

### 7. Clone a Docker image from an external registry

OmniRepo can pull an image tag (or digest) from `docker.io`, `quay.io`,
or any public OCI registry and store it locally under a repo of your
choice. Use the **Clone image** dialog on a Docker repo's page, or the
REST endpoint:

```bash
curl -u admin:<pw> -X POST \
  https://<host>:8443/api/v1/projects/platform/repos/docker/images/pull-external \
  -H 'Content-Type: application/json' \
  -d '{
    "src_image": "docker.io/library/nginx:1.27",
    "dst_tag":   "nginx:1.27"
  }'
# → 202 { "job_id": 43, "kind": "pull_external" }
```

Same sync-job progress UI as the package mirrors. Upstream credentials
(for private registries) use the same `/upstream-creds` endpoint with
`kind: "docker"`.

---

## Authentication for protocol endpoints

The browser session cookie authenticates the **web UI** and the
**`/api/v1/...` REST API** only. Every other surface — the native
package-manager endpoints exposed at `/{project}/pypi/{repo}/…`,
`/{project}/helm/{repo}/…`, `/{project}/rpm/{repo}/…`,
`/{project}/deb/{repo}/dists/…`, `/{project}/raw/{repo}/…`, plus the
OCI registry at `/v2/` and S3 at `/s3/…` — uses **HTTP Basic auth or
an API key**. Session cookies are ignored there, because `pip`,
`helm`, `docker`, `apt`, `rpm`/`dnf`, and AWS SDKs have no way to send
one.

If `curl https://<host>/platform/pypi/<repo>/simple/` returns 401 even
after you've logged into the web UI, that's expected. Send credentials
in `-u` / `Authorization: Basic …` instead.

### Three credential shapes

| Shape | Basic-auth form | Use case |
|---|---|---|
| User password | `-u <login>:<password>` | Interactive, not recommended for automation |
| User API key | `-u <any-login>:omr_u_…` (or `-u __token__:omr_u_…`) | CI, per-user tokens. The login field is ignored — the `omr_` prefix is how the middleware routes |
| Project API key | `-u project:<projname>:omr_p_…` | Per-project tokens (Go's `BasicAuth` splits on the first `:`, so the projname lands in the password field alongside the key) |

Examples:

```bash
# PyPI install with a user API key (pip convention: login = __token__)
pip install --index-url \
  https://__token__:omr_u_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx@<host>:8443/platform/pypi/pkg/simple/ mypackage

# Helm repo add with password Basic auth
helm repo add omnirepo https://<host>:8443/platform/helm/charts \
  --username alice --password '<password>'

# APT InRelease fetch with a project-scoped API key
echo "deb https://project:platform:omr_p_xxxxxxxxxxxxxxxx@<host>:8443/platform/deb/stable bookworm main" | \
  sudo tee /etc/apt/sources.list.d/omnirepo.list
```

Mint API keys from **Profile → API Keys** (user-owned) or **Project →
Settings → API Keys** (project-owned). Each key's plaintext is shown
exactly once; OmniRepo stores a SHA-256 hash with a prefix index.

### Anonymous reads (`public_read=true`)

Repos created with the `public_read` flag skip auth for GET/HEAD —
native clients can pull without credentials, e.g. `pip install` against
a public mirror. Writes always require an authenticated actor.
Middleware chain: `AnonymousReadOK → skipIfActor(BasicOrAPIKey)`.

### Two exceptions

- **OCI (`/v2/`)**: Docker clients follow the Bearer-token dance.
  `/v2/` returns `401 WWW-Authenticate: Bearer realm="…/v2/token"`;
  the client exchanges Basic creds (any of the three shapes above) at
  `/v2/token` for a 60-minute HS256 JWT, then uses the Bearer token
  for subsequent manifest/blob requests. `docker login` handles this
  transparently.
- **S3 (`/s3/`)**: AWS SigV4 only — no Basic auth. Mint a
  project-scoped S3 access key + secret via **Project → S3 Keys** (or
  `POST /api/v1/projects/<name>/s3-access-keys`) and pass them through
  the standard `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`
  environment variables. The secret is shown exactly once.

---

## Common operations

| Task | How |
|------|-----|
| Web UI | `https://<host>:8443/` — dashboard, projects, search, admin panels, profile, S3 bucket browser, per-repo mirror settings + snippets |
| REST API | `/api/v1/...` — OpenAPI 3.1 spec at `/api/docs/` (Swagger UI) |
| Health checks | `GET /healthz`, `GET /readyz` (no auth) |
| Trigger garbage collection | `POST /api/v1/admin/gc` (super-admin). Poll `GET /api/v1/admin/gc/status` for state (`idle`/`pending`/`running`/`done`/`failed` + `bytes_freed`) |
| Provision S3 bucket | `POST /api/v1/projects/<name>/s3-buckets/` or UI → project → S3 tab → Create Bucket |
| Browse bucket objects | `GET /api/v1/projects/<name>/s3-buckets/<bucket>/objects?prefix=…&marker=…&limit=…` or navigate to `/projects/<name>/s3/<bucket>` in the UI |
| Mint S3 access key | `POST /api/v1/projects/<name>/s3-access-keys` or UI → project → S3 Keys. Secret shown once |
| Create / edit upstream credentials | `POST/PATCH/DELETE /api/v1/projects/<name>/upstream-creds` or UI → project → Upstream Creds. Host-bound, encrypted at rest |
| Trigger mirror sync | `POST /api/v1/projects/<name>/repos/<type>/<repo>/sync` with empty body (mirror repo) or `{"upstream_url":...,"filter":...}` (non-mirror one-shot). UI → repo page → **Sync now** |
| Check sync progress | `GET /api/v1/projects/<name>/repos/<type>/<repo>/sync-jobs` (list) or `/sync-jobs/{id}` (detail: `progress_bytes`, `total_bytes`, `current_step`, `files_synced`, `status`). UI polls every 500 ms while running |
| Docker clone from external registry | `POST /api/v1/projects/<name>/repos/docker/<repo>/pull-external` with `{"src_image":"docker.io/library/nginx:1.27","dst_tag":"nginx:1.27"}`. UI → Docker repo page → **Clone image** dialog |
| Apply migrations manually | `omnirepo migrate up --config <path>` |
| Rotate TLS cert | Upload PEM pair via Admin → TLS Certificates; hot-swapped |
| Update Trivy DB | Upload DB tarball via Admin → Trivy Database; or enable the refresh button (requires `air_gap.allow_external_actions: true`) |
| Enter maintenance mode | Admin → Maintenance → toggle. Banner appears for all logged-in users |
| Force user password reset | Admin → Users → edit → "Force Password Reset" toggle; optionally set a temp password in the dialog. Existing sessions for that user are revoked |
| Copy client snippet | Navigate to any repo's detail page — the **Usage** panel renders a ready-to-copy `docker pull`, `pip install`, `helm repo add`, or `apt source.list` line pre-filled with your host, project, repo, and tag/version |

---

## Security notes

- **Passwords**: Argon2id (`m=64MiB, t=3, p=4`). `bcrypt` is not used.
- **API keys**: prefix-indexed SHA-256; plaintext shown once at creation.
- **Sessions**: opaque 32-byte tokens, HttpOnly + Secure + SameSite cookies, 12 h sliding + 7 d hard cap. Self-service password change preserves the current session and invalidates every other session for that user. Admin-forced resets revoke every session for the user.
- **Docker JWT**: HS256 with a per-install random 32-byte secret stored in the `settings` table; 60-minute TTL by default.
- **S3 SigV4**: full verifier with clock-skew window; multi-chunk streaming supported; `v4` signatures only.
- **TLS**: min version 1.2, self-signed on first boot, hot-reloadable. The Docker Bearer realm uses `server.external_hostnames[0]` when configured (closes Host-header injection).
- **Air-gap invariant**: no outbound HTTP from the binary without an explicit admin-user action. Enforced in CI via `make test-airgap`, which boots the binary and verifies it makes no outbound network calls on its own.

---

## Testing

```bash
make test               # unit + integration tests (in-memory + on-disk SQLite).
                        # Implicitly runs the visual-lint gates as a prereq:
                        #   lint-protocol-redaction, check-contrast,
                        #   lint-typography, lint-spacing-carveout, lint-axe-devdep
make test-airgap        # asserts the air-gap invariant at runtime (boots the
                        # binary, verifies zero outbound network calls on its own)
make bench              # perf benchmarks (bench-sqlite + bench-git + bench-throughput)
make conformance-all    # DinD conformance: real dnf/apt/pip/helm/git/crane clients
make lint               # golangci-lint (errcheck, govet, ineffassign, staticcheck, unused)
```

Optional live-server walkthroughs (skip automatically when env vars are unset):

```bash
# S3 SigV4 + CRUD + multipart suite (used during release verification)
OMNI_S3_ENDPOINT=http://localhost:8080 \
OMNI_S3_BUCKET=my-bucket \
OMNI_S3_AKID=AKIA... OMNI_S3_SECRET=... \
go test -tags=walkthrough -count=1 -v ./test/walkthrough/...
```

Frontend:

```bash
cd web
npm run lint            # ESLint (React hooks, react-refresh, typescript-eslint)
npx tsc --noEmit        # TypeScript type-check
npm test                # vitest unit + component tests (82 tests at v1.2)
npx playwright test     # E2E (54 specs at v1.2 — set OMNI_E2E_HTTP_PORT /
                        # OMNI_E2E_HTTPS_PORT / OMNI_E2E_BASE_URL if 8080/8443
                        # are held by other processes)
```

**CI gates** (`.github/workflows/ci.yml`, all required for merge):

| Job | What it runs |
|---|---|
| `build-test` | `make lint` → `make build` → `make test` (includes visual linters) → `make test-airgap` → `make bench-sqlite` |
| `conformance-oci` | OCI Distribution conformance suite driven by a real `crane` client against the running binary |
| `git-conformance` | Real `git` client clone/push/pull against both `go-git/v6` and `gitkit` fallback backends |
| `bench-git` | Streaming-clone memory-ceiling benchmark |

---

## Architecture at a glance

```
                          ┌────────────────┐
                          │  chi router    │  one port, one handler tree
                          └───────┬────────┘
                                  │
      ┌───────┬────────┬──────────┼──────────┬───────┬──────┬──────┐
      ▼       ▼        ▼          ▼          ▼       ▼      ▼      ▼
   /v2/*   /api/v1  /<p>/rpm   /<p>/deb   /<p>/pypi /<p>/  /<p>/  /<bkt>
   (OCI)   (admin)  (RPM)      (APT)      (PyPI)   helm   git    (S3)
      │       │        │          │          │       │      │      │
      └───────┴────────┴──────────┴──────────┴───────┴──────┴──────┘
                                  │
                                  ▼
                   ┌─────────────────────────────┐
                   │   Shared storage layer      │
                   │  - SQLite (modernc/sqlite)  │
                   │  - CAS blob tree (sha256)   │
                   │  - Per-project PathStore    │
                   │  - Audit: DB + NDJSON       │
                   └─────────────────────────────┘
```

Every protocol handler plugs into the same `chi` router and shares the same SQLite writer. Storage primitives (`storage.WriteAndRename`, `storage.CAS`, `storage.PathStore`) guarantee atomic renames with fsync'd parent directories.

---

## Project layout

```
cmd/omnirepo/             — main binary entry point + CLI (serve, migrate)
internal/
  api/                    — REST API handlers (admin, dashboard, projects, search, sync, upstream-creds, …)
  app/                    — server composition (Run, bootstrap, TLS, jobs wiring, router assembly)
  audit/                  — audit event types + logger + NDJSON mirror
  auth/                   — Actor, password hashing (argon2id), policy engine, JWT, middleware (session, basic+api-key)
  config/                 — YAML config schema + koanf loader + defaults
  crypto/                 — AEAD helpers for upstream credentials at-rest encryption
  httperr/                — canonical ApiErrorEnvelope + error-class taxonomy + incident-id (UUID-v7)
  httpx/                  — shared HTTP helpers (reserved-prefix guard, anonymous-read middleware, sync REST wiring, readyz/healthz handlers)
  jobs/                   — scan pool, sync pool, GC, throttled progress writer, counting reader
  metadata/               — SQLite repos (users, projects, repos, blobs, sessions, sync_jobs, upstream_creds, …) + migrations
  metadata/migrations/    — 025+ SQL migrations, applied at startup
  protocol/
    oci/                  — /v2 Docker Registry handler + pull-external (clone) orchestration
    rpm/                  — .rpm upload + repomd/primary regen + detached-sig + upstream mirror sync
    deb/                  — .deb upload + Packages/Release/InRelease + upstream mirror sync
    pypi/                 — PEP 503/691/694 upload + simple index + upstream mirror sync
    helm/                 — chart upload + index.yaml regen + upstream mirror sync (oci:// skipped)
    raw/                  — raw-blob CRUD
    s3/                   — gofakes3 backend + SigV4 verifier + access-key mgmt
    git/                  — go-git v6 backend + gitkit fallback
    protocoltest/         — shared protocol test harness (fake upstream, fixtures)
    regen/                — metadata-regen coalescer (batches repo-state recomputation)
  scan/                   — Trivy subprocess wrapper + SBOM plumbing
  storage/                — CAS, PathStore, atomic rename helpers, fsync'd parent dirs
  tls/                    — cert holder + hot-reload + upload handler
web/                      — React SPA (embedded into binary via go:embed)
  web/e2e/                — Playwright specs (mirror, sync, clone, auth, …)
test/
  airgap/                 — boots the binary, asserts zero outbound network calls on its own
  walkthrough/            — optional live-server SigV4 / CRUD / multipart suite (env-var-gated)
```

---

## Dependencies, versions, and licenses

Every dependency below is **Apache-2.0-compatible** — a hard constraint for
corporate deployment. **Exact pins live in `go.mod` and `web/package.json`**
— those are the source of truth. The tables below capture license + purpose
+ current major-version baseline at v1.2 ship. No CDN fonts, icons, or
Swagger assets — everything ships in the binary.

### Go core (single binary, pure-Go)

| Dependency | Pinned version | License | Purpose |
|---|---|---|---|
| Go toolchain | **1.25.x** | BSD-3 | `go-git/v6` requires Go 1.25; project baseline. Release build uses `-trimpath -ldflags="-s -w"`. |
| `github.com/go-chi/chi/v5` | v5.2.5 | MIT | HTTP router. Every protocol handler mounts as an `http.Handler` — no adapter glue. |
| `modernc.org/sqlite` | v1.48.2 (SQLite 3.51.x) | BSD-3 | Pure-Go SQLite with FTS5. Builds `FROM scratch`; no CGo, no `musl-dev`. |
| `github.com/google/go-containerregistry` (`pkg/registry`) | v0.21.5 | Apache-2.0 | OCI/Docker registry `http.Handler` with pluggable CAS. |
| `github.com/go-git/go-git/v6` | v6.0.0-alpha.1 / main (`backend/` pkg) | Apache-2.0 | Git Smart HTTP `http.Handler`; bare-repo CRUD. `sosedoff/gitkit` (MIT) kept as fallback behind the same interface. |
| `github.com/johannesboyne/gofakes3` | v1.0.0 | MIT | S3 `http.Handler` (multipart, versioning, CORS, conditional writes). SigV4 verification added as ~200 LOC middleware. |
| `github.com/cavaliergopher/rpm` | v1.3.0 | BSD-3 | `.rpm` header parsing. cpio payload walked by `github.com/cavaliergopher/cpio` for Trivy scan. |
| `github.com/ProtonMail/go-crypto` | v1.4.1 | BSD-3 | OpenPGP clearsign for APT `Release` → `InRelease`. Shared transitively with `go-git/v6`. |
| `helm.sh/helm/v3/pkg/repo` + `pkg/chart/loader` | v3.20.x | Apache-2.0 | Helm `index.yaml` regen. Staying on v3 SDK through v1.0. |
| `github.com/knadh/koanf/v2` | v2.3.4 | MIT | YAML + env config. Modular, no global state. |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | MIT | HS256 JWT for `/v2/token` Docker exchange. 60-min TTL; per-install secret. |
| `golang.org/x/crypto/argon2` | tracks Go ecosystem | BSD-3 | Password hashing: `m=64MiB, t=3, p=4`. |
| `github.com/oapi-codegen/oapi-codegen/v2` (CLI) + `github.com/oapi-codegen/runtime` | CLI v2.6.0 (installed via `go install`, build-time only); runtime v1.4.0 in `go.mod` | Apache-2.0 | CLI generates Go types from the hand-written OpenAPI 3.1 spec; runtime package provides a tiny helper shim. Routes are hand-written on chi. |
| `github.com/google/uuid` | v1.6.x | BSD-3 | Scan IDs, SBOM IDs, API-key IDs. |
| `github.com/opencontainers/go-digest` | v1.0.0 | Apache-2.0 | Canonical `sha256:<hex>` digest parsing. |
| PyPI PEP 503 Simple | stdlib `html/template` | — | Two HTML pages, no library needed. |
| **Trivy** (external binary) | v0.69.3 | Apache-2.0 | Subprocess in final image. Invoked with `--skip-db-update --offline-scan`. Wrapped behind a `Runner` interface. |
| `git` binary (fallback) | Alpine repo | GPLv2 *(not linked; invoked as subprocess only)* | Safety net behind `sosedoff/gitkit` if go-git v6 HTTP backend regresses. Default path is pure-Go. |

### Frontend (bundled; zero CDN at runtime)

Exact pins in `web/package.json`. Versions below are current at v1.2 ship.

| Dependency | v1.2 version | License | Purpose |
|---|---|---|---|
| React | 19.1.0 | MIT | SPA framework. |
| TypeScript | 5.8.3 | Apache-2.0 | `"moduleResolution": "bundler"`, `"jsx": "react-jsx"`. |
| Vite | 6.3.3 | MIT | Dev server + production build. Dev proxy forwards `/api`, `/v2`, `/s3`, `/git` to the Go backend when `OMNIREPO_DEV=1`. |
| Tailwind CSS | 4.1.4 | MIT | Requires `@tailwindcss/vite` plugin (CSS-first config). |
| shadcn CLI | ^4.2.0 | MIT | Generates Radix-based components into `web/src/components/ui/`. No runtime package. |
| @base-ui/react | ^1.4.0 | MIT | Base UI primitives complementing shadcn (used for some newer components). |
| TanStack Query | @tanstack/react-query 5.74.4 | MIT | Server-state cache. Query keys: `['projects', name, 'buckets', …]`. |
| React Router | react-router-dom 7.5.2 | MIT | Data-router API (`createBrowserRouter`). |
| lucide-react | ^0.487.0 | ISC | Tree-shaken SVG icons. |
| @dicebear/core + @dicebear/collection | 9.2.2 | MIT | Client-side SVG avatars from a seed string. |
| swagger-ui-dist | 5.21.0 | Apache-2.0 | Served from `/api/docs/`. Copied into `web/public/swagger/` at build time (`npm run copy-swagger` prebuild hook). |
| Milkdown (`@milkdown/kit`, `@milkdown/react`, `@milkdown/theme-nord`) | ^7.20.0 | MIT | Markdown editor for repo / project descriptions. |
| shiki | ^4.0.2 | MIT | Syntax highlighting in the web UI (code snippets, audit detail diffs). |
| framer-motion | ^12.38.0 | MIT | Motion primitives for UI transitions (respects `prefers-reduced-motion`). |
| sonner | ^2.0.7 | MIT | Toast notifications (error envelopes, success confirmations). |
| cmdk | ^1.1.1 | MIT | Command palette / fuzzy-search primitive. |
| dompurify (+ `@types/dompurify`) | ^3.4.0 | Apache-2.0 or MPL-2.0 (dual) | HTML sanitiser for Milkdown-rendered markdown. |
| next-themes | ^0.4.6 | MIT | Light/dark theme toggle. |
| react-diff-viewer-continued | ^4.2.0 | MIT | Diff view for audit log field-level changes. |
| class-variance-authority + clsx + tailwind-merge + tw-animate-css | current | MIT | Tailwind utility helpers shadcn templates depend on. |
| Inter + JetBrains Mono (`.woff2`) | Inter 4.x, JetBrains Mono 2.304+ | SIL OFL 1.1 | Self-hosted fonts. |
| Playwright | @playwright/test ^1.59.1 | Apache-2.0 | Developer-run E2E + pre-merge UI verification (54 specs at v1.2). |
| @axe-core/playwright | ^4.11.2 | MPL-2.0 | WCAG AA axe-core gate, scoped to individual UI regions in Playwright specs. |
| Vitest | ^4.1.4 | MIT | Frontend unit + component tests (82 tests at v1.2). `npm test`. |

### Build / dev tooling

| Tool | Purpose |
|---|---|
| `make` + `Makefile` | Orchestrates `dev`, `build`, `docker`, `test`, `e2e`, `seed`, `vendor`. |
| `air` (cosmtrek/air) v1.49+ | Go live-reload during `make dev`. |
| `golangci-lint` v1.62+ | `gofmt`, `govet`, `staticcheck`, `errcheck`, `gosec`, `revive`. Runs in `make test`. |
| `go mod vendor` | `vendor/` is committed so Docker's Go-build stage works in partially air-gapped envs. |
| `govulncheck` (`golang.org/x/vuln/cmd/govulncheck`) | Supply-chain vuln scan for OmniRepo's own Go deps. CI-gated. |
| `trivy fs .` (in CI) | Same Trivy binary we ship — dogfooded against the repo. |
| Docker Buildx | Multi-arch `linux/amd64` + `linux/arm64` images. |

### License summary (ship-affecting)

- **Apache-2.0** — go-containerregistry, go-git, Helm SDK, oapi-codegen
  (CLI + runtime), opencontainers/go-digest, swagger-ui-dist, Trivy,
  Playwright, TypeScript, dompurify.
- **BSD-3-Clause** — modernc.org/sqlite, cavaliergopher/rpm,
  ProtonMail/go-crypto, golang.org/x/crypto, google/uuid.
- **MIT** — chi, gofakes3, koanf, golang-jwt, sosedoff/gitkit (fallback),
  React, Vite, Tailwind, shadcn, @base-ui/react, @dicebear/\*, TanStack
  Query, React Router, Milkdown, shiki, framer-motion, sonner, cmdk,
  next-themes, react-diff-viewer-continued, class-variance-authority,
  clsx, tailwind-merge, tw-animate-css, Vitest.
- **ISC** — lucide-react.
- **MPL-2.0** — @axe-core/playwright (runs only in developer/CI Playwright
  suites; no ship exposure — axe-core is not bundled into the production
  SPA).
- **SIL OFL 1.1** — Inter, JetBrains Mono (OFL permits embedding +
  redistribution).
- **GPLv2** — the `git` binary in the final image; not linked, only
  invoked as a subprocess by the optional `gitkit` fallback. The default
  runtime path uses pure-Go `go-git` and does not execute `git`.

### Explicitly not used

| Rejected | Reason |
|---|---|
| MinIO | AGPLv3 infects the project; also uses `internal/` packages that block embedding. `gofakes3` + SigV4 middleware replaces it. |
| `containers/image` | CGo + libgpgme/libdevmapper required. `go-containerregistry` covers both sides. |
| `mattn/go-sqlite3` | CGo breaks `FROM scratch`. `modernc.org/sqlite` instead. |
| `golang-jwt/jwt` v4 | EOL. v5 only. |
| `golang.org/x/crypto/openpgp` | Removed from x/crypto. `ProtonMail/go-crypto/openpgp` is the drop-in replacement. |
| `spf13/viper` | Heavy deps, global state, case-insensitive keys. `koanf/v2` instead. |
| `gorilla/mux`, `gin-gonic/gin`, `labstack/echo` | Not `net/http`-native; would force adapters around every embedded registry/git/S3 handler. |
| `bcrypt` | 72-byte input cap, no memory-hardness. Argon2id instead. |
| Trivy imported as a Go library | ~400 transitive deps; Trivy's Go API is not stable. Subprocess it. |
| `gocloud.dev/blob` | Adds abstraction for backends we don't support (S3/GCS/Azure). Direct filesystem under `/var/lib/omnirepo/`. |
| CDN-hosted fonts / icons / Swagger assets | Violates air-gap invariant. Everything is embedded. |
| LDAP / OIDC in v1 | Out of scope. Built-in users + session cookies + API keys. |

### Compatibility notes

- **Go 1.25 + modernc.org/sqlite v1.48 + FTS5** — FTS5 is compiled in by
  default (v1.47+); startup verifies with `sqlite3_compileoption_used`.
- **go-git/v6 + ProtonMail/go-crypto v1.4.1** — pinned to exactly v1.4.1
  to avoid `go mod tidy` drift (shared transitive dep).
- **go-git/v6 + gofakes3** — both currently pinned to specific commit
  hashes rather than tagged releases (go-git/v6 is pre-GA alpha;
  gofakes3 carries post-v1.0.0 fixes we need). Upgrade audits happen per
  milestone; see `go.mod` for exact pseudo-versions.
- **Tailwind 4 + Vite 6 + shadcn@4** — Tailwind 4 requires
  `@tailwindcss/vite`; do not mix with Tailwind 3 docs found online.
- **React 19.1 + React Router 7 + TanStack Query 5** — all support
  React 19 natively.
- **Trivy v0.69 + baked DB** — the binary refuses a DB built by a
  significantly newer Trivy; both are pinned in the same Docker build stage.

---

## Versioning

Versioning follows [semver](https://semver.org/). Breaking changes to the
REST API or on-disk layout bump the major version.

Shipped releases (git tags):

- **v1.2 "Polish & Stability"** (2026-04-20) — sync-complete confirmation
  pill with `N files · X MB` shape; canonical `apt`/`deb` cred kind;
  typography lint clean; live PyPI + Helm mirror verification (4
  in-scope bug fixes); retired static `grep-cdn` (air-gap is a runtime
  property, enforced by `make test-airgap`).
- **v1.1 "Immediate Product Polish"** (2026-04-20) —
  `ApiErrorEnvelope` + visual foundation (axe / typography / contrast /
  spacing / reduced-motion gates); per-protocol client snippets +
  dashboard composition cards + shared empty states; upstream mirror
  repos for APT/RPM/PyPI/Helm; Docker clone from external registries;
  sync-job progress hook; upstream-credentials CRUD.
- **v1.0 MVP** (2026-04-17) — all protocol surfaces (OCI/Docker, RPM,
  APT, PyPI, Helm, Git, RAW, S3); admin UI; Trivy scan pipeline; GC
  loop; OpenAPI 3.1 contract; SigV4 verifier; multi-stage Docker image
  with baked Trivy DB.

---

## Further reading

- `/api/docs/` (at runtime) — Swagger UI for the REST API
- [Scheduled mirror sync (external cron)](docs/operations/scheduled-sync.md) — worked `curl` + crontab + Kubernetes CronJob example for driving mirror syncs on a cadence
- [Design specs](docs/design/specs/) — the original technology blueprint and the upstream-mirror design, with alternatives-considered commentary

## Reporting issues

File issues against the Git repo. Include:

- OmniRepo version (`omnirepo version`)
- Config (with secrets redacted)
- Relevant audit log entries (`/var/lib/omnirepo/logs/audit.ndjson`)
- Steps to reproduce
