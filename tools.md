# Building a Go artifact repository: the complete technical blueprint

A single Go binary can realistically serve as an OCI registry, RPM/APT/PyPI/Helm repository, S3-compatible store, and Git host — all on one port. The key enablers are **go-git v6's new HTTP backend** (pure-Go Git serving without CGI), **`google/go-containerregistry/pkg/registry`** (embeddable OCI registry as an `http.Handler`), and **`johannesboyne/gofakes3`** (embeddable S3 API). Most package repository protocols (Helm, PyPI, APT, RPM) are surprisingly simple metadata-over-HTTP formats that need no special libraries. This report covers the full technology stack: MCP development tooling, library selection for every subsystem, Git hosting feasibility, project architecture, and MVP features.

## Part 2: Go libraries for every subsystem

### OCI/Docker registry — embed, don't fork

**`google/go-containerregistry`** (~3,800 stars, Apache 2.0, updated April 11, 2026) is the recommended foundation. Its `pkg/registry` subpackage implements a **complete, embeddable OCI Distribution registry as a standard `http.Handler`** with minimal dependencies. It supports pluggable backends via `BlobHandler`, `BlobPutHandler`, and `BlobStatHandler` interfaces. Originally built for testing, it's increasingly used in production.

An even cleaner alternative is **`rogpeppe/ociregistry`**, which defines a composable `Interface` type with `ociserver` (HTTP server from any Interface), `ocimem` (in-memory implementation), and `ocifilter` (views/filters). For client-side artifact operations, **`oras-project/oras-go`** (~236 stars, Apache 2.0) handles pushing/pulling arbitrary OCI artifacts.

The reference implementation **`distribution/distribution`** (~10,400 stars, Apache 2.0) can technically be imported, but its interfaces are marked unstable and the coupling is tight. Most integrators fork it or run it as a sidecar. **`containers/image`** (~3,500 stars) powers Podman/skopeo but requires C libraries and is better suited for building registry clients, not servers.

### RPM repository — parse headers, generate XML

No single Go library provides a `createrepo` equivalent, but the metadata format is straightforward XML. **`cavaliergopher/rpm`** (~200 stars, BSD 3-Clause) parses RPM files in pure Go — headers, NEVRA, dependencies, files, signatures. **`google/rpmpack`** (~200 stars, Apache 2.0) creates RPM packages programmatically. **`sassoftware/go-rpmutils`** (~100 stars, Apache 2.0) adds PGP signature validation.

A valid yum/dnf repository needs just **`repomd.xml`** (master index), **`primary.xml.gz`** (package names, versions, dependencies, checksums), `filelists.xml.gz`, and `other.xml.gz`. In practice, just `repomd.xml` + `primary.xml.gz` + the RPM files is sufficient for most clients. Generate the XML with Go's `encoding/xml` from parsed RPM header data.

### APT/Debian repository — ar archives and GPG signing

Building from scratch is practical. A .deb file is an `ar` archive containing `control.tar.gz` (metadata) and `data.tar.gz` (files). Parse the control file to extract package metadata, then generate **Packages** (one stanza per .deb with fields from control plus Filename, Size, SHA256), **Release** (repository metadata + checksums of all index files), and **InRelease** (clearsigned Release).

**`ProtonMail/go-crypto`** (~1,000 stars, BSD 3-Clause) is the successor to the deprecated `golang.org/x/crypto/openpgp` and handles GPG signing for InRelease files. Study **`aptly-dev/aptly`** (~2,600 stars, MIT) for reference patterns — it's the most complete APT repository manager in Go, though its packages are tightly coupled to its LevelDB database.

### PyPI — trivially simple

PEP 503's Simple Repository API needs just **two HTML pages**: a root index (`/simple/`) listing project names as `<a>` links, and per-project pages (`/simple/<project>/`) listing files with `href="<url>#sha256=<hash>"`. This is implementable in **~100 lines of Go**. Package upload is a standard multipart POST. No special libraries needed — normalize project names (lowercase, replace `[-_.]` with `-`), serve HTML, done.

### Helm chart repository — use the SDK

The Helm chart repository protocol is just an `index.yaml` file plus downloadable `.tgz` chart archives. **`helm.sh/helm/v3/pkg/repo`** (from the ~27,000 star Helm project, Apache 2.0) provides `IndexFile` for generating `index.yaml` and `pkg/chart/loader` for parsing Chart.yaml from `.tgz` files. **ChartMuseum** (`helm/chartmuseum`, ~3,824 stars) adds upload API and dynamic index generation but is a standalone server, not an embeddable library.

