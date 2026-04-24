# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository status

This repo currently contains **design documentation only** — no source code, build system, or tests yet. The project (OmniRepo) is in the planning/blueprint phase.

- `README.md` — GitLab-generated boilerplate, not project-specific.
- `tools.md` — the real entry point: a detailed technical blueprint for the system to be built.

When asked to "run tests", "build", etc., there is nothing to run yet. Surface this rather than inventing commands.

## What OmniRepo is (per `tools.md`)

A single Go binary intended to serve multiple artifact protocols on one port:

- **OCI/Docker registry** — embed `google/go-containerregistry/pkg/registry` (or `rogpeppe/ociregistry`) as an `http.Handler`.
- **RPM repo** — parse with `cavaliergopher/rpm`; generate `repomd.xml` + `primary.xml.gz` via `encoding/xml`.
- **APT/Debian repo** — build from scratch (ar archives, Packages/Release/InRelease); sign with `ProtonMail/go-crypto`.
- **PyPI** — PEP 503 Simple API, ~100 lines of HTML generation.
- **Helm** — `helm.sh/helm/v3/pkg/repo` for `index.yaml`.
- **S3-compatible** — embed `johannesboyne/gofakes3` (MinIO is AGPLv3 and uses `internal/`, so it can't be embedded).
- **Git hosting** — go-git v6's new `backend/http` (pure-Go Smart/Dumb HTTP, pre-release Feb 2026).
- **Vulnerability scanning** — embed `golang.org/x/vuln/scan` for Go; run Trivy/Grype as sidecars for other ecosystems.
- **Routing** — `go-chi/chi` to multiplex the protocols on one port.

Architectural principle: **embed as `http.Handler`, don't fork.** Protocol handlers share one storage backend behind pluggable interfaces (`BlobHandler`, gofakes3 `Backend`, etc.). Most package-repo "protocols" are just metadata-over-HTTP and need no special library.

License watch-outs called out in the blueprint: MinIO (AGPLv3), `containers/image` (needs CGo), `distribution/distribution` (unstable interfaces).

Consult `tools.md` before proposing libraries or architecture — it already contains researched choices with rationale, stars, licenses, and update dates.

<!-- GSD:project-start source:PROJECT.md -->
## Project

**OmniRepo**

OmniRepo is a self-hosted, internal artifact repository server — a focused, simpler alternative to JFrog Artifactory or Sonatype Nexus for small-to-mid corporate environments. From a single Docker container on one HTTP/HTTPS port it serves OCI/Docker, RPM/YUM, APT/Debian, PyPI, Helm, RAW, S3-compatible buckets, and Git hosting, with a built-in user/project model, web UI, REST API, and Trivy-powered vulnerability scanning. Designed to run in air-gapped corporate networks.

**Core Value:** A single container that hosts every artifact type a corporate team produces or consumes — Docker images, Linux packages, Python wheels, Helm charts, raw blobs, S3 objects, Git repos — with vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.

### Constraints

- **Tech stack**: Go (modern, modular monolith) — single Go module with `internal/` packages enforcing module boundaries.
- **Tech stack**: React 18 + TypeScript + Vite + Tailwind + shadcn/ui for the UI; embedded into the Go binary via `//go:embed`.
- **Tech stack**: SQLite via `modernc.org/sqlite` (pure Go, cross-compile-friendly) for metadata.
- **Tech stack**: chi for HTTP routing; gocloud.dev/blob deliberately NOT used — local FS only.
- **Runtime air-gap**: zero outbound network calls without explicit user action. Trivy DB updates only via tarball upload or admin button.
- **No in-process schedulers**: OmniRepo has no internal cron, timers, or time-based job firers. Sync is triggered only by (a) the "Sync now" button in the UI, or (b) an external scheduler (crontab, systemd timer, Kubernetes CronJob, etc.) hitting the `/sync` REST endpoint — the worked example lives at `docs/operations/scheduled-sync.md` (shipped in v1.4). Proposals to add in-process cron (e.g. the SCHEDSYNC phase that was removed from v1.5 on 2026-04-24) are out of scope — adding a scheduler goroutine, a cron parser, next-run state, UI surface, and audit events gives negligible benefit over a one-line crontab entry and expands the failure surface.
- **Licensing**: every dependency must be Apache-2.0-compatible (corporate constraint). MinIO (AGPLv3) explicitly excluded; gofakes3 (MIT) used instead.
- **Persistence**: one mounted volume at `/var/lib/omnirepo/`; everything mutable lives there.
- **Security**: argon2id password hashing; HTTPS with self-signed cert by default; admin-uploaded certs hot-reload without restart.
- **Testing**: every feature ships with tests; `make test` green is the merge gate; UI verified with Playwright by the developer before declaring done.
- **No documentation files unless explicitly required** by the task — per project standard, prefer code over docs except for the spec/plan artifacts GSD generates.
<!-- GSD:project-end -->

<!-- GSD:stack-start source:research/STACK.md -->
## Technology Stack

## Recommended Stack
### Go core (single binary, pure-Go, Apache-2.0-compatible)
| Technology | Version (verified 2026-04-14) | License | Purpose | Why recommended |
|---|---|---|---|---|
| Go toolchain | 1.25.x | BSD-3 | Build, test, cross-compile | `go-git/v6` `go.mod` requires `go 1.25`. Use 1.25 as the project baseline. `go build -trimpath -ldflags="-s -w"` per spec §19. |
| `github.com/go-chi/chi/v5` | v5.2.5 | MIT | HTTP router, `http.Handler` composition | 100 % `net/http`-compatible. Lets us `Mount()` every protocol handler side-by-side on one port with per-group middleware. No adapter glue for `go-containerregistry`, `go-git` `backend`, or `gofakes3`. |
| `modernc.org/sqlite` | v1.48.2 (wraps SQLite 3.51.x, Mar 2026) | BSD-3 | Metadata store, FTS5 search | Pure-Go: builds `FROM scratch`, trivial cross-compile (amd64 + arm64), no CGo toolchain in the Docker build. FTS5 is compiled in. Perf penalty vs CGo is ~1.5-2× on writes (irrelevant for artifact-metadata write rates). |
| `github.com/google/go-containerregistry` | v0.21.5 (`pkg/registry` subpackage) | Apache-2.0 | OCI/Docker registry `http.Handler` | Authors self-describe as safe-for-tests, wanting prod integration feedback — already used at small scale in prod; maintainer-maintained, low-dep. Pluggable `BlobHandler`/`BlobStatHandler`/`BlobPutHandler` lets us write blobs into our CAS. Preferred over `distribution/distribution` which explicitly labels its interfaces unstable. |
| `github.com/go-git/go-git/v6` | v6.0.0-alpha.1 (tag) / main (has `backend/` pkg) | Apache-2.0 | Git Smart HTTP `http.Handler`, bare-repo management | `backend` package (`main`/v6) exposes `Backend` with `ServeHTTP` implementing smart + dumb HTTP. Used through `backend.New(loader)` with `transport.Loader` mapping URL → storage. Same module also gives bare-repo create/list refs/walk commits for UI browsing. **Alpha caveat** — see §"Risks & mitigations". |
| `github.com/johannesboyne/gofakes3` | v1.0.0 (tagged Sep 2025; first stable release) | MIT | S3-compatible `http.Handler` | v1.0.0 shipped multipart upload, versioning, CORS, conditional writes, spec-compliant errors, stdlib routing, and removed AWS SDK v1. Pluggable `Backend` interface → our filesystem driver. **Does not verify SigV4** — we add ~200 LOC middleware per spec §11. MinIO is excluded (AGPLv3 + `internal/` packages). |
| `github.com/cavaliergopher/rpm` | v1.3.0 | BSD-3 | Parse `.rpm` headers → NEVRA/deps/checksums | Pure-Go RPM header parser. `encoding/xml` (stdlib) covers `repomd.xml` + `primary.xml.gz` generation per spec §6. Optional peer: `sassoftware/go-rpmutils` for PGP sig validation of incoming RPMs. |
| `github.com/ProtonMail/go-crypto` | v1.4.1 | BSD-3 | OpenPGP InRelease clearsigning for APT | Successor to the removed `x/crypto/openpgp`. Already pulled in transitively by `go-git/v6` (`ProtonMail/go-crypto v1.4.1` in its `go.mod`) — zero additional tree cost. Used to clearsign `Release` → `InRelease` and detached-sign `Release.gpg`. |
| `helm.sh/helm/v3/pkg/repo` | v3.20.x (Helm 3.20.2 series) | Apache-2.0 | `index.yaml` generation, `Chart.yaml` loading from `.tgz` | Canonical library. Use `pkg/repo.IndexFile` + `pkg/chart/loader`. Helm 4.x is released (v4.1.4) but v3 SDK is the stable, widely-embedded API; v4 SDK still moving. Recommend staying on `helm.sh/helm/v3` through v1.0. |
| PyPI Simple (PEP 503) | — stdlib `html/template` | — | PEP 503 Simple repository HTML | No library required. Two HTML pages: index of projects and per-project file lists with `href="<url>#sha256=<hex>"`. Normalize project names: `re.sub(r"[-_.]+", "-", name).lower()` — implement in Go with `regexp` or manually. |
| `github.com/knadh/koanf/v2` | v2.3.4 | MIT | Config (YAML file + env overrides) | Modular (pick only YAML + env providers), no global state, ~3× smaller binary impact than Viper. v2 module path pinned in upstream `go.mod`. Pair with `providers/file` + `providers/env` + `parsers/yaml`. |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | MIT | HMAC-JWT for `/v2/token` Docker exchange | `v5` is the maintained line. Use HS256 with a random 256-bit secret stored in `settings` table (spec §13 `docker_token_hmac_secret`). 15-min TTL per spec. Do **not** use v4 — it's EOL. |
| `golang.org/x/crypto/argon2` | Tracks Go ecosystem | BSD-3 | Password hashing | Argon2id parameters: `time=3`, `memory=64 MiB`, `threads=4`, 16-byte salt (crypto/rand), 32-byte key. Encode as `$argon2id$v=19$m=65536,t=3,p=4$<salt_b64>$<hash_b64>`. These are OWASP 2026 low-RAM defaults. |
| `golang.org/x/crypto/acme/autocert` | — | BSD-3 | **Not used** | OmniRepo generates its own self-signed cert (spec §14) and accepts admin-uploaded certs; no ACME flow (air-gap). Listed here explicitly to pre-empt the question. |
| `github.com/oapi-codegen/oapi-codegen/v2` | v2.6.0 | Apache-2.0 | Generate Go types from hand-written OpenAPI 3.1 spec | Spec §16 locks: types only, routes hand-written on chi. Use `-generate types` only. No runtime dependency — `oapi-codegen` runs at `go generate` time. |
| Trivy (external binary) | v0.69.3 (Mar 2026) | Apache-2.0 | Vulnerability scanning, SBOM generation | Bundled as a binary in the Docker image (stage 3 of the multi-stage build). Invoked as subprocess with `--skip-db-update --offline-scan` always. We wrap it behind a `Runner` interface for test fakes (spec §18). **Do not import** — Trivy's Go dep tree is enormous and its Go API is not stable. |
| (optional fallback) `git` binary | Latest from Alpine repo at build time | GPLv2 (binary, not linked) | Git fallback if go-git v6 HTTP backend regresses | Shipped in the final Alpine image per spec §19. Used only by an opt-in code path wrapped behind `sosedoff/gitkit` (v0.4.0, MIT) if we discover go-git v6 HTTP backend blockers. Default remains pure-Go. |
| `github.com/google/uuid` | v1.6.x | BSD-3 | UUIDs for scan IDs, SBOM IDs, API key IDs | Standard choice; trivial drop-in. |
| `github.com/opencontainers/go-digest` | v1.0.0 | Apache-2.0 | Canonical `sha256:<hex>` digest parsing for OCI blobs | Pulled in transitively by `go-containerregistry` anyway; use it directly in our CAS paths so blob lookups match OCI digest format. |
### Frontend (bundled; zero CDN at runtime)
| Technology | Version (verified 2026-04-14 on npm) | License | Purpose | Why recommended |
|---|---|---|---|---|
| React | **19.2.5** | MIT | SPA framework | Stable React 19 is shipping. **Spec says "React 18" — recommend bumping to 19.2 before writing any UI code.** See §"Spec deviations". |
| TypeScript | 6.0.2 | Apache-2.0 | Type safety | TS 6 is stable as of 2026 Q1. `tsconfig.json`: `"moduleResolution": "bundler"`, `"jsx": "react-jsx"`. |
| Vite | **8.0.8** | MIT | Dev server + production build | Vite 8 is current. Spec just says "Vite" — pin to 8.x. Dev proxy (`server.proxy`) points `/api` and `/v2` at Go in dev mode when `OMNIREPO_DEV=1`. |
| Tailwind CSS | **4.2.2** | MIT | Utility CSS | **Tailwind 4 uses CSS-first config and requires the `@tailwindcss/vite` plugin (no more `tailwind.config.js` by default).** Scaffold accordingly — there is no drop-in upgrade from v3. |
| shadcn/ui (CLI) | shadcn@4.2.0 | MIT | Generator for Radix+Tailwind components (copied into repo, not a runtime dep) | Use `npx shadcn@latest init` then `npx shadcn@latest add button dialog form input ...`. Output lands in `web/src/components/ui/`. Fully owned code — no external runtime dependency. Pair with **Radix UI primitives** (MIT) for underlying accessibility. |
| TanStack Query | @tanstack/react-query 5.99.x (2026-04 release) | MIT | Server-state cache for REST API | Dominant choice for fetching/caching/retries. Query keys structured as `['projects', projectName, 'repos', repoName]`. |
| React Router | react-router-dom 7.14.x | MIT | Client-side routing in SPA | v7 unifies the old RR + Remix APIs. Use data-router API (`createBrowserRouter`). |
| lucide-react | 1.8.0 (tree-shakeable SVG icons) | ISC | Icon set | Tree-shaken at build time — only imported icons end up in the bundle. Ships SVG as JSX; no icon font, no network fetch. |
| @dicebear/core + @dicebear/collection | 9.4.2 | MIT | Avatar generation from a stored seed string | Renders SVG client-side from a seed — zero network at runtime. Pick one collection (`initials` or `shapes`) and pin it to keep bundle small. |
| swagger-ui-dist | 5.32.3 (npm package) | Apache-2.0 | Swagger UI served from `/api/docs` | Copy `dist/` contents into `web/public/swagger/` at build time so they end up embedded via `//go:embed` alongside the SPA. No runtime CDN. Load `openapi.yaml` from `/api/v1/openapi.yaml` (served by Go). |
| Inter + JetBrains Mono `.woff2` | Inter 4.x, JetBrains Mono 2.304+ | SIL OFL 1.1 | Self-hosted fonts | Pull `.woff2` files into `web/src/assets/fonts/`, reference via `@font-face` with `font-display: swap`. OFL allows redistribution. |
| Playwright | @playwright/test 1.56+ | Apache-2.0 | Developer-run E2E tests | Per global rule + spec §18, UI flows verified via Playwright by the developer before declaring feature done. Run headlessly in CI if available, otherwise local only. |
### Development tools
| Tool | Purpose | Notes |
|---|---|---|
| `make` + Makefile | Orchestrate build/test/dev | Targets from spec §20: `dev`, `build`, `docker`, `test`, `e2e`, `seed`, `vendor`. |
| `air` (cosmtrek/air) v1.49+ | Live reload for Go during `make dev` | Watches `internal/` + `cmd/`, rebuilds on change. Pairs with Vite dev server. |
| `golangci-lint` v1.62+ | Linting gate | Enable `gofmt`, `govet`, `staticcheck`, `errcheck`, `gosec`, `revive`. Run in `make test`. |
| `go vendor` | Reproducible offline builds | `make vendor` commits `vendor/` so the Docker Go-build stage works even in a partially air-gapped build env. |
| `govulncheck` (`golang.org/x/vuln/cmd/govulncheck`) | Supply-chain vuln check for Go deps | Run in CI / as part of `make test`. Lightweight, offline-capable with cached DB. |
| `trivy fs .` (in CI) | Supply-chain vuln check for npm + Go | Same Trivy we ship; dogfooding. |
| Docker Buildx | Multi-arch image builds | Emit `linux/amd64` + `linux/arm64`. |
| `docker-compose.yaml` | Local dev bring-up | Per spec §19: OmniRepo + throwaway client container for manual protocol testing. |
## Installation (representative)
# Go module init (one-time)
# Frontend (run in web/)
## Alternatives Considered
| Recommended | Alternative | Why we stayed with the recommended |
|---|---|---|
| `modernc.org/sqlite` | `ncruces/go-sqlite3` (WASM via wazero) | ncruces benchmarks ~3× faster on prepared-statement reads, but it's newer (less battle-tested in server contexts) and the WASM runtime adds binary size. Our DB is metadata-only — modernc's perf is more than adequate. Re-evaluate if FTS5 queries become a bottleneck. |
| `modernc.org/sqlite` | `mattn/go-sqlite3` (CGo) | Breaks `FROM scratch`, complicates cross-compile, forces the Alpine Go-build stage to install `musl-dev` + `gcc`. Against the single-binary constraint. |
| `go-chi/chi/v5` | `labstack/echo` v4 | Echo is fine, but its handler type (`func(c echo.Context) error`) isn't `http.Handler`; every embedded third-party handler needs a wrapper. Chi's `net/http`-native design is the specific reason multi-protocol mounting is clean. |
| `google/go-containerregistry/pkg/registry` | `rogpeppe/ociregistry` | `ociregistry`'s composable Interface is elegant but has fewer downstream users and less integration testing against real-world Docker/Podman/crane clients. go-containerregistry is what `crane`, `ko`, and countless CI tools drive. |
| `google/go-containerregistry/pkg/registry` | `distribution/distribution` (embedded) | Its interfaces are explicitly marked unstable; embedding = pinning against a moving target. Run it as a sidecar or fork — neither fits the single-binary constraint. |
| `go-git/v6` backend | `sosedoff/gitkit` (shells to `git`) | Kept as **fallback** only. Works fine but requires the `git` binary in the image (we ship it anyway as safety net) and spawns a subprocess per request — slower, more complex error handling. Use only if go-git v6 HTTP backend regresses. |
| `johannesboyne/gofakes3` | MinIO embedded | **AGPLv3** — infects the entire project. Also uses `internal/` packages blocking embedding. Explicitly excluded by the corporate license constraint. |
| `johannesboyne/gofakes3` | Writing S3 protocol from scratch | ~6000 LOC of spec-work duplicated. gofakes3 v1.0.0 is now a stable tagged release with pluggable backend — just use it and layer SigV4 verification on top. |
| Trivy (subprocess) | `anchore/grype` (Go library) | Grype's SBOM/vuln pipeline is comparable, but Trivy covers more ecosystems (OS packages, language packages, misconfigs, secrets) with one invocation and its DB is smaller. Also: spec §10 already locks Trivy — no reason to re-open. |
| Trivy (subprocess) | `golang.org/x/vuln/scan` (embed) | `x/vuln` only covers Go modules. Use both: Trivy for artifacts, `govulncheck` for OmniRepo's own supply chain in CI. |
| `knadh/koanf/v2` | `spf13/viper` | Viper is heavier, has global state, and pulls ~20 transitive deps. Koanf is modular (we only need yaml + env) and ~3× smaller binary impact. |
| `oapi-codegen` (types only) | Full stub generation | Spec §10 explicitly locks "types only; routes written by hand" for control over middleware ordering, auth handling, and streaming uploads. Keep that decision. |
| React 19.2 | React 18.3 | See spec deviation below — 19.2 is stable and shipping; starting new UI on 18 just means a forced upgrade later. |
| Tailwind v4 | Tailwind v3 | v4 is stable and faster (Lightning CSS), but config model changes. Worth the one-time learning curve on a greenfield project. |
| shadcn CLI v4 | Radix UI + hand-rolled components | shadcn generates the Radix integration we'd write by hand anyway, and keeps the code in-repo (no runtime package to track). |
## What NOT to Use
| Avoid | Why | Use instead |
|---|---|---|
| MinIO (any version) | AGPLv3 (infects project) + uses `internal/` packages (can't embed) | `gofakes3` v1.0.0 + SigV4 middleware |
| `distribution/distribution` embedded | Interfaces explicitly unstable; tight coupling; intended to run standalone | `google/go-containerregistry/pkg/registry` |
| `containers/image` | Requires CGo + system libs (libgpgme, libdevmapper); client-oriented, not server-oriented | `google/go-containerregistry` for both client (sync-from-external) and server |
| `mattn/go-sqlite3` | CGo — breaks `FROM scratch`, complicates cross-compile | `modernc.org/sqlite` |
| `golang-jwt/jwt` v4 | Deprecated (EOL); security fixes only land in v5 | `golang-jwt/jwt/v5` |
| `golang.org/x/crypto/openpgp` | Removed from x/crypto; unmaintained | `github.com/ProtonMail/go-crypto/openpgp` (drop-in replacement) |
| `spf13/viper` | Heavy deps, global state, case-insensitive keys cause real bugs | `knadh/koanf/v2` |
| `gorilla/mux` | Archived (2022), resurrected but development is low-tempo; no `Mount` semantics for sub-routers | `go-chi/chi/v5` |
| `gin-gonic/gin` | Not `http.Handler`-compatible for handler functions; mounting foreign handlers requires adapters | `go-chi/chi/v5` |
| `bcrypt` for new passwords | Caps input at 72 bytes, slower than Argon2id on tuned params, no memory-hardness | `argon2id` via `golang.org/x/crypto/argon2` |
| Any package-registry web framework that isn't `http.Handler`-native | Fights the one-port-many-protocols architecture | `chi` + `http.Handler` everywhere |
| Running Trivy as a Go library import | Dep tree explosion (~400+ transitive deps); Trivy's Go API is not stabilized | Trivy binary as subprocess |
| `gocloud.dev/blob` | Adds abstraction for backends we're **not** supporting (S3/GCS/Azure). Spec §"Constraints" explicitly excludes. | Direct `os`/`io/fs` against `/var/lib/omnirepo/` |
| CDN-hosted fonts / icons / Swagger UI | Violates air-gap invariant | `.woff2` in repo, `lucide-react` tree-shaken, `swagger-ui-dist` copied at build |
| LDAP / OIDC libraries in v1 | Out of scope per spec §1; adds auth complexity | Built-in users + session cookies + API keys |
## Stack patterns by variant
- Fall back to `sosedoff/gitkit` v0.4.0 behind the same `GitServer` interface.
- Keep the `git` binary in the final image (already shipped as safety net per spec §19 item 3).
- Swap is a single constructor change in `internal/protocol/git`.
- Use `github.com/google/go-containerregistry/pkg/v1/remote` (same module we already import for the server). No additional dep.
- Add a `tantivy-go` or `blevesearch/bleve` index as a parallel search store. Not needed for v1 volumes (<1 M rows).
- `golang.org/x/net/http3` + wrapping `tls.Config`. Not in v1 scope.
## Version compatibility notes
| Combination | Notes |
|---|---|
| Go 1.25 + modernc.org/sqlite v1.48 + FTS5 | FTS5 is built into modernc by default; no build tag needed as of v1.47+. Verify with `sqlite3_compileoption_used` at startup. |
| go-git/v6 main + `ProtonMail/go-crypto` v1.4.1 | Shared dependency. Pin our direct dep to exactly v1.4.1 to avoid `go mod tidy` drift. |
| Tailwind 4 + Vite 8 + shadcn@4 | Tailwind 4 requires `@tailwindcss/vite` plugin; shadcn@4 CLI understands Tailwind 4 conventions. Do **not** mix with Tailwind 3 docs online. |
| React 19.2 + React Router 7 + TanStack Query 5 | All three support React 19 natively. React Router v7 removed the split between `react-router` and `react-router-dom`; use `react-router-dom` or the unified `react-router` package per v7 docs. |
| `swagger-ui-dist` 5.32 + OpenAPI 3.1 | 5.x supports OAS 3.1 fully (3.1 features: `null` type, `exclusiveMinimum` as number, etc.). |
| oapi-codegen v2.6 + OpenAPI 3.1 | Full 3.1 support landed in v2.x; v1.x was 3.0 only — do not use v1. |
| `go-chi/chi/v5` + all `http.Handler` embeds | Works as long as the embed is a `http.Handler`. go-containerregistry `registry.New()` returns one. go-git `backend.New(loader)` returns `*Backend` which implements `ServeHTTP`. gofakes3 `gofakes3.New(backend).Server()` returns one. |
| Trivy v0.69 + baked DB | Trivy DB schema is versioned; the binary refuses to use a DB built by a significantly newer Trivy. Pin both binary and DB in the same Docker build stage. |
## Spec deviations recommended
## Risks & mitigations (library-level)
| Risk | Likelihood | Mitigation |
|---|---|---|
| go-git v6 `backend` breaks API before tagged stable | Medium | Wrap it behind `internal/protocol/git.Server` interface; the `sosedoff/gitkit` fallback implements the same interface; swap takes <1 day. Phase 1 spike proves both paths before downstream work depends on either. |
| `google/go-containerregistry/pkg/registry` turns out to have bugs under heavy concurrent push load | Low | The maintainers explicitly invite prod feedback + PRs. We have a subprocess-fallback option (run `docker registry` as sidecar) but that's an escape hatch — don't plan for it. Write a push-concurrency conformance test early (spec §18). |
| gofakes3 SigV4 middleware complexity | Low | AWS publishes the canonical-request algorithm with examples; multiple open-source Go impls to crib from (e.g. aws-sdk-go-v2 signer internals). 200-300 LOC. Tests with `aws-sdk-go-v2` as client prove correctness. |
| Trivy binary + DB size (~70 MB + ~500 MB DB compressed) | Medium | This is the dominant image-size cost. Accept it — feature is user-requested. Consider distroless base later to claw back ~20 MB. |
| Helm 3 SDK maintenance vs Helm 4 | Low | Helm 3 line is LTS-ish; v3.20 is current. Migration to Helm 4 SDK is deferred work, not v1 blocker. |
| Tailwind 4 breaking changes vs shadcn templates | Low | shadcn CLI v4 targets Tailwind 4. Any third-party shadcn components found online must be audited for v3 assumptions (class names changed in a few cases). |
## License summary
- **Apache-2.0:** go-containerregistry, go-git, Helm SDK, oapi-codegen, opencontainers/go-digest, swagger-ui-dist, Trivy, Playwright
- **BSD-3-Clause:** modernc.org/sqlite, cavaliergopher/rpm, ProtonMail/go-crypto, golang.org/x/crypto, google/uuid
- **MIT:** chi, gofakes3, koanf, golang-jwt, sosedoff/gitkit (fallback), React, Vite, Tailwind, shadcn, lucide-react, @dicebear/*, TanStack Query, React Router, swagger-ui-dist (partial), tailwindcss
- **ISC:** lucide-react (verified)
- **SIL OFL 1.1:** Inter and JetBrains Mono fonts (allows embedding + redistribution)
## Sources
- **Context7** `/go-git/go-git` — confirmed v6 module path and `backend` package; 132 snippets reviewed
- **Context7** `/gitlab_cznic/sqlite` + `/modernc-org/sqlite` — confirmed pure-Go, current
- **Context7** `/google/go-containerregistry` — confirmed production-usable, pluggable backends
- **Context7** `/johannesboyne/gofakes3` — confirmed MIT + embeddable
- **Context7** `/knadh/koanf` — confirmed v2 module path
- **Context7** `/oapi-codegen/oapi-codegen` — confirmed current tooling
- **GitHub API** `go-git/go-git/contents/backend` — **direct verification** that `backend/backend.go` + `backend/http.go` exist on `main`, exposing `Backend{ServeHTTP}`; module path `github.com/go-git/go-git/v6`; requires Go 1.25
- **GitHub releases API** — verified latest tags: chi v5.2.5, modernc/sqlite v1.48.2, go-containerregistry v0.21.5, gofakes3 v1.0.0, helm v4.1.4 (but staying on v3.20 SDK), koanf v2.3.4, oapi-codegen v2.6.0, golang-jwt v5.3.1, cavaliergopher/rpm v1.3.0, ProtonMail/go-crypto v1.4.1, Trivy v0.69.3 (2026-03-03)
- **npm registry API** — verified latest: react 19.2.5, vite 8.0.8, tailwindcss 4.2.2, @tanstack/react-query 5.99.0, react-router-dom 7.14.1, lucide-react 1.8.0, @dicebear/core 9.4.2, swagger-ui-dist 5.32.3, typescript 6.0.2
- **pkg.go.dev** — gofakes3 v1.0.0 release notes (multipart, versioning, stdlib routing, no SigV4 native — confirms spec's middleware plan)
- **pkg.go.dev** — go-git v5 `plumbing/transport/server` (fallback reference; v5 has protocol logic but not HTTP handler)
- **`tools.md`** (project) — prior blueprint research, re-validated
- **`docs/superpowers/specs/2026-04-14-omnirepo-v1-design.md`** §10 — library choices validated; 5 version refinements flagged above
- **HIGH:** Go core libraries (chi, modernc/sqlite, go-containerregistry, gofakes3, cavaliergopher/rpm, ProtonMail/go-crypto, Helm SDK, koanf, oapi-codegen, golang-jwt, Trivy, argon2) — all version-verified via upstream today, all widely used.
- **HIGH:** Frontend libraries — npm-verified today, all stable releases.
- **MEDIUM:** go-git v6 — exists and has the backend package we need, but only `alpha.1` is tagged. Pure-Go Git HTTP serving is a real capability; the risk is pinning to `main` until v6.0 stable lands. Fallback (`gitkit` + `git` binary) is already in the spec.
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->
## Conventions

Conventions not yet established. Will populate as patterns emerge during development.
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->
## Architecture

Architecture not yet mapped. Follow existing patterns found in the codebase.
<!-- GSD:architecture-end -->

<!-- GSD:skills-start source:skills/ -->
## Project Skills

No project skills found. Add skills to any of: `.claude/skills/`, `.agents/skills/`, `.cursor/skills/`, or `.github/skills/` with a `SKILL.md` index file.
<!-- GSD:skills-end -->

<!-- GSD:workflow-start source:GSD defaults -->
## GSD Workflow Enforcement

Before using Edit, Write, or other file-changing tools, start work through a GSD command so planning artifacts and execution context stay in sync.

Use these entry points:
- `/gsd-quick` for small fixes, doc updates, and ad-hoc tasks
- `/gsd-debug` for investigation and bug fixing
- `/gsd-execute-phase` for planned phase work

Do not make direct repo edits outside a GSD workflow unless the user explicitly asks to bypass it.
<!-- GSD:workflow-end -->

<!-- GSD:profile-start -->
## Developer Profile

> Profile not yet configured. Run `/gsd-profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
