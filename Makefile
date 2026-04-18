GO ?= go
DATA_ROOT ?= /var/lib/omnirepo
BENCH_DURATION ?= 30s
BENCH_WORKERS ?= 16

.PHONY: dev build test test-airgap bench-sqlite bench-git-fixture bench-git \
	vendor lint seed grep-cdn lint-protocol-redaction \
	check-contrast lint-typography lint-spacing-carveout lint-axe-devdep \
	conformance conformance-oci conformance-rpm conformance-deb \
	conformance-pypi conformance-helm conformance-s3 conformance-git \
	test-git-conformance conformance-all \
	frontend build-all docker e2e bench bench-throughput

build:
	$(GO) build -mod=vendor -o bin/omnirepo ./cmd/omnirepo

test: lint-protocol-redaction check-contrast lint-typography lint-spacing-carveout lint-axe-devdep
	$(GO) test -mod=vendor ./...
	$(MAKE) test-airgap

test-airgap:
	$(GO) test -mod=vendor ./test/airgap/...

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
	$(GO) test -tags=bench -mod=vendor -count=1 -timeout=15m -v ./test/bench/git/...

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

# grep-cdn enforces the air-gap invariant: no external https:// URLs in
# either the built SPA bundle or the Phase 3 protocol handler packages
# (D-33). The Perl-style negative-lookahead requires `grep -P`.
#
# Allowed hosts (none of which the binary fetches at runtime):
#   - localhost, 127.0.0.1                    — loopback
#   - example.com, example.invalid, x.y       — RFC 2606 / test placeholders
#   - upstream.example                        — package-doc placeholder
#   - linux.duke.edu                          — XML namespace identifier for
#                                               RPM repodata (URN, not URL —
#                                               required by the createrepo_c
#                                               schema; never dereferenced)
#   - wiki.debian.org                         — comment-only spec link
grep-cdn:
	@set -e; \
	echo "grep-cdn: web/dist/"; \
	! grep -rPI 'https?://(?!localhost|127\.0\.0\.1|example\.com|example\.invalid)' web/dist/ 2>/dev/null \
		|| (echo "ERROR: external URL in web/dist/" && exit 1); \
	echo "grep-cdn: internal/protocol/{rpm,deb,pypi,helm}/"; \
	! grep -rPI --include='*.go' \
		'https?://(?!localhost|127\.0\.0\.1|example\.com|example\.invalid|upstream\.example|repo\.example|linux\.duke\.edu|wiki\.debian\.org|x\.y)' \
		internal/protocol/rpm internal/protocol/deb internal/protocol/pypi internal/protocol/helm 2>/dev/null \
		|| (echo "ERROR: external https URL leaked into Phase 3 handler code" && exit 1); \
	echo "grep-cdn: clean"

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
