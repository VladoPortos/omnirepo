# OmniRepo Upstream Mirror & Docker Clone — Design

**Status:** Ready for user review → implementation
**Author:** brainstorming session 2026-04-19
**Target milestone:** v1.1 (bumps scope to add one phase before 1.0 public release)
**Predecessor phase:** Phase 7 (Snippet Polish)

---

## TL;DR

Add first-class upstream mirroring to 4 protocols (APT, RPM, PyPI, Helm) as a
per-repo `is_mirror` flag set at creation, and a per-click "Clone external image"
modal with a live progress bar for Docker. The backend sync machinery already
exists (`internal/protocol/{deb,rpm,pypi,helm}/sync_handler.go`,
`internal/protocol/oci/pull_external.go`, `internal/jobs/pool.go`,
`internal/metadata/upstream_creds.go`). This phase wires the UI, adds a
mirror-config schema, blocks uploads to mirror repos, and adds optional
Trivy scan-on-sync gating.

---

## Context — what already exists

Confirmed by code walk on 2026-04-19 against `main` (v1.1 in progress):

- `POST /api/v1/projects/{name}/repos/{type}/{repo}/sync` — generic endpoint,
  returns `202 {job_id, kind}`, API-key-authable. Enqueues `sync_jobs` row.
  [`internal/httpx/sync_rest.go:74–234`]
- Per-protocol sync handlers, all functional:
  APT (`deb/sync_handler.go`), RPM (`rpm/sync_handler.go`),
  PyPI (`pypi/sync_handler.go`), Helm (`helm/sync_handler.go`).
- `SyncFilter{Names, Globs, Suites, Components, Arches}` on APT
  [`internal/protocol/deb/upstream_parse.go:44`].
- `POST /api/v1/projects/{name}/repos/docker/{repo}/pull-external` — streams
  blobs into CAS with `google/go-containerregistry`, handles anonymous/basic/bearer,
  walks image indexes, kicks auto-scan if enabled [`internal/protocol/oci/pull_external.go`].
- AES-GCM-256 encrypted upstream credentials with full CRUD REST
  [`internal/api/upstream_creds.go`, `internal/metadata/upstream_creds.go`].
- Jobs pool with lease/backoff/retry; `Kick()` for immediate dispatch
  [`internal/jobs/pool.go`].

**What's missing:**

1. UI — Docker "Pull External" dialog is a stub that toasts "API not yet connected"
   [`web/src/pages/repo/DockerRepoPage.tsx:323–467`]. APT/RPM/PyPI/Helm repo
   pages have no sync UI at all. Upstream-creds CRUD has no UI.
2. Mirror-config persistence — the sync endpoint is stateless (body-driven);
   there is no "this repo is a mirror of X" concept on the repos table.
3. Progress reporting — `sync_jobs` has `status` but no byte/step tracking.
4. Reject-upload guard for mirror repos.
5. Scan-on-sync toggle — today OCI auto-scans after pull-external; the other
   four don't, and there's no per-repo opt-in separate from the upload-path
   `auto_scan` flag.

---

## Decisions (locked)

1. **Scope:** wire UI for all 5 protocols (Docker + APT/RPM/PyPI/Helm).
   Raw, S3, Git are out of scope.
2. **Mirror model:** APT/RPM/PyPI/Helm repos get a `is_mirror` flag at
   **creation time**. URL is immutable; filter + scan-on-sync flag are editable.
   Deleting the repo and recreating is the escape hatch for URL changes.
3. **Docker model:** no `is_mirror` flag. Per-click "Clone external image"
   modal with source ref, optional retag, optional cred, optional scan override.
4. **Upload block on mirror repos:** 403 from every protocol's upload handler
   when `repo.is_mirror=true`. Prevents hybrid push+pull conflicts.
5. **Deletion semantics:** accumulator only (option A from discussion). Re-sync
   adds new, never removes. No drift purge in v1.1.
6. **Cron trigger:** reuse existing `POST /sync` with API key. No new endpoint.
   For mirror repos, empty body = "sync from configured upstream."
7. **Concurrency:** reject a second sync on the same repo while one is running
   (409 `sync_already_running`).
