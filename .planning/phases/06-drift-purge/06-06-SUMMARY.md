---
phase: 06-drift-purge
plan: 06
subsystem: driftpurge
tags: [driftpurge, adapters, sync_jobs, pypi, rpm, deb, helm]

# Dependency graph
requires:
  - phase: 06-drift-purge
    provides: "06-03 driftpurge engine + DriftAdapter interface (5 methods)"
  - phase: 06-drift-purge
    provides: "06-04 Trash.MoveWithSnapshot + RowSnapshot sidecar"
provides:
  - "Four DriftAdapter implementations (pypiAdapter, rpmAdapter, debAdapter, helmAdapter) — one per mirror protocol per D-12"
  - "NewPyPIAdapter / NewRPMAdapter / NewDEBAdapter / NewHelmAdapter constructors"
  - "Per-protocol PathFn types (PyPIPathFn / RPMPathFn / DEBPathFn / HelmPathFn) — caller binds path-resolution at construction"
  - "SyncJobsRepo.SetSummaryDriftPurged(ctx, jobID, count) — JSON-merge writer for sync_jobs.summary.drift_purged (D-21)"
  - "PyPIFilesRepo.ListByRepo (Rule 3 deviation — symmetric with rpm/deb/helm)"
  - "Four round-trip integration tests under internal/driftpurge/adapter_test.go"
affects:
  - "06-07 (sync_handler.Handle wiring + admin_trash kind-dispatch)"

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "adapter-per-protocol behind a narrow interface (clone of internal/jobs/PartialSyncError shape)"
    - "verbatim column-map snapshot via map[string]any -> json.Marshal -> Trash.MoveWithSnapshot"
    - "json_set merge writer for opaque per-job summary metadata"
    - "suite_id -> (suite, component) projection cache built once per LocalRows call"

key-files:
  created:
    - internal/driftpurge/pypi_adapter.go
    - internal/driftpurge/rpm_adapter.go
    - internal/driftpurge/deb_adapter.go
    - internal/driftpurge/helm_adapter.go
    - internal/driftpurge/adapter_test.go
  modified:
    - internal/metadata/sync_jobs.go (added SetSummaryDriftPurged)
    - internal/metadata/sync_jobs_test.go (added TestSyncJobs_SetSummaryDriftPurged)
    - internal/metadata/pypi_files.go (added ListByRepo per Rule 3)

key-decisions:
  - "DEB Key flattens 5-tuple {name, version, arch, component, suite} into 3 string slots: Key{A: name+\"|\"+component+\"|\"+suite, B: version, C: arch}. Caller (06-07) MUST project upstream identically."
  - "PyPIFilesRepo.ListByRepo added (Rule 3 - Blocking) — pypi_files.go shipped only ListByProject + ListProjects; the four protocols now stay symmetric via parallel ListByRepo APIs."
  - "Helm adapter covers both HTTP and OCI ingest paths per D-14 — same helm_charts.(name, version) UNIQUE for both, same UpstreamEntry collectFn boundary in helm/sync_handler.go."
  - "RPM Key uses the coarser {name, version, arch} per D-12 / PATTERNS.md Pitfall 7 — the DB UNIQUE is {name, epoch, version, release, arch} but upstream repomd.xml does not always spell epoch / release identically across rebuilds. Purge deletes by row.id (PK), so DB-uniqueness collisions cannot corrupt the diff."
  - "All four adapters tolerate os.ErrNotExist from Trash.MoveWithSnapshot (legacy empty markers): sidecar lands and DELETE proceeds. Any other error is fatal — propagated to the caller's tx for rollback."
  - "Snapshots exclude id and uploaded_at: Restore (plan 06-07) calls Insert which assigns a fresh PK and stamps uploaded_at via strftime per PATTERNS.md §3."

patterns-established:
  - "Per-protocol DriftAdapter file naming: <proto>_adapter.go alongside engine.go in internal/driftpurge/."
  - "Adapter constructors are always: NewXAdapter(upstreamKeys []Key, repos..., trash storage.Trash, pathFn XPathFn) DriftAdapter."
  - "Snapshot map[string]any keys mirror DB column names verbatim — trivially auto-shaped for the v1.5 Phase 6 admin_trash restore handler."

