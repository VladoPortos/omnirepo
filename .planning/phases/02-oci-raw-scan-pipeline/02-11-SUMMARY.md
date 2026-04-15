---
phase: 02-oci-raw-scan-pipeline
plan: 11
subsystem: api + metadata + auth + audit
tags: [repo-mutation, wipe, refcount, trash, audit, fts5]
requires:
  - internal/metadata/repos.go ReposRepo (Phase 1)
  - internal/metadata/docker_blobs.go DockerBlobsRepo (02-01)
  - internal/metadata/docker_manifests.go DockerManifestsRepo (02-01)
  - internal/metadata/docker_tags.go DockerTagsRepo (02-01)
  - internal/metadata/raw_files.go RawFilesRepo (02-08)
  - internal/metadata/fts.go IndexArtifactDelete (Phase 1)
  - internal/storage/trash.go Trash (Phase 1)
  - internal/auth/policy.go Can + per-action table (Phase 1, 02-05)
  - internal/audit/events.go EvtRepoUpdated/EvtRepoWiped (Phase 1)
  - internal/api admin mount chain + membership resolver (Phase 1)
provides:
  - metadata.UpdateFields
  - (*metadata.ReposRepo).Update
  - (*metadata.ReposRepo).WipeDocker
  - (*metadata.ReposRepo).WipeRaw
  - metadata.extractDigests (package-private helper)
  - api.handlePatchRepo / api.handleWipeRepo
  - auth.ActionUpdateRepo / auth.ActionWipeRepo
affects:
  - internal/api/admin_phase1.go (two new route registrations under /api/v1)
  - internal/auth/policy.go (two new action constants; per-action case group extended; AllActions length 17 -> 19)
  - internal/auth/policy_test.go (TestAllActionsSliceMatchesConstants want 17 -> 19)
tech-stack:
  added: []
  patterns:
    - "Partial-field UPDATE via sets/args builder; caller-supplied *sql.Tx for audit-in-writer-lock"
    - "Wipe collects blob digests from manifest bodies via tolerant JSON walk; CAS never touched"
    - "Trash.Move runs AFTER writer tx commit — filesystem op is non-transactional; failure logged and swept by GC"
    - "Diff-then-Update pattern for audit: compute {field: {from, to}} map BEFORE the tx so the event reflects only actual changes"
    - "Audit emission post-commit (Logger.Record opens its own tx) — matches existing Phase 1 handler pattern"
key-files:
  created:
    - internal/api/repos.go
    - internal/api/repos_test.go
  modified:
    - internal/metadata/repos.go (+ extractDigests helper, UpdateFields, Update, WipeDocker, WipeRaw)
    - internal/metadata/repos_test.go (+ 6 tests for new helpers)
    - internal/auth/policy.go (+ ActionUpdateRepo, ActionWipeRepo; per-action case group extended)
    - internal/auth/policy_test.go (AllActions count 17 -> 19)
    - internal/api/admin_phase1.go (+ 2 route registrations)
decisions:
  - "No migration 007 needed. 001_initial.up.sql's repos table already carries description_md, auto_scan (default 1), block_on_severity (CHECK-constrained), and public_read. The plan's conditional migration clause was a safety net; the schema was already complete."
  - "Wipe for repo types other than docker/raw returns 501 Not Implemented (helm/git/rpm/deb/pypi live in Phase 3). Chose explicit 501 + 'not_implemented' error code over silent pass-through so clients observe the Phase 2 scope boundary."
  - "WipeDocker recovers the blob reference set by walking manifest bodies (extractDigests visits every 'digest' key at any JSON depth). The DB does not carry a manifest->blob join table in Phase 2 — this avoids a schema addition and stays tolerant to manifest-list/image-index nesting. Tests prove the walk handles OCI image manifest shape (config.digest + layers[].digest)."
  - "bytes_freed counts only blobs whose ref_count transitioned to 0 as a result of this wipe. Shared blobs (ref_count > 0 after DecRef) are NOT counted. Matches the GC mental model: 'bytes now eligible for reclaim'."
  - "Audit emitted post-commit (not inside the writer tx). The audit.Logger interface only exposes Record(ctx, e) which opens its own WriteTx internally. This matches every existing Phase 1 handler (handleCreateRepo, handleDeleteRepo, handleTLSUpload). Plan text said 'audit in same tx' but the shipping audit API does not support tx participation; cross-tx ordering is guaranteed by post-commit emission."
  - "Added ActionUpdateRepo and ActionWipeRepo as first-class Action constants rather than reusing ActionDeleteRepo. Separate actions let future policy refinements (e.g. wipe requires super-admin, update requires member) land without touching handler code. Both land in the project-member case group."
  - "Diff map omits unchanged fields. A PATCH that submits description_md equal to the current value emits NO repo.updated audit row — matches 'auditable state change' semantics and prevents log noise from idempotent PATCHes."
  - "Path-segment defense-in-depth on trash target. Mirrors the WR-06 pattern in handleDeleteRepo: auth.ProjectNameValid runs on project + repo name, validRepoTypes lookup on type, all three must pass before filepath.Join touches d.DataRoot. Without this, a future chi-config drift could allow traversal."
