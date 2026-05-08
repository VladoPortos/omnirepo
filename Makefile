GO ?= go
DATA_ROOT ?= /var/lib/omnirepo
BENCH_DURATION ?= 30s
BENCH_WORKERS ?= 16

.PHONY: dev build test test-airgap test-perf test-live-oci test-live-git bench-sqlite bench-git-fixture bench-git \
	vendor lint seed lint-protocol-redaction \
	check-contrast lint-typography lint-spacing-carveout lint-axe-devdep \
	lint-reset-beforeEach \
	lint-go-vet-oci \
	lint-docs \
	conformance conformance-oci conformance-rpm conformance-deb \
	conformance-pypi conformance-helm conformance-s3 conformance-git \
	conformance-lifecycle \
	test-git-conformance conformance-all \
	frontend build-all docker e2e bench bench-throughput

build:
	$(GO) build -mod=vendor -o bin/omnirepo ./cmd/omnirepo

test: lint-protocol-redaction check-contrast lint-typography lint-spacing-carveout lint-axe-devdep lint-reset-beforeEach lint-docs lint-go-vet-oci
	$(GO) test -mod=vendor ./...
	$(MAKE) test-airgap

test-airgap:
	$(GO) test -mod=vendor ./test/airgap/...

# test-perf (DBHEALTH-07 / SC3): runs the build-tagged perf500 suite with
# a 20-minute budget. Grows the test DB to 500 MB before exercising
# GET /api/v1/admin/db/health; asserts p95 < 100 ms per the spec budget.
# NOT a prerequisite of `test` — invoked separately in CI so the fast
# merge-gate stays fast. The 10-MB proxy in admin_db_health_test.go is
# the fast merge-gate; this target is the authoritative spec assertion.
test-perf:
	$(GO) test -mod=vendor -tags=perf500 -timeout=20m \
		-run TestAdminDBHealth_PerfBudget_500MB ./internal/api/...

# test-live-oci (OCIHELM-08 / D-16): live E2E against
# oci://registry-1.docker.io/bitnamicharts/nginx. Gated behind the
# `live_oci` Go build tag so default `make test` never touches Docker
# Hub. Requires DOCKERHUB_USER + DOCKERHUB_TOKEN env vars (Docker Hub
# PAT with Read:Public_Repos scope is sufficient); skips cleanly when
# absent so CI without secrets stays green.
#
# NOT a prerequisite of `test` — live endpoints belong behind an
# opt-in target. Mirrors the Phase 10 test-perf / perf500 pattern.
# See internal/protocol/helm/sync_oci_live_test.go for the test body
# and the scope-guard rationale (three smokes only — hermetic tests
# in plan 11-03 cover the full Handle round-trip).
test-live-oci:
	@set -e; \
	if [ -z "$$DOCKERHUB_USER" ] || [ -z "$$DOCKERHUB_TOKEN" ]; then \
		echo "SKIP: DOCKERHUB_USER / DOCKERHUB_TOKEN unset — live OCI test requires Docker Hub PAT"; \
		exit 0; \
	fi; \
	$(GO) test -mod=vendor -tags=live_oci -timeout=300s -v \
		-run TestLiveOCIBitnamiSync ./internal/protocol/helm/...

# test-live-git (GITMIRROR-09): live E2E mirror of a real public GitHub
# HTTPS repo via the Phase-11 git.SyncHandler. Default upstream is
# https://github.com/pallets/click.git (~4 MB, pure Python, LFS-free,
# stable since 2014); operators can override with LIVE_GIT_UPSTREAM
# (useful when GitHub is unreachable in air-gapped pre-release runs —
# point at an internal GitLab or Gitea HTTPS mirror).
#
# Gated behind the `live_git` Go build tag so default `make test`
# never touches the network. NO env-guard at the Makefile level —
# the test itself pre-flights the upstream with a 5-second HEAD probe
# and t.Skip's cleanly when unreachable, so CI without outbound
# connectivity stays green either way.
#
# NOT a prerequisite of `test` — live endpoints belong behind an
# opt-in target. Mirrors the Phase 10 test-perf and Phase 11
# test-live-oci pattern. See internal/protocol/git/sync_live_test.go
# for the test body and the scope-guard rationale (single E2E
# scenario; hermetic tests in plan 11-06 cover correctness).
test-live-git:
	$(GO) test -mod=vendor -tags=live_git -timeout=300s -v \
		-run TestLiveGitHubMirrorSync ./internal/protocol/git/...

