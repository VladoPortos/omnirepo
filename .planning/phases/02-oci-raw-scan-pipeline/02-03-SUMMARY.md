---
phase: 02-oci-raw-scan-pipeline
plan: 03
subsystem: scan
tags: [trivy, subprocess, runner, air-gap, fixtures, config]
requires:
  - internal/config (Phase 1)
provides:
  - scan.Runner interface (Image, Filesystem, SBOM)
  - scan.NewTrivyRunner (production exec driver)
  - scan.NewFakeRunner (test double)
  - scan.ParseTrivyJSON (tolerant decoder)
  - config.Trivy block with D-44 defaults
affects:
  - none yet (consumer is Phase 02-09 scan handler)
tech_stack_added: []
patterns:
  - argv-slice exec (no shell interpolation) for subprocess fan-out
  - centralized baseFlags() to make air-gap invariant grep-auditable
  - queue-backed fake + FIFO pop for deterministic downstream tests
  - snapshot-fixtures-across-versions guarding against schema drift
key_files_created:
  - internal/scan/runner.go
  - internal/scan/trivy.go
  - internal/scan/trivy_test.go
  - internal/scan/fake.go
  - internal/scan/fake_test.go
  - internal/scan/parse.go
  - internal/scan/parse_test.go
  - internal/scan/testdata/trivy/v0.69-image-nginx.json
  - internal/scan/testdata/trivy/v0.68-image-alpine.json
  - internal/scan/testdata/trivy/v0.67-fs-empty.json
key_files_modified:
  - internal/config/config.go
  - internal/config/config_test.go
decisions:
  - Centralize mandatory flags in baseFlags() + re-list in SBOM path; grep gate (>=2 hits) enforces invariant
  - Tolerant parse with summary map pre-seeded to all 5 severity keys (no map-miss downstream)
  - FakeRunner uses split queues (image / fs / sbom) so tests can interleave without cross-talk
  - Fixture Trivy version pinned at v0.69.x for prod target; v0.68/v0.67 fixtures prove drift tolerance
metrics:
  duration_minutes: ~25
  tasks_completed: 3
  completed_date: 2026-04-15
requirements_complete:
  - SCAN-01
  - SCAN-02
  - SCAN-05
  - SCAN-06
  - SCAN-08
---

# Phase 2 Plan 03: Trivy Runner Summary

One-liner: Air-gap-safe Trivy subprocess driver behind a 3-method Runner interface with queue-backed fake and schema-drift-proof JSON parser, ready for the Phase 02-09 scan handler to wire without touching exec or JSON internals.

## What Shipped

- **`scan.Runner` interface** (`internal/scan/runner.go`) with `Image(ctx, ociLayoutDir)`, `Filesystem(ctx, dir)`, `SBOM(ctx, dir, format, outPath)`. `SBOMFormat` constants `FormatCycloneDX` and `FormatSPDX` match the exact strings Trivy's `--format` flag expects.
- **`trivyRunner`** (`internal/scan/trivy.go`) — production impl using `exec.CommandContext` with an argv `[]string` slice. Never shells out. `baseFlags()` centralizes D-22 mandatory flags (`--cache-dir`, `--db-repository file://<db>`, `--offline-scan`, `--skip-db-update`, `--format json`); the SBOM path re-lists them explicitly so `grep -c '"--offline-scan"'` across `trivy.go` returns ≥2 (currently: 2). `TRIVY_NO_PROGRESS=1` added to env. Stderr captured and folded into wrapped error on non-zero exit.
- **`FakeRunner`** (`internal/scan/fake.go`) — split queues (`image`, `fs`, `sbom`) with FIFO pop; drained queue yields `ErrNothingQueued`. Mutex-guarded. Ready for Phase 02-09 to drop in.
- **`ParseTrivyJSON`** (`internal/scan/parse.go`) — tolerant decoder (`json.NewDecoder` + no `DisallowUnknownFields`). Summary map pre-seeded with all 5 severity keys so downstream callers never hit a map-miss. Unknown severity strings (including empty) route to `Summary["unknown"]` rather than being silently dropped. `ErrEmptyInput` sentinel for `len(b)==0`.
- **Three frozen fixtures** under `internal/scan/testdata/trivy/`:
  - `v0.69-image-nginx.json` — 5 CRITICAL + 12 HIGH + 7 MEDIUM (24 vulns total)
  - `v0.68-image-alpine.json` — 1 CRITICAL + unknown root key `FutureField` + unknown nested key `FutureVulnField` to prove tolerance
  - `v0.67-fs-empty.json` — empty Results array, all counters 0
- **`config.Trivy` block** (`internal/config/config.go`) with D-44 defaults: `binary_path=/usr/local/bin/trivy`, `db_path=/var/lib/omnirepo/trivy/db`, `cache_path=/var/lib/omnirepo/trivy/cache`. Env overrides via `OMNIREPO_TRIVY__BINARY_PATH` etc.

## Tests

All tests pass via `go test -mod=vendor -count=1 ./internal/scan/... ./internal/config/...`:

- `TestTrivyRunnerImageInvokesRequiredFlags` — installs a POSIX shell mock at `dir/trivy` that records `$@` into `argv.log` and emits a minimal fixture JSON; asserts all 11 required tokens (including `image`, `--input <oci>`, `--cache-dir`, `--db-repository file:///db`, `--offline-scan`, `--skip-db-update`, `--format json`) and confirms `--insecure` never appears.
- `TestTrivyRunnerFilesystemInvokesFsSubcommand` — same harness for `fs` subcommand.
- `TestTrivyRunnerSBOMInvokesRequiredFlags` — asserts SBOM argv carries `--output`, `--format cyclonedx`, and the same air-gap flags.
- `TestTrivyRunnerExecFailurePropagatesStderr` — mock script exits 3 with stderr text; wrapped error contains it.
- `TestFakeRunnerImageFIFO`, `TestFakeRunnerImagePropagatesError`, `TestFakeRunnerFilesystemAndSBOM` — FIFO order, error passthrough, drain semantics.
- `TestParseTrivyEmptyReturnsErr`, `TestParseTrivyMalformedJSONWraps` — error edges.
- `TestParseTrivyNginxCounts`, `TestParseTrivyAlpineToleratesUnknownFields`, `TestParseTrivyEmptyFsAllZero`, `TestParseTrivyUnknownSeverityRoutesToUnknownBucket` — fixture invariants + unknown-severity routing.
- `TestTrivyDefaults`, `TestTrivyEnvOverride` — config wiring.

## Mock-script trick (design note)

The real `trivy` binary is 70+ MB and its DB is 500+ MB. CI should not depend on either. Instead, tests install a tiny POSIX shell script named `trivy` into `t.TempDir()`, point `config.Trivy.BinaryPath` at it, and verify:

1. The argv OmniRepo passed (recorded via `printf '%s\n' "$@" > argv.log` in the script).
2. The parser consumes the fixture stdout correctly.

This lets us assert exact flag presence and exact parsing behavior without network, without a real trivy install, and without a pre-baked DB. Real-trivy integration is covered by the phase-level airgap conformance plan (02-13).

## Schema fields deliberately ignored

Trivy JSON carries a lot we don't need in Phase 2. Deferred to v1.1 / later plans:

- CVSS vectors and scores (`CVSS.nvd.V3Score`, `CVSS.redhat.V2Score`, ...) — useful but Phase 02-09 only consumes severity bucket + CVE list.
- References (advisory URLs) — deferred until UI can render them.
- Layer / Target attribution beyond block-level `Target` — deferred until Phase 5 UI needs per-layer attribution.
- `CreatedAt`, `LastModifiedAt` on vulns — deferred until we show "new since last scan" diffs.

`TrivyDBVersion` is populated from `Metadata.DBVersion` when present, falling back to `Family/Name` from `Metadata.OS`. Phase 02-09 uses this only as an audit hint.

## Final Trivy version pinned for production fixtures

`v0.69.x` (per `.planning/research/STACK.md` Trivy v0.69.3 binary). Older schema fixtures (v0.68, v0.67) are kept as drift anchors; when we bump to v0.70 we should ADD a `v0.70-*` fixture rather than modifying the existing ones.

## Deviations from Plan

Two minor adjustments — none required user intervention:

1. **[Rule 3 - Ordering]** Implemented `parse.go` + `ParseTrivyJSON` in Task 2 rather than Task 3 because `trivy.go` (Task 2) calls it from `runJSON`, and the Task 2 test asserts `res.SchemaVersion != 0` which needs the real parser. Task 3 then added the fixtures, the `parse_test.go` suite, and the unknown-severity routing tests. Functional outcome identical to the plan; only the order of two co-dependent files shifted.
2. **[Rule 2 - Correctness]** Added `TRIVY_NO_PROGRESS=1` to `cmd.Env` on both runJSON and SBOM paths. Plan mentioned it informally in the behavior block; code and tests both wire it so stdout doesn't get polluted with progress bars that would break JSON parsing.

## Threat Model Compliance

| Threat | Mitigation | Evidence |
|--------|-----------|----------|
| T-02-03-01 (shell injection) | argv slice only | `grep -E 'exec\.Command(Context)?\(.*shell\|sh -c' internal/scan/trivy.go` returns no matches |
| T-02-03-02 (air-gap break) | baseFlags centralizes, SBOM repeats | `grep -c '"--offline-scan"' internal/scan/trivy.go` = 2; `grep -c '"--skip-db-update"'` = 2 |
| T-02-03-04 (schema drift) | Tolerant parse + 3 snapshot fixtures | `TestParseTrivyAlpineToleratesUnknownFields`; Summary keys always all 5 |
| T-02-03-06 (empty-output bypass) | ErrEmptyInput sentinel | `TestParseTrivyEmptyReturnsErr` |

No new threat flags — all surface is internal subprocess exec paths covered by the plan's threat register.

## Commits

- `f7833db` feat(02-03): add scan.Runner interface and trivy config block
- `db53b74` feat(02-03): trivyRunner exec driver + FakeRunner test double
- `f76b789` test(02-03): Trivy JSON parser snapshot fixtures across 3 schemas

## Self-Check: PASSED

- internal/scan/runner.go — FOUND
- internal/scan/trivy.go — FOUND
- internal/scan/trivy_test.go — FOUND
- internal/scan/fake.go — FOUND
- internal/scan/fake_test.go — FOUND
- internal/scan/parse.go — FOUND
- internal/scan/parse_test.go — FOUND
- internal/scan/testdata/trivy/v0.69-image-nginx.json — FOUND
- internal/scan/testdata/trivy/v0.68-image-alpine.json — FOUND
- internal/scan/testdata/trivy/v0.67-fs-empty.json — FOUND
- Commits f7833db, db53b74, f76b789 — FOUND in git log
- `go build -mod=vendor ./internal/scan/...` — exit 0
- `go test -mod=vendor -count=1 ./internal/scan/... ./internal/config/...` — exit 0
