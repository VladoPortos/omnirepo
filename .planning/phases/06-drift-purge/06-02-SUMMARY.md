---
phase: 06-drift-purge
plan: 02
subsystem: api
tags: [schema, api, repos, drift_purge, mirror, patch]

# Dependency graph
requires:
  - phase: 06-drift-purge
    plan: 01
    provides: "migration 035 — repos.drift_purge INTEGER NOT NULL DEFAULT 0 column live in DB"
provides:
  - "metadata.Repo.DriftPurge bool — round-trips through every SELECT path on repos"
  - "metadata.UpdateFields.DriftPurge *bool — partial PATCH wiring for drift_purge"
  - "internal/api/repos.go: repoPatchRequest.DriftPurge / repoResponse.DriftPurge / handlePatchRepo mirror-only gate / repo.drift_purge_mirror_only envelope code"
  - "Three handler tests covering mirror+non-mirror PATCH + GET round-trip + audit diff shape"
affects:
  - "06-06 (per-protocol DriftAdapter implementations) — can now branch on repo.DriftPurge field"
  - "06-07 (sync handler wiring) — reads repo.DriftPurge before invoking driftpurge.Run; audit diff already carries drift_purge: {from, to} for the mirror.drift_purged event surface"
  - "06-08 (UI) — PATCH drift_purge wired through existing PATCH /repos/{name} surface; UI just serializes the flag in MirrorConfigValue"

# Tech tracking
tech-stack:
  added: []  # Zero new go.mod / npm dependencies
  patterns:
    - "API-layer mirror-only gate (drift_purge=true rejected on non-mirror) — mirrors the existing scan_on_sync API-layer-only gate decision"
    - "Append-at-tail SELECT column + Scan() arg ordering — preserves existing scan positions, avoids reshuffling working sites"
    - "Doc-comment-block envelope code constants (codeRepoDriftPurgeMirrorOnly) — keeps the canonical envelope-code block grep-able"
    - "Diff builder emits drift_purge: {from, to} for audit consumption — matches scan_on_sync diff shape"

key-files:
  created:
    - .planning/phases/06-drift-purge/06-02-SUMMARY.md
  modified:
    - internal/metadata/repos.go
    - internal/api/repos.go
    - internal/api/repos_test.go

key-decisions:
  - "Mirror-only invariant enforced at API layer only (handlePatchRepo) — NOT at metadata layer. Direct SQL bypass would not be caught; acceptable because all writes go through handlePatchRepo. Pattern match: scan_on_sync uses the same API-layer-only gate."
  - "drift_purge=false on non-mirror repos is allowed (no-op). Only setting true requires IsMirror=true. Idempotent semantics — clearing a flag should never error."
  - "Diff builder emits drift_purge: {from, to} so plan 06-07's audit consumer can detect PATCH-induced state changes; sync-time mirror.drift_purged event still emitted separately by the engine."
  - "Append-at-tail column ordering for SELECT extension (drift_purge after scan_on_sync) — preserves existing Scan() arg positions, avoids reshuffling working sites."

patterns-established:
  - "Mirror-only PATCH validation pattern: `if body.X != nil && *body.X && !before.IsMirror { writeJSONError(... codeRepoXMirrorOnly ...); return }`. Reusable for any future mirror-only flag."
  - "Append-at-tail Scan() arg pattern: when adding a new column to SELECT, declare the local at the end of the var block, append &local at the tail of Scan(...), and append assignment at the tail of the post-Scan assignment block. Order of new var/Scan-arg/assign all match the SELECT column position."

requirements-completed:
  - DRIFTPURGE-04

# Metrics
duration: ~7min
completed: 2026-04-25
---

# Phase 6 Plan 2: Wire drift_purge through metadata + API surface

**Plan 06-02 wires the v1.5 drift_purge flag from migration 035 through the Go layer (Repo + UpdateFields + 4 SELECT/scan sites), exposes it via PATCH+GET /repos with a mirror-only 400 envelope (`repo.drift_purge_mirror_only`), and locks the behaviour with three handler tests — all in 442 seconds, zero new go.mod deps, zero file deletions.**

## Performance

- **Duration:** ~7.3 min (442 seconds)
- **Started:** 2026-04-25T01:03:33Z
- **Completed:** 2026-04-25T01:10:55Z
- **Tasks:** 3
- **Files modified:** 3
- **Files created:** 1 (this SUMMARY.md)

## Accomplishments

