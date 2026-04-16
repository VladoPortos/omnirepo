# Roadmap: OmniRepo v1

**Defined:** 2026-04-14
**Granularity:** coarse (5 phases)
**Core Value:** A single container that hosts every artifact type a corporate team produces or consumes — Docker images, Linux packages, Python wheels, Helm charts, raw blobs, S3 objects, Git repos — with vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.
**Coverage:** 167/167 v1 requirements mapped

## Phases

- [ ] **Phase 1: Foundation** — Config, SQLite (reader/writer split + FTS5), storage primitives, auth + policy, audit, TLS hot-reload, bootstrap JSON, chi router, healthz/readyz, air-gap CI gate, go-git v6 spike
- [x] **Phase 2: OCI + RAW + Scan Pipeline** — OCI registry, RAW pass-through, two-pool job runner, Trivy subprocess driver, blob_uploads registry + GC, scan-severity gate, public_read flag, Docker pull-external + promote, FTS5 write integration (completed 2026-04-15)
- [ ] **Phase 3: Package Repos (RPM + APT + PyPI + Helm)** — Four package protocols sharing per-repo mutex; PEP 691 JSON; full RPM repodata + repomd.xml.asc; APT InRelease clearsign; public-key endpoints; Helm index regen; sync-from-external
- [ ] **Phase 4: S3 + Git** — gofakes3 + SigV4 middleware with AES-GCM encrypted keys; go-git v6 backend with gitkit fallback; per-repo push size limit; memory benchmark gate
- [ ] **Phase 5: REST API + Web UI + Production Dockerfile** — Hand-written OpenAPI 3.1 + bundled Swagger UI; React 19 + Vite 8 + Tailwind 4 + shadcn/ui SPA; Trivy DB admin; trash viewer; maintenance toggle; multi-stage Dockerfile with baked Trivy DB

## Phase Details

### Phase 1: Foundation

**Goal**: A secure, air-gapped Go binary boots from a bootstrap JSON and serves `/healthz` and `/readyz` over HTTP and HTTPS, with all shared substrates (SQLite, storage, auth, policy, audit, TLS hot reload, per-repo locks, FTS5 schema) proven end-to-end against a real CI gate.

**Depends on**: Nothing (first phase)

**Requirements**: FOUND-01, FOUND-02, FOUND-03, FOUND-04, FOUND-05, FOUND-06, FOUND-07, FOUND-08, FOUND-09, FOUND-10, FOUND-11, FOUND-12, FOUND-13, FOUND-14, TEN-01, TEN-02, TEN-03, TEN-04, TEN-05, TEN-06, TEN-07, TEN-08, TEN-09, TEN-10, TEN-11, TEN-12, TEN-13, TEN-14, TEN-15, TEN-16, TEN-17, KEY-01, KEY-02, KEY-03, KEY-04, KEY-05, KEY-06, KEY-07, KEY-08, BOOT-01, BOOT-02, BOOT-03, BOOT-04, BOOT-05, REPO-01, REPO-02, REPO-03, REPO-04, REPO-06, REPO-08, OPS-01, OPS-02, OPS-08, AIR-04, AIR-05, AIR-06, AIR-07, TEST-01, TEST-06, TEST-08, TEST-09

**Success Criteria** (what must be TRUE):
  1. Fresh binary booted with `--network=none` seeds super-admin + users + projects + API keys from `bootstrap.json`, serves `200 OK` at `/healthz` and `/readyz` over both HTTP (8080) and HTTPS (8443, self-signed), and the Playwright air-gap invariant test passes.
  2. A bootstrapped user can `curl -u user:pass` the internal admin REST surface to create a project and a repo of any supported type; a second request with `must_change_password=true` user returns `403 password-change-required` on every auth surface (REST, Docker-style Basic, S3-style, Git-style).
  3. An admin uploads a new TLS cert + key via the internal test endpoint; a subsequent TLS handshake presents the new certificate without restart, while an in-flight connection is unaffected.
  4. SQLite contention bench (`make bench-sqlite`) runs 16 concurrent writes across simulated protocols with zero `SQLITE_BUSY`; FTS5 tables (`repos_fts`, `artifacts_fts`, `cves_fts`) exist and accept inserts inside the write transaction.
  5. Deleting a repo soft-moves its row and on-disk tree into `/var/lib/omnirepo/trash/<ts>-<id>/`; audit log (DB + NDJSON file) carries an entry for every state-changing action performed in tests.

**Plans**: 6 plans

