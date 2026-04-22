---
phase: 11-mirror-infrastructure-widening
plan: 03
subsystem: protocol/helm + api/mirror-validation
tags: [mirror, oci, helm, sync, tag-rebound, docker-hub, trash, audit]

# Dependency graph
requires:
  - phase: 11-mirror-infrastructure-widening
    provides: "11-01 (ociclient.Client + FakeClient) + 11-02 (EntrySourceOCI + refuseDockerHubWithoutCred + EvtOciTagRebound) — both consumed as foundational primitives by the OCI sync integration and API cred-gate in this plan."
  - phase: 03-deb-rpm-pypi-helm
    provides: "internal/protocol/helm (sync_handler, fetchAndCommit, regen, Parse, pathStore layout, helm_charts metadata repo) — the HTTP-path sync infrastructure that 11-03 branches alongside for the new OCI path."
  - phase: 08-mirror-infra
    provides: "repoPatchRequest + handlePatchRepo mirror-field wiring (MIRROR-01..07) that 11-03 extends with the cred-gate on PATCH and a non-pointer json.RawMessage fix."

provides:
  - "helm.SyncDeps.OCIClient + .Trash fields — dispatch primitive for the OCI sync branch and soft-delete primitive for tag-rebound retention (D-02)."
  - "helm.SyncHandler.fetchAndCommitOCI — end-to-end OCI pull + tag-rebound-aware commit-tail, branched from fetchAndCommit on UpstreamEntry.Source == EntrySourceOCI."
  - "helm.parseOCIRef(ref) → (name, version) — canonical OCI ref parser for the last-segment/tag convention."
  - "Docker Hub cred gate at POST /api/v1/projects/{name}/repos (helm type, oci:// upstream, registry-1.docker.io/*, no cred) → 422 mirror.docker_hub_requires_credential."
  - "Docker Hub cred gate at PATCH /api/v1/projects/{name}/repos/helm/{repo} with effective-state post-patch invariant."
  - "phase3_sync.wireSync now injects ociclient.New(httpClient) + storage.NewTrash into helm.SyncDeps — production wiring for every helm sync job."

affects: [11-04]  # Live Bitnami OCI E2E (OCIHELM-08) — consumes fetchAndCommitOCI end-to-end against the real registry-1.docker.io/bitnamicharts/*.

# Tech tracking
tech-stack:
  added: []  # Zero new go.mod deps. Every primitive needed was in scope from plans 11-01 (ociclient) + 11-02 (validator + audit) or pre-existing (storage.Trash, helm_charts repo).
  patterns:
    - "Per-transport dispatch at fetchAndCommit on UpstreamEntry.Source — keeps the HTTP path bit-for-bit unchanged and isolates the OCI path to one method, so regression risk stays scoped to the new branch."
    - "Pre-flight Resolve for dedup → conditional PullChart — avoids wasting Docker Hub rate-limit quota (and bandwidth) on digests we already have cached. Matches the convention ORAS/crane clients use for layer pulls."
    - "Two-stage digest comparison: manifest digest (Resolve) for pre-pull dedup, chart-layer digest (PullChart.Digest) for post-pull dedup. Covers the case where the stored row captured a layer digest from a prior pull but Resolve now returns a manifest digest — cheap to double-check before writing."
    - "Trash.Move with kind='oci_tag_rebound' as a distinct retention label — lets operators grep the filesystem for CVE-driven republication timelines without joining tables. Reuses the existing Trash interface verbatim (no schema change)."
    - "API-layer validation invoked via writeEnvelope(*httperr.Error) — keeps mixed envelope styles (legacy writeJSONError + modern writeEnvelope) side-by-side in the same handler so the Docker Hub gate can speak the structured 422 shape without disturbing the surrounding deb/rpm/pypi/helm mirror validators."
    - "PATCH effective-state cred-gate pattern: compute post-patch cred_id (patch value if field set, else existing row value), run validator. Mirrors how other idempotent-PATCH gates (e.g. mirror_cred_wrong_project) validate the resulting state rather than the delta."

