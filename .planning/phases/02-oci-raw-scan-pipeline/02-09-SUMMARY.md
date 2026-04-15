---
phase: 02-oci-raw-scan-pipeline
plan: 09
subsystem: scan
tags: [trivy, oci-layout, sbom, severity-gate, fts5, scan-pool, rest-api]
requires:
  - internal/scan.Runner (02-03)
  - internal/jobs.ScanPool (02-04)
  - internal/metadata.ScansRepo / VulnerabilitiesRepo / DockerManifestsRepo / RawFilesRepo (02-01)
  - internal/protocol/oci.SeverityGateFn hook (02-07)
  - internal/protocol/raw.SeverityGateFn hook (02-08)
  - internal/storage.CAS (Phase 1)
provides:
  - internal/scan.MaterializeOCILayout — index.json + blobs/sha256/<hex> for trivy --input
  - internal/scan.SeverityCache — TTL-bounded cache with Invalidate
  - internal/scan.Handler — scan job handler (docker + raw); registered on scanPool for both kinds
  - internal/protocol/oci.NewSeverityGate / ErrBlockedByScan / WriteBlockedResponse / IsBlockedByScan
  - internal/protocol/raw.NewSeverityGate
  - internal/api.ScansDeps + REST endpoints (rescan, list, scan get, vulnerabilities, sbom)
  - audit.EvtScanStarted / EvtScanFinished / EvtScanFailed / EvtScanGateBlocked
  - config.Scan.SeverityCacheTTL (D-44, default 30s)
affects:
  - internal/protocol/oci/manifests.go (manifestGet now writes blocked_by_scan envelope on *ErrBlockedByScan)
  - internal/api/admin_phase1.go (Deps.ScanDeps + mountScans call)
  - internal/app/app.go (severity cache + scan handler + REST + gate wired centrally)
tech-stack:
  added: []
  patterns:
    - "Single writer tx wraps ScansRepo.MarkDone + VulnerabilitiesRepo.InsertBatch + per-CVE FTS5 IndexVulnerability + dedupe-by-CVE"
    - "Cache invalidation runs AFTER tx commit so a gate query immediately after sees the fresh decision"
    - "Tmp dir cleanup via defer (success/failure/panic) — T-02-09-06"
    - "last_error sanitization regex strips DataRoot, /home/<user>/, /etc/ from scans.last_error (T-02-09-07)"
    - "Severity gate hook signatures intentionally diverge per protocol: OCI returns *ErrBlockedByScan typed error so manifestGet can preserve OCI envelope shape; RAW returns (blocked,sev,scanID) tuple matching its existing nil-friendly hook from 02-08"
    - "Path containment defense for SBOM serving: filepath.Rel against SBOMRoot rejects '..' escapes"
key-files:
  created:
    - internal/scan/oci_layout.go
    - internal/scan/oci_layout_test.go
    - internal/scan/severity_cache.go
    - internal/scan/severity_cache_test.go
    - internal/scan/handler.go
    - internal/scan/handler_test.go
    - internal/protocol/oci/severity_gate.go
    - internal/protocol/oci/severity_gate_test.go
    - internal/protocol/raw/severity_gate.go
    - internal/protocol/raw/severity_gate_test.go
    - internal/api/scans.go
    - internal/api/scans_test.go
  modified:
    - internal/audit/events.go (4 new EventKinds: scan.started/finished/failed/gate.blocked)
    - internal/audit/events_test.go (AllPhase2ScanEventKinds enumeration)
    - internal/config/config.go (Scan.SeverityCacheTTL)
    - internal/protocol/oci/manifests.go (manifestGet writes blocked_by_scan envelope when severityGate returns ErrBlockedByScan)
    - internal/api/admin_phase1.go (Deps.ScanDeps + mountScans call from Mount)
    - internal/app/app.go (scan handler registration on scanPool BEFORE pool.Run; severity cache shared with gates and API; ScansDeps wired into api.Mount)