bench-sqlite:
	$(GO) run -mod=vendor ./cmd/bench/sqlite --duration=$(BENCH_DURATION) --workers=$(BENCH_WORKERS)

# TEST-07 gate: deterministic 200 MB bare-repo fixture for the git memory
# bench. Generated once; cached in .bench/git-fixture/ (gitignored).
bench-git-fixture:
	@mkdir -p .bench/git-fixture
	@test -d .bench/git-fixture/big.git || \
		$(GO) run -tags=generator -mod=vendor ./test/bench/gitgen -out .bench/git-fixture/big.git -seed 42

# TEST-07 gate: git clone memory benchmark. Launches omnirepo as a child
# process, clones the 200 MB fixture, samples VmRSS at 50 ms, asserts
# peak_rss < 3 * repo_bytes for the gogit backend (hard gate). Also runs
# against gitkit for comparison (not gated). Results in .bench/git-results.json.
bench-git: bench-git-fixture
	$(GO) test -tags=bench -mod=vendor -count=1 -timeout=25m -v ./test/bench/git/...

vendor:
	$(GO) mod tidy
	$(GO) mod vendor

lint:
	golangci-lint run ./...

seed:
	@test -n "$(FILE)" || (echo "FILE=path/to/bootstrap.json required"; exit 2)
	@mkdir -p $(DATA_ROOT)/config
	@cp $(FILE) $(DATA_ROOT)/config/bootstrap.json
	@chmod 0600 $(DATA_ROOT)/config/bootstrap.json

# Air-gap invariant is enforced at RUNTIME by `make test-airgap`, which boots
# the binary and verifies it makes no outbound network calls on its own.
# The earlier `grep-cdn` static-text gate was retired in v1.2 / Phase 9 —
# it produced false positives on bundled third-party library URLs (Swagger
# UI RFC links, React error-page URLs, W3C / JSON Schema namespace URIs,
# license-comment homepage fields) without catching the real invariant.
# The runtime test is the single source of truth.

# lint-protocol-redaction enforces ERR-03 for the protocol handler tree:
# no http.Error call site may emit a %v-interpolated Go error value to
# the wire body. Real errors MUST be logged via slog.ErrorContext keyed
# by middleware.GetReqID (X-Incident-Id) and the client MUST receive
# a static generic message. *_test.go is excluded so test fixtures can
# legitimately stage leak-before-fix behavior.
#
# Sister check: internal/protocol/protocoltest/TestNoPercentVLeakInHTTPError
# runs the same grep in-process, so `go test ./...` fails the same way
# this target does.
lint-protocol-redaction:
	@set -e; \
	echo "lint-protocol-redaction: scanning internal/protocol/ for http.Error leaks"; \
	matches=$$(grep -rnE --include='*.go' --exclude='*_test.go' 'http\.Error\([^)]*%v' internal/protocol/ 2>/dev/null || true); \
	if [ -n "$$matches" ]; then \
		echo "ERROR: protocol handler emits %v-interpolated error to client (ERR-03 leak):"; \
		echo "$$matches"; \
		echo ""; \
		echo "Fix: redact the interpolation. Log the real error via slog.ErrorContext"; \
		echo "with slog.Any(\"err\", err) + slog.String(\"incident_id\", chimw.GetReqID(r.Context()))"; \
		echo "then call http.Error(w, \"<generic message>\", <status>)."; \
		exit 1; \
	fi; \
	echo "lint-protocol-redaction: clean"

# conformance-oci runs the OCI Distribution conformance suite. Gated behind
# the `conformance` build tag so default `make test` never requires crane.
# The vendored crane binary lives at test/conformance/bin/crane; see
# test/conformance/bin/README.md for install instructions.
conformance-oci:
	@test -x test/conformance/bin/crane || (echo "Missing crane binary at test/conformance/bin/crane; see test/conformance/bin/README.md"; exit 1)
	$(GO) test -mod=vendor -tags=conformance -count=1 ./test/conformance/docker/...

