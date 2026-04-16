GO ?= go
DATA_ROOT ?= /var/lib/omnirepo
BENCH_DURATION ?= 30s
BENCH_WORKERS ?= 16

.PHONY: dev build test test-airgap bench-sqlite bench-git-fixture bench-git \
	vendor lint seed grep-cdn \
	conformance conformance-oci conformance-rpm conformance-deb \
	conformance-pypi conformance-helm conformance-s3 conformance-git \
	test-git-conformance conformance-all \
	frontend build-all docker e2e bench bench-throughput

build:
	$(GO) build -mod=vendor -o bin/omnirepo ./cmd/omnirepo

test:
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