### S3-compatible API — gofakes3 as the foundation

**`johannesboyne/gofakes3`** (~740 stars, MIT) implements core S3 APIs as an embeddable `http.Handler`. It supports CreateBucket, ListBuckets, PutObject, GetObject, DeleteObject, multipart upload, versioning, and ListObjects v1/v2 with a clean pluggable `Backend` interface. Create a handler with `gofakes3.New(backend).Server()` and mount it in your router.

**MinIO cannot be embedded** — it uses `internal/` packages preventing external import and carries an **AGPLv3 license** requiring your entire project to be AGPLv3. For production use, add S3 Signature V4 verification as middleware (~200 lines of HMAC-SHA256 computation, well-documented in AWS specs) since gofakes3 accepts any credentials by default.

### Git hosting — go-git v6 changes everything

**go-git v6** (pre-release February 2026) introduces `backend/http`, a complete `http.Handler` implementing Git Smart HTTP and Dumb HTTP protocols in pure Go. This is the breakthrough — see Part 3 for the deep dive.

### Vulnerability scanning — embed for Go, sidecar for everything else

**`golang.org/x/vuln/scan`** (official Go project, BSD license) provides a stable API for scanning Go module dependencies against vuln.go.dev. It's lightweight with minimal dependencies — ideal for embedding. **Trivy** (~25k stars) and **Grype** (~11k stars) are both Apache 2.0 but bring massive dependency trees. Run them as sidecar processes rather than embedding.

### Web framework — chi for multi-protocol routing

**`go-chi/chi`** (~19k stars, MIT) is the strongest choice because it's **100% compatible with `net/http`**. This matters enormously when mounting diverse protocol handlers — OCI registry handlers, go-git v6's HTTP backend, gofakes3's S3 handler, and custom protocol handlers all implement `http.Handler`. Chi's `Mount()` composes them without adapter wrapping. Different middleware stacks per route group support varying auth requirements (Bearer for OCI, basic auth for Git, API keys for RPM/APT). Echo is a solid second choice for its built-in SPA serving and Casbin middleware.

### Database — pure Go SQLite wins

**`modernc.org/sqlite`** (BSD license, SQLite 3.51.3 as of March 2026) is the recommended choice. No CGo means trivial cross-compilation, simple Dockerfiles, and `FROM scratch` builds. Performance is **~1.5-2x slower than CGo** for inserts and within 10-50% for selects — adequate for metadata operations. An emerging alternative is **`ncruces/go-sqlite3`** (MIT, WASM-based via wazero), which benchmarks **3x+ faster than modernc** for prepared statement reads.

**`go.etcd.io/bbolt`** (~9,431 stars, MIT) is excellent for KV workloads but lacks SQL — too limited for the relational queries an artifact server needs (searching packages, filtering versions, managing permissions).

### Authentication — JWT + Casbin + OIDC

**`golang-jwt/jwt` v5** (~7k stars, MIT) for token issuance and validation. **`apache/casbin`** (~18k stars, Apache 2.0) for declarative RBAC authorization with REST path matching. **`coreos/go-oidc`** + `golang.org/x/oauth2` for corporate SSO. For environments needing JWK auto-refresh from OIDC providers, use **`lestrrat-go/jwx`** (~2.3k stars, MIT) — its JWK caching is particularly valuable.

### UI — embedded React/Vite SPA

Use Go 1.16+'s `embed` package to bundle a Vite-built React SPA into the binary (`//go:embed dist/*`). Serve with `http.FileServer(http.FS(distFS))`, fall back unmatched routes to `index.html` for SPA routing. In dev mode, proxy to Vite's dev server for HMR. For simple admin/status pages, **`a-h/templ`** with HTMX avoids the Node.js build toolchain.

### Storage abstraction — gocloud.dev/blob

**`gocloud.dev/blob`** (from `google/go-cloud`, ~9.5k stars, Apache 2.0) provides a unified `*blob.Bucket` API across S3, GCS, Azure, filesystem, and in-memory backends. URL-based opening (`blob.OpenBucket(ctx, "s3://my-bucket")`) makes the backend a configuration choice. Complement with **`spf13/afero`** (~6k stars, Apache 2.0) for local caching and temp file management.

