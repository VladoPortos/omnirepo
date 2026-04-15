GO ?= go
DATA_ROOT ?= /var/lib/omnirepo

.PHONY: dev build test test-airgap bench-sqlite vendor lint seed grep-cdn

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
	$(GO) run -mod=vendor ./cmd/bench/sqlite

vendor:
	$(GO) mod tidy
	$(GO) mod vendor

lint:
	golangci-lint run ./...

seed:
	@test -n "$(FILE)" || (echo "FILE=path/to/bootstrap.json required"; exit 2)
	@mkdir -p $(DATA_ROOT)/config
	@cp $(FILE) $(DATA_ROOT)/config/bootstrap.json

grep-cdn:
	@grep -rEI 'https?://(?!localhost|127\.0\.0\.1)' web/dist/ 2>/dev/null || true
