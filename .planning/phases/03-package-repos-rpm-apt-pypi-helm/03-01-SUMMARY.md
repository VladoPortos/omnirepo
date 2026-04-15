---
phase: 03-package-repos-rpm-apt-pypi-helm
plan: 01
subsystem: package-repos-foundation
tags: [migrations, fts5, signing-keys, coalescer, openpgp, config, auth, audit]
requires:
  - internal/crypto/aead.go
  - internal/storage/locks.go
  - internal/metadata/migrations (runner, embed)
  - internal/metadata/sqlitetest
provides:
  - migrations 008-015 (signing_keys, apt_suites, rpm/deb/pypi/helm packages, protocol FTS5 tables, repos.metadata_state column)
  - internal/metadata.SigningKeysRepo (AEAD-encrypted, meta-vs-private split)
  - internal/metadata.AptSuitesRepo / RPMPackagesRepo / DEBPackagesRepo / PyPIFilesRepo / HelmChartsRepo
  - internal/metadata.ReposRepo.SetMetadataState / SetLastRegenError / GetMetadataState
  - internal/metadata.IndexRPM / IndexDEB / IndexPyPI / IndexHelm + Delete counterparts
  - internal/crypto.GenerateRepoKey / ClearSign / DetachSign
  - internal/protocol/regen.Coalescer / Registry (debounce + maxWait)
  - internal/audit.EvtSigningKey*, EvtRPM/DEB/PyPI/HelmUpload+Delete, EvtRepoMetadataRegen/Failed
  - internal/auth.ActionRPMUpload / ActionDEBUpload / ActionPyPIUpload / ActionHelmUpload
  - config.RegenConfig / SyncConfig / SigningConfig (D-35)
affects:
  - internal/metadata/fts.go (4 new per-protocol helper pairs)
  - internal/audit/events.go (13 new event kinds)
  - internal/auth/policy.go (4 new actions routed through member branch)
  - internal/config/config.go (3 new config sections)
  - internal/metadata/repos.go (3 new metadata_state helpers + constants)
  - vendor/ (ProtonMail/go-crypto/openpgp/clearsign freshly vendored)
tech-stack:
  added:
    - ProtonMail/go-crypto/openpgp/clearsign (already transitively direct, now imported)
  patterns:
    - AEAD-encrypted secret-table repo pattern (mirrors UpstreamCredsRepo)
    - ON CONFLICT DO UPDATE upsert returning stable row id via read-back
    - sync.Map LoadOrStore with loser-shutdown for lazy per-key goroutines
    - Two-timer debounce + maxWait coalescer with size-1 kick channel
key-files:
  created:
    - internal/metadata/migrations/008_signing_keys.up.sql / .down.sql
    - internal/metadata/migrations/009_apt_suites.up.sql / .down.sql
    - internal/metadata/migrations/010_rpm_packages.up.sql / .down.sql
    - internal/metadata/migrations/011_deb_packages.up.sql / .down.sql
    - internal/metadata/migrations/012_pypi_files.up.sql / .down.sql
    - internal/metadata/migrations/013_helm_charts.up.sql / .down.sql
    - internal/metadata/migrations/014_protocol_fts.up.sql / .down.sql
    - internal/metadata/migrations/015_repo_metadata_state.up.sql / .down.sql
    - internal/metadata/signing_keys.go / signing_keys_test.go
    - internal/metadata/apt_suites.go / apt_suites_test.go
    - internal/metadata/rpm_packages.go / rpm_packages_test.go
    - internal/metadata/deb_packages.go / deb_packages_test.go
    - internal/metadata/pypi_files.go / pypi_files_test.go
    - internal/metadata/helm_charts.go / helm_charts_test.go
    - internal/crypto/pgpsign.go / pgpsign_test.go
    - internal/protocol/regen/coalescer.go / coalescer_test.go
    - internal/protocol/regen/registry.go / registry_test.go
  modified:
    - internal/metadata/fts.go / fts_test.go
    - internal/metadata/repos.go / repos_test.go
    - internal/metadata/migrations/runner_test.go
    - internal/audit/events.go / events_test.go
    - internal/auth/policy.go / policy_test.go
    - internal/config/config.go / config_test.go
    - go.mod / go.sum (unchanged — ProtonMail was already direct)
    - vendor/modules.txt (clearsign subpackage)
decisions:
  - 'SigningKeyMeta deliberately omits any private-bearing field; a reflect-based test guards the invariant (D-03).'
  - 'regen.Coalescer swallows fn errors — RegenFn owns repos.last_regen_error via its own writer tx. Separation keeps the coalescer protocol-agnostic.'
  - 'regen.Coalescer Shutdown does NOT cancel the inflight fn context; the contract is "wait for in-flight regen" (D-07). Shutdown caller may timebox via its own ctx.'
  - 'Registry.Get loser-race shuts down the duplicate coalescer rather than leaking a goroutine.'
  - 'pgpsign tests use 2048-bit RSA for speed; production default stays 4096 via config.Signing.GPGKeyBits.'
  - 'ActionRPM/DEB/PyPI/HelmUpload collapse into the same member-or-super-admin branch as ActionCreateRepo; no per-protocol policy fan-out.'