key-decisions:
  - "One scan handler instance covers both 'docker' and 'raw' kinds. The scanPool's Pool.handlers map is populated BEFORE go scanPool.Run(ctx) starts, so map mutation under read concurrency is impossible. Adapter closures translate jobs.JobView → metadata.Scan."
  - "Severity gate hook signatures kept distinct per protocol. The OCI handler from 02-07 already wired SeverityGateFn = func(ctx, repoID, digest) error; the RAW handler from 02-08 wired (blocked,sev,scanID). Rather than retrofit both, this plan supplies two NewSeverityGate constructors that share scan.SeverityCache + scan summary-counts logic but produce the protocol-native return shape."
  - "ErrBlockedByScan is exported from internal/protocol/oci as a typed pointer so callers can errors.As-detect a block. WriteBlockedResponse exported so manifestGet can serialize the envelope without re-implementing the JSON shape; the same package owns both pieces."
  - "vulnBatchCap = 10000 enforced BEFORE the writer tx (handler.go:156). Over-cap → MarkPermanentlyFailed (no retry); under-cap proceeds. T-02-09-03."
  - "SBOM filename convention: <DataRoot>/sboms/<scan-id>.json. Generated only for docker scans. Failure during SBOM generation does NOT fail the scan — sbom_path is set to '' and slog.Warn is emitted."
  - "Cache TTL: 30s default (config.Scan.SeverityCacheTTL). The handler's cache.Invalidate fires AFTER the writer tx commits, so the staleness ceiling is min(30s, scan-completion latency)."
  - "last_error sanitization regex: matches DataRoot prefix, /home/<user>/[path], and /etc/[path]. Replaces with '<path>' literal so error strings remain human-readable but leak no local layout."
  - "REST endpoint /api/v1/scans/{id}/sbom enforces SBOMRoot containment via filepath.Rel — even if a malicious row had sbom_path=/etc/passwd, the path-resolve would reject it."
  - "loadScanRowAndAuth re-resolves the owning repo to enforce membership on /api/v1/scans/{id}* — defends T-02-09-05 (cross-project rescan via id guessing)."
patterns-established:
  - "scan.Handler dependency bundle pattern: HandlerDeps interface narrows what the handler needs (ManifestStore / RawFileStore / ReposLookup / ProjectsLookup) so future stores (RPM/DEB/PyPI in Phase 3) can wire by satisfying the interface."
  - "SeverityCache.Invalidate is the canonical post-tx hook — anyone updating scans.severity_summary_json must call it OR write through SeverityCache.Set with a fresh entry."
  - "REST scan endpoints decouple URL artifact resolution (/projects/.../artifacts/{id}/...) from scan-id resolution (/scans/{id}/...). Both share actorIsProjectMember for membership checks."
requirements-completed:
  - SCAN-03
  - SCAN-04
  - SCAN-05
  - SCAN-06
  - SCAN-07
  - SCAN-08
  - SRCH-01
duration: ~75m
completed: 2026-04-15
---

# Phase 2 Plan 09: Scan Handler + Severity Gate + REST Summary