Plans:
- [x] 01-01-PLAN.md — Skeleton: Go module, vendored deps, Makefile+lint+dev Dockerfile, koanf config loader, chi router + reserved prefixes, first-boot data-root dirs, cmd/omnirepo subcommand dispatcher
- [x] 01-02-PLAN.md — Metadata: SQLite reader/writer split with pragmas + BEGIN IMMEDIATE, //go:embed migration runner, 001_initial DDL (every Phase 1 table + FTS5), sqlitetest helper, bench-sqlite (pitfall P2)
- [x] 01-03-PLAN.md — Storage & audit: atomic temp+fsync+rename helper, CAS + PathStore + Trash + per-repo locks, audit Logger with DB row + size-rotating NDJSON mirror, blob_uploads stub
- [x] 01-04-PLAN.md — Auth & policy: argon2id + OTP + API keys + sessions + Actor, single Can(actor, action, target) engine with must_change_password short-circuit (pitfall P5), SessionOrAPIKey + BasicOrAPIKey middleware (KEY-06), typed users/sessions/apikeys repos
- [x] 01-05-PLAN.md — TLS hot-reload + bootstrap atomic ingest + minimal admin REST: self-signed cert + CertHolder, atomic bootstrap V1-V24 (pitfall atomicity), every D-36 endpoint, healthz/readyz, projects/members/repos/settings repos, app.Run orchestrator
- [x] 01-06-PLAN.md — Spikes & air-gap gates: go-git v6 spike (4 git CLI invocations, hard gate for Phase 4), in-process air-gap boot test, make grep-cdn placeholder, GitHub Actions CI workflow wiring every gate

### Phase 2: OCI + RAW + Scan Pipeline

**Goal**: A user can `docker push` and `docker pull` an image end-to-end, auto-scan fires on upload, scan-severity gating blocks pulls as configured, RAW upload/download works, Docker pull-external and promote-retag work, and the `blob_uploads` registry + CAS refcounting let GC run safely while uploads are in flight.

**Depends on**: Phase 1

**Requirements**: REPO-05, REPO-07, REPO-09, OCI-01, OCI-02, OCI-03, OCI-04, OCI-05, OCI-06, OCI-07, OCI-08, OCI-09, OCI-10, RAW-01, RAW-02, RAW-03, RAW-04, RAW-05, SYNC-01, SYNC-02, SYNC-03, SYNC-04, SCAN-01, SCAN-02, SCAN-03, SCAN-04, SCAN-05, SCAN-06, SCAN-07, SCAN-08, SCAN-12, OPS-06, SRCH-01, TEST-02

**Success Criteria** (what must be TRUE):
  1. `docker login localhost:8443`, `docker push localhost:8443/dxc/oracle/nginx:1.25`, and `docker pull localhost:8443/dxc/oracle/nginx:1.25` all succeed end-to-end against the running binary, including chunked upload, cross-repo blob mount (`?from=...`), and `crane` conformance tests.
  2. Uploading a known-vulnerable image (e.g. `nginx:1.14`) triggers a Trivy scan on the scan pool; results land in `scans` + `vulnerabilities` tables; with `block_on_severity=high`, a subsequent `docker pull` of that tag returns `403` with a clear message.
  3. A project member triggers "pull external Docker image" (anonymous and Basic-auth upstream both covered) into a local repo with optional retag; a separate "promote" action retags an image between two local Docker repos with zero blob copy (verified by CAS refcount deltas).
  4. `curl -X PUT ... /dxc/raw/assets/logo.png` stores the file; a GET returns it with correct `Content-Type` + `Content-Length`; a directory GET returns JSON listing via `Accept: application/json`.
  5. Admin-triggered GC on a repo with 1000 tagged manifests + 10 orphan blobs + in-flight uploads hard-deletes only orphans whose `ref_count == 0 AND last_touched_at < now-1h`, and never deletes a digest present in `blob_uploads`; CI regression test proves the race is closed.

**Plans**: 13 plans