requirements-completed:
  - DRIFTPURGE-01
  - DRIFTPURGE-02

# Metrics
duration: ~8min
completed: 2026-04-25
---

# Phase 6 Plan 06: Per-protocol drift adapters + sync_jobs summary writer Summary

**Four DriftAdapter implementations (PyPI / RPM / DEB / Helm) covering D-12's per-protocol drift keys, plus the SyncJobsRepo.SetSummaryDriftPurged JSON-merge writer (D-21) — round-trip integration tests against real DB + real Trash root green under -race, zero new go.mod deps.**

## Performance

- **Duration:** ~8 minutes (worktree-bookkeeping cleanup excluded)
- **Started:** 2026-04-25T01:04:50Z
- **Completed:** 2026-04-25T01:13:02Z
- **Tasks:** 6 (1 RED + 1 GREEN for SetSummaryDriftPurged via TDD; 4 adapter creations; 1 consolidated integration-test file)
- **Files created:** 5 (4 adapter + 1 test)
- **Files modified:** 3 (sync_jobs.go, sync_jobs_test.go, pypi_files.go)
- **Commits:** 7 on `worktree-agent-ab6665a27bcb516aa` branch

## Accomplishments

- All four `DriftAdapter` implementations now satisfy the `internal/driftpurge.DriftAdapter` interface and pass `go vet` clean.
- Per-protocol drift keys land per D-12 with the exact `Key{A,B,C}` projections locked in `06-06-PLAN.md`:
  - **PyPI:** `Key{A: project_normalized, B: filename, C: digest}`
  - **RPM:** `Key{A: name, B: version, C: arch}`
  - **DEB:** `Key{A: name+"|"+component+"|"+suite, B: version, C: arch}` (5-tuple flattened into 3 slots)
  - **Helm:** `Key{A: name, B: version, C: ""}` (covers HTTP + OCI per D-14)
- `SyncJobsRepo.SetSummaryDriftPurged(ctx, jobID, count)` ships with a json_set-based merge writer that preserves sibling keys; idempotent-by-value across repeat calls; zero-count is a legal run-evidence path per D-10.
- `PyPIFilesRepo.ListByRepo` added (Rule 3 deviation) so pypi/rpm/deb/helm have symmetric list-all APIs the engine can call from `LocalRows`.
- Four `TestAdapter_*_DriftRoundTrip` tests cover: seed 3 rows -> upstream keeps 2 -> driftpurge.Run inside real WriteTx -> assert PurgedCount=1, sample lex-correct, trash holder has the right kind, sidecar carries the row_snapshot, DB has only the 2 surviving rows.

## Task Commits

| Task | Type | Hash | Description |
| ---- | ---- | ---- | ----------- |
| 1 RED | test | `d647361` | Failing test for SyncJobsRepo.SetSummaryDriftPurged (cherry-picked from inadvertent main commit `baec075`) |
| 1 GREEN | feat | `1e5135f` | json_set merge writer for sync_jobs.summary.drift_purged |
| 2 | feat | `4e158fd` | PyPI drift adapter + PyPIFilesRepo.ListByRepo |
| 3 | feat | `aa66e78` | RPM drift adapter |
| 4 | feat | `b777840` | DEB drift adapter with suite_id -> (suite, component) resolution |
| 5 | feat | `e01e7f4` | Helm drift adapter (HTTP + OCI per D-14) |
| 6 | test | `653ead7` | Per-adapter round-trip tests against real DB + real Trash root |

All worktree commits use `--no-verify` per parallel-executor protocol.

## Adapter constructor signatures (for plan 06-07 wiring)

```go
// internal/driftpurge/pypi_adapter.go
type PyPIPathFn func(row *metadata.PyPIFile) string
func NewPyPIAdapter(
    upstreamKeys []Key,
    files *metadata.PyPIFilesRepo,
    trash storage.Trash,
    pathFn PyPIPathFn,
) DriftAdapter

// internal/driftpurge/rpm_adapter.go
type RPMPathFn func(row *metadata.RPMPackage) string
func NewRPMAdapter(
    upstreamKeys []Key,
    pkgs *metadata.RPMPackagesRepo,
    trash storage.Trash,
    pathFn RPMPathFn,
) DriftAdapter

// internal/driftpurge/deb_adapter.go
type DEBPathFn func(row *metadata.DEBPackage) string
func NewDEBAdapter(
    upstreamKeys []Key,
    pkgs *metadata.DEBPackagesRepo,
    suites *metadata.AptSuitesRepo,
    trash storage.Trash,
    pathFn DEBPathFn,
) DriftAdapter

// internal/driftpurge/helm_adapter.go
type HelmPathFn func(row *metadata.HelmChart) string
func NewHelmAdapter(
    upstreamKeys []Key,
    charts *metadata.HelmChartsRepo,
    trash storage.Trash,
    pathFn HelmPathFn,
) DriftAdapter
```