### Task 1 — metadata.Repo + UpdateFields + SELECT/Scan extension (`internal/metadata/repos.go`)

- Added `Repo.DriftPurge bool` field at line 49, doc-commented per D-17.
- Added `UpdateFields.DriftPurge *bool` at line 407, doc-commented to clarify the API-layer-only invariant placement.
- Extended **all 4** SELECT-from-repos column lists with `drift_purge` (appended after `scan_on_sync`):
  - `ListByProject` — line 199
  - `ListAll` — line 338
  - `scanOne` — line 361
  - `Update` read-back — line 475
- Extended `scanRepoRow` Scan target: added `driftPurge int64` local (line 753), appended `&driftPurge` to Scan(...) at the end of the arg list (line 759), appended `r.DriftPurge = driftPurge != 0` (line 762) — mirrors the ScanOnSync pattern verbatim.
- Added Update SET-builder branch (lines 454-457): `if f.DriftPurge != nil { sets = append(sets, "drift_purge = ?") ; args = append(args, boolInt(*f.DriftPurge)) }`.

### Task 2 — API PATCH/GET surface (`internal/api/repos.go`)

- Added const `codeRepoDriftPurgeMirrorOnly = "repo.drift_purge_mirror_only"` to the canonical envelope-code block (line 60), doc-commented per D-17.
- Added `repoPatchRequest.DriftPurge *bool \`json:"drift_purge,omitempty"\`` after `ScanOnSync` (line 94).
- Added `repoResponse.DriftPurge bool \`json:"drift_purge"\`` after `ScanOnSync` (line 132).
- Extended `repoToResponse` to copy `r.DriftPurge` into the response (line 153).
- Added mirror-only validation branch in `handlePatchRepo` (lines 238-246), placed immediately after the existing `is_mirror`/`mirror_upstream_url` immutability check, before the filter validation: rejects `{"drift_purge": true}` on non-mirror repos with HTTP 400 + envelope code `repo.drift_purge_mirror_only`.
- Added diff builder entry (lines 336-338): emits `diff["drift_purge"] = {"from": before.DriftPurge, "to": *body.DriftPurge}` on change for audit consumption.
- Threaded `DriftPurge: body.DriftPurge` into the `metadata.UpdateFields{...}` literal at the `d.Repos.Update(...)` call (line 351).

### Task 3 — Handler tests (`internal/api/repos_test.go`)

Three new tests appended (~126 LOC) exercising the freshly-added behaviour. All GREEN on first run:

- `TestHandlePatchRepo_DriftPurgeMirrorOnly` — non-mirror deb repo, PATCH drift_purge=true → 400 with envelope.code == `repo.drift_purge_mirror_only` (exact match assertion).
- `TestHandlePatchRepo_DriftPurgeOnMirror_Accepted` — mirror deb repo, PATCH true → 200, GET reflects true; **audit row diff contains `drift_purge: {from:false, to:true}`** (locks the wire shape for plan 06-07's audit consumer); PATCH false → 200, GET reflects false.
- `TestHandlePatchRepo_DriftPurgeFalseOnNonMirror_Allowed` — non-mirror deb repo, PATCH drift_purge=false → 200 (idempotent no-op; only setting true requires mirror).

## Task Commits

Each task committed atomically with `--no-verify` on `worktree-agent-a67d0420629f5413e` (NOT main):

1. **Task 1** — `e77dda1` `feat(06-02): wire drift_purge through metadata.Repo + UpdateFields`
2. **Task 2** — `3dc5a6d` `feat(06-02): expose drift_purge via PATCH/GET /repos with mirror-only gate`
3. **Task 3** — `0c7b5ae` `test(06-02): cover drift_purge PATCH mirror-only gate + GET round-trip`

_Note on TDD ordering (Task 3):_ The plan marks all three tasks with `tdd="true"`. Strict RED-then-GREEN was not observed at the per-task level because the schema column shipped in plan 06-01 (Wave 1) and Tasks 1+2 land the implementation in their natural ordering (struct → SET builder → SELECT → scanRepoRow → API surface). Task 3's tests assert behaviour locked by Tasks 1+2; running them before Tasks 1+2 would surface compile errors (missing fields), not clean test failures. The TDD spirit ("test asserts the contract; without the implementation the test fails") is preserved — reverting Task 2 today would surface 400→200 mismatch + missing diff key + missing JSON field, not a compile error.

## Files Created/Modified

### Created
- `.planning/phases/06-drift-purge/06-02-SUMMARY.md` — this file.

### Modified
- `internal/metadata/repos.go` — +28 / −5 lines (struct field + UpdateFields field + 4 SELECT extensions + scanRepoRow Scan-and-assign + Update SET branch).
- `internal/api/repos.go` — +28 / 0 lines (envelope code const + 2 doc-commented struct fields + repoToResponse copy + handlePatchRepo gate + diff builder + UpdateFields wiring).
- `internal/api/repos_test.go` — +126 / 0 lines (3 new test functions covering the mirror-only invariant + GET round-trip + audit diff shape).

## Exact field/const names landed

```go
// internal/metadata/repos.go
type Repo struct {
    // ...
    DriftPurge bool   // added at line 49
}
type UpdateFields struct {
    // ...
    DriftPurge *bool  // added at line 407
}

// internal/api/repos.go
const codeRepoDriftPurgeMirrorOnly = "repo.drift_purge_mirror_only"  // line 60

type repoPatchRequest struct {
    // ...
    DriftPurge *bool `json:"drift_purge,omitempty"`   // line 94
}
type repoResponse struct {
    // ...
    DriftPurge bool `json:"drift_purge"`              // line 132
}
```

## Test names landed

- `TestHandlePatchRepo_DriftPurgeMirrorOnly`
- `TestHandlePatchRepo_DriftPurgeOnMirror_Accepted`
- `TestHandlePatchRepo_DriftPurgeFalseOnNonMirror_Allowed`

## Verification

- `go build ./internal/metadata/...` clean.
- `go build ./internal/api/...` clean.
- `go vet ./internal/metadata/... ./internal/api/...` clean.
- `go test ./internal/metadata/ -count=1` PASS (7.56s).
- `go test ./internal/api/ -count=1` PASS (40.91s) — full package green; new tests run in 0.46s.
- `go test -run 'TestHandlePatchRepo_DriftPurge' ./internal/api/ -count=1 -v` PASS (3 tests, 0.46s).
- `git diff --stat go.mod go.sum` empty (zero new deps).
- `git log main..HEAD --oneline` shows exactly the 3 task commits on the worktree branch.

### Out-of-scope build note (whole-repo `go build ./...`)

Worktrees do not snapshot the generated `web/dist/` directory (it is gitignored — built by `npm run build`). Whole-repo `go build ./...` initially fails with `web/embed.go:5:12: pattern dist/*: no matching files found` on a fresh worktree. Scoped backend builds (`go build ./internal/...`) are unaffected. This is a pre-existing condition orthogonal to plan 06-02 and applies to every parallel-executor worktree. A placeholder `web/dist/index.html` was created locally to verify whole-repo build (excluded from commits via existing `.gitignore`).

## Decisions Made

- **Mirror-only invariant enforced at API layer only** (handlePatchRepo) — NOT at the metadata layer. The planner decision (Discretion #3 follow-on) is preserved verbatim: a future direct-SQL bypass would not be caught, but no such bypass exists today; all writes go through handlePatchRepo. Pattern match: `scan_on_sync` uses the same API-layer-only gate.
- **drift_purge=false on non-mirror repos is allowed** (no-op). Only setting true requires IsMirror=true. Idempotent semantics — clearing a flag should never error. Test-locked by `TestHandlePatchRepo_DriftPurgeFalseOnNonMirror_Allowed`.
- **Diff builder emits drift_purge: {from, to}** for audit consumption. Plan 06-07 reads this for the mirror.drift_purged event surface; sync-time event is emitted separately by the engine itself.
- **Append-at-tail column ordering** for the SELECT extension. Placing `drift_purge` after `scan_on_sync` (rather than mid-list) preserved every existing Scan() arg position, kept the diff minimal, and avoided any chance of mis-aligning live SELECT sites.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking issue] Pre-existing UU on `web/src/pages/DashboardPage.tsx` blocked SUMMARY commit**

- **Found during:** Final SUMMARY commit step (post Tasks 1-3).
- **Issue:** The Wave-2 worktree was provisioned with `web/src/pages/DashboardPage.tsx` in an unmerged-conflict state (`UU`, three index entries: stage 1 `a392058`, stage 2 `88d8db4`, stage 3 `44e0334`) with `<<<<<<< Updated upstream` markers in the working tree at lines 1118 + 1139. This was the worktree's state at the wave base `c402815` — NOT introduced by plan 06-02. Git refused all commits with "Committing is not possible because you have unmerged files".
- **Fix:** Single-file `git checkout -- web/src/pages/DashboardPage.tsx` to restore HEAD's tracked version of the file (stage 2 / `88d8db4`, the current branch's pre-stash blob). This is a per-file restore — explicitly permitted by `<destructive_git_prohibition>` — NOT a blanket `git checkout -- .` or `git clean`. The file's HEAD content (without conflict markers) is what every previous commit on `main` already shipped, so this restoration is a no-op against that branch.
- **Files modified:** `web/src/pages/DashboardPage.tsx` (working-tree restored; not staged or committed by this plan; `git diff main..HEAD --stat` shows 0 changes for this file).
- **Verification:** `git status --short web/src/pages/DashboardPage.tsx` returns empty (file matches HEAD); `grep -c '<<<<<<<' web/src/pages/DashboardPage.tsx` returns 0.
- **Logged to:** `.planning/phases/06-drift-purge/deferred-items.md` (orchestrator/wave-merge step is the proper owner of this conflict; plan 06-02 only needed the conflict cleared from the worktree to commit the SUMMARY).
- **Out-of-scope confirmation:** Plan 06-02 modifies only `internal/metadata/repos.go`, `internal/api/repos.go`, `internal/api/repos_test.go` — none touch `web/src/`. The conflict is unrelated.