Plans:
- [x] 02-01-PLAN.md — Schema migrations (002_jobs, 003_oci, 004_upstream_creds) + typed repos + FTS5 helpers
- [x] 02-02-PLAN.md — AES-GCM helper + upstream_creds repo + REST CRUD
- [x] 02-03-PLAN.md — Trivy Runner interface + trivyRunner + fakeRunner + tolerant parser + 3 schema-drift fixtures
- [x] 02-04-PLAN.md — Two-pool job runner (sync + scan) with lease/backoff/boot recovery/SIGTERM drain
- [x] 02-05-PLAN.md — OCI /v2 skeleton: ping, /v2/token HMAC-JWT, Bearer middleware, AnonymousReadOK, Can() anonymous-read branch
- [x] 02-06-PLAN.md — OCI blob upload state machine (POST/PATCH/PUT/GET) + cross-repo mount + GET/HEAD/DELETE; SCAN-12 race close
- [x] 02-07-PLAN.md — OCI manifests/tags/_catalog + cosign tag-presence badge + ref-delta on tag overwrite
- [x] 02-08-PLAN.md — RAW handler (PUT/GET/HEAD/DELETE/listing) + raw_files table + auto-scan enqueue
- [x] 02-09-PLAN.md — Scan handler worker (materialize OCI layout, run Trivy, write rows + SBOM + FTS) + severity gate + manual rescan REST
- [x] 02-10-PLAN.md — Pull-external (pkg/v1/remote) + promote/retag (zero-blob-copy) + REST endpoints
- [x] 02-11-PLAN.md — Repo PATCH (settings) + wipe (REPO-05/07/09) with refcount-aware deletion
- [x] 02-12-PLAN.md — GC handler (mark+sweep) + super-admin REST + SCAN-12 race regression test
- [x] 02-13-PLAN.md — Crane conformance harness (TEST-02) + airgap test extensions (D-43) + CI wiring

### Phase 3: Package Repos (RPM + APT + PyPI + Helm)

**Goal**: A user can install packages from all four package-repo protocols using real clients (`dnf`, `apt-get`, `pip`/`uv`, `helm`) against signed metadata, with per-repo mutex serializing metadata regeneration, public signing keys downloadable, and one-shot sync-from-external pulling upstream artifacts idempotently into any of the four repo types.

**Depends on**: Phase 1, Phase 2

**Requirements**: RPM-01, RPM-02, RPM-03, RPM-04, RPM-05, RPM-06, APT-01, APT-02, APT-03, APT-04, APT-05, APT-06, PYPI-01, PYPI-02, PYPI-03, PYPI-04, PYPI-05, PYPI-06, HELM-01, HELM-02, HELM-03, HELM-04, HELM-05, SYNC-05, SRCH-02

**Success Criteria** (what must be TRUE):
  1. On a Docker-in-Docker Rocky/RHEL host, `dnf install` against `/<project>/rpm/<repo>/` succeeds after `rpm --import` of the repo's `public-key.asc`; `repodata/` contains `primary.xml.gz`, `filelists.xml.gz`, `other.xml.gz`, `repomd.xml`, and `repomd.xml.asc`; concurrent uploads serialize via the per-repo mutex without corrupting `repomd.xml`.
  2. On a Debian/Ubuntu DinD host, `apt-get update` + `apt-get install` succeed against a repo with clearsigned `InRelease`, full `dists/<suite>/<component>/binary-<arch>/Packages(.gz)` tree, and a reachable `public-key.asc` — across at least two (suite, component, architecture) tuples.
  3. `pip install --index-url https://localhost:8443/dxc/pypi/internal/simple/ <pkg>` and `uv pip install --index-url ... <pkg>` both succeed; the same endpoint returns PEP 691 JSON when `Accept: application/vnd.pypi.simple.v1+json` is sent; `twine upload` populates the index correctly with normalized names.
  4. `helm repo add`, `helm pull`, and `helm install --dry-run` all succeed against an uploaded chart; `index.yaml` is fully regenerated from disk after every upload/delete via `helm.sh/helm/v3/pkg/repo`.
  5. A project member triggers "sync from external URL" against an RPM, APT, PyPI, or Helm upstream; re-running the same sync is idempotent (no duplicate downloads); local metadata is re-signed with the local key; FTS5 artifact rows are inserted in the same transaction as the artifact rows.

**Plans**: 7 plans

Plans:
- [x] 03-01-PLAN.md — Migrations 008-015 + typed repos + pgpsign + FTS helpers + regen coalescer + config/audit/auth extensions
- [x] 03-02-PLAN.md — Helm protocol: chart upload/download/delete, .prov pass-through, index.yaml regen via helm SDK
- [x] 03-03-PLAN.md — PyPI protocol: PEP 503/691 simple index, twine /legacy/ + PEP 694 /+upload/, Normalize at every boundary, regen
- [x] 03-04-PLAN.md — RPM protocol: eager signing-key at repo-create, .rpm parse, repodata (primary/filelists/other + repomd.xml.asc) regen, public-key endpoint
- [x] 03-05-PLAN.md — APT protocol: eager signing-key + default suite matrix, .deb parse (ar+tar+gz/xz/zst), Packages/InRelease/Release.gpg staging-dir swap regen
- [x] 03-06-PLAN.md — SYNC-05: per-protocol upstream parsers + sync job handlers + /sync REST endpoint, idempotency by checksum, host-match + error scrubbing
- [x] 03-07-PLAN.md — DinD conformance harness (rpm/deb/pypi/helm) + air-gap extensions + grep-cdn gate + CI workflow

