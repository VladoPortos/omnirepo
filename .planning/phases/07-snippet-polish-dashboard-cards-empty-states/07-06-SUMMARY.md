---
phase: 07-snippet-polish-dashboard-cards-empty-states
plan: 06
subsystem: [dashboard, deb-sync, walkthrough-fixes]
tags: [backend, storage, deb, sqlite, tdd, walkthrough-fix]
requires:
  - modernc.org/sqlite driver (typed Scan behavior)
  - SQLite json_each / json_extract / CAST(... AS INTEGER)
  - net/mail stdlib RFC 822 header parser
  - internal/api.Deps (DB.Reader for dashboard storage query)
  - internal/protocol/deb sync handler (fetchAndCommit hook site)
provides:
  - ref-counted repoSizeExpr SQL fragment (W-02)
  - ResolvePoolPath helper — Release-file-aware DEB pool-path resolver (W-03)
  - extractFirstComponent helper (internal) — parses Release via net/mail.ReadMessage
  - isSafeComponent helper (internal) — T-07-06-01 traversal mitigation
  - TestDashboardStorage_RefCountsSharedBlobs (new)
  - TestResolvePoolPath_* (6 new sub-tests)
affects:
  - internal/api/dashboard.go (repoSizeExpr rewritten + doc comment replaced)
  - internal/protocol/deb/sync_handler.go (relPoolPath signature extended; single call site updated)
tech-stack-added: []
tech-stack-patterns:
  - "SQLite * 1.0 forces REAL division, then CAST(... AS INTEGER) casts back because modernc sqlite refuses REAL→int64 on Scan"
  - "net/mail.ReadMessage with synthesized blank-line terminator for RFC-822-style Debian Release files"
  - "defensive isSafeComponent() reject on `/`, `..`, NUL, >64 chars — traversal mitigation"
  - "thin-wrapper pattern: relPoolPath preserves callability; ResolvePoolPath is the exported API"
key-files-created:
  - internal/protocol/deb/pool_release.go
  - internal/protocol/deb/pool_release_test.go
key-files-modified:
  - internal/api/dashboard.go
  - internal/api/dashboard_test.go
  - internal/protocol/deb/sync_handler.go
decisions:
  - "[07-06] CAST(SUM(b.size_bytes * 1.0 / b.distinct_repos) AS INTEGER) is mandatory — modernc.org/sqlite's driver returns an error rather than silently truncating REAL→int64 on Scan; without the CAST the dashboard silently returned 0 for every repo holding a docker blob"
  - "[07-06] Existing TestDashboardStorage_ReturnsRepoBreakdown stays green unchanged because its fixture plants zero docker_blobs rows — Pitfall 5 verified: the blob sub-expression evaluates to 0 with or without the rewrite"
  - "[07-06] relPoolPath signature extended in place (repoRoot, project, repo, suite, filename, ctrl) — single call site in fetchAndCommit already had every parameter in scope, so no shim or helper struct needed"
  - "[07-06] Control struct used as-is from parse.go (Package field only) — no adaptation needed; the test seeded Control{Package: X} matches production callers"
  - "[07-06] Traversal-rejection mitigation (T-07-06-01) moved into isSafeComponent helper rather than inline Rule-2 auto-add — added as part of the primary design because the threat model explicitly called for a `mitigate` disposition on crafted Release bytes"
metrics:
  duration: "6m16s"
  completed-date: "2026-04-18"
  tasks-completed: "2 / 2"
  files-touched: "5"
---

# Phase 7 Plan 06: Walkthrough Micro-Fixes (W-02 + W-03) Summary

**One-liner:** W-02 ref-counts shared docker blob bytes via
`CAST(SUM(size * 1.0 / distinct_repos) AS INTEGER)`; W-03 reads
`dists/<suite>/Release` through `net/mail.ReadMessage` with a
traversal-safe fallback to the legacy filename-inference path.

## What Shipped

### W-02 — ref-counted repoSizeExpr

`internal/api/dashboard.go:repoSizeExpr` rewritten. The docker-blob
sub-expression now:

1. Finds every blob referenced (via `json_each` on layers + `json_extract`
   on `config.digest`) by a manifest in repo `r.id`.
2. Groups those blob rows by digest, computing `COUNT(DISTINCT m2.repo_id)`
   as the number of distinct repos whose manifests reference that blob.
3. Sums `size_bytes * 1.0 / distinct_repos` across the grouped rows and
   `CAST(... AS INTEGER)` back to integer for the outer addition.

A 2 GiB blob referenced by two repos contributes ~1 GiB to each repo's
reported `size_bytes` instead of 2 GiB to both. The `dashboard.storage_used_bytes`
aggregate now reflects actual disk usage rather than an N× inflation.

Doc comment above `repoSizeExpr` replaces the old "NOT split across repos"
framing with a "split across referencing repos by COUNT(DISTINCT repo_id)"
explanation plus a note that billing-grade attribution is still v2.0 work.

**Why existing TestDashboardStorage_ReturnsRepoBreakdown still passes
(Pitfall 5 verification):** that fixture plants a single `docker_manifests`
row with a 1 GiB body but zero `docker_blobs` rows. The ref-counted
sub-expression's `JOIN docker_blobs db` produces an empty row set,
`SUM(...)` is NULL, `COALESCE(..., 0)` returns 0, so the blob contribution
is 0 with or without the rewrite. Only the manifest-body column
(`docker_manifests.size_bytes`) contributes, which is untouched.

### W-03 — Release-aware DEB pool-path resolution

Three artifacts:

- **`internal/protocol/deb/pool_release.go` (new, 92 lines):**
  - `ResolvePoolPath(repoRoot, project, repo, suite, filename, ctrl)` —
    the exported helper. Attempts to read
    `<repoRoot>/<project>/deb/<repo>/dists/<suite>/Release` and honor its
    first-listed `Components:` entry; falls back to
    `pool/main/<initial>/<pkg>/<filename>` on any failure mode.
  - `extractFirstComponent(data []byte)` (unexported) — parses Release
    via `net/mail.ReadMessage` with a synthesized blank-line terminator
    when the file omits the header/body separator.
  - `isSafeComponent(s string)` (unexported) — T-07-06-01 traversal
    mitigation. Rejects any component containing `/`, `..`, a NUL byte,
    or exceeding 64 chars. Returns false → fallback to `"main"`.

- **`internal/protocol/deb/sync_handler.go` (modified):**
  - `relPoolPath` signature extended from `(filename, ctrl)` to
    `(repoRoot, project, repo, suite, filename, ctrl)` — a thin wrapper
    over `ResolvePoolPath`.
  - Sole call site in `fetchAndCommit` (line 260) updated to thread
    `h.deps.RepoRoot`, `projectName`, `repo.Name`, and `ent.Suite` (with
    default `"stable"` fallback matching `SyncPayload.Suite`).

- **`internal/protocol/deb/pool_release_test.go` (new, 126 lines):**
  Six sub-tests cover the three documented branches plus threat-model
  mitigation and nil-Control:
  1. `ReadsReleaseFile` — Release with `Components: main contrib` →
     `pool/main/...`
  2. `CustomComponent` — Release with first component `contrib` →
     `pool/contrib/...`
  3. `FallsBackWhenReleaseMissing` — no Release file on disk →
     `pool/main/...`
  4. `FallsBackOnMalformedRelease` — Release without `Components:` line
     → `pool/main/...`
  5. `RejectsTraversalInComponent` — adversarial
     `Components: ../evil main` → `pool/main/...` (T-07-06-01
     mitigation)
  6. `NilControl` — `ResolvePoolPath(... nil)` → `pool/main/x/x/...`

## Caller-site decision (W-03)