| Category | Recommended Library | Stars | License | Embeddable |
|----------|-------------------|-------|---------|------------|
| OCI Registry | `google/go-containerregistry/pkg/registry` | 3.8k | Apache 2.0 | ✅ `http.Handler` |
| RPM Parsing | `cavaliergopher/rpm` | ~200 | BSD 3-Clause | ✅ Pure Go |
| APT Signing | `ProtonMail/go-crypto` | ~1k | BSD 3-Clause | ✅ Pure Go |
| PyPI | No library needed (PEP 503 is ~100 LOC) | — | — | — |
| Helm | `helm.sh/helm/v3/pkg/repo` | 27k+ | Apache 2.0 | ✅ |
| S3 API | `johannesboyne/gofakes3` | ~740 | MIT | ✅ `http.Handler` |
| Git HTTP | `go-git/go-git/v6/backend/http` | ~6k | Apache 2.0 | ✅ `http.Handler` |
| Vuln Scan | `golang.org/x/vuln/scan` | Official | BSD | ✅ Stable API |
| Web Framework | `go-chi/chi` | ~19k | MIT | ✅ net/http |
| Database | `modernc.org/sqlite` | N/A | BSD | ✅ Pure Go |
| Auth | `golang-jwt/jwt` v5 + `apache/casbin` | 7k / 18k | MIT / Apache 2.0 | ✅ |
| Storage | `gocloud.dev/blob` | ~9.5k | Apache 2.0 | ✅ |

---

## Part 3: Git hosting is feasible in pure Go

### go-git v6 delivers a complete HTTP backend

The most significant finding in this research: **go-git v6** (pre-release tagged February 17, 2026) ships a `backend/http` package that implements the full Git Smart HTTP protocol as a standard `http.Handler`. This eliminates the need for the `git` binary entirely.

```go
import githttp "github.com/go-git/go-git/v6/backend/http"

backend := githttp.NewBackend(loader)
backend.Prefix = "/git"
// Mount directly in chi router
r.Mount("/git/", backend)
```

The internal route table handles `info/refs` (GET), `git-upload-pack` (POST), and `git-receive-pack` (POST) — the three endpoints constituting the Git Smart HTTP protocol. It also supports Dumb HTTP for compatibility. The `transport.Loader` interface maps URL paths to repository storage, and v6 provides `server.NewFilesystemLoader()` and `server.MapLoader` as built-in implementations.

**Risk factor**: go-git v6 is pre-release. The fallback is go-git v5's `plumbing/transport/server` package, which handles upload-pack and receive-pack at the pack protocol level but requires ~200 lines of custom HTTP bridging code. Alternatively, `sosedoff/gitkit` (~400 stars, MIT) provides a battle-tested `http.Handler` that shells out to the git binary.

### How Gitea and Soft-Serve do it differently

Gitea's `modules/git` package is explicitly described as *"a Go module for Git access through shell"* — it requires Git ≥ 2.0 and calls `git upload-pack`/`git receive-pack` as child processes via `exec.Command`, streaming stdin/stdout through HTTP. Gitea's experimental go-git build tag (`TAGS="gogit"`) is primarily for read operations and was criticized for performance issues.

Soft-Serve takes a **hybrid approach**: go-git v5 for reading repositories (branches, commits, file trees for TUI display), but shells out to git via `go-git-daemon` for protocol serving. Its packages are mostly `internal/` — not designed for reuse. The only importable component is `pkg/lfs`.

### The Git Smart HTTP protocol is elegant in its simplicity

Only **three HTTP endpoints** are needed:

- **`GET /<repo>/info/refs?service=git-upload-pack`** — Returns pkt-line formatted reference listing for clone/fetch
- **`POST /<repo>/git-upload-pack`** — Receives want/have negotiation, returns packfile
- **`POST /<repo>/git-receive-pack`** — Receives ref update commands + packfile, returns status

Each pkt-line is prefixed with 4 hex digits indicating total line length; `0000` is a flush packet. The protocol is **stateless** from the server side — all state is managed by the client, enabling simple load balancing.

### go-git provides complete bare repo management