key-files:
  created:
    - internal/protocol/helm/sync_oci_integration_test.go (651 lines — 6 hermetic tests covering single-chart, dedup, tag-rebound, mixed HTTP+OCI, nil-OCIClient, and regen-HTTP-urls)
    - internal/api/repos_docker_hub_gate_test.go (200 lines — 5 tests covering POST/PATCH cred-gate + GHCR bypass + same-cred no-op)
  modified:
    - internal/protocol/helm/sync_handler.go (+246 lines — OCIClient/Trash fields on SyncDeps; collectFn now accepts EntrySourceOCI; fetchAndCommit dispatches to fetchAndCommitOCI; fetchAndCommitOCI implements Resolve → dedup → PullChart → tag-rebound Trash.Move + EvtOciTagRebound → commit-tail; parseOCIRef helper; retired skipped_oci_entries++ counter)
    - internal/app/phase3_sync.go (+9 lines — ociclient + storage imports, OCIClient + Trash wired into helm.SyncDeps construction)
    - internal/api/admin_phase1.go (+21 lines — Docker Hub cred gate in handleCreateRepo, looks up cred.kind via UpstreamCreds.Get before invoking refuseDockerHubWithoutCred + writeEnvelope)
    - internal/api/repos.go (+45/-11 — Docker Hub cred gate on PATCH with effective-state cred_id computation; fixed repoPatchRequest.MirrorCredIDRaw from *json.RawMessage → json.RawMessage so null-vs-absent distinction survives JSON round-trip)
    - internal/protocol/helm/sync_progress_test.go (+72/-55 — TestHelmSync_SkipsOCIEntries renamed to TestHelmSync_MixedHTTPAndOCIEntries and rewritten to use ociclient.FakeClient so both http-tgz AND oci entries commit; skippedOCI++ assertion retired)

key-decisions:
  - "Use HelmCharts.FindByNameVersion (pre-existing) rather than adding FindByKey. The plan sketched a new method but the existing NameVersion scan is the exact same shape — no new metadata surface needed. Saves a migration and keeps the repo API lean."
  - "Fix the PATCH handler's json.RawMessage pointer bug (Rule 1) rather than working around it in the test. repoPatchRequest.MirrorCredIDRaw was *json.RawMessage; Go's encoding/json nils the pointer on JSON null, collapsing 'absent' and 'null' into one state — defeating the handler's 'null → clear' branch. Switched to non-pointer json.RawMessage with len-based detection. Behavior change: PATCH `{\"mirror_cred_id\": null}` now actually clears the cred (previously was a silent no-op)."
  - "Keep skipped_oci_entries=0 on the SyncProgress audit rather than drop the key entirely. Dashboards + audit-grep downstream may already filter on this field; zeroing it preserves backward compat while signalling the stub retirement (Success Criterion 1 in ROADMAP). The skipped_non_http_entries counter (for file://, ftp://, etc.) stays live as the remaining skip category."
  - "Two-stage digest compare (manifest + chart-layer) rather than one or the other. Resolve returns a manifest digest; PullChart returns a chart-layer digest. Storing only one would cause false rebounds when a prior row captured the other shape. Compare against both before deciding to write; the second check is cheap and covers operator-driven migrations from layer-digest-recorded storage to manifest-recorded storage."
  - "Record the chart-layer digest (PullResult.Digest) in helm_charts.digest — matches the HTTP path's semantics (sha256 of the tgz bytes) so cross-source comparisons stay consistent. Fall back to manifest digest only when PullChart returns an empty Digest field (defensive)."

