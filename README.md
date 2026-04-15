# OmniRepo

See `docs/superpowers/specs/2026-04-14-omnirepo-v1-design.md` for the approved v1 design spec.
See `tools.md` for the technology blueprint.
Planning artifacts live under `.planning/`.
Build from source: `make build`. Development loop: `make dev`.

## Local development

CI (`.github/workflows/ci.yml`) runs every Phase 1 invariant gate on each
PR. Mirror locally before pushing:

```
make lint
make test
make test-airgap
make grep-cdn
make bench-sqlite
go test -mod=vendor -tags=spike ./internal/protocol/git/spike/...
```

All six must exit 0 for the PR to merge.
