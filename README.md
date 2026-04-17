# OmniRepo

**A single self-hosted container that serves every artifact type your team produces or consumes — OCI images, RPM/APT/PyPI/Helm packages, Git repos, S3 buckets, and raw blobs — with built-in vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.**

Designed as a focused, simpler alternative to JFrog Artifactory or Sonatype Nexus for small-to-mid corporate and air-gapped environments.

---

## What OmniRepo is

One Go binary, one HTTP/HTTPS port, one mounted volume. Drop it on a host, point your build tools at it, and it speaks every major artifact protocol natively.

### Protocols served

| Protocol | Endpoint shape | Clients | Spec |
|----------|----------------|---------|------|
| **OCI/Docker Registry** | `/v2/...` | `docker`, `podman`, `crane`, `skopeo` | Distribution v2, Bearer JWT |
| **RPM/YUM repo** | `/<project>/rpm/<repo>/...` | `dnf`, `yum`, `zypper` | `repomd.xml` + signed `primary.xml.gz` |
| **APT/Debian repo** | `/<project>/deb/<repo>/dists/<suite>/...` | `apt`, `apt-get` | `Release` + PGP `InRelease` |
| **PyPI** | `/<project>/pypi/<repo>/simple/...` | `pip`, `uv`, `twine` | PEP 503 + 691 + 694 uploads |
| **Helm** | `/<project>/helm/<repo>/index.yaml` | `helm` | Helm v3 `index.yaml` + `.tgz` + `.prov` |
| **Raw blobs** | `/<project>/raw/<repo>/<path>` | `curl`, `wget`, any HTTP client | Plain HTTP with digest headers |
| **S3-compatible** | `/<bucket>/<key>` with SigV4 | `aws-cli`, `s3cmd`, any AWS SDK | AWS SigV4 + gofakes3 backend |
| **Git hosting** | `/<project>/git/<repo>.git` | `git` CLI (Smart HTTP) | go-git v6 backend + gitkit fallback |

### Core features

- **Project-scoped access control** — flat user/project/member model with super-admin bypass. Every artifact lives under a project; project members get read+write, everyone else gets 403.
- **API keys** — user-owned or project-owned, shown once at creation, hashed at rest, prefix-indexed for O(1) lookup.
- **Vulnerability scanning** — embedded Trivy subprocess with an air-gapped baked DB. Per-repo `block_on_severity` gate can refuse pulls of artifacts whose latest scan found findings ≥ threshold. RPM cpio payloads and PyPI wheel transitive deps are unpacked before the scan so Trivy sees real filesystem entries.
- **Built-in web UI** — React 19 + Tailwind 4 + shadcn/ui, embedded in the Go binary via `//go:embed`. Zero runtime CDN. Includes a first-class S3 bucket browser (prefix drill-down, object table, delete flow).
- **Audit log** — every auth decision, upload, admin action, and settings change recorded in SQLite with an NDJSON mirror.
- **Air-gap by default** — no outbound network calls without an explicit admin action. Trivy DB updates only via tarball upload or admin button.
- **Hot-reloadable TLS** — admin-uploaded certs swap into the live listener without restart.
- **Maintenance mode** — global read-only switch with an in-UI banner visible to all users.

---

## How it works

### Single binary, single port, single volume

Everything multiplexes on one HTTP/HTTPS port via a single `chi` router. Each protocol handler is embedded as an `http.Handler` — no sidecars, no reverse proxy required. All mutable state lives under one mounted volume (default `/var/lib/omnirepo`) holding:

```
/var/lib/omnirepo/
  db/omnirepo.sqlite      — metadata (users, projects, repos, scans, audit log)
  blobs/sha256/xx/...     — OCI content-addressed blob tree
  repos/<project>/...     — per-project artifact trees (rpm, deb, pypi, helm, raw, git)
  certs/server.{crt,key}  — live TLS pair (self-signed by default)
  certs/uploaded/<ts>/    — rollback archive of admin-uploaded certs
  tmp/                    — upload staging (truncated on boot)
  logs/audit.ndjson       — audit log mirror
  config/bootstrap.json   — first-boot seed (consumed once; see below)
```

### Tech stack

- **Go 1.25** — single static binary, pure-Go SQLite (`modernc.org/sqlite`), cross-compiled for `linux/amd64` + `linux/arm64`.
- **SQLite with FTS5** — one WAL-mode database for every metadata need (including full-text search across repos, artifacts, CVEs, and packages).
- **React 19 + Vite 8 + Tailwind 4 + shadcn/ui** — frontend built into `web/dist/` and embedded into the Go binary.
- **Apache-2.0-compatible dependencies only** — MinIO (AGPL) explicitly excluded; gofakes3 (MIT) used for S3 instead.

See `CLAUDE.md` for the full licensing and version-pinning matrix.

---

## Installation

### Docker (recommended)

```bash
docker run -d \
  --name omnirepo \
  -p 8080:8080 -p 8443:8443 \
  -v /srv/omnirepo:/var/lib/omnirepo \
  omnirepo:1.0
```

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
# Vite proxies /api and /v2 to the Go server, so hot module reload works
# against the live backend.
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

Drop a `bootstrap.json` at the configured path to seed the super-admin (and optionally users, projects, repos, and API keys). Consumed exactly once; if any user row exists, it's skipped.

```json
{
  "schema_version": 1,
  "super_admin": {
    "login": "admin",
    "email": "admin@example.com",
    "password": "ChangeMe!",
    "must_change_password": true
  },
  "projects": [
    { "name": "platform", "description_md": "Shared infra artifacts", "members": [] }
  ],
  "repos": [
    { "project": "platform", "type": "docker", "name": "images", "auto_scan": true }
  ]
}
```

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