patterns-established:
  - "UpstreamEntry.Source-based dispatch at fetchAndCommit boundary — reusable template for any future transport widening on the helm sync path (e.g. custom auth-headers, mTLS upstream)."
  - "Tag-rebound accounting via Trash.Move + distinct retention_label kind — the existing Trash primitive supports any label; callers pass a semantic kind string. No new trash schema needed for future rebound-like scenarios (e.g. GitMirror force-push replacement)."
  - "PATCH effective-state invariant gating — compute post-patch value, validate, then commit. Encodes the correct semantics for any invariant that must hold AFTER the PATCH rather than just checking the delta. Transferable to GITMIRROR plan 11-05."
  - "json.RawMessage (non-pointer) for 3-state field signalling (absent / null / typed-value) — use this shape for any PATCH request type that needs to distinguish the three. Documented inline at repoPatchRequest.MirrorCredIDRaw with rationale."

requirements-completed: [OCIHELM-01, OCIHELM-02, OCIHELM-03, OCIHELM-04, OCIHELM-05, OCIHELM-06]

# Metrics
duration: 16m 52s
completed: 2026-04-22
---

# Phase 11 Plan 03: OCI Helm pull + Docker Hub cred-gate Summary

**End-to-end OCI Helm pull via ociclient.Client replaces the v1.2 skipped_oci_entries skip stub, with tag-rebound detection (Trash.Move + EvtOciTagRebound audit), dedup on (repo_id, name, version, digest), and a Docker Hub cred-gate at both POST + PATCH /api/v1/projects/{name}/repos endpoints.**

## Performance

- **Duration:** 16m 52s
- **Started:** 2026-04-22T01:54:55Z
- **Completed:** 2026-04-22T02:11:47Z
- **Tasks:** 3/3 (5 atomic commits — T1 straight, T2 RED+GREEN, T3 RED+GREEN)
- **Files created:** 2 (651-line OCI integration test + 200-line API cred-gate test)
- **Files modified:** 5
- **Lines of code:** +1337 (1173 / added + test harness)

## Accomplishments

- Helm SyncDeps now carries `OCIClient` (ociclient.Client interface from 11-01) and `Trash` (storage.Trash) fields; phase3_sync.wireSync populates both from the shared httpClient + DataRoot/trash in production wiring.
- `fetchAndCommitOCI` implements the complete OCI Helm chart pull: pre-flight Resolve for dedup gating, conditional PullChart, tag-rebound detection via `HelmCharts.FindByNameVersion`, soft-delete to trash under kind `oci_tag_rebound` (D-02), `EvtOciTagRebound` audit emit with the full D-05 details shape (`name`, `version`, `old_digest`, `new_digest`, `upstream_url`, `repo_id`, `replaced_at`), and commit-tail identical to the HTTP path (Put → Parse → WriteTx with Insert/IndexHelm/Scans.Enqueue).
- The v1.2 `skipped_oci_entries` skip stub is retired. The collectFn now routes oci:// entries through the fetchAndCommit dispatch; `skippedOCI` counter is gone; the SyncProgress audit emits `skipped_oci_entries: 0` as a backward-compat signal (Success Criterion 1 in ROADMAP).
- Docker Hub cred-gate now fires at both `POST /api/v1/projects/{name}/repos` and `PATCH /api/v1/projects/{name}/repos/helm/{repo}` for helm mirrors with `oci://registry-1.docker.io/*` upstream and no attached cred → 422 `mirror.docker_hub_requires_credential` envelope (OCIHELM-05 / D-04). The PATCH path computes the effective post-patch cred_id so un-credentialing an existing Docker Hub mirror is refused.
- Pre-existing `*json.RawMessage` bug fixed (Rule 1): `repoPatchRequest.MirrorCredIDRaw` switched to non-pointer `json.RawMessage` so `{"mirror_cred_id": null}` now reliably triggers the "clear cred" branch. Previously the null path was dead code due to Go's pointer/null interaction — no pre-existing tests hit the path so the bug was latent until this plan needed it.
- Regen invariant preserved: OCI-sourced charts, after committing to `<repoRoot>/<proj>/helm/<repo>/charts/<name>-<version>.tgz`, are served by the existing `helmrepo.IndexDirectory` regen with HTTP-scheme URLs (NEVER `oci://`). Proven by `TestRegenIndexServesHTTPURLs_OCISourced` — the regen path is entirely source-agnostic (OCIHELM-06).
- 6 hermetic OCI integration tests + 5 API cred-gate tests; full `go test ./...` green (36+ packages).

