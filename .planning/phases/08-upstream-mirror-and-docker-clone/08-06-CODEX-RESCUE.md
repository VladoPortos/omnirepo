# Phase 8 Plan 06 — Codex rescue pass

**Invoked:** 2026-04-20
**Method:** `codex exec --full-auto` CLI against the OmniRepo repo (Claude
Code's Agent tool for `codex:codex-rescue` was not available to this
sequential executor; the CLI fallback documented in CLAUDE.md targets the
same Linux-native codex binary).
**Model:** codex-cli 0.121.0
**Time-box:** 15 minutes (enforced via `timeout 900`).
**Prompt length:** 847 words (under the 1200-word cap).

## Prompt

```
You are performing a code-review rescue pass on Phase 8 of the OmniRepo
project (a self-hosted artifact repository in Go + React).

## What shipped in Phase 8 (Upstream Mirror & Docker Clone)

Five plans (08-01..08-05), reviewed individually below. Full details in
.planning/phases/08-upstream-mirror-and-docker-clone/08-0{1-5}-SUMMARY.md.

- 08-01 (Backend foundation): Migration 024 added 5 columns to `repos`
  (is_mirror, mirror_upstream_url, mirror_filter_json, mirror_cred_id with
  ON DELETE SET NULL, scan_on_sync) and 3 to sync_jobs (progress_bytes,
  total_bytes, current_step). internal/api/repos.go Create+Patch
  validation with 5 envelope codes. internal/httpx/sync_rest.go 3-way
  branch on /sync with 16 KiB body cap + in-flight concurrency guard.
  internal/httpx/mirror_guard.go MirrorGuard + MirrorGuardFixed middleware
  wired into 5 protocol upload paths.
- 08-02 (Progress tracking): internal/jobs/progress.go ProgressWriter with
  200 ms throttle + change-detection + Flush-bypass; internal/jobs/
  counting_reader.go shared CountingReader with n > 0 guard.
  internal/api/openapi.yaml SyncJob schema extended. Per-protocol
  byte-level instrumentation for OCI/APT/RPM/PyPI; step-based for Helm.
- 08-03 (Docker clone modal): useJobProgress hook with pure helpers,
  500 ms poll cadence, per-repo URL /projects/{p}/repos/{t}/{r}/sync-jobs/
  {id}. CloneImageDialog 3-state machine.
- 08-04 (Mirror flag UI): MirrorConfigSection + 4 FilterWidget* with
  PascalCase wire keys. CreateRepoDialog extracted, SyncNowButton shared,
  RepoSettingsTab. PATCH body structurally excludes is_mirror +
  mirror_upstream_url (T-08-04-01).
- 08-05 (Upstream-creds CRUD UI): 3 TanStack mutations, UpstreamCredDialog
  + UpstreamCredsTab + ProjectSettingsPage. Blank-preserves-existing
  PATCH. UpstreamCred TS type has no password/token fields.

## Correctness questions (focus here, NOT on style)

1. Does the mirror_cred FK ON DELETE SET NULL actually protect against
   dangling references?
2. Does any sync handler leak %v error detail to the client (ERR-03
   invariant)?
3. Does the upload-reject middleware cover every upload path on every
   protocol?
4. Are progress writes truly throttled (>=200 ms AND changed) via
   internal/jobs/progress.go?
5. Is current_step bounded? Could upstream control any part of it?
6. Playwright mocks: do they actually exercise ErrorEnvelopeRenderer or
   just assert the dialog exists?
7. CountRepoInflight / Enqueue race (T-08-01-04 documented residual):
   does the cost of a double-enqueue really exceed "one wasted pending
   row"?
8. PATCH reject is_mirror flip: does it reject is_mirror: false → true
   on an existing repo, not just URL changes?
9. Cross-project cred check (T-08-01-07): does mirrorCredOwnership fire
   on PATCH when mirror_cred_id changes?

## Response format

For each finding, give `file:line | severity (blocker/real-issue/minor/
noise) | one-line fix`. Group under numbered question. Cap report at
1200 words. Time-box: 15 minutes max.
```

