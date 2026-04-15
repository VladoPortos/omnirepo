---
phase: 02-oci-raw-scan-pipeline
plan: 07
subsystem: protocol (oci)
tags: [oci, manifests, tags, catalog, cosign, fts5, refcount, pitfall-1, pitfall-5, pitfall-7]
requires:
  - internal/protocol/oci.Handler skeleton (02-05)
  - internal/protocol/oci blob state machine (02-06)
  - internal/metadata.DockerManifestsRepo / DockerTagsRepo / DockerBlobsRepo (02-01)
  - internal/metadata.ScansRepo.Enqueue (02-01 / 02-04)
  - internal/jobs.ScanPool.Kick (02-04)
provides:
  - internal/protocol/oci.Handler.manifestGet / manifestHead / manifestPut / manifestDelete
  - internal/protocol/oci.Handler.tagsList / tagDelete
  - internal/protocol/oci.Handler.catalog (anonymous + member + super-admin scoping)
  - internal/protocol/oci.Handler.cosignBadge + MountCosign
  - internal/protocol/oci.CosignTag (sha256:<hex> → sha256-<hex>.sig)
  - internal/protocol/oci.SeverityGateFn (no-op stub; 02-09 plugs real impl)
  - internal/protocol/oci.ManifestMaxBytes (4 MiB cap)
  - internal/metadata.DockerManifestsRepo.GetByDigestTx
  - internal/metadata.DockerTagsRepo.ListPaginated / ExistsTag / CountForDigest / CountForDigestTx
  - internal/metadata.ReposRepo.ListDockerCatalog + metadata.CatalogScope
  - audit.EvtOCIManifestUploaded / EvtOCIManifestDeleted / EvtOCITagDeleted
  - ErrCodeManifestInvalid / ErrCodeTagInvalid OCI error constants
affects:
  - internal/protocol/oci/handler.go (Deps + Handler extended, /_catalog mounted outside guarded group)
  - internal/protocol/oci/token_verify_test.go (swapped /_catalog → repo-scoped path for guarded-route tests)
  - internal/protocol/oci/handler_test.go (same swap + placeholder expectation update)
  - internal/app/app.go (Manifests/Tags/Scans/ScanKick wiring + MountCosign)

tech-stack:
  added: []
  patterns:
    - "Tx-scoped reads inside WriteTx (*Tx variants) — Reader-pool reads inside an in-flight writer tx deadlock under -race on WAL + single writer. Every repo with read helpers called from manifest-write paths grew a *Tx sibling."
    - "Manifest body stored byte-for-byte as BLOB; GET writes stored bytes verbatim (no re-marshal) so Docker-Content-Digest survives round-trip (Pitfall 5)."
    - "Ref-delta on tag overwrite runs inside the SAME writer tx that inserts the new manifest: resolve priorDigest, decrement refs on prior manifest's referenced blobs/child manifests, increment refs on the new manifest's (Pitfall 1)."
    - "SQLite: SELECT aliases are NOT referenceable in WHERE — inline the expression (ListDockerCatalog uses const pathExpr)."
    - "catalogAuth middleware: no-auth header → anonymous; auth header present but invalid → 401 challenge. Avoids the VerifyBearer binary 401 on a partially-public endpoint."

key-files:
  created:
    - internal/protocol/oci/manifests.go
    - internal/protocol/oci/manifests_test.go
    - internal/protocol/oci/tags.go
    - internal/protocol/oci/tags_test.go
    - internal/protocol/oci/catalog.go
    - internal/protocol/oci/catalog_test.go
    - internal/protocol/oci/cosign.go
    - internal/protocol/oci/cosign_test.go
  modified:
    - internal/audit/events.go (3 new EventKinds)
    - internal/metadata/docker_manifests.go (GetByDigestTx)
    - internal/metadata/docker_tags.go (ListPaginated, ExistsTag, CountForDigest*, CountForDigestTx)
    - internal/metadata/repos.go (ListDockerCatalog + CatalogScope)
    - internal/protocol/oci/handler.go (manifest/tags/catalog routes + SeverityGate + Manifests/Tags/Scans deps)
    - internal/protocol/oci/handler_test.go (route-swap + placeholder expectation)
    - internal/protocol/oci/oci_err.go (ErrCodeManifestInvalid/TagInvalid)
    - internal/protocol/oci/token_verify_test.go (route-swap to guarded manifest path)
    - internal/app/app.go (wire manifests/tags/scans + MountCosign)