### Phase 4: S3 + Git

**Goal**: A user can `aws s3 cp` against the S3 protocol surface with a real SigV4 signature and `git clone` / `git push` against a Git repo, both gated by real authentication, with the AES-GCM S3 secret scheme, clock-skew rejection, per-repo push size limit, and go-git v6 memory behavior all verified by benchmarks.

**Depends on**: Phase 1

**Requirements**: S3K-01, S3K-02, S3K-03, S3K-04, S3K-05, S3-01, S3-02, S3-03, S3-04, S3-05, S3-06, GIT-01, GIT-02, GIT-03, GIT-04, GIT-05, GIT-06, GIT-07, TEST-07

**Success Criteria** (what must be TRUE):
  1. `aws --endpoint-url https://localhost:8443/s3 s3 cp ./file.bin s3://dxc-artifacts/path/file.bin` with a dedicated S3 key pair succeeds; wrong secret returns `403 SignatureDoesNotMatch`, missing key returns `403 InvalidAccessKeyId`, and a request with a timestamp > 15 min off returns `RequestTimeTooSkewed` with server time echoed — all against real `aws-sdk-go-v2`.
  2. Multipart upload (`CreateMultipartUpload` → N × `UploadPart` → `CompleteMultipartUpload`) and virtual-host-style routing (`<bucket>.<host>/<key>`) both work; S3 secrets at rest are AES-GCM encrypted with a per-install key from `settings` and decrypted on each HMAC recompute.
  3. `git clone https://user:apikey@localhost:8443/git/dxc/infra.git` and `git push` over HTTPS Basic both succeed via the go-git v6 `backend` package; `info/refs`, `git-upload-pack`, and `git-receive-pack` all respond correctly; `git_refs` table reflects post-push state.
  4. Per-repo push size limit (default 500 MB) rejects an over-cap push with a clear error; `make bench-git` clones a 200 MB synthetic repo with RSS < 3× repo size (hard gate); gitkit fallback activates via config flag and passes the same conformance suite.
  5. The Phase 1 air-gap test is extended to `/s3/` and `/git/` routes and still passes with `--network=none`.

**Plans**: 13 plans

Plans:
- [x] 04-01-PLAN.md — Wave 0 probes: gitkit Go 1.25 compile, gofakes3 MultipartBackend surface, AWS SigV4 test vectors vendored, conformance images pinned
- [x] 04-02-PLAN.md — Migrations 016–019 (s3_access_keys, git_extensions, s3_objects, s3_multipart) + typed repos
- [x] 04-03-PLAN.md — Config schema extension (server.git_backend, repos.git.max_push_bytes, external_hostnames) + reserved-prefix verify
- [x] 04-04-PLAN.md — SigV4 verifier: canonical/errors + Verify + STREAMING chunked parser
- [x] 04-05-PLAN.md — S3 access-key service (AEAD lookup) + admin REST /api/v1/projects/{name}/s3-access-keys + auth.Can ActionS3Bucket{Read,Write,Admin}
- [x] 04-06-PLAN.md — gofakes3 Backend + multipart (staging + streaming merge + orphan GC)
- [x] 04-07-PLAN.md — S3 route wiring: vhost middleware + SigV4 middleware + auth.Can + gofakes3 mount
- [x] 04-08-PLAN.md — GitServer interface + gogit production backend (spike promotion) + gitkit fallback + pktline sideband + delete spike
- [x] 04-09-PLAN.md — Git middleware chain (BasicOrAPIKey project: variant + perRepoMutex + pushSizeLimit) + backend selection via config
- [x] 04-10-PLAN.md — Post-ReceivePack refs walker (git_refs sync) + bare-repo lifecycle hooks (OnRepoCreate/OnRepoDelete)
- [x] 04-11-PLAN.md — S3 conformance via aws-sdk-go-v2 (positive + negative matrix) + CI job
- [ ] 04-12-PLAN.md — Git conformance via real git CLI (gogit + gitkit parameterized) + oversize-push gate + CI job
- [ ] 04-13-PLAN.md — Memory bench (TEST-07 hard gate: peak_rss < 3× repo_bytes) + air-gap extension (/s3 + /git routes)

