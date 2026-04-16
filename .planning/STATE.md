---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: Ready to execute
last_updated: "2026-04-16T11:11:56.283Z"
progress:
  total_phases: 5
  completed_phases: 4
  total_plans: 52
  completed_plans: 49
  percent: 94
---

# STATE: OmniRepo

**Last updated:** 2026-04-14

## Project Reference

- **Core value**: A single container that hosts every artifact type a corporate team produces or consumes — Docker images, Linux packages, Python wheels, Helm charts, raw blobs, S3 objects, Git repos — with vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.
- **Current focus**: Phase 1 — Foundation (config, SQLite reader/writer split + FTS5, storage primitives, auth + policy, audit, TLS hot-reload, bootstrap JSON, chi router, healthz/readyz, air-gap CI gate, go-git v6 spike)
- **Granularity**: coarse (5 phases)

## Current Position

Phase: 05 (rest-api-web-ui-production-dockerfile) — EXECUTING
Plan: 10 of 12

- **Phase**: 1 — Foundation
- **Plan**: none yet (planning not started)
- **Status**: not started
- **Progress**: `[                    ] 0/5 phases complete`

## Performance Metrics

| Phase | Plans | Status | Started | Completed |
|-------|-------|--------|---------|-----------|
| 1. Foundation | 0/0 | Not started | - | - |
| 2. OCI + RAW + Scan Pipeline | 0/0 | Not started | - | - |
| 3. Package Repos (RPM + APT + PyPI + Helm) | 0/0 | Not started | - | - |
| 4. S3 + Git | 0/0 | Not started | - | - |
| 5. REST API + Web UI + Production Dockerfile | 0/0 | Not started | - | - |
| Phase 01 P01 | 55 min | 3 tasks | 24 files |
| Phase 01 P02 | 28m | 3 tasks | 16 files |
| Phase 01 P03 | 7m | 2 tasks | 21 files |
| Phase 01 P01-04 | 25m | 3 tasks | 24 files |
| Phase 01 P05 | 55m | 5 tasks | 24 files |
| Phase 01 P06 | 40m | 3 tasks | 11 files |
| Phase 03-package-repos-rpm-apt-pypi-helm P02 | 55m | 2 tasks | 12 files |
| Phase 03-package-repos-rpm-apt-pypi-helm P03 | 70m | 2 tasks | 10 files |
| Phase 03-package-repos-rpm-apt-pypi-helm P04 | 50m | 3 tasks | 13 files |
| Phase 03-package-repos-rpm-apt-pypi-helm P05 | 70m | 3 tasks | 16 files |
| Phase 03 P06 | 75min | 2 tasks | 16 files |
| Phase 03-package-repos-rpm-apt-pypi-helm P07 | 65min | 2 tasks | 12 files |
| Phase 04-s3-git P01 | 7m | 3 tasks | 35 files |
| Phase 04-s3-git P02 | 15m | 2 tasks | 16 files |
| Phase 04-s3-git P03 | 3m | 1 tasks | 4 files |
| Phase 04-s3-git P08 | 18m | 1 tasks | 12 files |
| Phase 04-s3-git P04 | 20m | 2 tasks | 6 files |
| Phase 04-s3-git P06 | 35m | 2 tasks | 5 files |
| Phase 04-s3-git P05 | 5min | 2 tasks | 10 files |
| Phase 04-s3-git P09 | 12m | 2 tasks | 13 files |
| Phase 04-s3-git P07 | 15m | 1 tasks | 9 files |
| Phase 04-s3-git P10 | 15m | 1 tasks | 6 files |
| Phase 04-s3-git P11 | 13m | 1 tasks | 5 files |
| Phase 04-s3-git P12 | 4m | 1 tasks | 4 files |
| Phase 04-s3-git P13 | 10m | 2 tasks | 9 files |
| Phase 05-rest-api-web-ui-production-dockerfile P01 | 13m | 2 tasks | 14 files |
| Phase 05 P02 | 5m | 2 tasks | 85 files |
| Phase 05 P03 | 11m | 2 tasks | 21 files |
| Phase 05 P04 | 12m | 2 tasks | 19 files |
| Phase 05 P05 | 6m | 2 tasks | 39 files |
| Phase 05 P06 | 8m | 2 tasks | 38 files |
| Phase 05 P07 | 9m | 2 tasks | 14 files |
| Phase 05 P08 | 10m | 2 tasks | 16 files |
| Phase 05 P09 | 7m | 2 tasks | 7 files |