key-decisions:
  - "/v2/_catalog is mounted OUTSIDE the guarded chi group behind a dedicated catalogAuth middleware. Anonymous requests are scoped to public_read repos; invalid Bearer still 401s. Moving catalog inside the guarded group would have required a second extractor branch inside AnonymousReadOK, which is already load-bearing for blobs and manifests."
  - "DockerManifestsRepo.GetByDigestTx added because Reader-pool reads INSIDE a WriteTx callback deadlock on WAL + writer-pool-size-1 under -race. Tx-scoped reads are the idiomatic fix and mirror the Phase 1 refcount-in-tx pattern."
  - "DockerTagsRepo.CountForDigestTx added for the same reason. Every repo helper called from manifest-write paths now has a *Tx sibling when needed."
  - "ReposRepo.ListDockerCatalog inlines the (project||/docker/||name) expression in both SELECT and WHERE rather than using an alias — SQLite does not allow alias references in WHERE."
  - "Cosign badge mounted on /api/v1 with BasicOrAPIKey auth (NOT Bearer). Clients already authenticated to /v2 re-use their Basic credentials; the badge is an OmniRepo-specific REST surface, not an OCI Distribution route."
  - "SeverityGate hook is a Handler-level SeverityGateFn field, not a constructor switch. 02-09 sets it; absence == always-allow, matching the plan's no-op-stub requirement."
  - "TDD approach collapsed to single feat commit per task given cross-cutting metadata/handler extensions. Tests written alongside implementation verify the critical gates (TestRefDeltaOnTagOverwrite, byte-identical roundtrip, 413 cap, catalog scoping, cosign derivation)."

patterns-established:
  - "Tx-scoped read helpers for any read that may be invoked inside a WriteTx callback (prevents Reader/Writer deadlock under -race)."
  - "Manifest body stored as BLOB; GET serves bytes verbatim via w.Write (no encoding/json re-marshal)."
  - "Catalog scoping done in SQL via JOIN + IN(?,?,...) rather than fetching everything and filtering in Go — hot path scales."
  - "Cosign = tag-presence; CosignTag(digest) is the exported derivation so tests and future REST handlers share one source of truth."

requirements-completed:
  - OCI-04
  - OCI-05
  - OCI-06
  - OCI-07
  - OCI-10
  - SRCH-01

duration: 55m
completed: 2026-04-15
---

# Phase 2 Plan 07: OCI Manifests + Tags + Catalog + Cosign Summary

OCI Distribution v1.1 manifest surface completed: byte-identical-body PUT/GET/HEAD, single-tx ref-delta on tag overwrite (Pitfall 1), cascading DELETE (Pitfall 7), cursor-paginated tags list, project-scoped /v2/_catalog with anonymous+member+super-admin semantics, and a tag-presence cosign badge wired at /api/v1. Brings the OCI surface to crane-drivable parity (minus pull-external + promote, which live in 02-10).

## Performance

- **Duration:** 55 min
- **Started:** 2026-04-15T10:27:39Z
- **Completed:** 2026-04-15T11:23:05Z
- **Tasks:** 2 (combined feat commits, TDD-style tests accompanying each)
- **Files modified:** 17 (8 created, 9 modified)

## Accomplishments