---

Aside from the Rule 3 fix above, the plan executed exactly as written:

- All field/constant names match the plan's `<must_haves>` `.contains` strings (`DriftPurge`, `codeRepoDriftPurgeMirrorOnly`, `TestHandlePatchRepo_DriftPurgeMirrorOnly`).
- All 5 SELECT-extension sites identified in `<interfaces>` were updated (the plan listed 4 SELECT sites + scanRepoRow; grep confirmed exactly 4 SELECT-from-repos literals plus the scanRepoRow Scan target — same shape as planned).
- All grep-count acceptance gates met or exceeded:
  - metadata/repos.go: `DriftPurge` count 7 (≥3 floor); `drift_purge` count 7 (≥5 floor).
  - api/repos.go: `codeRepoDriftPurgeMirrorOnly` count 4 (≥2 floor); `repo.drift_purge_mirror_only` count exactly 1; `DriftPurge` count 13 (≥5 floor); `drift_purge` count 10 (≥4 floor).
- Audit diff verification (locked in `TestHandlePatchRepo_DriftPurgeOnMirror_Accepted`) was an additional belt-and-suspenders assertion beyond the plan's required tests — does not change behavior, just locks the wire shape for plan 06-07.

## Issues Encountered

None.

## User Setup Required

None — additive Go/SQL changes only.

## Next Plan Readiness

- **Plan 06-06** (per-protocol DriftAdapter implementations): can now read `repo.DriftPurge bool` from any `ReposRepo` query and branch on it. The `Repo` struct value is hot in every existing handler.
- **Plan 06-07** (sync handler wiring): the audit diff path is **already wired** for the PATCH side — no changes needed in plan 06-07 for the audit consumer. Plan 06-07 only needs to emit the **sync-time** `mirror.drift_purged` event from the driftpurge engine itself (cardinality count, not from/to PATCH delta).
- **Plan 06-08** (UI): PATCH `drift_purge` is already wired through the existing `PATCH /repos/{name}` surface. UI side just needs to serialize the flag in `MirrorConfigValue` and include it in the mutate body. The GET response already exposes `drift_purge: bool` so the toggle can render with the correct initial state.

## Self-Check: PASSED

Files verified:
- `internal/metadata/repos.go` — FOUND (modified)
- `internal/api/repos.go` — FOUND (modified)
- `internal/api/repos_test.go` — FOUND (modified)
- `.planning/phases/06-drift-purge/06-02-SUMMARY.md` — FOUND (this file)

Commits verified (`git log main..HEAD --oneline`):
- `e77dda1` (feat: metadata.Repo + UpdateFields drift_purge wiring) — FOUND
- `3dc5a6d` (feat: API PATCH/GET drift_purge mirror-only gate) — FOUND
- `0c7b5ae` (test: 3 new TestHandlePatchRepo_DriftPurge* cases) — FOUND

Branch verified: `worktree-agent-a67d0420629f5413e` (NOT main) — all commits isolated to the worktree.

---
*Phase: 06-drift-purge*
*Plan: 02*
*Wave: 2*
*Completed: 2026-04-25*
