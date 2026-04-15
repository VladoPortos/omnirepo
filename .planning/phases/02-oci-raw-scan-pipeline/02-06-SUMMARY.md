---
phase: 02-oci-raw-scan-pipeline
plan: 06
subsystem: protocol (oci)
tags: [oci, registry, chunked-upload, mount, cas, refcount, gc-race, scan-12]
requires:
  - internal/storage/cas.go (Phase 1)
  - internal/metadata.DockerBlobsRepo (02-01)
  - internal/metadata.BlobUploadsRepo (Phase 1 stub)
  - internal/metadata.BlobUploadSessionsRepo (02-01)
  - internal/metadata.MembersRepo (Phase 1)
  - internal/metadata.ReposRepo.FindByTriple (Phase 1)
  - internal/auth.Can / ActorFromContext / ActionRepoRead / ActionUpdateRepo
  - internal/audit.Logger (Phase 1)
  - internal/protocol/oci.Handler skeleton (02-05)
provides:
  - internal/storage.CAS.PutFromPath (atomic tmp -> CAS rename)
  - internal/protocol/oci.Handler.blobPostDispatch / blobUploadPost / blobUploadPatch
      / blobUploadPut / blobUploadStatus / blobGet / blobHead / blobDelete
  - internal/protocol/oci.blobMount (POST ?mount=&from=)
  - internal/audit.EvtOCIBlobUploaded / EvtOCIBlobMounted / EvtOCIBlobDeleted
  - oci.Deps.CAS / Blobs / BlobUploads / Sess / Audit / DataRoot / Members
      / ChunkMaxBytes / SessionMaxBytes
affects:
  - internal/protocol/oci/handler.go (Deps + Handler extended; routes registered)
  - internal/app/app.go (oci.New wired with new deps)
  - internal/audit/events.go (3 new EventKind constants)
  - internal/audit/audit_test.go (TestEveryStateChangingActionEmitsEvent extended)
tech-stack:
  added:
    - github.com/google/uuid (already in go.mod via other deps; first direct import in oci/)
  patterns:
    - "SCAN-12 ordering: blobUploads.Start ALWAYS precedes cas.PutFromPath in finalize"
    - "O_APPEND|O_WRONLY streaming: chunk bytes never buffered in RAM"
    - "http.ServeContent for blob GET — stdlib handles Range/If-Range correctly"
    - "Monolithic POST reuses PATCH+PUT body shape; no code duplication"
    - "Mount falls back to normal upload when blob absent — OCI spec compliant"
key-files:
  created:
    - internal/protocol/oci/blobs.go
    - internal/protocol/oci/blobs_test.go
    - internal/protocol/oci/mount.go
  modified:
    - internal/storage/cas.go (PutFromPath added + interface extended)
    - internal/storage/cas_test.go (3 new tests)
    - internal/protocol/oci/handler.go (Deps/Handler/Mount extended)
    - internal/app/app.go (oci.New wiring)
    - internal/audit/events.go (3 new EventKind constants)
    - internal/audit/audit_test.go (3 new kinds in enumeration test)
decisions:
  - "Per-chunk cap default 64 MiB, per-session cap default 10 GiB (matches plan)."
  - "Chunk cap enforced via io.LimitReader(body, cap+1); overflow truncates the
     tmp file back to its pre-chunk length so a rejected 413 leaves the session
     in a clean resumable state."
  - "Mount falls back to blobUploadPost on missing-blob per OCI spec §4.2.2
     rather than returning an error; clients then chunk-upload normally."
  - "Blob DELETE guarded by ActionUpdateRepo (write intent) not a new
     ActionDeleteBlob constant — one fewer action to enumerate; the only
     relevant safety property is ref_count==0, enforced directly."
  - "Cross-repo mount accepts shorthand from=<project>/<repo> (type inferred
     docker) in addition to the canonical three-segment form, matching what
     docker CLI / crane send."
  - "blob GET duplicates the CAS on-disk layout (dataRoot/blobs/sha256/...)
     in oci.casFilePath rather than adding a CAS.OpenReadSeeker API. CAS.Get
     returns io.ReadCloser (no Seek), which http.ServeContent cannot range
     over — the coupling is acceptable and reviewed in SUMMARY.md."
metrics:
  duration: ~35m
  tasks: 2
  files: 3 created + 6 modified
  completed: 2026-04-15
requirements_complete:
  - OCI-03
  - OCI-04
  - OCI-07
  - SCAN-12
---

# Phase 2 Plan 06: OCI Blob State Machine + Mount + GET/HEAD/DELETE — Summary

Hand-rolled /v2 blob surface: chunked + monolithic upload, cross-repo mount,
range-aware GET, HEAD, and ref-count-guarded DELETE. All seven routes register
inside the guarded /v2 group from 02-05 so AnonymousReadOK + VerifyBearer run
on every path. SCAN-12 race closed by the plan's mandated ordering:
`blob_uploads.Start(digest)` fires BEFORE `cas.PutFromPath` on every finalize
path (chunked PUT and monolithic POST).

## Final per-chunk + per-session size cap defaults