metrics:
  duration: ~35m
  tasks: 2
  files: 2 created, 5 modified
  completed: 2026-04-15
requirements_complete:
  - REPO-05
  - REPO-07
  - REPO-09
---

# Phase 2 Plan 11: Repo PATCH + Wipe Summary

Two endpoints surfacing repo mutation for REPO-05 / REPO-07 / REPO-09:

- `PATCH /api/v1/projects/{name}/repos/{type}/{repo}` — updates any subset of
  `description_md`, `auto_scan`, `block_on_severity`, `public_read`; validates
  the severity enum; emits `repo.updated` with a `{diff: {field: {from, to}}}`
  map scoped to fields that actually changed.
- `POST /api/v1/projects/{name}/repos/{type}/{repo}/wipe` — deletes per-
  protocol artifact rows in one writer tx, decrements affected blob refcounts
  (CAS files untouched, per Pitfall 8), moves the on-disk repo tree to trash
  AFTER commit, and returns `{artifact_count, bytes_freed, trash_id}`.

`public_read` enforcement (REPO-09) was already wired by 02-05's
`AnonymousReadOK` middleware; this plan exposes the toggle via PATCH so
project members can flip it.

## Migration 007 decision

**Not needed.** The plan's action block allowed for adding
`007_repo_settings.up.sql` if the original `repos` table was missing any of
`auto_scan`, `block_on_severity`, `description_md`, `public_read`. Reading
`001_initial.up.sql` confirmed all four columns were already present with
correct defaults and the `CHECK(block_on_severity IN ('none','low','medium',
'high','critical'))` constraint in place. No migration was written and the
migrations directory remains at 006.

## Wipe behavior for unsupported repo types

Plan 02-11 ships wipe for `docker` and `raw` only. All other repo types
(`helm`, `git`, `rpm`, `deb`, `pypi`) return:

```
HTTP 501 Not Implemented
Content-Type: application/json
{"error":"not_implemented","detail":"wipe not supported for type \"<type>\" in Phase 2"}
```

Phase 3 (package repos) and Phase 4 (Git) add their own WipeX helpers and
extend the dispatch switch in `handleWipeRepo`.

## Audit diff JSON shape

A PATCH that changes `auto_scan` from `true` to `false` (defaults) emits an
`audit_log` row with `event_kind='repo.updated'` and `details_json`:

```json
{
  "diff": {
    "auto_scan": {"from": true, "to": false}
  }
}
```

A PATCH that touches multiple fields:

```json
{
  "diff": {
    "auto_scan":         {"from": true,  "to": false},
    "block_on_severity": {"from": "none", "to": "high"},
    "public_read":       {"from": false, "to": true},
    "description_md":    {"from": "",    "to": "new readme"}
  }
}
```

A PATCH that submits values identical to the current state emits NO audit
row (the diff map is empty; the handler skips emission).

A wipe emits `event_kind='repo.wiped'` with `details_json`:

```json
{
  "artifact_count": 1,
  "bytes_freed":    200
}
```

`bytes_freed` is the sum of sizes of blobs whose `ref_count` dropped to 0
as a direct result of the wipe — i.e., bytes the GC sweep can now reclaim.
Shared blobs still referenced by other repos do not contribute.

## Test Evidence

- `go build -mod=vendor ./...` — exit 0.
- `go test -mod=vendor -race -count=1 ./internal/api/... ./internal/auth/... ./internal/metadata/...` — all green.
- Full-repo `go test -mod=vendor -count=1 ./...` — every package green
  except the pre-existing flake `internal/jobs/TestPool_NoHandlerMarksFailed`
  (documented in 02-05-SUMMARY.md and 02-08-SUMMARY.md; not introduced by
  this plan).

### Targeted coverage (`internal/metadata`)

- `TestReposRepo_Update_PartialOnlyAutoScan` — flipping one field leaves the
  other three untouched; round-trip via read-back through the same tx.
