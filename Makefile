GO ?= go
DATA_ROOT ?= /var/lib/omnirepo
BENCH_DURATION ?= 30s
BENCH_WORKERS ?= 16

.PHONY: dev build test test-airgap bench-sqlite vendor lint seed grep-cdn conformance-oci

dev:
	$(GO) run ./cmd/omnirepo serve

build:
	$(GO) build -mod=vendor -o bin/omnirepo ./cmd/omnirepo

test:
	$(GO) test -mod=vendor ./...
	$(MAKE) test-airgap

test-airgap:
	$(GO) test -mod=vendor ./test/airgap/...

bench-sqlite:
	$(GO) run -mod=vendor ./cmd/bench/sqlite --duration=$(BENCH_DURATION) --workers=$(BENCH_WORKERS)

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

grep-cdn:
	@grep -rEI 'https?://(?!localhost|127\.0\.0\.1)' web/dist/ 2>/dev/null || true

# conformance-oci runs the OCI Distribution conformance suite. Gated behind
# the `conformance` build tag so default `make test` never requires crane.
# The vendored crane binary lives at test/conformance/bin/crane; see
# test/conformance/bin/README.md for install instructions.
conformance-oci:
	@test -x test/conformance/bin/crane || (echo "Missing crane binary at test/conformance/bin/crane; see test/conformance/bin/README.md"; exit 1)
	$(GO) test -mod=vendor -tags=conformance -count=1 ./test/conformance/docker/...