## Response (verbatim)

```
**1**
internal/metadata/migrations/024_mirror_and_progress.up.sql:20 | noise |
No fix: FK has `ON DELETE SET NULL`; stale queued `cred_id` is
revalidated by worker-side `Creds.Lookup` before use.

**2**
internal/jobs/pool.go:242 | real-issue | Sanitize/map handler errors
before persisting `last_error`, because repo sync-job APIs expose it
back to clients.

**3**
internal/protocol/pypi/handler.go:135 | real-issue | Move
`DELETE /packages/{filename}` into the `MirrorGuardFixed(..., "pypi")`
group.
internal/protocol/oci/handler.go:285 | real-issue | Wrap both OCI tag
DELETE routes with `mirrorGuard`.
internal/protocol/oci/handler.go:255 | noise | No fix: OCI 3-segment
and 4-segment blob upload POST/PATCH/PUT plus manifest PUT are guarded.

**4**
internal/jobs/progress.go:106 | noise | No fix: `ProgressWriter.Set`
holds `mu` across throttle state update and DB write, so concurrent
callers cannot both pass the gate.

**5**
internal/jobs/progress.go:102 | real-issue | Clamp and sanitize `step`
centrally, since APT/RPM/PyPI/Helm steps include upstream-controlled
package or filename text.

**6**
web/e2e/mirror-upload-rejected.spec.ts:165 | noise | No fix: test
asserts `[data-envelope-class="permission"]`, not only text visibility.

**7**
internal/httpx/sync_rest.go:183 | real-issue | Make in-flight check
plus enqueue atomic, because duplicate pending rows can both be leased
and run, causing duplicate downloads/audit/scans despite upserted
package rows.

**8**
internal/api/repos.go:213 | noise | No fix: PATCH rejects any
`is_mirror` field, including false to true flips.

**9**
internal/api/repos.go:245 | noise | No fix: PATCH calls
`mirrorCredOwnership` when `mirror_cred_id` is set to an integer.
```

## Triage