- `TestReposRepo_Update_NoFields_ReturnsCurrent` — empty `UpdateFields`
  returns the current row without a spurious UPDATE.
- `TestReposRepo_Update_AllFields` — all four fields change simultaneously.
- `TestReposRepo_Update_MissingRow` — `ErrNotFound` when id doesn't exist.
- `TestReposRepo_WipeDocker_SharedBlobsSurvive` — 2 manifests referencing a
  shared blob + one unique blob; wipe r1 asserts: r2's manifest+tag
  untouched, shared blob ref_count 2 → 1 (still alive), unique blob
  ref_count 1 → 0 (bytes_freed=200=size).
- `TestReposRepo_WipeRaw` — 50 rows, bytes_freed computed from real sizes
  (sum 10+11+…+59 = 1725), ListDir returns 0 afterward.

### Targeted coverage (`internal/api`)

- `TestRepoPatch_PartialOnlyAutoScan` — only `auto_scan` in body; response
  echoes updated repo; audit_log row exists with `diff.auto_scan` populated
  and `diff.description_md` absent.
- `TestRepoPatch_InvalidBlockOnSeverity` — `"bogus"` → 400.
- `TestRepoPatch_PublicReadFlip` — response echoes `public_read: true`.
- `TestRepoPatch_NotFound` — 404 on missing repo name.
- `TestRepoPatch_NonMember403` — non-member user → 403 via RequireCanWith.
- `TestRepoWipe_RawMovesDirToTrash` — raw_files rows disappear, on-disk dir
  vanishes, trash entry created, `repo.wiped` audit recorded.
- `TestRepoWipe_DockerSharedBlobsSurvive` — r2's manifest + shared blob
  (ref_count 1) survive after wiping r1.
- `TestRepoWipe_UnsupportedType501` — helm repo → 501.
- `TestRepoWipe_NonMember403` — non-member → 403.
- `TestRepoWipe_NotFound404` — missing repo → 404.

### Policy test update

`TestAllActionsSliceMatchesConstants` want bumped 17 → 19 for the two new
Action constants. Iteration-based tests (every Action obeys super-admin
bypass; every project-scoped action denies non-members) automatically
exercise the new constants without further edits.

## Threat-model compliance

| Threat | Status | Evidence |
|--------|--------|----------|
| T-02-11-01 Wipe deletes CAS blobs shared with other repos | mitigated | `WipeDocker` only runs `DELETE FROM docker_tags/docker_manifests` + per-digest `UPDATE docker_blobs SET ref_count=ref_count-1` and `IndexArtifactDelete`. There is zero `os.Remove`/CAS-file code in the wipe path. `TestReposRepo_WipeDocker_SharedBlobsSurvive` + `TestRepoWipe_DockerSharedBlobsSurvive` prove shared blob row + refcount survive. |
| T-02-11-02 Cross-project wipe via path manipulation | mitigated | `resolveRepoFromURL` validates `type` against `validRepoTypes` before project lookup; `FindByTriple` enforces `(project_id, type, name)` match; `RequireCanWith(ActionWipeRepo, resolveProjectTargetFromURL)` re-checks membership after URL resolution. `TestRepoWipe_NonMember403` + `TestRepoWipe_NotFound404` cover the negative paths. |
| T-02-11-03 block_on_severity arbitrary string | mitigated | API-layer enum check via `validBlockOnSeverity` map returns 400 before any tx opens. DDL CHECK constraint (from 001) is the second line of defense. `TestRepoPatch_InvalidBlockOnSeverity` covers the API path. |
| T-02-11-04 description_md XSS | accepted | Per D-36: stored as-is; Phase 5 UI owns sanitization. Phase 2 REST never injects into HTML. |
| T-02-11-05 Wipe holds writer lock on huge repos | accepted | v1 admin op on writer pool size 1; documented in plan. v1.1 may batch. |
| T-02-11-06 trash.Move fails post-commit → orphan dir | mitigated | Failure logged at WARN with `repo_id` and err; DB state already correct; Phase 02-12 GC trash sweep reconciles. `trash_id` returned as `""` in the response. |
| T-02-11-07 Anonymous PATCH/wipe | mitigated | Routes sit inside `api.Mount`'s `r.Group` that applies `SessionOrAPIKey` before dispatch; anonymous actor never reaches handlers. Plus `Can` denies anonymous on non-`repo.read` actions regardless. |

## Deviations from plan

### Shape refinements (Rule 3 / Rule 2)