# Phase 3 DinD conformance gates (D-29..D-31). Each target drives a real
# protocol client (dnf, apt-get, pip+uv, helm) inside a pinned base image
# from test/conformance/images.txt against an in-process omnirepo. Tests
# skip cleanly on hosts without the docker CLI; CI is expected to provide
# docker so the gate fires.
conformance-rpm:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=10m ./test/conformance/rpm/...

conformance-deb:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=10m ./test/conformance/deb/...

conformance-pypi:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=10m ./test/conformance/pypi/...

conformance-helm:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=10m ./test/conformance/helm/...

conformance-s3:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=5m ./test/conformance/s3/...

# v1.6 Phase 1 / LIFECYCLE-11 gate: cross-protocol denial conformance for
# project soft-delete + restore. Boots omnirepo in-process, provisions a
# project + 4 repos + S3 access key + S3 bucket + project-owned API key + 4
# indexed packages, then asserts every protocol surface (S3 SigV4, REST API
# key, search) denies access after soft-delete and works again after Restore
# (D-21 regression guard). Runs in <2 minutes typical; 10m upper bound.
conformance-lifecycle:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=10m ./test/conformance/lifecycle/...

# Phase 4 D-46 gate: Git Smart-HTTP conformance via real `git` CLI (DinD).
# Exercises both gogit and gitkit backends: clone/push/fetch matrix,
# oversize-push gate, D-31 project-auth variant, bad-auth rejection.
conformance-git:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=10m ./test/conformance/git/...

# test-git-conformance is an alias for conformance-git (plan spec name).
test-git-conformance: conformance-git

# conformance-all runs every protocol's conformance suite in one invocation.
# Used by the CI conformance job (D-31).
conformance-all:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=15m ./test/conformance/...

# Phase 3 protocols only (rpm + deb + pypi + helm). The CI matrix runs
# docker / s3 / git in dedicated jobs (different env requirements:
# docker needs crane, s3 needs aws-cli DinD, git needs git binary), so
# the phase-3 job uses this scoped target to avoid pulling those tests
# into a runner that doesn't satisfy their prerequisites.
conformance-phase3:
	$(GO) test -mod=vendor -tags=conformance -count=1 -timeout=15m \
		./test/conformance/deb/... \
		./test/conformance/helm/... \
		./test/conformance/pypi/... \
		./test/conformance/rpm/...

# conformance is an alias of conformance-all for callers that just want
# "the conformance gate" without thinking about per-protocol granularity.
conformance: conformance-all

# Frontend build (npm ci + vite build)
frontend:
	cd web && npm ci --no-audit --no-fund && npm run build

# Full build: frontend + Go binary
build-all: frontend build

# Docker image build with version injection
docker:
	docker build --build-arg VERSION=$$(git describe --tags --always --dirty 2>/dev/null || echo dev) -t omnirepo:dev .

# Dev mode: run Go + Vite dev servers in parallel
dev:
	@echo "Starting Go server + Vite dev server..."
	@OMNIREPO_DEV=1 $(GO) run ./cmd/omnirepo serve &
	@cd web && npm run dev

# Playwright E2E tests
e2e:
	cd web && npx playwright test

# Bench throughput (TEST-05): upload + API throughput benchmarks
bench-throughput:
	$(GO) test -tags=bench -mod=vendor -count=1 -timeout=10m -bench=. ./test/bench/throughput/...

# Bench target (TEST-05): run all benchmarks
bench: bench-sqlite bench-git bench-throughput
	@echo "All benchmarks complete"

# ---------------------------------------------------------------------------
# Phase 6 / plan 06-08 visual-foundation gates
# ---------------------------------------------------------------------------

# check-contrast (VISUAL-08): parses web/src/index.css :root, extracts the
# 18 --status-* OKLCH triplets, computes WCAG 2.1 contrast ratios, asserts
# every status passes AA (>= 4.5:1) for foreground-on-fill. Zero npm deps.
# Sister test: web/e2e/a11y-audit.spec.ts crawls 5+ live pages via
# @axe-core/playwright for the broader WCAG AA breadth check.
check-contrast:
	@node scripts/check-contrast.mjs