## Accumulated Context

### Decisions

From `.planning/PROJECT.md` (Key Decisions — all Pending until phase execution):

- Modular monolith in Go, single binary preferred
- Local filesystem as the only storage backend (S3 is a served protocol, not a store)
- go-git v6 `backend` package with gitkit fallback
- Trivy as bundled subprocess, DB baked at build time
- `modernc.org/sqlite` (pure Go, no CGo)
- Built-in users only in v1 (no LDAP/OIDC/SSO)
- Flat project membership
- One-time password + forced change on first login
- First-run JSON bootstrap
- API keys: full-power within owner reach, revealed once
- Reserved root prefixes enforced at routing
- OpenAPI 3.1 hand-written; `oapi-codegen/v2` for types only
- All UI assets bundled, zero runtime CDN
- Scope reductions: no upstream/proxy, no cron, no webhooks, no quotas, no LDAP in v1

Version-drift corrections to apply at first commit (from `.planning/research/SUMMARY.md`):

- React 18 → React 19.2.5
- Tailwind → Tailwind 4.2.2 (CSS-first config, `@tailwindcss/vite`)
- Vite → Vite 8.0.8
- `oapi-codegen` → `oapi-codegen/v2`
- `koanf` → `koanf/v2`
- go-git v6 import path: `github.com/go-git/go-git/v6/backend` (single package)
- [Phase 01]: Module path pinned to github.com/dxc-internal/omnirepo (OQ-2 default, changeable via go mod edit -module) — Greenfield choice; can be changed later without touching any internal imports.
- [Phase 01]: koanf sub-providers use mixed paths: providers/env/v2 (v2), providers/file + parsers/yaml + providers/structs (non-v2) — Upstream has only partially reorganized under /v2 — confirmed via go get. Downstream plans adopt same import patterns.
- [Phase 01]: go-git v6 isolated behind build tag 'spike' — Avoid dragging ProtonMail/go-crypto and full Git dependency tree into default build; plan 01-06 flips the tag on for the spike.
- [Phase 01]: golangci-lint upgraded to v2.11.4 (Go 1.25 toolchain) with v2 schema config — System binary built with Go 1.24 refused the Go 1.25 module.
- [Phase 01]: Chose _txlock=immediate DSN extension for BEGIN IMMEDIATE (not explicit Exec); every sql.Tx becomes immediate automatically
- [Phase 01]: JSON1 probed functionally via SELECT json(); SQLite 3.38+ compiles it in unconditionally and no longer reports via compile_options
- [Phase 01]: OQ-9 locked: audit DB insert strict, NDJSON mirror best-effort (slog.Warn + swallow). Tests prove each half.
- [Phase 01]: blob_uploads.Start idempotent via ON CONFLICT DO UPDATE (refresh expires_at), not PK error.
- [Phase 01]: [Phase 01] API-key revocation: FindByPrefixSha excludes revoked rows (keeps middleware branch-free). Future admin UI will use separate FindByID for audit.
- [Phase 01]: [Phase 01] auth.Can membership resolution: ctx-attached set via WithProjectMembership (pure, sync, no DB); handlers pre-resolve using MembersRepo (plan 05).
- [Phase 01]: [Phase 01] middleware.Deps uses pointer-to-concrete repos, not interfaces (premature-interface avoidance; one impl in Phase 1).
- [Phase 01]: Repo type validator enforced at both DDL CHECK and Go app layer; Go layer surfaces typed 422 error, DDL is last line of defense
- [Phase 01]: Repo names reuse auth.ProjectNameValid (slug + reserved-prefix) for defense-in-depth, even though repos never sit at top-level URL
- [Phase 01]: Bootstrap idempotency triggered on 'any user row exists' rather than 'super-admin exists' — simpler, safer against partial restores
- [Phase 01]: go-git v6 spike PASSED; Phase 4 proceeds with v6 primary + gitkit fallback behind config flag. v6 alpha ships transport primitives not a backend.Backend type; ~150 LOC Smart-HTTP wrapper required.
- [Phase 01]: CI workflow (.github/workflows/ci.yml) binds every Phase 1 gate to PR merges: lint, build, test, test-airgap, grep-cdn, bench-sqlite, spike.
- [Phase 03-package-repos-rpm-apt-pypi-helm]: Helm v3 chart-repo shipped: PUT/GET/DELETE + .prov pass-through + IndexDirectory-based regen via coalescer.
- [Phase 03-package-repos-rpm-apt-pypi-helm]: PyPI v1 protocol shipped: PEP 503 + 691 reads, twine + PEP 694 uploads, in-memory PEP 694 sessions with TTL + actor binding
- [Phase 03-package-repos-rpm-apt-pypi-helm]: RPM v1 protocol shipped: eager RSA-4096 GPG keygen at repo-create (atomic), .rpm upload, debounced repodata regen with content-hash names + detached-signed repomd.xml.asc, /public-key.asc with RWMutex cache
- [Phase 03-package-repos-rpm-apt-pypi-helm]: Composed repo-create hook: CreateRPMRepoHook (signing key, rpm+deb) + CreateDEBRepoHook (apt_suites matrix) run in one writer tx; avoids adding a second RepoCreateHookFn slot.
- [Phase 03-package-repos-rpm-apt-pypi-helm]: No control_raw column on deb_packages: reconstructControlParagraph emits stored fields in canonical dpkg order for Packages regen.
- [Phase 03-package-repos-rpm-apt-pypi-helm]: Arch-from-control (D-24): client ?suite=&component= picks the tuple, Architecture comes from the parsed .deb; FindByTuple rejects unknown tuples rather than auto-adding.
- [Phase 03-package-repos-rpm-apt-pypi-helm]: Phase 3 conformance gates: per-protocol DinD pkgs (//go:build conformance), pinned image digests in test/conformance/images.txt, exec.LookPath('docker') skip-without-docker; airgap test creates rpm/deb repos via REST so signing-key hooks fire; grep-cdn allowlist for legitimate XML namespace URN (linux.duke.edu)
- [Phase 04-s3-git]: gofakes3 MultipartBackend PRESENT — Plan 06 uses embedded interface directly (no custom multipart handler)
- [Phase 04-s3-git]: gofakes3 has no v1.0.0 tag upstream — pinned to master pseudo-version v0.0.0-20260208201424-4c385a1f6a73
- [Phase 04-s3-git]: gitkit v0.4.0 compiles clean on Go 1.25 — Plan 09 fallback unblocked
- [Phase 04-s3-git]: Probe files named probe_test.go (not _probe_test.go) — Go ignores _-prefixed sources
- [Phase 04-s3-git]: Phase 4 Plan 02 landed migrations 016-019 + typed repos (S3KeysRepo, S3ObjectsRepo, S3MultipartRepo, GitRefsRepo) + repos.GitMaxPushBytes; strftime timestamp convention adopted for new tables for Phase-3 consistency
- [Phase 04-s3-git]: ErrS3AccessKeyNotFound collapses missing+revoked (D-12 no-oracle enforced at repo layer)
- [Phase 04-s3-git]: Config validator-with-fallback pattern: max_push_bytes=0 is sentinel for inherit-default; negative rejected (T-04-03-02 DoS mitigation)
- [Phase 04-s3-git]: Plan 03 landed Phase 4 config surface: server.git_backend (gogit|gitkit), repos.git.max_push_bytes (500 MiB), external_hostnames; first typed Config.Validate() method
- [Phase 04-s3-git]: Plan 08 promoted Phase 1 git spike to production GitServer interface: gogit (go-git v6 transport primitives) + gitkit (subprocess fallback) + pktline sideband + InitBare helper; spike files deleted per D-28
- [Phase 04-s3-git]: SigV4 verifier landed: hand-rolled canonical-request/HMAC-chain/constant-time-compare + STREAMING chunked parser (64 MiB per-chunk cap); all 5 aws4_testsuite vectors byte-exact + 14 behavioral tests green
- [Phase 04-s3-git]: Plan 06 landed: gofakes3.Backend + MultipartBackend on storage.WriteAndRename with known-vector multipart ETag + 24h orphan GC
- [Phase 04-s3-git]: gofakes3 CreateBucket gated by Backend.DefaultProjectID=0 in production; REST uses CreateBucketForProject(name, projectID)
- [Phase 04-s3-git]: Added ActionManageS3Keys (new action) + ActorKindS3Key for project-scoped S3 auth dispatch; shown-once secret in POST only
- [Phase 04-s3-git]: Plan 09: HTTP Basic auth project: variant uses login='project', pw='<projname>:<key>' — Go's BasicAuth splits on first colon; capturingReader+bufferingWriter pattern for MaxBytesError capture in pushcap middleware
- [Phase 04-s3-git]: Plan 07: VHostRewrite as global middleware before routes (chi constraint); SigV4 verifies against original pre-rewrite path via context stash; r.Host injected into r.Header for canonical-request computation
- [Phase 04-s3-git]: Plan 10: HEAD fetched via explicit Reference(plumbing.HEAD) + dedup; Git hook composed into existing chain; audit event git.refs.synced carries ref_count only
- [Phase 04-s3-git]: Clock-skew test uses hand-rolled SigV4 (aws-sdk-go-v2 has no NowFunc); bucket provisioning via direct DB insert (S3 CreateBucket admin-gated)
- [Phase 04-s3-git]: Git DinD conformance: conformance-git + test-git-conformance alias; 10 MiB cap for oversize test; alpine/git:2.43.0 pinned image
- [Phase 04-s3-git]: TEST-07 hard gate: child-process VmRSS bench at 50ms with peak_rss < 3x repo_bytes; gitgen uses git CLI with fixed env for deterministic byte-identical packs
- [Phase 05]: ErrorResponse excluded from OpenAPI spec to avoid pointer-field conflicts with existing errors.go
- [Phase 05]: FTS5 UNION ALL arms use subquery wrapping for per-arm LIMIT in SQLite compound selects
- [Phase 05]: oapi-codegen v2.6.0 types-only generation with required fields for concrete Go types
- [Phase 05]: Latest available npm versions used (React 19.1, Vite 6.3, Tailwind 4.1) since plan-specified versions don't exist yet
- [Phase 05]: Reuse ActionTriggerGC gate for all admin endpoints (super-admin-only) rather than adding new action constants
- [Phase 05]: MaintenanceMode middleware accepts *SettingsRepo parameter (nil-safe backward compat)
- [Phase 05]: Trivy DB upload: temp dir + atomic rename to DataRoot/trivy/db with path traversal prevention
- [Phase 05]: Git browse uses go-git v6 PlainOpen for read-only tree/blob/commit walking (no subprocess)
- [Phase 05]: SPA handler serves only from embedded dist/ FS; dev proxy to Vite on :5173 when OMNIREPO_DEV=1
- [Phase 05]: framer-motion upgraded 12.7.4->12.38.0 for motion-dom compat; admin page stubs for lazy loading
- [Phase 05]: base-ui Button uses render prop for Link composition (not asChild); framer-motion ease needs 'as const'; common components in web/src/components/common/
- [Phase 05]: RepoPageLayout extracts shared breadcrumb/tabs/settings for all repo types
- [Phase 05]: PyPI/Helm use grouped expandable table pattern; RAW/S3 share prefix navigation pattern
- [Phase 05]: Shiki core with on-demand language loading to avoid bundling all grammars
- [Phase 05]: DOMPurify sanitization of Shiki output as defense-in-depth (T-05-08-01)
- [Phase 05]: DiceBear avatars rendered as data URI images for XSS safety

### Todos

(none — awaiting `/gsd-plan-phase 1`)

### Blockers

(none)

### Research Flags

- **Phase 4 go-git v6**: Alpha-tagged library; Phase 1 includes a spike as a hard gate. Gitkit fallback preserved behind a config flag.
- **Phase 3 APT data model**: Multi-suite × component × arch tuple representation needs explicit schema design before sprint start.
- **Phase 5 Tailwind 4 CSS-first config**: One-time scaffold decision; handle at Phase 5 kickoff, not mid-sprint.

## Session Continuity

- **Next action**: Run `/gsd-plan-phase 1` to decompose Phase 1 (Foundation) into executable plans.
- **Artifacts on disk**:
  - `.planning/PROJECT.md`
  - `.planning/REQUIREMENTS.md` (167 v1 requirements, all mapped)
  - `.planning/ROADMAP.md` (5 phases, this file's source of phase structure)
  - `.planning/research/SUMMARY.md`, `STACK.md`, `FEATURES.md`, `ARCHITECTURE.md`, `PITFALLS.md`
  - `docs/superpowers/specs/2026-04-14-omnirepo-v1-design.md`
  - `tools.md` (technology blueprint)
  - `.planning/config.json` (granularity=coarse, parallelization=true, model_profile=quality)

---
*State initialized: 2026-04-14*