metrics:
  duration: ~45 minutes
  completed: 2026-04-15
---

# Phase 3 Plan 01: Package-Repos Foundation Summary

Landed the full Phase 3 data-plane substrate — migrations 008-015, seven typed metadata repos, the AEAD-encrypted signing-keys store, OpenPGP signing helpers, per-protocol FTS5 write paths, the debounced regen coalescer + per-repo registry, and the auth/audit/config extensions every downstream protocol plan (03-02..03-05) imports from.

## What Was Built

### Migrations 008–015

Eight migration pairs apply cleanly forward and reverse cleanly down (modernc sqlite 3.51 supports `ALTER TABLE DROP COLUMN` for 015):

| Migration | Shape |
|-----------|-------|
| 008_signing_keys | UNIQUE(repo_id, scope='repo'); gpg_rsa4096 kind CHECK; private_enc BLOB |
| 009_apt_suites | UNIQUE(repo_id, suite, component, architecture) |
| 010_rpm_packages | UNIQUE(repo_id, name, epoch, version, release, arch); digest index |
| 011_deb_packages | UNIQUE(repo_id, suite_id, package, version, architecture); digest index |
| 012_pypi_files | UNIQUE(repo_id, filename); wheel\|sdist CHECK; project_normalized index |
| 013_helm_charts | UNIQUE(repo_id, name, version); JSON keywords/maintainers columns |
| 014_protocol_fts | Four FTS5 virtual tables (rpm_fts, deb_fts, pypi_fts, helm_fts) with unicode61+diacritics tokenizer; repo_id UNINDEXED |
| 015_repo_metadata_state | ALTER TABLE repos ADD metadata_state (clean\|dirty\|regenerating) + last_regen_error |

A new `TestPhase3MigrationsForwardAndDown` test applies all migrations, verifies every table/column exists, then exec's each .down.sql in reverse order and verifies everything is scrubbed.

### Typed metadata repos

`SigningKeysRepo` enforces the meta-vs-private split structurally — `SigningKeyMeta` has no private field; a reflect-based `TestSigningKeyMetaHasNoPrivateField` fails if a future refactor adds one. `LookupPrivate` is the only `AEAD.Decrypt` path; it is reserved for the regen goroutine scope. `Insert` and `Delete` take an external `*sql.Tx` so signing-key creation rides in the same writer tx that created the repo.

`AptSuitesRepo.Insert` is idempotent (INSERT OR IGNORE + read-back of the stable id). `RPM/DEB/PyPI/HelmPackagesRepo` all use `ON CONFLICT DO UPDATE` upserts on their UNIQUE tuples — re-uploading a package refreshes digest/size/uploaded_at while keeping the row id stable (tests assert this). Every mutator takes a `*sql.Tx` so regen state transitions can ride alongside.

`ReposRepo` gains `SetMetadataState`, `SetLastRegenError`, `GetMetadataState` plus `MetadataStateClean|Dirty|Regenerating` string constants. A CHECK-constraint test confirms unknown states are rejected by the DB, not silently accepted.

### FTS5 helpers

`internal/metadata/fts.go` gains `IndexRPM / IndexDEB / IndexPyPI / IndexHelm` plus four `Delete` counterparts. All take `(ctx, tx, repoID, name, version, archOrRuntime, summary)` and run inside the caller's writer tx; the shared column shape lets SRCH-02 issue a single UNION query across protocols. Round-trip tests match each inserted row via `MATCH` and verify deletes evict by the four-column composite key.

### pgpsign

`internal/crypto/pgpsign.go` wraps ProtonMail/go-crypto into three exports:

- `GenerateRepoKey(uid, bits)` → (privArmored, pubArmored, fingerprint). RSA-only; rejects bits < 2048; fingerprint uppercase-hex 40-char.
- `ClearSign(privArmored, body)` → OpenPGP clearsigned bytes, suitable for APT InRelease.
- `DetachSign(privArmored, body)` → armored detached sig, suitable for repomd.xml.asc / Release.gpg.

Round-trip tests verify ClearSign bytes via `clearsign.Decode` + `openpgp.CheckDetachedSignature`, and DetachSign via `openpgp.CheckArmoredDetachedSignature`. Tests use 2048-bit keys for speed; production default stays 4096 per `config.Signing.GPGKeyBits`.

### Regen coalescer + registry

`internal/protocol/regen/coalescer.go` implements a two-timer debounce + maxWait coalescer:

- `Kick()` is non-blocking (size-1 buffered channel) — a burst of N calls yields at most one fire.
- `New(debounce, maxWait, fn)` launches a goroutine that waits idle for the first Kick, then restarts the debounce timer on every subsequent Kick while leaving maxWait alone — so continuous kicks still fire by maxWait (D-07 starvation prevention).
- `Shutdown(ctx)` closes the stop channel and waits on `done`; it does NOT cancel the in-flight fn (by design — the contract is "wait for in-flight regen to complete"). Caller time-boxes via the supplied ctx.

`internal/protocol/regen/registry.go` is the per-repo lazy store: `Get(repoID)` returns an existing `*Coalescer` or creates one via the factory. Concurrent `Get` on a cold cache is handled by `sync.Map.LoadOrStore` and the losing goroutine's coalescer is promptly shut down to reclaim its goroutine. `ShutdownAll(ctx)` drains every live coalescer.

Tests pass with `-race -count=5`:

- `TestCoalescerDebounceCollapses` — 100 kicks yield 1 fire.
- `TestCoalescerMaxWaitFires` — continuous 40ms kicks still fire within 300ms maxWait.
- `TestCoalescerShutdownWaitsForInflight` — Shutdown blocks until fn returns.
- `TestCoalescerConcurrentKicksSingleFire` — 1000 concurrent kicks collapse to 1 fire.
- `TestRegistryLazyCreate` / `TestRegistryShutdownAll` / `TestRegistryDifferentReposFireIndependently`.

### Auth / Audit / Config extensions

- `internal/auth`: Added `ActionRPMUpload`, `ActionDEBUpload`, `ActionPyPIUpload`, `ActionHelmUpload`. Wired into the existing member-or-super-admin branch of `Can`. `AllActions` length now 24; a new `TestPackageUploadActionsMemberOnly` exercises member/outsider/anonymous for each.
- `internal/audit`: Added 13 Phase 3 `EventKind` constants (signing-key lifecycle, per-protocol upload/delete, repo metadata regen/failed) and a new `TestPhase3EventKindsDistinctAndCount` enumeration test.
- `internal/config`: New `RegenConfig { DebounceMs, MaxWaitMs }`, `SyncConfig { MaxParallelDownloadsPerJob, UpstreamHTTPTimeout }`, `SigningConfig { GPGKeyBits }` structs with D-35 defaults and env-override tests.

## Deviations from Plan

None. The plan executed exactly as written. The only post-TDD iteration was fixing `TestClearSignRoundTrip` to tolerate RFC-4880 CRLF normalization (this is an RFC property of clearsign, not a plan deviation).

## Commits

| Hash | Description |
|------|-------------|
| 7091fe4 | feat(03-01): migrations 008-015, FTS helpers, auth/audit/config extensions |
| e47d2b1 | feat(03-01): typed repos for signing_keys, apt_suites, rpm/deb/pypi/helm packages |
| 41ee847 | feat(03-01): pgpsign helpers + debounced regen coalescer with per-repo registry |

## Verification

- `go build ./...` — clean.
- `go test ./... -count=1` — all packages green (api, app, audit, auth, config, crypto, jobs, metadata, protocol/*, regen, scan, storage, tls, airgap).
- `go test -race ./internal/protocol/regen/... -count=5` — clean.
- Migrations forward+down test — green.
- Acceptance grep checks:
  - `grep -c 'CREATE TABLE signing_keys' .../008_signing_keys.up.sql` = 1
  - `grep -c 'UNIQUE(repo_id, name, epoch, version, release, arch)' .../010_rpm_packages.up.sql` = 1
  - `grep -c 'CREATE VIRTUAL TABLE rpm_fts' .../014_protocol_fts.up.sql` = 1
  - `grep -c 'ALTER TABLE repos ADD COLUMN metadata_state' .../015_repo_metadata_state.up.sql` = 1
  - `grep -c 'func IndexRPM' internal/metadata/fts.go` = 1 (same for DEB, PyPI, Helm + 4 Delete counterparts)
  - `grep -c 'EvtRPMUpload' internal/audit/events.go` ≥ 1
  - `grep -c 'ActionRPMUpload' internal/auth/policy.go` ≥ 1
  - `grep -c 'SetMetadataState' internal/metadata/repos.go` = 1

## Known Stubs

None. Every table has a typed repo; every helper declared in the plan is implemented and exercised by a test. The Phase 3 event kinds declared in `internal/audit/events.go` are constants only — their *emission* happens in Plans 03-02..03-07; this is by design (single enumeration point, D-34) and is not a stub.

## Self-Check: PASSED

- Migrations 008 through 015 .up.sql and .down.sql: FOUND
- Typed repos signing_keys/apt_suites/rpm_packages/deb_packages/pypi_files/helm_charts: FOUND
- internal/crypto/pgpsign.go + pgpsign_test.go: FOUND
- internal/protocol/regen/coalescer.go + registry.go + tests: FOUND
- Commit 7091fe4: FOUND (git log --oneline)
- Commit e47d2b1: FOUND
- Commit 41ee847: FOUND
- `go test ./...` all packages green: CONFIRMED