- Manifest PUT with 4 MiB cap, tag-overwrite ref-delta logic (Pitfall 1 gate `TestRefDeltaOnTagOverwrite` passes).
- Byte-identical round-trip: body bytes stored as BLOB, GET writes them verbatim (Pitfall 5).
- Tag-form + digest-form DELETE with single-tx ref decrements (Pitfall 7).
- Cursor-paginated tags/list with Link rel=next.
- /v2/_catalog with anonymous / member / super-admin scoping via `ReposRepo.ListDockerCatalog`.
- Cosign badge endpoint computing `sha256-<hex>.sig` tag-presence.
- Auto-scan enqueue wired via `ScansRepo.Enqueue` + `ScanKick` callback.
- SeverityGate hook stub (`SeverityGateFn`) exposed for 02-09 to fill.

## Task Commits

1. **Task 1: Manifest GET/HEAD/PUT/DELETE with ref-delta** — `b451f1a` (feat)
2. **Task 2: Tags/list + _catalog + cosign badge** — `880a53b` (feat)

## Files Created/Modified

- `internal/protocol/oci/manifests.go` — PUT/GET/HEAD/DELETE handlers + ref-delta logic.
- `internal/protocol/oci/manifests_test.go` — 8 tests covering the Pitfall 1/5/7 gates.
- `internal/protocol/oci/tags.go` — tags/list cursor pagination + tag DELETE.
- `internal/protocol/oci/tags_test.go` — pagination + catalog scoping + cosign badge e2e.
- `internal/protocol/oci/catalog.go` — /_catalog handler + catalogAuth middleware.
- `internal/protocol/oci/catalog_test.go` — placeholder (tests live with tags).
- `internal/protocol/oci/cosign.go` — badge endpoint + MountCosign + CosignTag export.
- `internal/protocol/oci/cosign_test.go` — derivation unit tests.
- `internal/protocol/oci/handler.go` — Deps + Handler extended; 4 manifest routes + 2 tag routes + /_catalog mounted outside guarded group with catalogAuth.
- `internal/protocol/oci/oci_err.go` — ErrCodeManifestInvalid, ErrCodeTagInvalid.
- `internal/audit/events.go` — EvtOCIManifestUploaded/Deleted, EvtOCITagDeleted.
- `internal/metadata/docker_manifests.go` — GetByDigestTx.
- `internal/metadata/docker_tags.go` — ListPaginated, ExistsTag, CountForDigest(+Tx).
- `internal/metadata/repos.go` — ListDockerCatalog + CatalogScope.
- `internal/app/app.go` — oci.New call now passes Manifests/Tags/Scans/ScanKick; MountCosign registered.

## Decisions Made

See `key-decisions:` in frontmatter. Most impactful:

1. **Tx-scoped read helpers inside WriteTx.** Reader-pool reads inside an in-flight writer tx deadlock on WAL + writer-pool-size-1 under `-race`. Every repo helper invoked from manifest-write paths (`GetByDigest`, `CountForDigest`) grew a `*Tx` sibling.
2. **/v2/_catalog outside the guarded group.** Dedicated `catalogAuth` middleware: no auth → anonymous; invalid auth → 401; valid Bearer → resolved actor. Preserves the challenge invariant without wedging the extractor.
3. **Cosign mounted under /api/v1 with BasicOrAPIKey** (not Bearer) — it is an OmniRepo-specific REST surface, not an OCI Distribution route. `CosignTag()` exported so tests and future REST handlers share one derivation.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking] Reader-pool deadlock when reads are called from WriteTx callback**