## Per-protocol Key projections (caller MUST mirror this)

| Protocol | Key.A | Key.B | Key.C | Trash kind |
| -------- | ----- | ----- | ----- | ---------- |
| PyPI | `project_normalized` | `filename` | `digest` | `pypi_file_drift` |
| RPM | `name` | `version` | `arch` | `rpm_package_drift` |
| DEB | `package + "|" + component + "|" + suite` | `version` | `architecture` | `deb_package_drift` |
| Helm | `name` | `version` | `""` | `helm_chart_drift` |

Adapter UpstreamKeys() returns whatever the caller passes in. Plan 06-07 sync_handler.Handle MUST build []Key with the same projection — RPM coarser-than-DB-UNIQUE intentional per D-12 / PATTERNS.md Pitfall 7; DEB 5-tuple flattened into 3 slots intentional per planner decision in 06-06-PLAN.md.

## SetSummaryDriftPurged wire shape (D-21)

```go
func (r *SyncJobsRepo) SetSummaryDriftPurged(ctx context.Context, jobID, count int64) error
```

Plan 06-07 calls this immediately after `driftpurge.Run` returns — independent of `report.Skipped` (D-10: zero-count IS legal run evidence).

```go
// inside helm/sync_handler.go Handle's happy path, after driftpurge.Run:
if h.deps.SyncJobs != nil {
    _ = h.deps.SyncJobs.SetSummaryDriftPurged(ctx, jobID, int64(report.PurgedCount))
}
```

The writer uses SQLite's `json_set('$.drift_purged', ?)` so multiple summary writers can coexist without lost-update races; merge with whatever future v1.6 keys land (`bytes_downloaded`, etc.) is automatic.

## Round-trip test evidence

```
$ go test ./internal/driftpurge/... -count=1
ok  	github.com/dxc-internal/omnirepo/internal/driftpurge	0.148s

$ go test -race ./internal/driftpurge/ -count=1
ok  	github.com/dxc-internal/omnirepo/internal/driftpurge	2.511s

$ go test ./internal/metadata/ -run '^TestSyncJobs_SetSummaryDriftPurged$' -count=1
ok  	github.com/dxc-internal/omnirepo/internal/metadata	0.036s

$ git diff --stat go.mod go.sum
(no output — zero diff)

$ go vet ./internal/driftpurge/... ./internal/metadata/...
(clean)
```

Trash holder + sidecar shape (PyPI test, abbreviated):

```json
{
  "original_path": "<tempdir>/foo-1.0.1.tar.gz",
  "kind": "pypi_file_drift",
  "original_id": 2,
  "moved_at_unix": 1745543582,
  "deleted_by": "tester",
  "empty": true,
  "row_snapshot": {
    "core_metadata_json": "{}",
    "digest": "sha256:b",
    "filename": "foo-1.0.1.tar.gz",
    "kind": "sdist",
    "project_normalized": "foo",
    "repo_id": 1,
    "requires_python": "",
    "size_bytes": 0,
    "version": "1.0.1"
  }
}
```

## Decisions Made

See `key-decisions` in frontmatter. Key choices:

- **DEB Key projection** — flattening the 5-tuple `{name, version, arch, component, suite}` into the engine's 3-slot Key by joining `name+"|"+component+"|"+suite` is the planner-decision lock from `06-06-PLAN.md`. The caller (06-07) MUST mirror it. Alternative (a 5-field `Key`) would have widened the engine API; alternative (b nested key types) would have moved suite_id resolution into the engine. The flattening keeps the engine protocol-agnostic.
- **suite_id resolution lives in the adapter** — `LocalRows` calls `AptSuitesRepo.ListByRepo` once per Run, builds a `map[int64]debSuiteRef` for the duration of the call, then projects each `deb_packages` row through it. The component+suite lookup is O(1) per row and the cache amortizes to one extra query per drift run.
- **PyPI ListByRepo addition** — pypi_files.go shipped only `ListByProject` + `ListProjects`. The drift engine's `LocalRows` needs every row for the repo in one call to avoid N+1 queries. Adding `ListByRepo` keeps pypi symmetric with rpm/deb/helm and matches the existing code pattern. Rule 3 deviation, no scope creep.
- **Snapshot map uses string keys mirroring DB columns** — trivial for plan 06-07's restore handler to consume: `json.Unmarshal -> map[string]any -> typed conversion`. Future column additions auto-shape; future column renames break Restore (acceptable under the 7-day trash retention per D-02).
- **All four adapters use the same os.ErrNotExist short-circuit** — Trash.MoveWithSnapshot returns os.ErrNotExist when the source file is missing; the adapter still completes the DELETE so the database state matches the upstream truth (drift removal is the goal — file presence is incidental).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Add PyPIFilesRepo.ListByRepo**

- **Found during:** Task 2 (PyPI adapter implementation)
- **Issue:** `internal/metadata/pypi_files.go` ships `ListByProject(ctx, repoID, normalized)` and `ListProjects(ctx, repoID)` but no `ListByRepo(ctx, repoID)`. The plan's Task 2 `<action>` block calls `a.files.ListByRepo(ctx, repoID)` directly. Without it, the PyPI adapter cannot compile — and pulling every project's rows via `ListProjects` + N×`ListByProject` would have been a wasteful N+1 anti-pattern.
- **Fix:** Added `ListByRepo` to `PyPIFilesRepo`, mirroring the shape of `RPMPackagesRepo.ListByRepo` / `DEBPackagesRepo.ListByRepo` / `HelmChartsRepo.ListByRepo`. Single query, ordered deterministically by `(project_normalized, version DESC, filename)`.
- **Files modified:** `internal/metadata/pypi_files.go`
- **Verification:** `go build ./internal/metadata/...` clean; the four protocols now have symmetric list-all APIs.
- **Committed in:** `4e158fd` (folded into the PyPI adapter commit since the adapter is the only caller today)

### Workflow issue (worktree isolation)

The Task 1 RED test commit (`baec075`) inadvertently landed on `main` because an early `cd /home/vladoportos/omnirepo` shell prefix swapped my working directory to the shared checkout. Detected immediately after the commit (`git branch --show-current` returned `main`); cherry-picked the same content onto the worktree branch as `d647361`; subsequent commits avoided `cd` entirely and used the absolute worktree path with `git -C <worktree>`. **No `git reset` or destructive operation was performed on main** — per the destructive-git-prohibition. The orchestrator's merge of this worktree will see the duplicate engine + sync_jobs commits as already-applied content and resolve cleanly. (Identical to the workflow note documented in plan 06-03's SUMMARY.)

**Total deviations:** 1 (Rule 3) + 1 workflow note. Zero scope creep.
**Fix-attempt counter:** 0 across the six tasks.

## Issues Encountered

- **Worktree CLAUDE.md is stale.** Same as plan 06-04: the worktree's checked-in `CLAUDE.md` claims "design documentation only — no source code". The actual repo has full source. Followed the *real* project constraints from the live code + the global `~/.claude/CLAUDE.md` + `.planning/PROJECT.md` context. Logged here for visibility; not actionable for this plan.
- **`web/dist/` missing in worktree.** Pre-existing — the workspace-wide `go build ./...` complains because `web/embed.go` has `//go:embed dist/*`. The driftpurge + metadata test suites (which is what this plan ships) are unaffected: they don't import `web/`. No action needed.

## User Setup Required

None — no external service configuration, env vars, or admin actions required.

## Notes for Plan 06-07 (sync_handler wiring + admin_trash kind-dispatch)

### Adapter wiring at end-of-Handle

For each protocol's `sync_handler.Handle` happy path, immediately after rows persist (and before `SetFilesSynced`):