`relPoolPath` had exactly one caller (`fetchAndCommit` at sync_handler.go:260),
and that caller had every required context parameter in scope already
(`h.deps.RepoRoot`, `projectName`, `repo.Name`, `ent.Suite`). The planner's
alternative "thin shim + new `relPoolPathFor`" path was avoided in favor of
a direct signature extension. Zero call-site duplication; `relPoolPath` now
exists only as a thin semantic wrapper preserving the legacy function-name
reference for future readers. Tests assert behaviour via the exported
`ResolvePoolPath` API (the public contract).

## Control struct adaptation

None. The existing `Control` struct in `internal/protocol/deb/parse.go`
already exposes `Package` as the only field `ResolvePoolPath` consults;
tests seed `Control{Package: "foo"}` directly without wrapper shims.

## Test counts

- **New Go tests: 7 total**
  - `TestDashboardStorage_RefCountsSharedBlobs` (1 in `internal/api`)
  - `TestResolvePoolPath_ReadsReleaseFile`
  - `TestResolvePoolPath_CustomComponent`
  - `TestResolvePoolPath_FallsBackWhenReleaseMissing`
  - `TestResolvePoolPath_FallsBackOnMalformedRelease`
  - `TestResolvePoolPath_RejectsTraversalInComponent`
  - `TestResolvePoolPath_NilControl` (6 in `internal/protocol/deb`)

- **Existing tests touched: 0.** No pre-existing test file required
  updates; `TestDashboardStorage_ReturnsRepoBreakdown` passes unchanged
  (Pitfall 5), and the existing `sync_handler_test.go` paths call into
  `fetchAndCommit` with realistic fixtures that still produce valid
  pool paths through the Release-aware resolver.

## Deviations from Plan

**None rated Rule 1–3 in the spec sense** — the plan called for a
`CAST` note but initially omitted it from the code snippet; discovering
the modernc driver's strict REAL→int64 scan behavior through the test
run is an expected TDD iteration, not a deviation. Documented as a
decision (`[07-06]`) rather than a deviation.

**Rule 2 (additive security):** `isSafeComponent` was added as a
first-class helper (not an inline check) because T-07-06-01 in the
threat model explicitly disposes "mitigate" on crafted Release bytes
and the mitigation needed a dedicated, testable code path. The plan
named this as mitigation-required so the work was anticipated;
including it as part of the primary design rather than a post-hoc
Rule-2 auto-add keeps the test surface honest.

## Verification

- `go test -count=1 ./internal/api/ -run TestDashboardStorage -v` →
  3/3 PASS (existing 2 + new 1).
- `go test -count=1 ./internal/protocol/deb/ -run TestResolvePoolPath -v` →
  6/6 PASS.
- `go test ./internal/api/` → PASS (no regression).
- `go test ./internal/protocol/deb/` → PASS (no regression).
- `go test ./...` → every package green, zero failures.
- `go build ./...` → clean.
- `make test` → all 5 Phase 6 lint gates green
  (`lint-protocol-redaction`, `check-contrast`, `lint-typography`,
  `lint-spacing-carveout`, `lint-axe-devdep`) + airgap tests cached-green.

## Threat Flags

No new surface introduced outside the plan's `<threat_model>`. The
T-07-06-01 mitigation ships as the first-class `isSafeComponent`
helper and is exercised by `TestResolvePoolPath_RejectsTraversalInComponent`.

## Commits

- `a094cff` test(07-06): add failing test for ref-counted docker blob size attribution
- `04a1f19` feat(07-06): ref-count shared docker blob bytes in repoSizeExpr
- `03eb808` test(07-06): add failing tests for Release-aware DEB pool-path resolution
- `9c60eb2` feat(07-06): read dists/<suite>/Release for DEB pool-path resolution

## Self-Check: PASSED

- `internal/api/dashboard.go` — FOUND (modified, 4 file)
- `internal/api/dashboard_test.go` — FOUND (modified)
- `internal/protocol/deb/pool_release.go` — FOUND (new)
- `internal/protocol/deb/pool_release_test.go` — FOUND (new)
- `internal/protocol/deb/sync_handler.go` — FOUND (modified)
- Commits `a094cff`, `04a1f19`, `03eb808`, `9c60eb2` — all FOUND in `git log`.