- **Found during:** Task 1 (first run of `TestRefDeltaOnTagOverwrite` with `-race`).
- **Issue:** `DockerManifestsRepo.GetByDigest` uses `db.Reader.QueryRowContext` (separate pool). When called *inside* a `WriteTx` callback that already holds the writer, the reader connection ends up blocked — under `-race` the test timed out at 10 minutes. Goroutine dump showed the stuck frame at `docker_manifests.go:72` inside `manifestPut.func1`.
- **Fix:** Added `DockerManifestsRepo.GetByDigestTx(ctx, tx, ...)` that reads via the caller-supplied tx. Added the same variant `DockerTagsRepo.CountForDigestTx` when `TestTagDeleteUnlinks` hit the same issue. Updated `manifestPut` and `tagDelete` to use the Tx variants inside their writer callbacks.
- **Files modified:** `internal/metadata/docker_manifests.go`, `internal/metadata/docker_tags.go`, `internal/protocol/oci/manifests.go`, `internal/protocol/oci/tags.go`.
- **Verification:** `TestRefDeltaOnTagOverwrite`, `TestTagDeleteUnlinks` both pass under `-race` in <100 ms.
- **Committed in:** `b451f1a` (GetByDigestTx), `880a53b` (CountForDigestTx).

**2. [Rule 1 — Bug] SQLite alias-in-WHERE is not allowed**

- **Found during:** Task 2 (`TestCatalogScoping`).
- **Issue:** First cut of `ListDockerCatalog` used `SELECT ... AS path ... WHERE path > ?` which SQLite silently evaluates to no-op — query returned empty even when rows existed.
- **Fix:** Inlined the expression `(p.name || '/docker/' || r.name)` in both SELECT and WHERE via a `const pathExpr`.
- **Files modified:** `internal/metadata/repos.go`.
- **Verification:** `TestCatalogScoping` now sees both `proj/docker/app` and `open/docker/y` for the member.
- **Committed in:** `880a53b`.

**3. [Rule 2 — Correctness] /v2/_catalog anonymous vs. guarded-route challenge tests**

- **Found during:** Task 2 (regression in `TestProtectedRoute_WWWAuthenticateChallenge`, `TestVerifyBearer_*`).
- **Issue:** Those tests used `/v2/_catalog` as the guarded-route placeholder. When catalog became partially-anonymous, they started getting 200 instead of 401.
- **Fix:** Swapped their target path to a repo-scoped manifest path (`/v2/nope/docker/nope/manifests/latest`) whose repo does not exist — AnonymousReadOK falls through to VerifyBearer which 401s. The catalog-specific challenge path is now exercised by the auth-present-but-invalid branch inside `catalogAuth` (covered by the existing expired/bad-sig/alg-none test cases against `/v2/_catalog`).
- **Files modified:** `internal/protocol/oci/handler_test.go`, `internal/protocol/oci/token_verify_test.go`.
- **Verification:** All 41 OCI tests pass.
- **Committed in:** `880a53b`.

**4. [Rule 2 — Correctness] Placeholder test expectation update**

- **Found during:** Task 2 (`TestProtectedRoute_ValidBearer_Passes`).
- **Issue:** Test asserted 501 (placeholder) for authenticated `/v2/_catalog`. Catalog is now real → 200.
- **Fix:** Updated assertion to expect 200.
- **Files modified:** `internal/protocol/oci/handler_test.go`.
- **Committed in:** `b451f1a`.

---

**Total deviations:** 4 auto-fixed (1 blocking, 1 bug, 2 correctness).
**Impact on plan:** All four were necessary to ship the plan as specified — none expanded scope. The Reader/Writer deadlock fix is a broader architectural observation that benefits every future writer path doing tx-scoped reads.

## Issues Encountered

- Deadlock under `-race` with mixed Reader/Writer-pool access in the same tx (see Deviation 1). The `*Tx` helper pattern is now the idiom to follow; future reviewers should watch for `r.db.Reader.QueryRowContext` called from handlers that wrap themselves in `WriteTx`.
- SQLite alias-in-WHERE caveat (Deviation 2) is a recurring trap — worth a note in PITFALLS.md for Phase 3/4 SQL work.

## User Setup Required

None — no external service configuration required.

## Cosign badge semantics (per plan output request)

- A manifest at `sha256:<hex>` is reported `signed: true` iff a tag named
  `sha256-<hex>.sig` exists in the SAME docker repo.
