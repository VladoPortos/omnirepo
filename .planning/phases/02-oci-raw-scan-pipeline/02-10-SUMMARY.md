---
phase: 02-oci-raw-scan-pipeline
plan: 10
subsystem: protocol (oci) + api + jobs
tags: [oci, pull-external, promote, retag, sync-pool, go-containerregistry, byte-identical, zero-blob-copy]
requires:
  - internal/protocol/oci.Handler skeleton + /v2 surface (02-05/06/07)
  - internal/metadata.UpstreamCredsRepo (02-02)
  - internal/metadata.SyncJobsRepo (02-01)
  - internal/jobs.SyncPool (02-04)
  - internal/protocol/oci.writeManifestWithRefcounts (02-07, refactored here)
provides:
  - internal/protocol/oci.PullExternalHandler + PullExternalJob + PullExternalJobKind
  - internal/protocol/oci.PullExternalREST
  - internal/protocol/oci.PromoteREST + PromoteRequest + PromoteResponse
  - internal/protocol/oci.SanitizeUpstreamErrExported (test hook)
  - internal/protocol/oci.(*Handler).writeManifestWithRefcounts (extracted from manifestPut)
  - internal/api.OCIActionsDeps + RegisterOCIActionsRoutes
  - audit.EvtOCIPullExternalStarted/Finished/Failed, audit.EvtOCIPromote
affects:
  - internal/protocol/oci/manifests.go (manifestPut tx body extracted into writeManifestWithRefcounts)
  - internal/api/admin_phase1.go (Deps.OCIActions + RegisterOCIActionsRoutes call)
  - internal/app/app.go (construct oci.Handler BEFORE api.Mount; register pull_external sync-handler; move pool.Run after api.Mount)
  - internal/audit/events.go (4 new EventKinds)
  - go.mod / go.sum / vendor (github.com/google/go-containerregistry v0.21.5 pinned)
tech-stack:
  added:
    - github.com/google/go-containerregistry v0.21.5 (pkg/authn, pkg/name, pkg/v1, pkg/v1/remote)
  patterns:
    - "Shared tx helper: writeManifestWithRefcounts owns the single-tx body for manifest PUT / pull-external / promote. Identical refcount + FTS + auto-scan semantics across all three paths."
    - "Byte-identical manifest roundtrip (Pitfall 5): pull-external stores img.RawManifest() verbatim; promote copies the src manifest BLOB."
    - "cred_host_mismatch enforced at BOTH REST (synchronous 400) AND job (returns error → MarkFailed retry). Defense in depth for D-12."
    - "Authorization: header scrub via regexp.ReplaceAllString applied to any error that lands in sync_jobs.last_error (T-02-10-02)."
    - "Sync pool handler registered BEFORE go pool.Run(ctx). App.Run was reordered: api.Mount now follows ociHandler construction so OCIActionsDeps can carry handlers built against the same *oci.Handler."
key-files:
  created:
    - internal/protocol/oci/pull_external.go
    - internal/protocol/oci/pull_external_test.go
    - internal/protocol/oci/promote.go
    - internal/protocol/oci/promote_test.go
    - internal/api/oci_actions.go
  modified:
    - internal/protocol/oci/manifests.go
    - internal/api/admin_phase1.go
    - internal/app/app.go
    - internal/audit/events.go
    - go.mod / go.sum / vendor/modules.txt