## Task Commits

1. **Task 1: Wire ociclient.Client + Trash into helm.SyncDeps** — `ff66a61` (feat)
2. **Task 2 RED: Failing OCI sync integration tests** — `3448726` (test)
3. **Task 2 GREEN: Replace OCI skip stub with real pull + tag-rebound** — `d1cf39d` (feat)
4. **Task 3 RED: Failing Docker Hub cred-gate tests on POST/PATCH** — `c44b858` (test)
5. **Task 3 GREEN: Docker Hub cred gate wired into admin_phase1 + repos PATCH** — `baac7c0` (feat)

## Files Created/Modified

- `internal/protocol/helm/sync_oci_integration_test.go` (NEW — 651 lines): recordingAudit fake + ociHelmFixture bundler + makeOCIIndex helper + 6 tests (SingleChart, DedupSkipsPull, TagRebound, MixedIndex_HTTPEntriesUnchanged, NilOCIClient_FailsGracefully, RegenIndexServesHTTPURLs_OCISourced).
- `internal/api/repos_docker_hub_gate_test.go` (NEW — 200 lines): 5 tests covering POST/PATCH cred-gate behavior, GHCR bypass, and PATCH with same-cred no-op.
- `internal/protocol/helm/sync_handler.go` (MODIFIED — +246 lines):
  - SyncDeps: added `OCIClient ociclient.Client` + `Trash storage.Trash` fields.
  - Imports: added `errors`, `auth`, `ociclient`.
  - Handle: collectFn no longer hardcodes `skippedOCI++` on `oci://` — routes by `ent.Source` tag; retired the `skippedOCI` local variable; SyncProgress audit now emits `skipped_oci_entries: 0` as a backward-compat signal.
  - fetchAndCommit: branch on `ent.Source == EntrySourceOCI` → `fetchAndCommitOCI`.
  - fetchAndCommitOCI: new method implementing the full OCI pull + tag-rebound flow (Resolve → dedup → PullChart → Trash.Move + EvtOciTagRebound on rebound → commit-tail).
  - parseOCIRef: helper extracting `(name, version)` from `[oci://]host/path/<name>:<tag>`.
- `internal/app/phase3_sync.go` (MODIFIED — +9 lines): added `ociclient` + `path/filepath` imports; helm.SyncDeps constructor now populates `OCIClient: ociclient.New(httpClient)` and `Trash: storage.NewTrash(filepath.Join(d.cfg.DataRoot, "trash"))`.
- `internal/api/admin_phase1.go` (MODIFIED — +21 lines): handleCreateRepo's mirror-validation block now invokes `refuseDockerHubWithoutCred(req.MirrorUpstreamURL, credKind)` after the ownership check; cred kind resolved via `UpstreamCreds.Get` when `req.MirrorCredID != nil`.
- `internal/api/repos.go` (MODIFIED — +45/-11): handlePatchRepo gains the same cred-gate with post-patch effective-state computation; `repoPatchRequest.MirrorCredIDRaw` switched from `*json.RawMessage` to `json.RawMessage` with rationale comment; handler's `if body.MirrorCredIDRaw != nil` → `if len(body.MirrorCredIDRaw) > 0`.
- `internal/protocol/helm/sync_progress_test.go` (MODIFIED — +72/-55): former `TestHelmSync_SkipsOCIEntries` renamed to `TestHelmSync_MixedHTTPAndOCIEntries` and rewritten to use `ociclient.FakeClient` so the oci entry is pulled (not skipped); now asserts `helm_charts` rowcount = 2 and `progress_bytes = 2`.

## Decisions Made