# lint-typography (VISUAL-07): grep gate that fails any new file under
# web/src/ using forbidden font-weight classes (font-medium / font-bold /
# font-light) or forbidden text-size classes (text-base / text-xl / text-3xl /
# text-4xl). The Phase 6 typography scale is exactly 2 weights (400 regular,
# 600 semibold) and exactly 4 sizes (text-xs, text-sm, text-lg, text-2xl).
#
# scripts/typography-allowlist.txt enumerates files that predate Phase 6
# and legitimately contain these classes (shadcn primitives, v1.0 pages);
# they are excluded from the grep. New files created in plans 06-01..06-07
# MUST NOT appear in the allowlist.
lint-typography:
	@set -e; \
	echo "lint-typography: scanning web/src/ for forbidden weight/size classes"; \
	excludes=$$(grep -v '^\#' scripts/typography-allowlist.txt 2>/dev/null | grep -v '^$$' | awk -F/ '{print "--exclude=" $$NF}' | tr '\n' ' '); \
	weight_hits=$$(grep -rnE --include='*.tsx' --include='*.ts' $$excludes '\bfont-(medium|bold|light)\b' web/src/ 2>/dev/null || true); \
	size_hits=$$(grep -rnE --include='*.tsx' --include='*.ts' $$excludes '\btext-(base|xl|3xl|4xl)\b' web/src/ 2>/dev/null || true); \
	if [ -n "$$weight_hits" ]; then \
		echo "ERROR: forbidden font-weight class in new code (Phase 6 typography discipline):"; \
		echo "$$weight_hits"; \
		echo ""; \
		echo "Allowed weights: default (400 regular) or font-semibold (600)."; \
		echo "Allowed sizes:   text-xs, text-sm, text-lg, text-2xl."; \
		echo "If file predates Phase 6 and legitimately uses a forbidden class,"; \
		echo "add its basename to scripts/typography-allowlist.txt."; \
		exit 1; \
	fi; \
	if [ -n "$$size_hits" ]; then \
		echo "ERROR: forbidden text-size class in new code (Phase 6 typography discipline):"; \
		echo "$$size_hits"; \
		echo ""; \
		echo "Allowed sizes: text-xs, text-sm, text-lg, text-2xl."; \
		echo "If file predates Phase 6, add its basename to scripts/typography-allowlist.txt."; \
		exit 1; \
	fi; \
	echo "lint-typography: clean"