decisions:
  - "Refactored 02-07 manifestPut writer-tx body into Handler.writeManifestWithRefcounts BEFORE writing pull-external/promote. The helper accepts (repoID, repoPath, reference, mfDigest, mediaType, body, refs, isIndex, autoScan) and returns scanEnqueued. manifestPut now calls it; promote calls it; pull-external calls it. Single source of truth for ref-delta on tag overwrite (Pitfall 1), FTS index upkeep (D-40), and auto-scan enqueue (SCAN-01)."
  - "Pull-external index (manifest-list) handling: walks children first, commits each child manifest with reference=\"\" (so the helper skips tag upsert), then commits the index body itself with the user-supplied dst_tag. writeManifestWithRefcounts.incRefs for index bodies uses docker_manifests.IncRef on child digests — so children MUST exist in dst repo before the index commit. Matches Pitfall 1 discipline."
  - "Pull-external inline creds (src_username/src_password) stored cleartext in sync_jobs.payload_json for v1. Acceptable because: the row is hard-deleted when the pool flips status='done'; operators who want at-rest encryption use cred_id instead; AEAD would require a writer-tx for every enqueue (D-13 stored creds already have one). Documented as a v1 simplification in the plan."
  - "sanitizeUpstreamErr returns errors.New(scrubbed), deliberately dropping the original wrap chain. Rationale: go-containerregistry errors sometimes retain the credential bytes inside %w-wrapped inner structs. Flattening to a plain errors.New guarantees the wrap chain cannot re-expose the header via errors.Unwrap."
  - "Sync-pool handler registration moved AFTER api.Mount: api.Mount depends on ociHandler (for OCIActionsDeps), ociHandler must exist before syncHandlers[pull_external] can be set, and go syncPool.Run must run AFTER the map is populated. Three ordering constraints satisfied by rearranging app.go into: construct pools → construct ociHandler → register sync handlers → api.Mount (passes OCIActionsDeps) → go pool.Run."
  - "Per-job timeout default 30 min (DefaultPullExternalTimeout). Enforced via context.WithTimeout inside PullExternalHandler.Handle. Configurable via PullExternalDeps.Timeout."
  - "Test upstream registry is hand-rolled (~60 LOC httptest.Server) rather than vendoring pkg/registry. pkg/registry is not vendored and (per 02-RESEARCH Critical Finding 1) has architectural misfits for OmniRepo's use. The hand-rolled mock serves /v2/ ping, /v2/<name>/manifests/<ref>, and /v2/<name>/blobs/<digest> — sufficient for pkg/v1/remote pull."
metrics:
  duration: ~60m
  tasks: 2
  files: 5 created + 5 modified
  completed: 2026-04-15
requirements_complete:
  - OCI-08
  - OCI-09
  - SYNC-01
  - SYNC-02
---

# Phase 2 Plan 10: OCI Pull-External + Promote Summary

Pull-external (OCI-08) imports an upstream image into a local Docker repo with optional retag; the job runs on the sync pool, uses pkg/v1/remote with optional Basic (via stored cred_id or inline), streams layers+config into CAS, and commits the manifest body byte-for-byte (Pitfall 5). Promote (OCI-09) performs zero-blob-copy retag between any two local Docker repos in one writer tx, verified by CAS file-count delta and per-blob ref_count delta tests. Both paths share `writeManifestWithRefcounts`, extracted from 02-07's `manifestPut`, keeping ref-delta + FTS + auto-scan semantics identical across manifest PUT / pull-external / promote.

## Final go-containerregistry version pinned

`github.com/google/go-containerregistry v0.21.5`. Vendored. Import subpaths used:
- `pkg/authn` — `authn.Basic`, `authn.Bearer`
- `pkg/name` — `name.ParseReference`, `name.Tag`, `name.Digest`, `(Reference).Context().RegistryStr()`
- `pkg/v1` — `v1.Image`, `v1.Layer`
- `pkg/v1/remote` — `remote.Get`, `remote.WithContext`, `remote.WithAuth`