Without any git binary, go-git can create bare repositories (`git.PlainInit(path, true)`), list branches/tags/references, walk commit history, read file trees and content at any commit, resolve revisions (`HEAD~3`, tag names), diff between commits, blame, and grep. This is sufficient for a **basic web viewer** showing repositories, branches, commit logs, and file contents — all the functionality needed for an artifact repository's code browsing feature.

---

## Part 4: Architecture that scales from MVP to production

### Recommended project structure

Analysis of four reference projects — distribution/distribution (gorilla/mux + factory-pattern storage drivers), ChartMuseum (gin + extracted storage/auth modules), Soft-Serve (multi-protocol single binary), and Athens (composite storage interface + handler-creates-handler pattern) — reveals a consistent architecture.

The recommended structure uses a **single Go module with `internal/` enforced boundaries** (all four reference projects use single-module repos):

```
freighter/
├── cmd/freighter/main.go           # Thin entrypoint: config → wire → start
├── internal/
│   ├── app/                         # Bootstrap, dependency wiring, server setup
│   ├── config/                      # Configuration (YAML + env vars)
│   ├── protocol/                    # Protocol handler modules
│   │   ├── registry.go              # Handler interface + registry
│   │   ├── oci/                     # OCI Distribution (/v2/...)
│   │   ├── helm/                    # Helm Charts (/api/charts/...)
│   │   ├── apt/                     # APT (/dists/..., /pool/...)
│   │   ├── rpm/                     # RPM (/repodata/...)
│   │   ├── pypi/                    # PyPI (/simple/...)
│   │   ├── git/                     # Git HTTP (/git/...)
│   │   └── s3/                      # S3 API (/s3/...)
│   ├── storage/                     # BlobStore + MetadataStore interfaces
│   │   ├── filesystem/
│   │   ├── s3/
│   │   └── memory/
│   ├── auth/                        # Authenticator + Authorizer interfaces
│   ├── metadata/                    # SQLite-backed repository for repos, users, permissions
│   └── middleware/                   # Shared: logging, metrics, recovery, request ID
├── pkg/types/                       # Public shared types (Digest, Descriptor)
├── web/                             # React/Vite SPA source
├── configs/freighter.yaml
└── deployments/Dockerfile
```

### The key interfaces

Three interfaces define the entire system's architecture. **`protocol.Handler`** returns a `Name()`, `Prefix()`, and `chi.Router()` — each protocol module is self-contained and receives dependencies through an `Init(deps)` method. **`storage.BlobStore`** provides `Get`/`Put`/`Exists`/`Delete`/`Stat` for content-addressable blobs. **`auth.Authenticator`** extracts identity from requests while `auth.Authorizer` checks permissions against resources.

### Multi-protocol routing with chi

The routing strategy uses chi's `Mount()` to compose independent protocol routers, each with their own middleware stacks:

```go
r := chi.NewRouter()
r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
r.Get("/healthz", healthHandler)
r.Handle("/metrics", promhttp.Handler())

// Each protocol handler returns its own chi.Router
for _, handler := range protocols.Handlers() {
    r.Mount(handler.Prefix(), handler.Router())
}
```

This pattern — borrowed from Athens's `external/server.go` which creates `http.Handler` from `storage.Backend` — allows OCI to use Bearer token auth, Git to use basic auth, APT to be anonymous, and S3 to use SigV4, all on the same port with different middleware chains per `Mount()` group.

### Why single module, not multi-module

A single `go.mod` with `internal/` packages avoids `go.work` complexity while Go's compiler enforces encapsulation. The protocol handler registration pattern enables runtime enable/disable per configuration without build-time modularity. This matches how distribution/distribution, Athens, and Soft-Serve are all structured.

---

## Part 5: MVP features and operational concerns

### Webhooks with retry logic

Build a DIY webhook sender using `crypto/hmac` + `crypto/sha256` from stdlib, following the Standard Webhooks spec. Store subscriptions and delivery logs in SQLite. Dispatch asynchronously with a goroutine worker pool using exponential backoff (1s, 2s, 4s, 8s, up to 5 attempts). Key events: `artifact.pushed`, `artifact.deleted`, `tag.created`, `repository.created`. For a turnkey solution, **`svix/svix-webhooks`** (~3.5k stars, MIT) handles deliverability, retries, and HMAC signing as a self-hosted Docker service.

### Garbage collection via mark-and-sweep