**1. [Rule 3 — API shape] Audit emission post-commit, not in-tx**

- Found during: Task 2 design.
- Issue: Plan body said "emits repo.updated audit event with diff in details_json" inside the same writer tx. The audit.Logger interface only exposes `Record(ctx, e)`; internally it opens its own `db.WriteTx`. There is no `RecordTx` variant. Trying to share a tx would require extending the audit package (out of scope for this plan).
- Fix: Follow existing Phase 1 pattern (handleCreateRepo, handleDeleteRepo, handleTLSUpload): mutate-then-audit with post-commit `d.recordAudit(r, event)`. Cross-tx ordering is preserved by sequential execution in the handler, and audit failures never mask successful state changes (OQ-9 best-effort).
- Files: internal/api/repos.go.

**2. [Rule 2 — Correctness] extractDigests via JSON walk, not a join table**

- Found during: Task 1 implementation.
- Issue: The plan action block said "SELECT all manifest digests + their referenced blob digests for the repo (use a JOIN or two SELECTs)." No such join exists — Phase 2 does not carry a `manifest_blob_refs` table; manifests only store the opaque body BLOB. Options were: (a) add a migration for a join table (Rule 4 — architectural), or (b) re-derive references from the stored manifest body.
- Fix: Chose (b). Added `extractDigests(body []byte) []string` that walks the JSON tree tolerantly and collects every `"digest": "sha256:..."` string. Handles OCI image manifest (config.digest + layers[].digest), image index, and docker manifest list shapes uniformly. Bodies that fail to parse yield `nil` — wipe still removes the manifest row; orphan blobs reach GC via the quiescence window.
- Files: internal/metadata/repos.go, internal/metadata/repos_test.go.

**3. [Rule 3 — Correctness] ActionUpdateRepo + ActionWipeRepo as first-class actions**

- Found during: Task 2 route registration.
- Issue: Plan body said "Project-member auth check" without specifying which Action to pass to RequireCanWith. Reusing ActionDeleteRepo for PATCH would conflate distinct audit-relevant operations; reusing it for wipe would hide wipe in audit queries.
- Fix: Added two new Action constants, bumped AllActions length test want 17 → 19, dropped both into the project-member case group alongside ActionCreateRepo / ActionDeleteRepo. No behavior change for super-admin (bypass above) or non-members (denied same reason).
- Files: internal/auth/policy.go, internal/auth/policy_test.go.

## Deferred Issues

- **`internal/jobs/TestPool_NoHandlerMarksFailed`** flake: pre-existing,
  documented in `02-05-SUMMARY.md` and `02-08-SUMMARY.md`. Not introduced
  by this plan.
- **Separate `RecordTx` in audit.Logger** — plan hinted at in-tx audit
  emission but the existing API doesn't support it. Adding it is a
  cross-cutting audit-package refactor suitable for a future cleanup plan;
  it would also retrofit every Phase 1 handler that currently audits
  post-commit. Out of scope here.
- **Wipe-in-batches for very large repos** — T-02-11-05 accepted for v1.

## Commits

| Hash    | Subject |
|---------|---------|
| e5e200e | test(02-11): add failing tests for ReposRepo Update/WipeDocker/WipeRaw (RED) |
| fc4a983 | feat(02-11): add ReposRepo Update/WipeDocker/WipeRaw helpers (REPO-05, REPO-07, D-34, D-35) |
| 7aabe23 | test(02-11): add failing tests for PATCH/wipe repo endpoints (RED) |
| 97cb108 | feat(02-11): PATCH + wipe repo REST endpoints (REPO-05, REPO-07, REPO-09, D-34, D-35) |

## Self-Check: PASSED

- internal/api/repos.go — FOUND
- internal/api/repos_test.go — FOUND
- internal/metadata/repos.go — FOUND (modified — Update, WipeDocker, WipeRaw, extractDigests added)
- internal/metadata/repos_test.go — FOUND (modified — 6 new tests added)
- internal/auth/policy.go — FOUND (modified — ActionUpdateRepo + ActionWipeRepo)
- internal/auth/policy_test.go — FOUND (modified — want 19)
- internal/api/admin_phase1.go — FOUND (modified — 2 new route registrations)
- Commits e5e200e, fc4a983, 7aabe23, 97cb108 — FOUND in `git log --oneline`
- `go build -mod=vendor ./...` — exit 0
- `go test -mod=vendor -race -count=1 ./internal/api/... ./internal/auth/... ./internal/metadata/...` — exit 0