`pkg/registry` is explicitly NOT imported (per 02-RESEARCH Critical Findings — it has architectural misfits for OmniRepo's CAS-backed model).

## Mock-upstream test approach

Hand-rolled httptest.Server (~60 LOC in pull_external_test.go) implementing the minimum /v2 surface pkg/v1/remote requires:

- `GET /v2/` → 200 with `Docker-Distribution-API-Version: registry/2.0`
- `GET /v2/<name>/manifests/<ref>` → 200 with stored body + media type + `Docker-Content-Digest`
- `GET /v2/<name>/blobs/<digest>` → 200 with stored bytes
- HEAD variants for both

Optional Basic-auth gate (configured per-fixture). Tests exercise:

1. `TestPullExternal_Anonymous_ImportsManifestByteIdentical` — anonymous pull, verifies dst manifest body is byte-equal to upstream.
2. `TestPullExternal_BasicAuth_ImportsViaCred` — requires Basic; cred stored via UpstreamCredsRepo; Lookup used at Handle time.
3. `TestPullExternal_CredHostMismatch_JobReturnsError` — cred.host ≠ src.host → Handle returns error containing `cred_host_mismatch`.

REST layer is tested by mounting the handler on a minimal chi router and attaching actors + membership via `auth.WithActor` / `auth.WithProjectMembership` directly on ctx (identical to the approach other Phase 02 REST tests use).

## sanitizeUpstreamErr regex + scrubbing strategy

```go
var authRegex = regexp.MustCompile(`(?i)Authorization:\s*[^\r\n"']*`)

func sanitizeUpstreamErr(err error) error {
    if err == nil { return nil }
    scrubbed := authRegex.ReplaceAllString(err.Error(), "Authorization: REDACTED")
    return errors.New(scrubbed)
}
```

Case-insensitive; matches `Authorization:` followed by any whitespace and any non-delimiter bytes until EOL / quote. Returns a plain `errors.New` (drops the wrap chain) so nested wrapped errors in pkg/v1/remote cannot re-surface the credential via `errors.Unwrap`.

`TestSanitizeUpstreamErr_ScrubsAuthHeader` exercises this with a fabricated input containing `Authorization: Basic YWxpY2U6c2VrcmV0` and asserts the base64 creds are absent AND `REDACTED` marker is present.

## Per-job timeout chosen

**30 minutes** (`DefaultPullExternalTimeout`), enforced via `context.WithTimeout` at the start of `PullExternalHandler.Handle`. Matches the spec §18 "long multi-GiB layer pull over a slow link" upper bound. Configurable via `PullExternalDeps.Timeout`.

## Test Evidence

All 11 new tests + full OCI suite pass under `-race`:

```
go test -mod=vendor -race -count=1 ./internal/protocol/oci/... 2>&1 | tail
--- PASS: TestPromote_ZeroBlobCopy (0.11s)
--- PASS: TestPromote_NonMember_Returns403 (0.06s)
--- PASS: TestPromote_SrcTagNotFound_Returns404 (0.06s)
--- PASS: TestSanitizeUpstreamErr_ScrubsAuthHeader (0.00s)
--- PASS: TestPullExternal_Anonymous_ImportsManifestByteIdentical (0.07s)
--- PASS: TestPullExternal_BasicAuth_ImportsViaCred (0.06s)
--- PASS: TestPullExternal_CredHostMismatch_JobReturnsError (0.05s)
--- PASS: TestPullExternalREST_HappyPath_Enqueues202 (0.06s)
--- PASS: TestPullExternalREST_CredHostMismatch_Returns400 (0.06s)
--- PASS: TestPullExternalREST_Unauthenticated_Returns401 (0.06s)
--- PASS: TestPullExternalREST_NonMember_Returns403 (0.08s)
ok  github.com/dxc-internal/omnirepo/internal/protocol/oci (with race)  18.9s
```

Full suite under `-race`: every package green EXCEPT the pre-existing flake `internal/jobs/TestPool_NoHandlerMarksFailed` (documented in 02-05, 02-08, 02-09 SUMMARY — reproduces on pristine HEAD before this plan; not introduced here).

Acceptance criteria verification:

- `grep 'go-containerregistry' go.mod` → `github.com/google/go-containerregistry v0.21.5` ✓
- `grep -E 'remote\.Image|remote\.Get|authn\.Basic|name\.ParseReference' internal/protocol/oci/pull_external.go` → all present ✓
- `grep 'cred_host_mismatch' internal/protocol/oci/pull_external.go internal/protocol/oci/pull_external_test.go` → both ✓
- Byte-identical roundtrip: `TestPullExternal_Anonymous_ImportsManifestByteIdentical` PASS ✓
- CAS file count delta == 0 on promote: `TestPromote_ZeroBlobCopy` PASS ✓
- Per-blob ref_count incremented by exactly 1: asserted inside `TestPromote_ZeroBlobCopy` ✓
- `grep -E 'cas\.Put\(|cas\.Copy' internal/protocol/oci/promote.go` → 0 matches ✓
- Cross-project non-member → 403: `TestPromote_NonMember_Returns403` PASS ✓
- Audit oci.promote emitted: asserted in `TestPromote_ZeroBlobCopy` ✓

## Threat model compliance

| Threat | Status | Evidence |
|--------|--------|----------|
| T-02-10-01 SSRF via pull-external | mitigated | cred_host_mismatch enforced (REST synchronous + job async); anonymous pull still user-initiated; audit oci.pull_external.started records every invocation. |
| T-02-10-02 upstream creds in logs | mitigated | sanitizeUpstreamErr regex + TestSanitizeUpstreamErr_ScrubsAuthHeader. Error passes through errors.New (wrap chain dropped). |
| T-02-10-03 manifest tampering | mitigated | img.RawManifest() stored byte-for-byte; TestPullExternal_Anonymous_ImportsManifestByteIdentical asserts. |
| T-02-10-04 disk fill via huge image | accepted for v1 | CAS.Put writes to disk; no per-job byte cap in v1 (documented). |
| T-02-10-05 promote across projects without dst membership | mitigated | canOnRepo for ActionRepoRead (src) AND ActionUpdateRepo (dst); TestPromote_NonMember_Returns403. |
| T-02-10-06 promote tag overwrite without ref delta | mitigated | writeManifestWithRefcounts handles prior-digest ref decrement; same code path as 02-07 manifestPut. |
| T-02-10-07 UpstreamCredsRepo.Lookup logs plaintext | mitigated | Already verified in 02-02; no format-string logging of password/token in pull-external either (grep confirms). |
| T-02-10-08 redirect-host following | accepted for v1 | pkg/v1/remote default redirect behavior; documented as v1.1 allowlist candidate. |
| T-02-10-09 job hangs on slow remote | mitigated | context.WithTimeout(30m) in Handle; pool ctx cancel on shutdown also cancels. |

## Deviations from plan

### Auto-fixed

**1. [Rule 3 — Blocking] writeJSONErr name collision with cosign.go**

- Found: after first build of pull_external.go + promote.go.
- Issue: cosign.go already exports package-private `writeJSONErr(w, status, msg)` (3-arg signature). My helper had 4 args.
- Fix: renamed to `writeActionErr` / `writeActionOK` in pull_external.go; sed-renamed call sites in promote.go.
- Files: `internal/protocol/oci/pull_external.go`, `internal/protocol/oci/promote.go`.
- Commit: 7654a4d (Task 1) and 35513ef (Task 2 — promote.go inherits the renamed helpers since they live in the same package).

**2. [Rule 3 — Ordering] Sync pool Run started BEFORE pull_external handler registered**

- Found: reviewing the app.Run sequence after adding pull-external.
- Issue: `go syncPool.Run(ctx)` sat at line 282 BEFORE `ociHandler` was constructed (line 302) and BEFORE `syncHandlers[oci.PullExternalJobKind] = ...` (line 352). The dispatcher could race the map mutation.
- Fix: moved `go syncPool.Run(ctx)` and `go scanPool.Run(ctx)` to AFTER api.Mount at the end of the setup sequence. Added an explanatory comment at the prior location. Both pool dispatchers are goroutines and do not read handlers until their first poll/kick, so the ordering is now safe.
- Files: `internal/app/app.go`.
- Commit: 7654a4d.

**3. [Rule 2 — Correctness] api.Mount called BEFORE ociHandler construction originally**

- Found: when wiring OCIActionsDeps.
- Issue: api.Mount needs `OCIActionsDeps` which in turn references `*oci.Handler` (for handler methods). Original app.Run constructed ociHandler AFTER api.Mount.
- Fix: reordered — api.Mount now follows ociHandler construction + pull-external/promote REST wiring. The chi router is shared so mount-order among handlers doesn't matter; only construction-order for dep structs matters.
- Files: `internal/app/app.go`.
- Commit: 7654a4d.

### No deviations from D-04, D-05, D-12, D-13

- D-04: pull-external uses pkg/v1/remote with authn.Basic; runs on sync pool (PullExternalJobKind registered on syncHandlers map). ✓
- D-05: promote INSERTs manifest in dst repo + IncRefs per-blob in one WriteTx; zero blob copy asserted by CAS file-count delta test. ✓
- D-12: cred_host_mismatch check enforced both at REST (synchronous 400) and in Handle (returns error, job MarkFailed retries — but the REST check means the common case never reaches the job). ✓
- D-13: upstream_cred.created/updated/deleted already emit in 02-02; upstream_cred.used now emits from pull-external at Handle time; oci.pull_external.started emits at REST enqueue; oci.pull_external.finished emits after successful commit; oci.promote emits from PromoteREST after tx commit. ✓

## Commits

| Hash | Subject |
|------|---------|
| 7654a4d | feat(02-10): OCI pull-external job + REST enqueue (D-04, D-12, D-13) |
| 35513ef | feat(02-10): OCI promote/retag (D-05, zero-blob-copy) |

## Self-Check: PASSED

- internal/protocol/oci/pull_external.go — FOUND
- internal/protocol/oci/pull_external_test.go — FOUND
- internal/protocol/oci/promote.go — FOUND
- internal/protocol/oci/promote_test.go — FOUND
- internal/api/oci_actions.go — FOUND
- Commits 7654a4d, 35513ef — FOUND in `git log --oneline`
- `go build -mod=vendor ./...` → exit 0
- `go test -mod=vendor -race -count=1 ./internal/protocol/oci/... ./internal/api/... ./internal/app/...` → all packages green
- Acceptance criteria (grep gates, ref_count asserts, CAS delta asserts, audit emission) verified in test output above