- **Per chunk:** 64 MiB (`Deps.ChunkMaxBytes`, zero = default).
- **Per session:** 10 GiB (`Deps.SessionMaxBytes`, zero = default).
- **Enforcement:** `io.LimitReader(body, cap+1)`. If the resulting byte count
  exceeds `cap`, the handler truncates the tmp file back to the session's
  pre-chunk length (`os.Truncate`) so the session is resumable, then emits
  `413 SIZE_INVALID`.
- **Why `+1`:** Clients may send `Transfer-Encoding: chunked` bodies with no
  advance Content-Length. Reading cap+1 bytes lets us detect overflow without
  a pre-flight header check.

## Test approach for SCAN-12 race (channel-based coordination)

`TestBlobUploadSurvivesConcurrentGC` exercises the harder interleaving: a
simulated GC worker takes its `blob_uploads` snapshot BEFORE the PUT runs,
waits for the PUT to finish, then enumerates GC candidates and asserts the
just-promoted digest is EXCLUDED by the snapshot.

Coordination:

```
main goroutine                   gc goroutine
--------------------             -----------------------------------
POST + PATCH upload              (blocked on chan-start)
                                 rows <- blob_uploads (snapshot = {})
                                 close(gcStarted)
<-gcStarted                      <-putDone
PUT  →  blobUploads.Start(d)     (blocked)
     →  cas.PutFromPath()
     →  writer tx UpsertZeroRef
close(putDone)                   (resumes)
                                 cands <- GCCandidates(0)
                                 for c in cands:
                                   if c.digest == d: fail
<-gcResult                       gcResult <- nil
```

The `GCCandidates(0)` call uses zero quiescence so a just-promoted blob with
`ref_count=0` IS in the candidate set. The only thing keeping it safe is the
`blob_uploads` exclusion contract. If the plan's ordering were violated
(`Start` after `PutFromPath`), the snapshot would miss the digest and the
test would fail with a deterministic error message.

The test also greps the `blobs.go` source file to assert the textual
ordering (`h.blobUploads.Start` offset < `h.cas.PutFromPath(r.Context(), tmpPath)`
offset), a belt-and-braces guard against refactors that move the calls.

## Deviations from RESEARCH.md Pattern 1 ordering

None. The implementation follows Pattern 1 verbatim in `blobUploadPut`:

1. Read the session row from `blob_upload_sessions` (repo scoping check).
2. Validate claim `sha256:<hex>` format (`validDigest`).
3. Append the final (possibly empty) chunk via `appendChunk` (O_APPEND).
4. Enforce per-chunk cap → 413 with tmp-truncate on overflow.
5. Hash the FULL tmp file (`sha256OfFile`).
6. If actual != claim → 400 DIGEST_INVALID.
7. **`h.blobUploads.Start(actual, 1h)` — SCAN-12 gate.**
8. **`h.cas.PutFromPath(tmpPath)` — atomic rename.**
9. Writer tx: `docker_blobs.UpsertZeroRef + Touch + sess.Delete`.
10. Best-effort audit `oci.blob.uploaded` AFTER tx commit (OQ-9 semantics).

The monolithic POST path (`blobMonolithicPost`) follows the same ordering but
skips the session row since no PATCH is involved. Both paths use
`cas.PutFromPath` so the tmp file is never io.Copy'd a second time.

## Auto-fixed / shape decisions

### 1. [Rule 2 — Correctness] Three-segment `{project}/{type}/{repo}` URL, not two

The plan's action block sketched `parseRepoPath(repoPath)` returning
`(project, repoName, ok)` — implying the URL is `/v2/<project>/<repo>/...`.
The must_haves, however, mandate **"chi URL param 'name' resolves to
(project, docker, repo)"** — three segments.

02-05 already shipped `extractRepoFromV2URL` with the three-segment
convention (project/type/repo), and the AnonymousReadOK substrate depends
on it. Diverging here would have broken public-repo anonymous reads for
blob GET.

The handler therefore uses three chi URL params (`{project}`, `{type}`,
`{repo}`) and `resolveRepo` rejects non-docker types with NAME_INVALID.
Mount's `from=` accepts both the three-segment and two-segment shorthand
(inferring docker) so OCI clients that follow the /v2/<name>/blobs/<digest>
convention still interoperate.

### 2. [Rule 2 — Correctness] Blob GET uses direct `os.Open` on a rebuilt CAS path

`http.ServeContent` requires an `io.ReadSeeker`. The existing CAS API
returns `io.ReadCloser` (no `Seek`). Rather than widening the CAS
interface, `blobGet` rebuilds the canonical CAS file path (
`<dataRoot>/blobs/sha256/<xx>/<hex>`) in `h.casFilePath` and opens it
directly. This couples the handler to the storage layout but has two
upsides:

- No CAS API change affecting other subsystems.
- `http.ServeContent` can stat the file and honor `If-Modified-Since`.

If the CAS layout ever changes, this helper plus `storage.cas.blobPath`
move in lockstep — both live in the same module.

### 3. [Rule 3 — Blocking] `google/uuid` import was transitive, now direct

`google/uuid` was already in `go.mod` (transitive). This plan is the first
direct consumer inside `internal/protocol/oci/`. No `go get` or vendoring
change was needed.