| Q | File:line | Severity | Disposition | Rationale | Commit |
|---|-----------|----------|-------------|-----------|--------|
| 1 | migrations/024_mirror_and_progress.up.sql:20 | noise | discarded | Codex verified the FK ON DELETE SET NULL is correct and the worker revalidates `cred_id` before use. No action needed. | — |
| 2 | internal/jobs/pool.go:242 | real-issue | apply | `sync_jobs.last_error` is exposed by the public API via GET /sync-jobs/{id} (`internal/api/repos_list.go:144`); handler errors can embed Authorization bytes, absolute filesystem paths, and multi-KB wrapped error chains. Added `sanitizeJobError` (scrub Authorization, /var/lib/omnirepo, /tmp; truncate to 1 KiB). Raw error still flows to slog for operators. | 9369e71 |
| 3a | internal/protocol/pypi/handler.go:135 | real-issue | apply | Confirmed — `r.Delete(".../packages/{filename}")` sat outside the MirrorGuardFixed group. A mirror repo could have its cached packages deleted via DELETE. Moved into the guard group; added regression test `TestDeletePackage_MirrorRepoReturns403`. | 4844bb1 |
| 3b | internal/protocol/oci/handler.go:285,288 | real-issue | apply | Confirmed — both tag DELETE routes (3-segment + 4-segment {image} variants) lacked `r.With(mirrorGuard)`. A mirror Docker repo could have its tags deleted. Wrapped both; added regression test `TestOCITagDelete_MirrorRepoReturns403`. | 4844bb1 |
| 3c | internal/protocol/oci/handler.go:255 | noise | discarded | Codex verified blob POST/PATCH/PUT + manifest PUT routes are guarded on both URL shapes. Matches plan 08-01's existing coverage. | — |
| 4 | internal/jobs/progress.go:106 | noise | discarded | `ProgressWriter.Set` holds `mu` across the throttle gate + DB write, so the "two callers both pass the gate" race is impossible. Verified via source reading (mu.Lock at line 106, mu.Unlock deferred; DB write at line 119 inside the critical section). | — |
| 5 | internal/jobs/progress.go:102 | real-issue | apply | `step` text is derived from APT Package names, RPM filenames, PyPI filenames, Helm chart names — all upstream-controlled. A 10 MB package name in a hostile upstream would persist verbatim. Added `MaxStepLen` (1 KiB) + `clampStep` with UTF-8-safe truncation and `…` marker at the ProgressWriter boundary. | 9369e71 |
| 6 | web/e2e/mirror-upload-rejected.spec.ts:165 | noise | discarded | Spec already asserts on `[data-envelope-class="permission"]`, which is the DOM hook `ErrorEnvelopeRenderer` emits. Not a text-visibility-only assertion. | — |
| 7 | internal/httpx/sync_rest.go:183 | real-issue | apply | Codex upgraded the documented T-08-01-04 race above the plan's "wasted pending row" classification: duplicate pending rows can both be leased and run, causing duplicate bandwidth/downloads, duplicate `EvtSyncStarted` audit rows, and a failed second job on UNIQUE-by-digest insert conflicts. Added `CountRepoInflightTx` method and moved the authoritative check inside the `WriteTx` closure; SQLite serialises writer-pool statements so the count+Enqueue pair is now atomic. Existing Reader-pool check retained as fast-path short-circuit. | 65acd35 |
| 8 | internal/api/repos.go:213 | noise | discarded | Codex verified the PATCH handler rejects `is_mirror != nil` and `mirror_upstream_url != nil` uniformly. A false→true flip attempt fires `codeRepoMirrorURLImmutable`. | — |
| 9 | internal/api/repos.go:245 | noise | discarded | Codex verified `mirrorCredOwnership` is called on PATCH when the cred ID is set. Cross-project creds are rejected on both Create and Patch. | — |

## Applied fixes (summary)

| Commit | Scope | Regression tests added |
|--------|-------|------------------------|
| `4844bb1` | Extend MirrorGuard to PyPI + OCI tag DELETE routes | `TestDeletePackage_MirrorRepoReturns403`, `TestOCITagDelete_MirrorRepoReturns403` |
| `9369e71` | Clamp `current_step`; sanitize `sync_jobs.last_error` | `TestProgressWriter_ClampsHugeStep`, `TestProgressWriter_ShortStepNotClamped`, `TestSanitizeJobError_*` (5 cases) |
| `65acd35` | Close CountRepoInflight/Enqueue race with tx-scoped check | `TestSyncJobsRepo_CountRepoInflightTx` (including same-tx enqueue+count race-closing assertion) |

All applied fixes ship with at least one regression test and a comment
pointing back to this file. The fifth `real-issue` finding (Q2 handler
error sanitization) is the class of fix that catches every future sync
handler automatically — no protocol-level changes needed.

## Verification after applying fixes

- `go test ./... -count=1 -timeout=300s` — all 35 packages green.
- `go build ./...` — clean.
- `npm run build` — clean (1,339 kB bundle, no regression).
- `npm test -- --run` — 78/78 vitest green.
- `npx playwright test --list` — 79 tests across 22 files parse cleanly.

Pre-existing failures (both documented in
`.planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md`):

- `make lint-typography` — 4 pre-existing files predate Phase 6 (App.tsx,
  ArtifactDetail.tsx, AptRepoPage.tsx, ScanReportPage.tsx).
- `make grep-cdn` — pre-existing fixture URLs in handler test files
  (mirror.centos.org, archive.ubuntu.com, pypi.org, charts.bitnami.com)
  + minified React error URLs in `web/dist/`.

Neither pre-existing failure was introduced by Phase 8 Plan 06's Codex
fixes; no new external URLs were introduced by the fix commits.