- No crypto: no Sigstore keyless, no Fulcio/Rekor network, no signature
  payload parsed. This is the D-08 intentional simplification — the v1.1
  roadmap can layer real verification behind a feature flag without moving
  this endpoint.

## SeverityGate hook interface (per plan output request)

```go
// SeverityGateFn is set on Deps by plan 02-09 (scan handler). Until then,
// a nil field means "always allow".
type SeverityGateFn func(ctx context.Context, repoID int64, digest string) error

// In manifestGet: if h.severityGate != nil { if err := h.severityGate(ctx, repo.ID, digest); err != nil { writeOCIErr 403 DENIED; return } }
```

02-09 creates a `block_on_severity` evaluator that queries the latest scan
for (repoID, digest) and returns a non-nil error when severity crosses the
repo's configured threshold. The hook signature is deliberately minimal —
the gate wraps whatever caching/TTL policy 02-09 picks.

## Catalog query plan + index decisions

- `ListDockerCatalog` joins `repos r` + `projects p` with `r.type='docker'`
  and `r.deleted_at IS NULL` predicates. The existing indexes are
  sufficient: `projects.id` is the PK, `repos(project_id,type,name)` is
  UNIQUE, and SQLite will drive the plan by the `type='docker'` selectivity
  filter for the anonymous/super-admin branches and by the `project_id IN`
  filter for the member branch. An explicit covering index
  `idx_repos_type_project` could shave a little per-query but isn't
  warranted until profiling shows real catalog load.

## Index manifest (manifest list) ref tracking

- Index bodies (`application/vnd.oci.image.index.v1+json` /
  `manifest.list.v2+json`) carry child-manifest digests under
  `.manifests[].digest`. `manifestRefs(body)` detects an index by probing
  for the `manifests` key and returns `(refs, isIndex=true, nil)`.
- When `isIndex`, `incRefs`/`decRefs` refcount on `docker_manifests`
  (child manifests) rather than `docker_blobs`. `DockerManifestsRepo.IncRef`
  was already wired for this in Plan 02-01.
- A single tx that PUTs an index therefore: (1) inserts the index manifest
  row, (2) bumps `ref_count` on every child manifest in the same repo,
  (3) refuses if any child is absent (404 MANIFEST_UNKNOWN). This matches
  Pitfall 1 semantics — tag overwrite of a tag pointing to an index
  decrements child-manifest refs for the prior index.

## Self-Check: PASSED

All created files exist on disk (`ls` verified for 8 new files).
Commit hashes `b451f1a` and `880a53b` resolvable via `git log --oneline`.
Full `go test -mod=vendor -count=1 ./...` green.

Acceptance criteria verification:
- `grep -E 'r\.(Get|Head|Put|Delete)\(.*manifests' internal/protocol/oci/handler.go` → 4 matches.
- `grep 'http.MaxBytesReader' internal/protocol/oci/manifests.go` → 1 match (cap enforcement).
- `grep -n 'priorDigest' internal/protocol/oci/manifests.go` → present (ref-delta).
- `grep -n 'IndexArtifact' internal/protocol/oci/manifests.go` → present (FTS5 inline).
- `grep -n 'scans.Enqueue\|scans\.Enqueue' internal/protocol/oci/manifests.go` → present.
- `TestRefDeltaOnTagOverwrite` PASS.
- Byte-identical roundtrip (`TestManifestPutAndGetByteIdentical`) PASS.
- 4 MiB cap (`TestManifestOversizedReturns413`) PASS.
- `grep -E 'r\.Get\(.*_catalog|tags/list' internal/protocol/oci/handler.go` → both present.
- Catalog scoping test (`TestCatalogScoping`) PASS — anonymous sees public only; member sees memberships ∪ public.
- Cosign badge (`TestCosignBadge`) PASS — false without .sig tag, true after insertion.
- Tags pagination (`TestTagsListPagination`) PASS — 100/100/50 across three pages.