### Phase 5: REST API + Web UI + Production Dockerfile

**Goal**: A super-admin can operate the full OmniRepo product from a browser — log in, force password change, browse dashboards, create projects and repos across every type, upload and scan artifacts, run sync jobs, manage API keys, hot-swap TLS certs, upload/pull Trivy DBs, toggle maintenance mode, trigger GC, restore from trash, and browse audit + search — using a React 19 SPA served from a production multi-arch Docker image with a baked Trivy DB and zero runtime outbound calls.

**Depends on**: Phase 1, Phase 2, Phase 3, Phase 4

**Requirements**: SYNC-06, SCAN-09, SCAN-10, SCAN-11, OPS-03, OPS-04, OPS-05, OPS-07, OPS-09, API-01, API-02, API-03, API-04, API-05, API-06, SRCH-03, SRCH-04, UI-01, UI-02, UI-03, UI-04, UI-05, UI-06, UI-07, UI-08, UI-09, UI-10, UI-11, UI-12, UI-13, AIR-01, AIR-02, AIR-03, TEST-03, TEST-04, TEST-05

**Success Criteria** (what must be TRUE):
  1. `docker run -v omnirepo-data:/var/lib/omnirepo ghcr.io/.../omnirepo:v1` from the multi-stage image (linux/amd64 + linux/arm64, non-root UID 1000, baked Trivy DB seeded from `/opt/trivy-db/` on first boot) comes up to a working SPA at `https://localhost:8443/`, with the full air-gap Playwright suite green under `--network=none`.
  2. A first-time bootstrapped user completes login → forced password change → dashboard → create project → create one repo of each type (rpm/deb/pypi/docker/helm/git/raw/s3-bucket) → upload an artifact via dropzone → view scan results → copy the "use this repo" snippet → log out, entirely through Playwright E2E against the real binary; dark mode is the default theme.
  3. Super-admin admin pages: users CRUD, full filterable audit log, TLS cert upload (hot swap), Trivy DB upload + online-pull (with clear error on offline), Trivy DB status widget showing version/age/source, maintenance mode toggle returning `503` on writes while allowing reads, GC trigger, trash viewer with restore — all functional via UI and mirrored REST endpoints at `/api/v1/admin/...`.
  4. `/api/v1/...` serves hand-written chi routes typed from `oapi-codegen/v2` against a committed OpenAPI 3.1 spec; Swagger UI at `/api/docs` renders entirely from bundled assets; every endpoint except `auth/login`, `/healthz`, `/readyz` requires auth; list endpoints paginate via `?limit=&cursor=`.
  5. `GET /api/v1/search?q=&kind=&severity=&project=` returns ranked results across repos, artifacts, and CVEs (filename, image tag, checksum exact match, CVE ID, prefix match) from FTS5, and the search screen renders them with type+severity filters linking back to source entities; grep gate `grep -rEI 'https?://(?!localhost|127\.0\.0\.1)' web/dist/` returns only self-references.

**Plans**: TBD
**UI hint**: yes

## Progress

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundation | 0/0 | Not started | - |
| 2. OCI + RAW + Scan Pipeline | 13/13 | Complete   | 2026-04-15 |
| 3. Package Repos (RPM + APT + PyPI + Helm) | 2/7 | In Progress|  |
| 4. S3 + Git | 6/13 | In Progress|  |
| 5. REST API + Web UI + Production Dockerfile | 0/0 | Not started | - |

## Coverage Summary

- v1 requirements: 167 total
- Mapped to phases: 167
- Unmapped: 0

Per-phase requirement counts:
- Phase 1: 60 requirements (Foundation, Tenancy, Keys, Bootstrap, base Repo CRUD, core Ops, air-gap invariants, base tests)
- Phase 2: 33 requirements (OCI, RAW, Scan pipeline, base Sync, GC, FTS schema, repo toggles)
- Phase 3: 25 requirements (RPM, APT, PyPI, Helm, package-repo sync, FTS write integration)
- Phase 4: 19 requirements (S3 keys, S3 protocol, Git protocol, Git memory bench)
- Phase 5: 36 requirements (REST API, Swagger, UI, admin screens, Trivy DB admin, search API, Dockerfile, top-level tests)

---
*Roadmap created: 2026-04-14*