```go
if repo.DriftPurge {
    upstream := buildKeys(entries) // protocol-specific projection
    var report driftpurge.DriftReport
    if err := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
        adapter := driftpurge.NewPyPIAdapter(
            upstream, h.deps.PyPIFiles, h.deps.Trash, pypiPathFn,
        )
        var rerr error
        report, rerr = driftpurge.Run(ctx, tx, repo.ID, actor, adapter)
        return rerr
    }); err != nil {
        return h.fail(ctx, repoID, pl, startedAt, err)
    }

    // D-21 run-evidence (always, including report.PurgedCount == 0
    // and report.Skipped == true).
    if h.deps.SyncJobs != nil {
        _ = h.deps.SyncJobs.SetSummaryDriftPurged(ctx, jobID, int64(report.PurgedCount))
    }

    // D-10 audit gates (count > 0 only) + D-08 skipped audit (skipped only).
    // Plan 06-07 implements these — both kinds already exist after plan 06-05.
}
```

### Caller-side []Key projections

Build upstream keys identically to the adapter's `Row.Key()` for the diff to match:

```go
// PyPI
upstream := make([]driftpurge.Key, 0, len(entries))
for _, ent := range entries {
    upstream = append(upstream, driftpurge.Key{
        A: ent.ProjectNormalized,
        B: ent.Filename,
        C: ent.Digest,
    })
}

// RPM
upstream := make([]driftpurge.Key, 0, len(entries))
for _, ent := range entries {
    upstream = append(upstream, driftpurge.Key{A: ent.Name, B: ent.Version, C: ent.Arch})
}

// DEB — flattened 5-tuple
upstream := make([]driftpurge.Key, 0, len(entries))
for _, ent := range entries {
    upstream = append(upstream, driftpurge.Key{
        A: ent.Package + "|" + ent.Component + "|" + ent.Suite,
        B: ent.Version,
        C: ent.Architecture,
    })
}

// Helm — covers HTTP + OCI identically per D-14
upstream := make([]driftpurge.Key, 0, len(entries))
for _, ent := range entries {
    upstream = append(upstream, driftpurge.Key{A: ent.Name, B: ent.Version, C: ""})
}
```

### Restore handler (admin_trash.go kind-dispatch)

Each adapter's snapshot is a `map[string]any` of column names. To restore a row:

1. `json.Unmarshal(entry.RowSnapshot, &snap)`.
2. Reconstruct the typed struct (e.g. `metadata.PyPIFile`).
3. Call the existing `Insert` method (which is already an UPSERT — `ON CONFLICT DO UPDATE` per D-04).
4. Validate `snap["repo_id"]` is a live, non-soft-deleted repo BEFORE the Insert; return `409 restore.conflict.repo_missing` per D-05 if not.
5. Then `Trash.Restore(ctx, trashPath, originalPath)` to move the file back.

Note: JSON numbers decode to `float64`, not `int64` — cast each int field explicitly (e.g. `int64(snap["repo_id"].(float64))`).

### Restore method (deferred per planner)

Plan 06-06 ships only the `DriftAdapter` interface (Protocol/TrashKind/UpstreamKeys/LocalRows/Purge). The restore method (`RestoreFromSnapshot`) is **not** on the interface — it lives on the concrete adapter struct in plan 06-07 if the planner wants per-adapter restore methods, or directly inside the admin_trash kind-dispatch as inline `Insert` calls if the planner wants the simpler shape. Either is consistent with the existing engine.

## Threat Flags

None. Threat register from `06-06-PLAN.md <threat_model>` is fully addressed:

- **T-06-06-01 (Tampering — incomplete UpstreamKeys):** Engine's empty-upstream guard (D-08, plan 06-03) catches the total-empty case; subset-incorrect relies on sync_handler correctness — covered by plan 06-07 integration tests, not this plan.
- **T-06-06-02 (DoS — huge upstream + huge local):** Accepted — O(N+M) algorithm; set-based lookup. Real mirror sizes <100K rows per repo.
- **T-06-06-03 (Information Disclosure — snapshot leaks columns):** Accepted — trash root is admin-only (0o750); snapshot contains only artifact metadata. No new surface.
- **T-06-06-04 (EoP — snapshot tampered to forge repo_id):** Mitigation deferred to plan 06-07 admin_trash restore handler — it MUST validate `repo_id` exists + is not soft-deleted BEFORE Insert.