8. **Scan policy:** new `scan_on_sync` column on `repos`, default **OFF**.
   Bulk mirror syncs only scan when this is on. Docker per-click clone reuses
   existing `auto_scan` flag but exposes a per-modal override checkbox.
9. **Progress:** `sync_jobs` gains `progress_bytes`, `total_bytes`,
   `current_step`. UI polls `GET /api/v1/jobs/{id}` every 500 ms while a modal
   is open. Helm progress is step-based (no bytes — `index.yaml` lacks sizes).

---

## Non-goals — deferred to v1.2

- Drift purge / stale-artifact tracking (option C from discussion).
- Scheduled/recurring sync (use external cron curl — that's the v1.1 story).
- Git repo mirroring.
- Pull-through proxy cache semantics.
- Air-gap enforcement (`AllowExternalActions` gate).
- Raw / S3 upstream support.
- Change-upstream-URL flow.

---

## Architecture

### Flow A — APT/RPM/PyPI/Helm mirror-at-creation

```
User                         UI                          Backend                  DB
 │                            │                            │                        │
 │─ create repo, is_mirror=1 ─▶ POST /projects/X/repos   ──▶ insert repos(…,         ├─ repos row
 │   upstream_url, filter,    │                            │   is_mirror=1,          │
 │   cred_id, scan_on_sync    │                            │   mirror_upstream_url,  │
 │                            │                            │   mirror_filter_json,   │
 │                            │                            │   mirror_cred_id,       │
 │                            │                            │   scan_on_sync)         │
 │                            │                            │                        │
 │── click "Sync now" ────────▶ POST /sync (empty body)   ──▶ read repo.mirror_*    ├─ sync_jobs row (pending)
 │                            │                            │   enqueue sync_jobs     │
 │                            │◀─ 202 {job_id} ───────────│   Kick() pool           │
 │                            │                            │                        │
 │                            ├─ poll GET /jobs/{id} ────▶│                        │
 │                            │    every 500 ms            │   dispatcher leases    ├─ sync_jobs.status=running
 │                            │                            │   runs handler         │
 │                            │                            │   writes artifacts     │
 │                            │                            │   updates progress_*   │
 │                            │◀─ {status, progress_*} ──│                        │
 │                            │                            │   if scan_on_sync:     │
 │                            │                            │     enqueue scan_jobs  │
 │                            │                            │   mark done            ├─ sync_jobs.status=done
```

### Flow B — Docker per-click clone

```
User            UI "Clone" modal              Backend oci/pull_external
 │               │                              │
 │── fill ref,  ─▶  POST /pull-external       ──▶ enqueue sync_jobs(pull_external)
 │   retag,     │     {src, retag, cred_id,   │                     │
 │   cred,      │      scan_override}         │                     │
 │   scan       │◀─ 202 {job_id} ─────────────│                     │
 │              │                              │                     │
 │              ├─ poll GET /jobs/{id} ──────▶│  dispatcher runs     │
 │              │   every 500 ms               │  remote.Get(src)    │
 │              │                              │  streams layers +   │
 │              │                              │  updates progress_* │
 │              │◀─ {progress_bytes, total} ──│                     │
 │              │                              │                     │
 │              │   progress bar renders       │  writes manifest    │
 │              │   "Layer 3/7 · 42 / 103 MiB" │  ref-counts blobs   │
 │              │                              │                     │
 │              │                              │  if auto_scan or    │
 │              │                              │  modal-override:    │
 │              │                              │    kick scan        │
 │              │◀─ status=done ──────────────│                     │
 │              │                              │                     │
 │              │   modal shows success,       │                     │
 │              │   list refetches             │                     │
```

---

## Data model

Single migration, purely additive:

```sql
-- internal/metadata/migrations/00NN_mirror_and_progress.sql
ALTER TABLE repos ADD COLUMN is_mirror              INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repos ADD COLUMN mirror_upstream_url    TEXT;
ALTER TABLE repos ADD COLUMN mirror_filter_json     TEXT;
ALTER TABLE repos ADD COLUMN mirror_cred_id         INTEGER
    REFERENCES upstream_creds(id) ON DELETE SET NULL;
ALTER TABLE repos ADD COLUMN scan_on_sync           INTEGER NOT NULL DEFAULT 0;

ALTER TABLE sync_jobs ADD COLUMN progress_bytes     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_jobs ADD COLUMN total_bytes        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_jobs ADD COLUMN current_step       TEXT;
```

**Constraints enforced at the application layer** (not DB — SQLite lacks
CHECKs we can rely on for JSON):
- `is_mirror=1` only valid when `type IN ('deb','rpm','pypi','helm')`.
- If `is_mirror=1`, `mirror_upstream_url` must be a valid http(s) URL and
  `mirror_filter_json` must parse as the protocol's `SyncFilter`.
- If `is_mirror=0`, the four mirror columns are ignored (left null).

**Existing v1.0 repos:** all stay `is_mirror=0`. Zero behavior change.

---

## Backend API changes

### Modified endpoints

| Endpoint | Change |
|---|---|
| `POST /api/v1/projects/{name}/repos` | Accept optional `{is_mirror, mirror_upstream_url, mirror_filter, mirror_cred_id, scan_on_sync}`. Validate per above. |
| `PATCH /api/v1/projects/{name}/repos/{type}/{repo}` | Allow editing `mirror_filter_json`, `mirror_cred_id`, `scan_on_sync`. **Reject** edits to `is_mirror` or `mirror_upstream_url`. |
| `POST /api/v1/projects/{name}/repos/{type}/{repo}/sync` | If `repo.is_mirror=1` and body is empty, read mirror config from repo. If body is non-empty AND `is_mirror=1`, return 400 `mirror_overrides_not_allowed`. Pre-existing body-driven path remains for `is_mirror=0` callers (tests + back-compat). |
| `POST /api/v1/projects/{name}/repos/docker/{repo}/pull-external` | Already accepts `retag_as`. Add `scan_override: bool?` and emit per-job progress updates. |
| `GET /api/v1/jobs/{id}` | Response body gains `progress_bytes`, `total_bytes`, `current_step`. |
| All upload handlers (OCI manifest PUT, APT `.deb` PUT, RPM PUT, PyPI upload, Helm chart PUT) | Prepend shared middleware that returns 403 `repo_is_mirror` when `repo.is_mirror=1`. |

### No new endpoints

Everything routes through already-mounted paths.

### Concurrency guard

In `sync_rest.go` handler, before enqueue:

```go
inflight, err := syncJobs.CountRepoInflight(ctx, repo.ID) // pending+running
if err != nil { return ... }
if inflight > 0 { return httperr.Conflict("sync_already_running", "a sync is already running for this repo") }
```

Same guard applies to `pull-external` scoped per-repo (not per-image — two
concurrent clones into the same repo could race on manifest commits).

### Progress writer

Shared helper in `internal/jobs/progress.go` (new file):

```go
type ProgressWriter struct {
    jobID int64
    repo  metadata.SyncJobsRepo
}
func (p *ProgressWriter) Set(step string, done, total int64) error { … }
// Throttled: only write if changed and ≥ 200 ms since last write.
```

Protocol handlers wrap upstream `io.Reader`s with a counting reader that calls
`p.Set(...)` at flush boundaries.

---

## UI surfaces

All reuse Phase 6 primitives (`StatusBadge`, `SkeletonCard`, `CopyInline`,
`ErrorEnvelopeRenderer`, `EmptyState`). No new visual tokens.

### CreateRepoDialog (extend — `web/src/components/CreateRepoDialog.tsx`)

Add conditional section visible only when selected protocol is in
`{deb, rpm, pypi, helm}`:

```
[x] This repo is a mirror of an upstream

  Upstream URL    [__________________________]   (required, http(s))
  Filters         (protocol-specific widget — see below)
  Credential      [Select… ▾]  [+ New]           (optional)
  [ ] Scan synced artifacts with Trivy  (default off)

  ⓘ Uploads are disabled on mirror repos.
  ⓘ Upstream URL cannot be changed after creation.
```

Protocol-specific filter widgets:

- **APT:** Suite (text, e.g. `focal`), Components (multi-select: `main`,
  `universe`, `restricted`, `multiverse`, custom), Arches (multi-select:
  `amd64`, `arm64`, `i386`, custom), optional Names (comma-separated allow-list).
- **RPM:** Arches (multi-select: `x86_64`, `aarch64`, `noarch`, custom),
  optional Names.
- **PyPI:** Projects allow-list (comma-separated; empty = all).
- **Helm:** Charts allow-list (comma-separated; empty = all).

### DockerRepoPage (rewrite existing stub — `web/src/pages/repo/DockerRepoPage.tsx:433–467`)

Replace the stubbed "Pull External" dialog with a real modal:

```
Clone external image

Source reference   [docker.io/library/nginx:1.27__________]   (required)
Retag as (optional) [library/nginx:1.27__________________]   (default: same)
Credential         [Select ▾]  (optional)
[ ] Scan after pull (overrides repo auto_scan)

                                        [Cancel]  [Pull]

─── while running ───────────────────────────────────────

Pulling library/nginx:1.27 …
Layer 3 of 7 · 42 MiB / 103 MiB · 41%
[████████████░░░░░░░░░░░░░░░░░░]
                                        [Cancel pull]
```

Implementation: TanStack Query mutation POSTs, then polls
`GET /api/v1/jobs/{id}` every 500 ms until `status ∈ {done, failed}`.
On done: toast success + invalidate image list query. On failed: show
`ErrorEnvelopeRenderer` inside modal with retry button.

### AptRepoPage / RpmRepoPage / PypiRepoPage / HelmRepoPage

Add single affordance visible only when `repo.is_mirror=true`:

```
Mirror of https://archive.ubuntu.com/ubuntu   (focal · main, universe · amd64)
                                              [Sync now]

────── while running ───────────────────────────────────
Syncing · 1,234 MiB / 3,456 MiB · step: pulling libc6_2.35
[██████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░]
```

Button is disabled when a sync job is in-flight (derived from polling
`GET /api/v1/projects/.../sync-status` or similar — see M5 below).

For Helm where byte totals aren't available, the progress reads
"Pulling chart 3 of 12 · redis-17.0.0.tgz".

### RepoSettingsTab — new "Mirror config" card

Visible only on `is_mirror=true` repos. Displays:

- Upstream URL (readonly, with CopyInline)
- Current filter (editable with same widget as CreateRepoDialog)
- Linked credential (editable dropdown)
- `scan_on_sync` toggle
- [Save] [Cancel]

### ProjectSettingsPage — new "Upstream credentials" tab

Table of upstream creds for the project, with add/edit/delete. Uses existing
`/api/v1/projects/{name}/upstream-creds` CRUD. Secrets never echoed.

---

## Scan policy matrix

| Scenario | Governs | Default |
|---|---|---|
| Upload to non-mirror repo | `repo.auto_scan` (existing) | varies by repo |
| Docker per-click clone | `repo.auto_scan` with per-modal override | follows repo |
| APT/RPM/PyPI/Helm mirror sync | `repo.scan_on_sync` (**new**) | **OFF** |

Rationale for default-off on sync: syncing a full Ubuntu focal suite may
ingest thousands of packages; scanning all of them on first sync is
potentially hours of Trivy work on constrained hardware. Operator flips it
on once the mirror is healthy or runs a manual scan-all later.

---

## Testing strategy

### Unit

- Filter-JSON round-trip for each protocol.
- Mirror-config validator rejects http, allows https, rejects malformed URLs.
- `ProgressWriter` throttling (≥ 200 ms + change-check).
- Upload-reject middleware returns 403 envelope with `class=forbidden`.

### Integration (Go)

Per protocol, spin up an in-process fake upstream (`httptest.NewServer`) that
serves minimal but valid metadata + artifacts. One test per flow:

- APT: fake `dists/focal/{InRelease,main/binary-amd64/Packages.gz,pool/…}`;
  assert `deb_packages` rows inserted with correct component/arch.
- RPM: fake `repodata/{repomd.xml,primary.xml.gz,…}` + `.rpm`; assert
  metadata parsed and file on disk.
- PyPI: fake PEP 691 JSON; assert `pypi_files` rows + wheel on disk.
- Helm: fake `index.yaml` + `.tgz`; assert chart on disk and indexable.
- OCI: reuse existing `pull-external` tests; add a progress-assertion variant.

Idempotency test per protocol: run same sync twice, assert row counts stable.

Reject-upload test per protocol: create `is_mirror=1` repo, attempt an upload,
assert 403 with envelope `code="repo_is_mirror"`.

Concurrency test: enqueue two syncs back-to-back, assert second returns 409.

### Playwright e2e

- Create a repo with `is_mirror=true` via the UI, assert it saved.
- Click "Sync now", assert progress bar advances and completes.
- Open Docker clone modal, fill in a test reference (against a fake upstream
  in test fixtures), assert progress bar advances.
- Edit mirror filter from RepoSettingsTab, assert re-sync honors new filter.
- Create + delete an upstream cred from ProjectSettingsPage.

### Regression gates (must stay green)

- `make test` (Go + Node).
- Phase 6 lint gates (protocol-redaction, contrast, typography,
  spacing-carveout, axe-devdep).
- `make grep-cdn` (air-gap invariant on bundled assets).

---

## Risks & mitigations

| Risk | Severity | Mitigation |
|---|---|---|
| Large APT mirror fills disk without warning | High | Disk check before starting (free space vs. announced `Size:` sum in Packages). Abort with clear error if insufficient. `make test` integration test for the guard. |
| Progress rates throttle TXes on a busy DB | Med | `ProgressWriter` updates capped at ≥ 200 ms + change-detected. |
| User confuses `auto_scan` vs `scan_on_sync` | Med | UI labels explicitly: "Scan on upload" vs "Scan synced artifacts". RepoSettingsTab shows both toggles side by side with help text. |
| Mirror repo hiding existing manual upload behavior silently | Med | Validation blocks setting `is_mirror=1` on a non-empty repo. User must create mirror as a new empty repo. |
| Cred deletion orphans `mirror_cred_id` | Low | `ON DELETE SET NULL`. Next sync fails with clear "credential missing" envelope; user repoints cred via RepoSettingsTab. |
| Progress bytes wrong for Helm (no upstream size) | Low | Helm shows step-based progress only; explicitly documented in UI ("chart 3/12"). |
| Docker tag-only ref without digest pulls a moving target | Low | Document in modal help text: "syncing `nginx:latest` re-pulls whatever upstream currently tags as `latest`." |
| Upstream HTTP flakiness mid-sync | Med | Existing handlers already return error; job retries via existing backoff machinery. |

---

## Effort estimate

**~5–7 working days**, one phase (Phase 8 in v1.1 or Phase 1 in v1.2, operator choice).

| Milestone | Work | Estimate |
|---|---|---|
| M1: Backend foundation (schema, mirror-aware sync, upload-reject, concurrency guard) | Go | 1 day |
| M2: Progress tracking (writer helper + jobs endpoint extension + handler wraps) | Go | 0.5 day |
| M3: Docker clone modal with progress + retag + scan override | TypeScript | 1 day |
| M4: Create-repo mirror block + "Sync now" buttons on 4 protocol pages + RepoSettingsTab mirror card | TypeScript | 1.5 days |
| M5: Upstream-credentials CRUD UI on ProjectSettingsPage | TypeScript | 1 day |
| M6: Integration tests (fake upstreams × 5) + Playwright e2e + peer-review pass | Mixed | 1.5 days |

---

## Implementation plan — checkboxes (resumable across sessions)

> Mark `[x]` when done. Each milestone commits atomically. M1 blocks all
> others. M2 blocks M3/M4/M5 (progress reporting). M6 runs last.

### Milestone M1 — Backend foundation  ☐ not started

**Goal:** schema change + mirror-aware sync endpoint + upload-reject middleware
+ concurrency guard, all shipping together so nothing goes to main half-done.
**Dependencies:** none
**Files touched:** `internal/metadata/migrations/`, `internal/metadata/repos.go`,
`internal/metadata/sync_jobs.go`, `internal/httpx/sync_rest.go`,
`internal/httpx/mirror_guard.go` (new), `internal/protocol/{oci,deb,rpm,pypi,helm}/*upload*`,
`internal/api/repos.go`, `internal/api/repos_list.go`.

- [ ] **M1.1** Write migration `00NN_mirror_and_progress.sql` adding 5 columns
      to `repos` and 3 columns to `sync_jobs`. Register in migrations list.
- [ ] **M1.2** Extend `metadata.RepoMeta` struct and `ReposRepo` CRUD with the
      5 new fields. Unit test: round-trip through Insert/Update/Get.
- [ ] **M1.3** Extend `metadata.SyncJobsRepo` with `SetProgress(jobID, step,
      done, total)` + selecting progress fields in Get. Unit test throttling
      handled in ProgressWriter (M2).
- [ ] **M1.4** Add `CountRepoInflight(ctx, repoID)` to `SyncJobsRepo`. Unit
      test: returns correct count under concurrent enqueues.
- [ ] **M1.5** Update `CreateRepoRequest` + handler in `internal/api/repos.go`
      to accept + validate mirror fields. Validation rules per this doc's
      "Data model" section. Unit tests for each validation branch.
- [ ] **M1.6** Update `PatchRepoRequest` + handler to allow filter / cred /
      scan_on_sync edits; reject `is_mirror` or `mirror_upstream_url` changes
      with 400 `mirror_url_immutable`. Unit test each branch.
- [ ] **M1.7** Update `POST /sync` in `internal/httpx/sync_rest.go`:
      read mirror config from repo when body is empty AND `is_mirror=1`;
      reject body override when `is_mirror=1`; add concurrency guard.
      Integration test all three branches.
- [ ] **M1.8** Write `internal/httpx/mirror_guard.go` — middleware that
      returns 403 envelope `repo_is_mirror` when repo.is_mirror is true.
      Unit test envelope shape.
- [ ] **M1.9** Wire the mirror guard into every protocol's upload path:
      OCI manifest/blob PUT, APT `.deb` PUT, RPM PUT, PyPI upload, Helm PUT.
      Integration test per protocol — create mirror repo, attempt upload,
      assert 403.
- [ ] **M1.10** `go test ./...` green. Commit: `feat(backend): mirror-repo flag, sync mirror-aware endpoint, upload guard`.

### Milestone M2 — Progress tracking  ☐ not started

**Goal:** sync handlers emit byte-level progress; `GET /jobs/{id}` returns it.
**Dependencies:** M1
**Files touched:** `internal/jobs/progress.go` (new), `internal/api/admin_jobs.go`,
`internal/protocol/{oci,deb,rpm,pypi,helm}/sync_handler.go` (+ `pull_external.go`).

- [ ] **M2.1** Write `internal/jobs/progress.go`: `ProgressWriter` helper with
      throttling (≥ 200 ms + change detection). Unit test throttling
      under high-frequency calls.
- [ ] **M2.2** Extend `GET /api/v1/jobs/{id}` response schema with
      `progress_bytes`, `total_bytes`, `current_step`. Update openapi.yaml
      + regenerate types. Integration test shape.
- [ ] **M2.3** Wrap OCI pull-external's layer+config streams with a
      progress-counting `io.Reader`; compute `total_bytes` from manifest
      layers before pull starts; emit `current_step = "layer 3/7"`.
      Integration test progress advances.
- [ ] **M2.4** Wrap APT per-package download with progress counter;
      `total_bytes` from Packages `Size:` field sum (after filter). Integration test.
- [ ] **M2.5** Wrap RPM per-package download with progress counter;
      `total_bytes` from primary.xml `size package="…"` sum. Integration test.
- [ ] **M2.6** Wrap PyPI per-file download with progress counter;
      `total_bytes` from PEP 691 JSON sizes sum. Integration test.
- [ ] **M2.7** Step-based progress for Helm (no bytes): `current_step =
      "chart 3 of 12 · redis-17.0.0.tgz"`. `total_bytes = 0`. Integration test.
- [ ] **M2.8** `go test ./...` green. Commit: `feat(jobs): sync progress tracking across all protocols`.

### Milestone M3 — Docker clone modal with progress  ☐ not started

**Goal:** rewrite the Docker "Pull External" stub to wire the real endpoint
with live progress.
**Dependencies:** M2
**Files touched:** `web/src/pages/repo/DockerRepoPage.tsx`,
`web/src/components/CloneImageDialog.tsx` (new — extracted from inline dialog),
`web/src/hooks/useJobProgress.ts` (new), `web/src/lib/api.ts`.

- [ ] **M3.1** Write `useJobProgress(jobId)` TanStack Query hook:
      polls `GET /jobs/{id}` every 500 ms while `status ∈ {pending, running}`,
      stops polling on done/failed. Unit test with mock server.
- [ ] **M3.2** Extract `CloneImageDialog` from DockerRepoPage. Three states:
      form → progress → result. Uses `useJobProgress`.
- [ ] **M3.3** Wire `POST /pull-external` mutation with fields from design:
      src, retag_as, cred_id, scan_override. Invalidate image list on success.
- [ ] **M3.4** Render progress bar with `ShadCN Progress` component (already
      in repo per Phase 6 primitives — verify; if not, `shadcn add progress`).
- [ ] **M3.5** Error path: surface `ErrorEnvelopeRenderer` inside modal with
      retry button.
- [ ] **M3.6** Update `DockerRepoPage.tsx` to open new modal from existing
      "Pull External" button; remove "API not yet connected" toast.
- [ ] **M3.7** Playwright spec: mock upstream, click clone, assert progress
      advances, assert success.
- [ ] **M3.8** `npm run build` green + Playwright green. Commit: `feat(ui): Docker clone-external modal with progress`.

### Milestone M4 — Mirror flag UI on 4 protocols  ☐ not started

**Goal:** mirror flag at creation + Sync Now button + mirror config in settings.
**Dependencies:** M2
**Files touched:** `web/src/components/CreateRepoDialog.tsx`,
`web/src/components/MirrorConfigSection.tsx` (new — shared widget),
`web/src/components/FilterWidget{Apt,Rpm,Pypi,Helm}.tsx` (new × 4),
`web/src/pages/repo/{AptRepoPage,RpmRepoPage,PypiRepoPage,HelmRepoPage}.tsx`,
`web/src/components/SyncNowButton.tsx` (new — shared),
`web/src/pages/settings/RepoSettingsTab.tsx` (extend).

- [ ] **M4.1** Write `MirrorConfigSection` component rendering the mirror
      checkbox + URL input + cred picker + scan toggle. Protocol-aware
      slot for filter widget.
- [ ] **M4.2** Write `FilterWidgetApt` — suite/components/arches/names inputs
      producing `SyncFilter` JSON.
- [ ] **M4.3** Write `FilterWidgetRpm` — arches/names.
- [ ] **M4.4** Write `FilterWidgetPypi` — project allow-list.
- [ ] **M4.5** Write `FilterWidgetHelm` — chart allow-list.
- [ ] **M4.6** Integrate `MirrorConfigSection` into `CreateRepoDialog`,
      visible only for APT/RPM/PyPI/Helm protocol selection. Validate on submit.
- [ ] **M4.7** Write shared `SyncNowButton` — renders button + progress bar
      when active (reuses `useJobProgress`). POSTs to `/sync` with empty body.
- [ ] **M4.8** Add `SyncNowButton` to AptRepoPage (visible only when
      `repo.is_mirror`). Playwright assertion.
- [ ] **M4.9** Add `SyncNowButton` to RpmRepoPage + Playwright.
- [ ] **M4.10** Add `SyncNowButton` to PypiRepoPage + Playwright.
- [ ] **M4.11** Add `SyncNowButton` to HelmRepoPage + Playwright.
- [ ] **M4.12** Extend `RepoSettingsTab` with "Mirror config" card
      (visible only when `repo.is_mirror`). URL readonly, filter editable via
      same widget, cred + scan toggle editable. Save via PATCH.
- [ ] **M4.13** `npm run build` green + Playwright green. Commit: `feat(ui): mirror-repo creation + sync now + settings config`.

### Milestone M5 — Upstream credentials CRUD UI  ☐ not started

**Goal:** wire the already-existing `/upstream-creds` REST CRUD to a UI
tab on ProjectSettingsPage.
**Dependencies:** M2 (not M1 strictly — could run in parallel with M1-4,
but simpler to queue last before tests)
**Files touched:** `web/src/pages/settings/ProjectSettingsPage.tsx`,
`web/src/components/UpstreamCredsTab.tsx` (new),
`web/src/components/UpstreamCredDialog.tsx` (new — add/edit).

- [ ] **M5.1** Write `UpstreamCredsTab`: table of creds (host, kind, username,
      created_at). Action: Add / Edit / Delete per row. List pulled via
      TanStack Query from `GET /projects/{name}/upstream-creds`.
- [ ] **M5.2** Write `UpstreamCredDialog`: fields host, kind (dropdown:
      docker / apt / rpm / pypi / helm), username, password, token.
      On edit, password/token are write-only (leave blank to keep existing).
- [ ] **M5.3** POST create, PATCH update, DELETE delete. Error handling via
      `ErrorEnvelopeRenderer`.
- [ ] **M5.4** Wire tab into `ProjectSettingsPage` tabs list.
- [ ] **M5.5** Playwright: create cred, edit it, use it from a mirror repo
      creation flow, delete it, assert mirror repo now fails sync with clean
      "credential missing" envelope.
- [ ] **M5.6** `npm run build` green + Playwright green. Commit: `feat(ui): upstream-credentials CRUD tab`.

### Milestone M6 — Integration tests, Playwright e2e, peer review  ☐ not started

**Goal:** full test coverage + an independent peer-review pass.
**Dependencies:** M1, M2, M3, M4, M5
**Files touched:** `internal/protocol/{deb,rpm,pypi,helm,oci}/*sync*_integration_test.go` (new),
`test/playwright/mirror.spec.ts` (new or extended).

- [ ] **M6.1** Write fake-upstream fixtures for APT (`httptest.NewServer`
      serving valid dists structure). Integration test: create mirror repo,
      trigger sync, assert packages in DB + on disk, assert idempotent.
- [ ] **M6.2** Fake-upstream fixture for RPM; integration test.
- [ ] **M6.3** Fake-upstream fixture for PyPI (PEP 691 JSON); integration test.
- [ ] **M6.4** Fake-upstream fixture for Helm (`index.yaml` + `.tgz`);
      integration test.
- [ ] **M6.5** Fake-registry fixture for OCI (reuse existing tooling);
      integration test for clone including progress assertions.
- [ ] **M6.6** Playwright spec covering: create APT mirror repo → sync now →
      progress advances → success → packages listed. Verify with headless run.
- [ ] **M6.7** Playwright spec: Docker clone modal, mock upstream, progress +
      success.
- [ ] **M6.8** Playwright spec: upload to mirror repo returns 403, surfaced
      in UI as error toast with `ErrorEnvelopeRenderer`.
- [ ] **M6.9** Run `make test` — all green. Run `make grep-cdn` — all green.
- [ ] **M6.10** Independent peer-review pass of the mirror feature.
      Focus on correctness (idempotency, leakage, race around sync_jobs,
      cred decryption paths) and any leftover review findings. Apply valid
      findings, discard noise.
- [ ] **M6.11** Commit: `test(mirror): integration, e2e, review findings applied`.
- [ ] **M6.12** Milestone marked complete in the project roadmap.

---

## Glossary — quick reference

- **Mirror repo** — a repo with `is_mirror=true`. Pulls from one upstream.
  Uploads disabled. Only APT/RPM/PyPI/Helm.
- **Clone** — Docker-only, per-click pulling a specific external image ref
  into any Docker repo (mirror or not).
- **Sync** — bulk mirror pull. Triggered by button or external cron curl.
  Always reads mirror config from repo for mirror repos.
- **Drift purge** — optional future feature (v1.2) that removes locally-held
  artifacts that upstream has dropped.
- **`auto_scan`** — existing per-repo flag; governs scans on upload.
- **`scan_on_sync`** — new per-repo flag; governs scans during a mirror sync.
  Default OFF.