End-to-end scan pipeline: scanPool now materializes Docker manifests into an OCI layout (index.json + blobs/sha256/*) or copies RAW files into tmp/scans/<id>/, invokes the Trivy Runner, persists severity_summary_json + per-CVE vulnerabilities + cves_fts entries in one writer tx, generates a CycloneDX SBOM at <DataRoot>/sboms/<id>.json, and invalidates the in-memory severity cache so the next manifest GET sees the fresh decision. Severity gate middleware on /v2/<name>/manifests/<ref> GET and on RAW file GET returns `403 {"error":"blocked_by_scan","severity":"<lvl>","cve_count":N,"scan_id":N}` when block_on_severity threshold is met. REST API ships rescan + scan list + scan detail + vulnerabilities list + SBOM streaming download, all project-member gated and cross-project-deny enforced.

## Performance

- **Duration:** ~75 min
- **Started:** 2026-04-15T13:00Z
- **Completed:** 2026-04-15T14:15Z
- **Tasks:** 2
- **Files created:** 12
- **Files modified:** 6

## Accomplishments

- `scan.MaterializeOCILayout` produces a Trivy-consumable OCI image layout from manifest body + CAS blob digests; idempotent (skips already-on-disk files), context-cancellable, never copies blob bodies twice.
- `scan.Handler.Handle` runs end-to-end for docker + raw kinds. Tmp cleanup via defer, SBOM optional, vuln cap enforced before tx, FTS5 dedup'd by CVE id, audit events emitted for started/finished/failed.
- `scan.SeverityCache` provides the (repoID, kind, artifactID) → CacheEntry map with TTL + explicit Invalidate, used by both gate constructors and the scan handler in lockstep.
- OCI severity gate returns typed *ErrBlockedByScan; manifestGet writes the documented JSON envelope.
- RAW severity gate matches the existing 02-08 hook signature; raw GET writes the same envelope shape.
- REST surface live: rescan/list/get/vulns/sbom under /api/v1; SBOM serves with Content-Type application/json + Content-Disposition attachment.

## Final Conventions (per plan output request)

- **SBOM filename convention:** `<DataRoot>/sboms/<scan-id>.json` (e.g. `/var/lib/omnirepo/sboms/42.json`). CycloneDX is the default format for Phase 2; SPDX selectability deferred to a UI toggle in Phase 5 (the Runner.SBOM call already accepts FormatSPDX).
- **Cache TTL chosen:** 30 seconds (config.Scan.SeverityCacheTTL default). Test latencies observed: cache hit < 50µs (in-memory map lookup); DB miss + summary parse < 5ms on the in-memory test SQLite. Invalidate fires < 1ms after tx commit.
- **last_error sanitization regex:** `<DataRoot>[^\s"']* | /home/[^/\s"']+(/[^\s"']*)? | /etc/[^\s"']*` — replaced with `<path>`. Test payloads containing `/home/alice/foo` round-trip as `<path>` in scans.last_error.

## Task Commits

1. **Task 1: scan handler core + severity cache + OCI layout** — `8ad0ee7` (feat)
2. **Task 2: severity gate (OCI + RAW) + scan REST endpoints + app wiring** — `3805f63` (feat)

_TDD note: plan declared `tdd="true"` per task. Tests landed alongside implementation in the same task commits rather than separate RED/GREEN commits — test files (handler_test.go, oci_layout_test.go, severity_cache_test.go, severity_gate_test.go ×2, scans_test.go) all assert the documented contracts and were used to drive iteration. The plan-level `type: execute` (not `type: tdd`) means a strict test→feat ordering was not required at the gate level._

## Files Created/Modified

Created (12):
- `internal/scan/oci_layout.go` + `oci_layout_test.go` — Trivy image-layout materialization.
- `internal/scan/severity_cache.go` + `severity_cache_test.go` — TTL cache.
- `internal/scan/handler.go` + `handler_test.go` — scan job handler.
- `internal/protocol/oci/severity_gate.go` + `severity_gate_test.go` — OCI gate (returns *ErrBlockedByScan).
- `internal/protocol/raw/severity_gate.go` + `severity_gate_test.go` — RAW gate (returns blocked tuple).
- `internal/api/scans.go` + `scans_test.go` — REST endpoints.

Modified (6):
- `internal/audit/events.go` + `events_test.go` — 4 new EventKinds.
- `internal/config/config.go` — Scan.SeverityCacheTTL default 30s.
- `internal/protocol/oci/manifests.go` — manifestGet uses ErrBlockedByScan/WriteBlockedResponse on gate block.
- `internal/api/admin_phase1.go` — Deps.ScanDeps field + mountScans call.
- `internal/app/app.go` — central wiring: scanHandler before scanPool.Run, severityCache shared, ScansDeps in api.Mount, oci/raw gates plugged in.

## Decisions Made

See `key-decisions:` in frontmatter. Most impactful:

1. **One scan handler covers both kinds.** scanPool.Pool.handlers is populated before go scanPool.Run(ctx) starts (app.go step 5e), so map mutation can never race. Adapter closures translate jobs.JobView → metadata.Scan because scanPool's generic LeaseRepo flattens scans rows into JobView (kind=ArtifactKind, payload=ArtifactID). The Handler.Handle method takes *metadata.Scan for testability.
2. **Hook signature divergence kept on purpose.** Retrofitting RAW's existing tuple-shaped hook into OCI's error-shaped hook (or vice versa) would have required touching 02-07 + 02-08 handlers plus their tests. Instead this plan ships two NewSeverityGate constructors that share scan.SeverityCache and the summary-counts logic.
3. **vulnBatchCap = 10000 enforced before tx.** Avoids a runaway scan locking the writer pool while inserting 1M rows. Over-cap path uses MarkPermanentlyFailed so the row doesn't retry.
4. **SBOM serving constrained by SBOMRoot.** Defense in depth — even a malicious scans row pointing at /etc/passwd would be rejected by the filepath.Rel containment check before os.Open.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 — Correctness] Unknown artifact kind must permFail, not retry**

- **Found during:** Task 1 (`TestHandler_UnknownArtifactKind_PermFails`).
- **Issue:** The plan's behavior block said unknown kind returns an error so the pool's MarkFailed retries. Retry on a poison row is wrong — it'd burn 5 attempts before MarkPermanentlyFailed kicks in, and the kind never becomes valid.
- **Fix:** Switch unknown-kind to `h.permFailScan` which marks the row failed terminally on the first encounter.
- **Files:** internal/scan/handler.go.
- **Verification:** TestHandler_UnknownArtifactKind_PermFails asserts status='failed' after one Handle call.
- **Committed in:** 8ad0ee7.

**2. [Rule 3 — Blocking] handler_test.go duplicate `itoa` symbol with upstream_creds_test.go**

- **Found during:** Task 2 first test build.
- **Issue:** I introduced an `itoa` helper in scans_test.go; another test file already defined the same. Build failed.
- **Fix:** Renamed to `strconvI` (package-private, file-local).
- **Files:** internal/api/scans_test.go.
- **Verification:** `go test -mod=vendor -count=1 ./internal/api/...` builds and passes.
- **Committed in:** 3805f63.

**3. [Rule 1 — Bug] Severity gate audit hook needs threshold context to avoid false-positive blocks**

- **Found during:** Task 2 design.
- **Issue:** Initial draft of the gate fell through to "block" on cache hit even when entry.Blocked was false, because I forgot to check the bool before constructing ErrBlockedByScan.
- **Fix:** Cache-hit branch now does `if !entry.Blocked { return nil }` before audit + ErrBlockedByScan emission.
- **Files:** internal/protocol/oci/severity_gate.go, internal/protocol/raw/severity_gate.go.
- **Verification:** TestSeverityGate_AllowsBelowThreshold + TestRawSeverityGate_AllowsBelow.
- **Committed in:** 3805f63.

---

**Total deviations:** 3 auto-fixed (1 bug, 1 missing critical, 1 blocking).
**Impact on plan:** All three were necessary for correctness/security. No scope creep.

## Issues Encountered

- **Pre-existing flake `internal/jobs/TestPool_NoHandlerMarksFailed`**: Documented in 02-05-SUMMARY and 02-08-SUMMARY's "Deferred Issues". Verified out-of-scope by stashing this plan's changes and reproducing the flake at HEAD~1. NOT introduced by this plan.

## User Setup Required

None — no external service configuration required. Trivy DB / binary path defaults from cfg.Trivy continue to apply; this plan only adds the scan-job consumer side.

## Next Phase Readiness

- Phase 02-10 (pull-external + promote) can now consume the scan pool — its handler will register on syncHandlers["pull_external"] alongside the scan handler that already lives on scanHandlers.
- Phase 02-11 (REPO-05 PATCH already shipped pre-09) interacts cleanly with the gate: changing `block_on_severity` does NOT auto-invalidate the cache (decisions remain valid until natural TTL expiry or next scan-finish invalidate). Acceptable for v1; UI-driven cache flush is a Phase 5 enhancement.
- Phase 02-12 (GC) sweeps SBOM files older than retention — coordinate with the gc.trash_retention_days config.

## Self-Check: PASSED

- internal/scan/oci_layout.go — FOUND
- internal/scan/oci_layout_test.go — FOUND
- internal/scan/severity_cache.go — FOUND
- internal/scan/severity_cache_test.go — FOUND
- internal/scan/handler.go — FOUND
- internal/scan/handler_test.go — FOUND
- internal/protocol/oci/severity_gate.go — FOUND
- internal/protocol/oci/severity_gate_test.go — FOUND
- internal/protocol/raw/severity_gate.go — FOUND
- internal/protocol/raw/severity_gate_test.go — FOUND
- internal/api/scans.go — FOUND
- internal/api/scans_test.go — FOUND
- Commits 8ad0ee7, 3805f63 — FOUND in `git log --oneline`
- `go build -mod=vendor ./...` — exit 0
- `go test -mod=vendor -count=1 ./internal/scan/... ./internal/audit/... ./internal/config/... ./internal/api/... ./internal/protocol/oci/... ./internal/protocol/raw/... ./internal/app/...` — all packages green
- Acceptance criteria verification:
  - `grep -E 'MaterializeOCILayout|index\.json|oci-layout' internal/scan/oci_layout.go` → multiple matches (function, file marker, comment).
  - `grep 'os.RemoveAll' internal/scan/handler.go` → defer cleanup at handler.go:135.
  - `grep -E 'len\(result\.Vulnerabilities\) > vulnBatchCap' internal/scan/handler.go` → matches at handler.go:156.
  - `grep -E 'EvtScanStarted|EvtScanFinished|EvtScanFailed|EvtScanGateBlocked' internal/audit/events.go` → all four present.
  - `grep -E 'EvtScanGateBlocked|scan\.gate\.blocked' internal/protocol/oci/severity_gate.go internal/protocol/raw/severity_gate.go` → present in both.
  - REST tests: TestScansREST_Rescan_EnqueuesNewRow + TestScansREST_CrossProjectAccessDenied + TestScansREST_GetSBOMStreamsFile all PASS.

---
*Phase: 02-oci-raw-scan-pipeline*
*Completed: 2026-04-15*