- **Reuse `HelmCharts.FindByNameVersion` — no new FindByKey method.** The plan sketched a new method but the existing `FindByNameVersion(ctx, repoID, name, version) (*HelmChart, error)` scans the exact same columns. Adding a duplicate would be noise. Confirmed `ErrNotFound` is returned when no row matches; the caller uses `errors.Is(err, metadata.ErrNotFound)` to distinguish "fresh digest" from "real error".
- **Use the chart-layer digest (PullResult.Digest) in helm_charts.digest, fall back to manifest digest only when empty.** The HTTP path stores the sha256 of the downloaded tgz bytes; for parity the OCI path stores the chart-layer digest (Helm SDK's `res.Chart.Digest`) which is the sha256 of the chart content. Cross-source dedup queries (e.g. "find charts across repos with the same content") now work uniformly. Manifest digest is the fallback only when the SDK returns an empty layer digest (shouldn't happen in practice; defensive).
- **Pass `kind: "oci_tag_rebound"` directly to Trash.Move as the retention label.** Trash's `kind` parameter doubles as the retention_label per the existing Trash interface shape (see `storage/trash.go` holder format `<ts>-<kind>-<id>`). The plan referenced `retention_label = "oci_tag_rebound"` abstractly; concretely it's the `kind` arg. Keeps the Trash interface untouched.
- **Emit EvtOciTagRebound audit even when Trash.Move hits ErrNotExist on the old file.** A missing prior tgz is a data-consistency concern (repo directory drifted from DB) but should NOT block the rebound audit — operators need the audit trail regardless. Trash.Move errors other than ErrNotExist are fatal (surface to the sync job error log).
- **Fix `*json.RawMessage` → `json.RawMessage` in repoPatchRequest (Rule 1 pre-existing bug).** Go's encoding/json collapses JSON null into a nil pointer for `*json.RawMessage`, making the handler's "null → clear" branch unreachable through normal JSON serialization. Fixed by switching to a non-pointer `json.RawMessage` + `len > 0` check. Behavior change: PATCH `{"mirror_cred_id": null}` now actually clears the cred (was a silent no-op). No existing tests hit this path, so the fix is strict correctness.
- **Keep `skipped_oci_entries: 0` in the SyncProgress audit instead of dropping the key.** Dashboards + audit-grep downstream may already filter on this field. Emitting the field with value 0 signals the stub retirement (Success Criterion 1 in ROADMAP) while preserving backward compatibility with any consumer that expected the key to be present. The corresponding `skippedOCI` counter variable is retired.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — Bug] repoPatchRequest.MirrorCredIDRaw was \*json.RawMessage, collapsing JSON-null with field-absent**

- **Found during:** Task 3 (Docker Hub cred-gate on PATCH)
- **Issue:** The PATCH handler's "null → clear cred" branch was documented and implemented at `repos.go:232-237`, but the surrounding field type `*json.RawMessage` caused Go's encoding/json to nil the pointer on JSON null — making the "null" branch unreachable through the normal JSON-body path. Confirmed with a standalone Go program reproducing the collapse. No existing test covered this path so it was latent; my Task 3 test `TestPatchRepo_HelmMirror_RemoveDockerHubCred_Returns422` exposed it.
- **Fix:** Changed the field type to non-pointer `json.RawMessage`. When the field is absent, `len(raw) == 0`; when set to null, `string(raw) == "null"` (4 bytes). Updated the handler guard from `if body.MirrorCredIDRaw != nil` to `if len(body.MirrorCredIDRaw) > 0`. Added a docstring on the field explaining the rationale so future maintainers don't re-introduce the pointer.
- **Files modified:** `internal/api/repos.go`
- **Verification:** `TestPatchRepo_HelmMirror_RemoveDockerHubCred_Returns422` now passes with raw JSON `{"mirror_cred_id": null}` → 422 `mirror.docker_hub_requires_credential` + unchanged cred on the row (atomic refusal). Full `go test ./internal/api/...` green.
- **Committed in:** `baac7c0` (Task 3 GREEN)

**2. [Rule 2 — Missing Critical] Updated TestHelmSync_SkipsOCIEntries to match new semantics**