No new security-relevant surface beyond what the plan's threat model enumerated.

## Next Phase Readiness

**Plan 06-07 (sync_handler wiring + admin_trash kind-dispatch + integration tests):**

- All four adapter constructors wired and tested. Plan 06-07 only needs to thread `repo.DriftPurge`, build the per-protocol []Key, call `driftpurge.Run`, and emit audit + summary writes.
- Caller projections locked in the table above; mismatch will produce vacuous diffs.
- `SetSummaryDriftPurged` ready for unconditional emission (D-10).
- Restore handler shape sketched in the Notes section above — concrete implementation belongs to plan 06-07.

**Other Wave 2 / 3 plans:** unblocked. The four adapter files are pure additions in `internal/driftpurge/`; nothing they touch overlaps with parallel Wave 2 work.

## Self-Check: PASSED

```
$ git log main..HEAD --oneline
653ead7 test(06-06): per-adapter round-trip tests against real DB + Trash root
e01e7f4 feat(06-06): implement Helm drift adapter (HTTP + OCI per D-14)
b777840 feat(06-06): implement DEB drift adapter with suite_id resolution
aa66e78 feat(06-06): implement RPM drift adapter
4e158fd feat(06-06): implement PyPI drift adapter
1e5135f feat(06-06): add SyncJobsRepo.SetSummaryDriftPurged writer (D-21)
d647361 test(06-06): add failing test for SyncJobsRepo.SetSummaryDriftPurged

$ git rev-parse --abbrev-ref HEAD
worktree-agent-ab6665a27bcb516aa

$ go test ./internal/driftpurge/ ./internal/metadata/ -count=1   # exits 0
ok  	github.com/dxc-internal/omnirepo/internal/driftpurge	0.148s
ok  	github.com/dxc-internal/omnirepo/internal/metadata	8.422s

$ go test -race ./internal/driftpurge/ -count=1                  # exits 0
ok  	github.com/dxc-internal/omnirepo/internal/driftpurge	2.511s

$ go vet ./internal/driftpurge/... ./internal/metadata/...        # clean
(no output)

$ git diff --stat go.mod go.sum                                  # empty
(no output)
```

**File existence:**

- FOUND: `/home/vladoportos/omnirepo/.claude/worktrees/agent-ab6665a27bcb516aa/internal/driftpurge/pypi_adapter.go`
- FOUND: `/home/vladoportos/omnirepo/.claude/worktrees/agent-ab6665a27bcb516aa/internal/driftpurge/rpm_adapter.go`
- FOUND: `/home/vladoportos/omnirepo/.claude/worktrees/agent-ab6665a27bcb516aa/internal/driftpurge/deb_adapter.go`
- FOUND: `/home/vladoportos/omnirepo/.claude/worktrees/agent-ab6665a27bcb516aa/internal/driftpurge/helm_adapter.go`
- FOUND: `/home/vladoportos/omnirepo/.claude/worktrees/agent-ab6665a27bcb516aa/internal/driftpurge/adapter_test.go`
- FOUND: `/home/vladoportos/omnirepo/.claude/worktrees/agent-ab6665a27bcb516aa/.planning/phases/06-drift-purge/06-06-SUMMARY.md` (this file, after commit)

**Commit existence (worktree branch):**

- FOUND: `d647361` — test(06-06): add failing test for SyncJobsRepo.SetSummaryDriftPurged
- FOUND: `1e5135f` — feat(06-06): add SyncJobsRepo.SetSummaryDriftPurged writer (D-21)
- FOUND: `4e158fd` — feat(06-06): implement PyPI drift adapter
- FOUND: `aa66e78` — feat(06-06): implement RPM drift adapter
- FOUND: `b777840` — feat(06-06): implement DEB drift adapter with suite_id resolution
- FOUND: `e01e7f4` — feat(06-06): implement Helm drift adapter (HTTP + OCI per D-14)
- FOUND: `653ead7` — test(06-06): per-adapter round-trip tests against real DB + real Trash root

All success criteria green. STATE.md / ROADMAP.md intentionally NOT modified (orchestrator-owned per `<parallel_execution>` directive).

---
*Phase: 06-drift-purge*
*Plan: 06*
*Completed: 2026-04-25*