## Audit event emission strategy

All state-changing handlers call `Handler.emitAudit` AFTER the writer tx
commits, not inside. Rationale (OQ-9 from Phase 1):

- Audit.Record writes the DB row AND best-effort NDJSON mirror.
- If NDJSON write fails, the handler returns success — which would be
  misleading if audit.Record were part of the tx.
- If DB insert fails, we want the upload to succeed and the audit failure
  to surface in logs, not to roll back the blob promotion.

Raw handler uses the same pattern (`put.go:100`); this plan follows
suit for consistency.

## Test Evidence

`go test -mod=vendor -race -count=1 ./internal/protocol/oci/... ./internal/storage/... ./internal/audit/... ./internal/app/...` — all green.

Full-repo `go test -mod=vendor -race -count=1 ./...` — every package green
(internal/jobs flake from 02-05 did not reproduce on three consecutive runs
with these changes).

Targeted coverage (12 tests + 3 CAS tests):

- **storage:** `TestCASPutFromPath_PromoteTmpFile`, `TestCASPutFromPath_Idempotent`,
  `TestCASPutFromPath_MissingSource`.
- **oci blobs:** `TestBlobUploadChunked_HappyPath` (POST→PATCH→PUT→GET with
  header assertions), `TestBlobUploadMonolithic_HappyPath`, `TestBlobRangeGET`
  (Range 10-20 returns 206), `TestBlobHEAD`, `TestBlobDelete_RefCountZero_AllowsDelete`,
  `TestBlobDelete_WhenReferenced_405`, `TestBlobUpload_DigestMismatch` (400),
  `TestBlobUpload_OversizedChunk` (16-byte cap → 413), `TestBlobUpload_UnknownRepo`
  (404 NAME_UNKNOWN), `TestBlobUpload_NonDockerTypeRejected` (400 NAME_INVALID),
  `TestBlobUpload_ForbiddenActor` (stranger → 403 DENIED).
- **mount:** `TestBlobMount_CrossRepoSameProject`, `TestBlobMount_FallbackWhenBlobMissing`.
- **race:** `TestBlobUploadSurvivesConcurrentGC` (SCAN-12 regression gate).

## Threat model coverage

| Threat | Status | Evidence |
|--------|--------|----------|
| T-02-06-01 digest mismatch | mitigated | `sha256OfFile` over full tmp file; claim compared in PUT; `TestBlobUpload_DigestMismatch`. |
| T-02-06-02 memory exhaustion via huge chunk | mitigated | `io.LimitReader(body, cap+1)`; body streamed straight to disk via `O_APPEND`; `TestBlobUpload_OversizedChunk`. |
| T-02-06-03 disk exhaustion via huge session | mitigated | Per-session cap tracked via `sessions.bytes_so_far + n`; 413 on over-cap; tmp file truncated back to pre-chunk length. |
| T-02-06-04 GC/in-flight PUT race | mitigated | `blobUploads.Start` BEFORE `cas.PutFromPath` in chunked PUT (343<350) and monolithic (443<450); `TestBlobUploadSurvivesConcurrentGC` proves the exclusion set contract. |
| T-02-06-05 cross-repo write via crafted name | mitigated | `auth.ProjectNameValid` on `{project}`; repo type forced to `docker`; `FindByTriple` gate. |
| T-02-06-06 DELETE blob with active refs | mitigated | `blobs.Stat.RefCount > 0` → 405; `TestBlobDelete_WhenReferenced_405`. |
| T-02-06-07 cross-repo mount reads foreign repo | mitigated | `canOnRepo(actor, ActionRepoRead, src)` AND `canOnRepo(actor, ActionUpdateRepo, dst)` BOTH required. |
| T-02-06-08 Range-GET past EOF | accept | stdlib `http.ServeContent` handles Range bounds correctly. |
| T-02-06-09 anonymous write attempt | mitigated | `requireWriter` 401s anonymous actors; AnonymousReadOK only attaches for GET/HEAD + public_read. |

## Commits

| Hash    | Subject |
|---------|---------|
| 43f7674 | test(02-06): add failing tests for CAS.PutFromPath (RED) |
| ca8f9e1 | feat(02-06): CAS.PutFromPath for zero-copy upload promotion |
| bec7935 | feat(02-06): OCI blob state machine + mount + GET/HEAD/DELETE (D-03, OCI-03/04/07, SCAN-12) |

## Self-Check: PASSED

- internal/storage/cas.go → `PutFromPath` present (FOUND)
- internal/storage/cas_test.go → 3 new tests (FOUND)
- internal/protocol/oci/blobs.go → 6 handler methods + helpers (FOUND)
- internal/protocol/oci/blobs_test.go → 12 tests (FOUND)
- internal/protocol/oci/mount.go → `blobMount` + `parseFromRepo` (FOUND)
- internal/audit/events.go → 3 new EventKind constants (FOUND)
- Commits 43f7674, ca8f9e1, bec7935 → FOUND in `git log --oneline`
- `go build -mod=vendor ./...` → exit 0
- `go test -mod=vendor -race -count=1 ./...` → all packages green