- **Found during:** Task 2 GREEN (after removing the skipped_oci_entries stub)
- **Issue:** The v1.2 regression test `TestHelmSync_SkipsOCIEntries` (Phase 9 POLISH-05) asserted that oci entries are skipped and only the http-tgz chart lands — behavior that Task 2 deliberately inverts. Leaving the test unchanged would cause a failure unrelated to the plan's correctness; deleting it would lose the mixed-index fixture value.
- **Fix:** Renamed to `TestHelmSync_MixedHTTPAndOCIEntries` and rewritten to wire an `ociclient.FakeClient` so the oci entry is pulled. The test now asserts BOTH charts land (rowcount = 2, progress_bytes = 2), preserving the mixed-index fixture shape but under the new v1.4 semantics.
- **Files modified:** `internal/protocol/helm/sync_progress_test.go`
- **Verification:** `go test -run TestHelmSync_MixedHTTPAndOCIEntries ./internal/protocol/helm/...` green.
- **Committed in:** `d1cf39d` (Task 2 GREEN)

---

**Total deviations:** 2 auto-fixed (1 Rule 1 bug fix, 1 Rule 2 test update)
**Impact on plan:** Both auto-fixes were required for correctness — the `*json.RawMessage` fix is a genuine latent bug that blocked the plan's PATCH cred-gate from working end-to-end; the test rename is the mechanical corollary of removing the skip stub.

## Issues Encountered

- **`net/url` handling of oci:// scheme, verified experimentally.** `url.Parse("oci://registry-1.docker.io/bitnamicharts/nginx")` returns `scheme=oci, host=registry-1.docker.io, path=/bitnamicharts/nginx` — no error. The existing cred-host-mismatch check in `sync_handler.Handle` therefore works for oci:// upstreams without modification, because `pl.UpstreamURL` for a helm mirror remains the index.yaml URL (https) even when individual entries inside that index are oci://. The case where `pl.UpstreamURL` itself is oci:// is out of scope for this plan (ParseUpstream would try to fetch `oci://.../index.yaml` which fails at the http.Client boundary).
- **Go's encoding/json and \*json.RawMessage pointer collapse (documented as Rule 1 fix above).**
- **No other issues encountered.** Every test wrote cleanly on first run; the FakeClient's shallow-copy-on-return semantics (from 11-01) worked as advertised for the tag-rebound scenario where the same ref maps to two successive PullResults across sync runs.

## User Setup Required

None — in-repo code + tests only. No external service configuration, no new credentials, no new env vars. The Docker Hub cred-gate consumes existing `UpstreamCreds` rows created via the existing `/api/v1/projects/{name}/upstream-creds` endpoints.

## Self-Check

- [x] `internal/protocol/helm/sync_oci_integration_test.go` exists — VERIFIED
- [x] `internal/api/repos_docker_hub_gate_test.go` exists — VERIFIED
- [x] Commit `ff66a61` exists (Task 1) — VERIFIED
- [x] Commit `3448726` exists (Task 2 RED) — VERIFIED
- [x] Commit `d1cf39d` exists (Task 2 GREEN) — VERIFIED
- [x] Commit `c44b858` exists (Task 3 RED) — VERIFIED
- [x] Commit `baac7c0` exists (Task 3 GREEN) — VERIFIED
- [x] `grep skippedOCI++ internal/protocol/helm/sync_handler.go` returns nothing — VERIFIED
- [x] `grep fetchAndCommitOCI internal/protocol/helm/sync_handler.go` returns 5 matches (docstring + dispatch + method decl + comment refs) — VERIFIED
- [x] `grep oci_tag_rebound internal/protocol/helm/sync_handler.go` returns 5 matches — VERIFIED
- [x] `grep refuseDockerHubWithoutCred internal/api/admin_phase1.go` OK — VERIFIED
- [x] `grep refuseDockerHubWithoutCred internal/api/repos.go` OK — VERIFIED
- [x] `go build ./...` clean — VERIFIED
- [x] `go vet ./internal/protocol/helm/... ./internal/api/... ./internal/app/... ./internal/audit/... ./internal/metadata/...` clean — VERIFIED
- [x] `go test ./internal/protocol/helm/... ./internal/api/... ./internal/metadata/...` green (6 helm OCI tests + 5 cred-gate tests + all regressions) — VERIFIED
- [x] `go test ./...` green across entire codebase (36+ packages) — VERIFIED