# lint-reset-beforeEach (v1.5 Phase 1 / E2E-02): every Playwright spec under
# web/e2e/ MUST call resetServerState in its beforeEach after adminLoginAPI
# so each test starts from identical DB state in a shared DATA_ROOT webServer.
# visual-foundation.spec.ts is the one principled opt-out (hits the dev-only
# status-badge story page, no auth required, reset would add no value and
# risks breaking snapshot timing). The grep-gate skips it by filename.
#
# Rationale: plan 01-01 added POST /api/v1/admin/_reset, plan 01-02 added the
# resetServerState() helper, plan 01-03 rolled it out across 25 specs. This
# gate prevents future specs from silently regressing to the shared-state
# flake pattern that F-15.4 (v1.4) exposed.
lint-reset-beforeEach:
	@set -e; \
	echo "lint-reset-beforeEach: every e2e spec must call resetServerState in beforeEach (except visual-foundation.spec.ts)"; \
	missing=$$(for f in web/e2e/*.spec.ts; do \
		[ "$$(basename $$f)" = "visual-foundation.spec.ts" ] && continue; \
		if ! grep -q 'resetServerState' "$$f"; then echo "  $$f"; fi; \
	done); \
	if [ -n "$$missing" ]; then \
		echo "ERROR: the following e2e specs do not invoke resetServerState:"; \
		echo "$$missing"; \
		echo ""; \
		echo "Add \`await resetServerState(request)\` to each spec's test.beforeEach after adminLoginAPI."; \
		echo "Import from './helpers/auth'. See .planning/phases/01-e2e-state-isolation/ for context."; \
		exit 1; \
	fi; \
	echo "lint-reset-beforeEach: clean"

# lint-go-vet-oci (v1.5 Phase 4 / TECHDEBT-01): narrow go vet gate for the
# OCI subtree. Intentionally scoped to ./internal/protocol/oci/... only —
# the wider tree carries ~300 pre-existing golangci-lint findings that are
# deferred per v1.5 scope. Lifts to wider subtrees as future phases clean
# them.
lint-go-vet-oci:
	@$(GO) vet ./internal/protocol/oci/...
	@echo "lint-go-vet-oci: clean"

# lint-spacing-carveout (VISUAL-05): the 6px copy-button inset (right-1.5 /
# top-1.5) was originally grandfathered to the v1.0 SnippetPanel and
# OneTimeReveal files per 06-UI-SPEC §Spacing Exceptions. Phase 7 (plan 07-02)
# lifted SnippetPanel's body into the new SnippetList primitive and normalised
# the inset to 8px — SnippetPanel.tsx no longer needs the carve-out. The
# shadcn ui/sidebar.tsx primitive still ships the 6px classes as generated
# chrome (generated by the shadcn CLI in plan 05-02, predates Phase 6) and
# remains grandfathered. New copy-button placements MUST use 8px
# (right-2 top-2) per UI-SPEC.
lint-spacing-carveout:
	@set -e; \
	echo "lint-spacing-carveout: 6px inset outside OneTimeReveal/sidebar"; \
	hits=$$(grep -rnE --include='*.tsx' --include='*.ts' \
		--exclude='OneTimeReveal.tsx' --exclude='sidebar.tsx' \
		'\b(right-1\.5|top-1\.5)\b' web/src/ 2>/dev/null || true); \
	if [ -n "$$hits" ]; then \
		echo "ERROR: 6px inset (right-1.5 / top-1.5) found outside the grandfathered files:"; \
		echo "$$hits"; \
		echo ""; \
		echo "Phase 6+ new copy-button placements MUST use 8px (right-2 top-2)."; \
		echo "The 6px carve-out is limited to OneTimeReveal.tsx and the"; \
		echo "shadcn-generated ui/sidebar.tsx (both v1.0 pre-Phase-6)."; \
		exit 1; \
	fi; \
	echo "lint-spacing-carveout: clean"

# lint-axe-devdep: @axe-core/playwright is MPL-2.0, which is compatible with
# our Apache-2.0 licensing model ONLY as a devDependency (never shipped into
# a runtime artefact). This gate asserts the package is NEVER promoted into
# web/package.json `dependencies`.
lint-axe-devdep:
	@set -e; \
	echo "lint-axe-devdep: @axe-core/playwright must be devDep only"; \
	HAS_IN_DEPS=$$(node -e "const p=require('./web/package.json'); console.log(p.dependencies && p.dependencies['@axe-core/playwright'] ? 'yes' : 'no')"); \
	if [ "$$HAS_IN_DEPS" = "yes" ]; then \
		echo "ERROR: @axe-core/playwright (MPL-2.0) must NEVER be in web/package.json dependencies."; \
		echo "Move it to devDependencies. It is used by Playwright e2e only and never"; \
		echo "reaches the runtime binary, so keeping it devDep preserves the project's"; \
		echo "Apache-2.0-compatible license posture."; \
		exit 1; \
	fi; \
	echo "lint-axe-devdep: clean (axe is devDep only)"

# lint-docs (CRONDOCS-04 / Phase 10 D-22..D-25): shellcheck the canonical
# bash snippet embedded in docs/operations/scheduled-sync.md. The doc IS
# the fixture — no sidecar script — so any edit to the documented cron
# example goes through shellcheck on the same PR.
#
# Extraction: scripts/extract-doc-snippet.go walks the markdown line by
# line, finds `<!-- shellcheck-id: scheduled-sync -->`, and pipes the
# body of the following ```bash fence to a tmpfile.
# Linting:    default severity bar (error + warning) per D-24 — NO `-S`
# override that would hide real issues. Inline disables (if ever needed)
# must be added in the doc itself as `# shellcheck disable=SCxxxx`.
# Local dev: shellcheck is not a hard install dep; the target exits 0
# with a skip note when absent (D-25) so `make test` stays green on
# fresh laptops. CI installs shellcheck explicitly via apt-get in
# .github/workflows/ci.yml so the gate fires there.
lint-docs:
	@set -e; \
	if ! command -v shellcheck >/dev/null 2>&1; then \
		echo "lint-docs: shellcheck not installed — skipping (install via apt-get install shellcheck)"; \
		exit 0; \
	fi; \
	TMP=$$(mktemp); \
	trap 'rm -f $$TMP' EXIT; \
	$(GO) run -mod=vendor scripts/extract-doc-snippet.go \
		--id scheduled-sync \
		--file docs/operations/scheduled-sync.md > $$TMP; \
	shellcheck $$TMP; \
	echo "lint-docs: clean"