---

## Common operations

| Task | How |
|------|-----|
| Web UI | `https://<host>:8443/` — dashboard, projects, search, admin panels, profile, S3 bucket browser |
| REST API | `/api/v1/...` — OpenAPI 3.1 spec at `/api/docs/` (Swagger UI) |
| Health checks | `GET /healthz`, `GET /readyz` (no auth) |
| Trigger garbage collection | `POST /api/v1/admin/gc` (super-admin). Poll `GET /api/v1/admin/gc/status` for state (`idle`/`pending`/`running`/`done`/`failed` + `bytes_freed`) |
| Provision S3 bucket | `POST /api/v1/projects/<name>/s3-buckets/` or UI → project → S3 tab → Create Bucket |
| Browse bucket objects | `GET /api/v1/projects/<name>/s3-buckets/<bucket>/objects?prefix=…&marker=…&limit=…` or navigate to `/projects/<name>/s3/<bucket>` in the UI |
| Apply migrations manually | `omnirepo migrate up --config <path>` |
| Rotate TLS cert | Upload PEM pair via Admin → TLS Certificates; hot-swapped |
| Update Trivy DB | Upload DB tarball via Admin → Trivy Database; or enable the refresh button (requires `air_gap.allow_external_actions: true`) |
| Enter maintenance mode | Admin → Maintenance → toggle. Banner appears for all logged-in users |
| Force user password reset | Admin → Users → edit → "Force Password Reset" toggle; optionally set a temp password in the dialog. Existing sessions for that user are revoked |

---

## Security notes

- **Passwords**: Argon2id (`m=64MiB, t=3, p=4`). `bcrypt` is not used.
- **API keys**: prefix-indexed SHA-256; plaintext shown once at creation.
- **Sessions**: opaque 32-byte tokens, HttpOnly + Secure + SameSite cookies, 12 h sliding + 7 d hard cap. Self-service password change preserves the current session and invalidates every other session for that user. Admin-forced resets revoke every session for the user.
- **Docker JWT**: HS256 with a per-install random 32-byte secret stored in the `settings` table; 60-minute TTL by default.
- **S3 SigV4**: full verifier with clock-skew window; multi-chunk streaming supported; `v4` signatures only.
- **TLS**: min version 1.2, self-signed on first boot, hot-reloadable. The Docker Bearer realm uses `server.external_hostnames[0]` when configured (closes Host-header injection).
- **Air-gap invariant**: no outbound HTTP from the binary without an explicit admin-user action. Enforced in CI via `make grep-cdn`.

---

## Testing

```bash
make test               # unit + integration tests (in-memory + on-disk SQLite)
make test-airgap        # asserts the air-gap invariant (no runtime outbound calls)
make bench              # perf benchmarks (SQLite, git clone memory, throughput)
make conformance-all    # DinD conformance: real dnf/apt/pip/helm/git/crane clients
make lint               # golangci-lint (errcheck, govet, ineffassign, staticcheck, unused)
make grep-cdn           # asserts no external https:// URLs in the built SPA or handlers
```

Optional live-server walkthrough (the SigV4 + CRUD + multipart suite used
during release verification) — skips automatically when env vars are unset:

```bash
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
npx playwright test     # E2E (requires dev server running)
```

All six CI gates (`lint`, `test`, `test-airgap`, `grep-cdn`, `bench-sqlite`, plus the git-backend spike) must exit 0 for a PR to merge.

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
cmd/omnirepo/             — main binary entry point + CLI
internal/
  api/                    — REST API handlers (admin, dashboard, projects, search, …)
  app/                    — server composition (Run, bootstrap, TLS, jobs wiring)
  audit/                  — audit event types + logger
  auth/                   — Actor, password hashing, policy engine, JWT, middleware
  config/                 — YAML config schema + koanf loader
  crypto/                 — AEAD helpers for upstream credentials
  jobs/                   — scan pool, sync pool, GC
  metadata/               — SQLite repos (users, projects, repos, blobs, sessions, …)
  protocol/
    oci/                  — /v2 Docker Registry handler
    rpm/                  — .rpm upload + repomd/primary regen + detached-sig
    deb/                  — .deb upload + Packages/Release/InRelease
    pypi/                 — PEP 503/691/694 upload + simple index
    helm/                 — chart upload + index.yaml regen
    raw/                  — raw-blob CRUD
    s3/                   — gofakes3 backend + SigV4 verifier + key mgmt
    git/                  — go-git v6 backend + gitkit fallback
  scan/                   — Trivy subprocess wrapper
  storage/                — CAS, PathStore, atomic rename helpers
  tls/                    — cert holder + hot-reload + upload
web/                      — React SPA (embedded via go:embed)
test/                     — airgap, conformance, bench harnesses
```

---

## Versioning

**v1.0** is the first production release. Versioning follows [semver](https://semver.org/). Breaking changes to the REST API or on-disk layout bump the major version.

---

## Further reading

- `CLAUDE.md` — full dependency matrix, version pins, and licensing notes
- `tools.md` — original technology blueprint and alternatives-considered table
- `.planning/` — GSD workflow artifacts (project spec, roadmap, per-phase plans)
- `/api/docs/` (at runtime) — Swagger UI for the REST API

## Reporting issues

File issues against the Git repo. Include:

- OmniRepo version (`omnirepo version`)
- Config (with secrets redacted)
- Relevant audit log entries (`/var/lib/omnirepo/logs/audit.ndjson`)
- Steps to reproduce