## Self-Check: PASSED

## TDD Gate Compliance

Tasks 2 and 3 both followed the RED → GREEN cycle with atomic commits:
- Task 2: `test(helm)` (`3448726`) → `feat(helm)` (`d1cf39d`) ✓
- Task 3: `test(api)` (`c44b858`) → `feat(api)` (`baac7c0`) ✓

Task 1 is a straight wiring task without `tdd="true"` per plan frontmatter — committed as a single feat commit matching plan conventions.

No REFACTOR commits — the GREEN state for both TDD tasks was clean on first implementation (inline docstrings documenting invariants; helpers extracted where duplication would have appeared; `parseOCIRef` + `recordingAudit` carved out at implementation time).

## Next Phase Readiness

- **Plan 11-04 (Bitnami live OCI E2E) unblocked.** `fetchAndCommitOCI` is wired end-to-end; `ociclient.New(httpClient)` is populated at boot; the cred-gate forces Bitnami syncs to attach a basic cred, which exactly matches the D-16 / OCIHELM-08 fixture. The live E2E build tag (`-tags=live-oci`) can drive the SyncHandler against `oci://registry-1.docker.io/bitnamicharts/nginx:*` with a Docker Hub PAT.
- **Plan 11-05 / 11-06 (GITMIRROR) unblocked from the shared-surface side.** The PATCH handler's effective-state cred-gate pattern and the `writeEnvelope(*httperr.Error)` shape are both ready for reuse when Git mirror gains similar invariants (e.g. SSH-only upstreams require a ssh-key cred kind in v1.5+).
- **TestEveryStateChangingActionEmitsEvent coverage.** Plan 11-02 added `EvtOciTagRebound` to the test's kinds slice for sink-level round-trip coverage. Plan 11-03 is the production-emission wiring; the audit emit at `fetchAndCommitOCI` now fires the event under real conditions (asserted by `TestOCISync_TagRebound`). The test TEstEveryStateChangingActionEmitsEvent still validates the round-trip (test kinds → sink); production emission is covered by the integration test in this plan.

## Threat Flags

None new. The plan's threat register (T-11-03-01 tag-rebound CVE coverage, T-11-03-02 Docker Hub rate-limit exhaustion, T-11-03-04 digest mismatch, T-11-03-05 upstream error credential leakage) is fully mitigated by:

- **T-11-03-01:** `fetchAndCommitOCI` detects digest change → Trash.Move + audit → Insert of new row via UPSERT semantics (`ON CONFLICT(repo_id, name, version) DO UPDATE`), guaranteeing at most one active row per (repo_id, name, version) (INV-11-03-01).
- **T-11-03-02:** Docker Hub cred-gate at POST + PATCH refuses creation/un-credentialing without a basic cred (INV-11-03-04).
- **T-11-03-04:** Two-stage digest compare (manifest + chart-layer) before commit.
- **T-11-03-05:** `httpx.SanitizeUpstreamErr` wraps both Resolve and PullChart errors before they surface to the job log / UI.

Scope-bounded surface: `fetchAndCommitOCI`'s path construction is `filepath.Join(RepoRoot, projectName, "helm", repo.Name, "charts", filename)` where `filename = <chartName>-<chartVersion>.tgz` derived from parseOCIRef; never from the upstream URL. Satisfies INV-11-03-03.

No new `threat_flag:` entries — the surface is confined to the helm sync path which is already modeled.

---
*Phase: 11-mirror-infrastructure-widening*
*Completed: 2026-04-22*