Distribution/distribution's GC implementation runs in two phases: **mark** (scan all manifests, collect referenced blob digests) then **sweep** (iterate all blobs, delete any not in the mark set). The registry must be read-only during GC — uploading during GC risks deleting in-use layers. For MVP, trigger GC via an admin API endpoint with mandatory `--dry-run` support. Use soft-delete (move to trash directory) before permanent removal.

### Content-addressable storage layout

Store blobs at `/blobs/sha256/<first-2-hex>/<full-digest>/data` — the same layout distribution/distribution uses. Write to a temp file while computing SHA256, then atomic rename to the digest-based path. If the target path already exists, skip (natural deduplication). **`opencontainers/go-digest`** (~400 stars, Apache 2.0) provides standard digest parsing. The **`containerd/containerd/content`** `Store` interface is the canonical Go CAS design to study.

### Rate limiting and upload size control

**`ulule/limiter`** (~2.1k stars, MIT) provides pluggable stores (Redis, in-memory) with built-in middleware for chi, plus `X-RateLimit-*` headers. For upload size limiting, Go's built-in `http.MaxBytesReader` returns 413 when exceeded. Apply different limits per endpoint type — **10 uploads/sec for blob uploads**, 100 reads/sec for downloads, 50/sec for manifest operations.

### Health checks and Prometheus metrics

**`prometheus/client_golang`** (~5.6k stars, Apache 2.0) provides the standard metrics endpoint. Critical metrics for an artifact repository: `registry_http_requests_total` (counter by method/path/status), `registry_http_request_duration_seconds` (histogram), `registry_blob_upload_bytes_total` and `registry_blob_download_bytes_total` (bandwidth), `registry_storage_size_bytes` (gauge), and `registry_gc_blobs_deleted_total`. For health checks, **`alexliesenfeld/health`** (~800 stars) or `heptiolabs/healthcheck` provide `/live` and `/ready` endpoints for Kubernetes probes with async background checks.

### SQLite backup — Litestream for production

Three backup approaches in ascending sophistication. **`VACUUM INTO`** creates a compacted single-file backup from a live database via `db.Exec("VACUUM INTO ?", backupPath)`. The **SQLite Online Backup API** (via mattn/go-sqlite3) copies incrementally, N pages at a time, reducing lock duration. For production, **Litestream** (`benbjohnson/litestream`, ~11k stars, Apache 2.0) provides continuous WAL streaming replication to S3/GCS with point-in-time recovery — near-zero RPO with zero code changes.

Since blobs are immutable and content-addressed, backup consistency is straightforward: the SQLite snapshot captures which blobs should exist. Orphan blobs (present on disk but absent from metadata) are harmless and cleaned by the next GC run.

### Configuration with koanf

**`knadh/koanf`** (~3k stars, MIT) is preferred over Viper for its modular design, case-sensitive keys, no global state, and **313% smaller binaries**. Layer YAML file config with environment variable overrides (e.g., `REGISTRY_STORAGE_PATH=/data`). Group configuration by protocol with enable/disable flags, matching how distribution/distribution (YAML + env substitution) and Gitea (INI + env overrides) handle configuration.

---

## Conclusion: what makes this project tractable

The most surprising finding is how many artifact repository protocols reduce to "serve files + serve a metadata index." PyPI is ~100 lines, Helm is an `index.yaml` plus `.tgz` serving, APT is `Packages` + `Release` + `.deb` serving, and RPM is `repomd.xml` + `primary.xml.gz` + `.rpm` serving. The hard parts are OCI (complex upload workflows with chunked blob uploads and content negotiation) and Git (pack protocol negotiation), but both now have production-quality embeddable `http.Handler` implementations in Go.

The technology choices lock together well. **Chi's `net/http` compatibility** is the lynchpin — it lets you mount go-containerregistry's handler at `/v2/`, go-git v6's handler at `/git/`, and gofakes3's handler at `/s3/` alongside custom Helm/APT/RPM/PyPI handlers, all with per-group middleware. **Pure Go SQLite** (`modernc.org/sqlite`) keeps the single-binary, zero-dependency promise. **`gocloud.dev/blob`** makes the filesystem-vs-S3 storage decision a runtime configuration choice.

The go-git v6 pre-release status is the primary technical risk. The fallback — go-git v5's server transport package with ~200 lines of HTTP bridge code, or `sosedoff/gitkit` shelling out to git — is well-understood. Monitor v6's path to stable release and plan accordingly.
