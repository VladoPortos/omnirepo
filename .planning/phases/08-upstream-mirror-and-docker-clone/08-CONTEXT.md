# Phase 8: Upstream Mirror & Docker Clone - Context

**Gathered:** 2026-04-19
**Status:** Ready for planning
**Source:** Brainstorming session 2026-04-19 → design spec at `docs/superpowers/specs/2026-04-19-upstream-mirror-design.md`

<domain>
## Phase Boundary

Wire the UI for OmniRepo's already-shipped upstream-mirror backend (sync handlers
for APT/RPM/PyPI/Helm; pull-external for OCI; encrypted upstream credentials;
jobs pool) so the v1.1 public release lets operators point a repo at an upstream
archive (Ubuntu focal main/universe amd64 etc.) or clone individual Docker
images on demand.

**Includes:** (a) schema migration adding `is_mirror`, `mirror_upstream_url`,
`mirror_filter_json`, `mirror_cred_id`, `scan_on_sync` to `repos` and
`progress_bytes`, `total_bytes`, `current_step` to `sync_jobs`;
(b) mirror-aware `/sync` endpoint + concurrency guard; (c) upload-reject
middleware on every protocol's upload path for mirror repos; (d) sync-job
progress tracking across all 5 protocols; (e) Docker clone modal with live
progress bar + retag + scan override; (f) CreateRepoDialog mirror section
for APT/RPM/PyPI/Helm; (g) Sync Now buttons + RepoSettingsTab mirror card;
(h) ProjectSettingsPage upstream-credentials CRUD tab.

**Excludes (deferred to v1.2):** scheduler / cron / recurring sync;
drift purge / stale-artifact tracking; Git mirror; pull-through proxy cache;
air-gap enforcement of `AllowExternalActions`; Raw / S3 upstream support;
change-upstream-URL flow.

</domain>

<decisions>
## Implementation Decisions

### Scope (locked)
- **D-01:** Wire UI for all 5 protocols (Docker + APT/RPM/PyPI/Helm). Raw/S3/Git out of scope.
- **D-02:** APT/RPM/PyPI/Helm repos get `is_mirror` flag at **creation time**. URL is immutable post-creation; filter + scan_on_sync are editable. Deleting the repo and recreating is the escape hatch for URL changes.
- **D-03:** Docker model is different — NO `is_mirror` flag. Per-click "Clone external image" modal with source ref, optional retag, optional cred, optional scan override. Modal can be invoked repeatedly; each click clones one image into the chosen Docker repo.
- **D-04:** Uploads to mirror repos return 403 envelope `code="repo_is_mirror"` from every protocol's upload handler. Prevents hybrid push+pull conflicts.
- **D-05:** Deletion semantics = **accumulator only**. Re-sync adds new, never removes upstream-dropped artifacts. No drift purge in v1.1.
- **D-06:** Cron trigger reuses existing `POST /sync` with API key. No new endpoint. Empty body for mirror repos (reads config from repo row).
- **D-07:** Concurrency — reject a second sync on the same repo while one is running with 409 `sync_already_running`. Same applies to OCI pull-external scoped per-repo.

### Scan policy (locked)
- **D-08:** New `scan_on_sync` column on `repos`, **default OFF**. Only governs bulk mirror syncs for APT/RPM/PyPI/Helm. Rationale: syncing a full Ubuntu focal suite may ingest thousands of packages; scanning all of them on first sync is potentially hours of Trivy work on constrained hardware. Operator flips it on once the mirror is healthy.
- **D-09:** Docker per-click clone reuses existing `auto_scan` repo flag, exposes a per-modal **override checkbox** in the clone dialog. Separate from `scan_on_sync`.

### Progress (locked)
- **D-10:** Extend `sync_jobs` with `progress_bytes`, `total_bytes`, `current_step`. UI polls `GET /api/v1/jobs/{id}` every 500 ms while a modal is open.
- **D-11:** Byte-level progress for OCI (manifest tells us totals), APT (Packages `Size:` sum), RPM (primary.xml `size package="…"` sum), PyPI (PEP 691 JSON sizes sum). **Step-based** progress for Helm only (index.yaml lacks chart sizes) — current_step = "chart 3 of 12 · redis-17.0.0.tgz"; total_bytes = 0.
- **D-12:** Progress writes throttled: only persist when changed and ≥ 200 ms since last write (helper in `internal/jobs/progress.go`, new file).

### Data model (locked)
- **D-13:** One additive migration adding 5 columns to `repos` and 3 columns to `sync_jobs`. `mirror_cred_id REFERENCES upstream_creds(id) ON DELETE SET NULL` — orphans self-heal with clear "credential missing" envelope at next sync.
- **D-14:** Application-layer validation enforces: (a) `is_mirror=1` only valid when `type IN ('deb','rpm','pypi','helm')`; (b) if `is_mirror=1`, `mirror_upstream_url` must be valid http(s) and `mirror_filter_json` must parse as the protocol's `SyncFilter`; (c) if `is_mirror=0`, the four mirror columns are ignored (left null).

### UI surfaces (locked)
- **D-15:** `CreateRepoDialog` gains conditional "This repo is a mirror of an upstream" section, visible only when selected protocol is in {deb, rpm, pypi, helm}. Form fields: upstream URL, protocol-specific filter widget, cred picker, scan_on_sync toggle.
- **D-16:** `DockerRepoPage` rewrites the existing "Pull External" stub dialog (currently toasts "API not yet connected") into a real clone modal — form state → progress state → result state. Progress bar shows `Layer 3/7 · 42 MiB / 103 MiB · 41%`.
- **D-17:** AptRepoPage / RpmRepoPage / PypiRepoPage / HelmRepoPage gain single "Sync now" affordance visible only when `is_mirror=true`. Shared `SyncNowButton` component.
- **D-18:** `RepoSettingsTab` gains "Mirror config" card on `is_mirror=true` repos. URL readonly (CopyInline), filter editable via same widget as CreateRepoDialog, cred + scan_on_sync editable. Save via PATCH `/repos/{type}/{repo}`.
- **D-19:** `ProjectSettingsPage` gains "Upstream credentials" tab wrapping the already-mounted `/api/v1/projects/{name}/upstream-creds` CRUD. Secrets never echoed on response.

### Tests + verification (locked)
- **D-20:** Each of the 5 protocols gets a fake-upstream integration test using `httptest.NewServer`. Tests assert idempotency (run same sync twice, row counts stable).
- **D-21:** Reject-upload test per protocol: create is_mirror=1 repo, attempt upload, assert 403 envelope with `code="repo_is_mirror"`.
- **D-22:** Concurrency test: enqueue two syncs back-to-back, assert second returns 409.
- **D-23:** Playwright e2e: create APT mirror repo → Sync Now → progress advances → success → packages listed. Docker clone modal: mock upstream, progress + success. Upload to mirror repo returns 403 surfaced via `ErrorEnvelopeRenderer` toast.
- **D-24:** Codex rescue pass at phase close per CLAUDE.md global rule. Invoked via `Agent(subagent_type="codex:codex-rescue", ...)`.

### Plan decomposition (spec-locked, maps 1:1 to design spec milestones M1–M6)
- **Plan 08-01 (M1):** Backend foundation — schema + mirror-aware sync + upload-reject + concurrency guard.
- **Plan 08-02 (M2):** Progress tracking — writer helper + jobs endpoint extension + handler wraps across all 5 protocols.
- **Plan 08-03 (M3):** Docker clone modal with progress + retag + scan override.
- **Plan 08-04 (M4):** Mirror flag UI — CreateRepoDialog + SyncNowButton on 4 protocol pages + RepoSettingsTab mirror card.
- **Plan 08-05 (M5):** Upstream credentials CRUD UI tab on ProjectSettingsPage.
- **Plan 08-06 (M6):** Integration tests (5 fake upstreams) + Playwright e2e + Codex rescue.

### Claude's Discretion
- Exact file names for new components (CloneImageDialog.tsx vs PullExternalModal.tsx etc.) — pick conventional names consistent with existing `web/src/components/`.
- Exact text of help-text strings in UI.
- REQ-ID numbering (MIRROR-01..NN) — assign sequentially as plans are written.
- Whether shadcn `Progress` primitive is already in the tree; if not, `shadcn add progress` in M3.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Design spec (authoritative)
- `docs/superpowers/specs/2026-04-19-upstream-mirror-design.md` — full design, architecture diagrams, data model, API changes, UI surfaces, test strategy, risks, implementation plan with 54 checkbox tasks across 6 milestones. Every decision in the `<decisions>` block above is sourced from this spec.

### Backend references (read during planning to verify claims)
- `internal/httpx/sync_rest.go` (especially lines 74–234) — current generic /sync endpoint shape; M1 extends this to be mirror-aware.
- `internal/protocol/oci/pull_external.go` — current Docker pull flow; M2+M3 wrap with progress + modal.
- `internal/protocol/deb/sync_handler.go` + `internal/protocol/deb/upstream_parse.go` — APT sync + filter shape (Names, Globs, Suites, Components, Arches).
- `internal/protocol/rpm/sync_handler.go` — RPM sync.
- `internal/protocol/pypi/sync_handler.go` — PyPI sync.
- `internal/protocol/helm/sync_handler.go` — Helm sync.
- `internal/metadata/upstream_creds.go` — encrypted creds store (AES-GCM-256).
- `internal/api/upstream_creds.go` — CRUD REST endpoints already mounted.
- `internal/jobs/pool.go`, `internal/jobs/handlers.go`, `internal/metadata/sync_jobs.go` — jobs pool + queue.
- `internal/api/repos.go` + `internal/api/repos_list.go` + `internal/metadata/repos.go` — repo CRUD, to be extended with mirror columns.

### Frontend references
- `web/src/pages/repo/DockerRepoPage.tsx:323–467` — existing stub "Pull External" dialog (M3 rewrites).
- `web/src/pages/repo/{Apt,Rpm,Pypi,Helm}RepoPage.tsx` — pages that gain "Sync now" affordance in M4.
- `web/src/components/CreateRepoDialog.tsx` — extended in M4.
- `web/src/pages/settings/RepoSettingsTab.tsx` — extended in M4.
- `web/src/pages/settings/ProjectSettingsPage.tsx` — gains "Upstream credentials" tab in M5.
- `web/src/components/common/EmptyState.tsx`, `web/src/components/StatusBadge.tsx`, `web/src/components/common/CopyInline.tsx`, `web/src/components/CopyButton.tsx`, `web/src/components/common/SkeletonCard.tsx` — Phase 6 primitives, reused verbatim.
- `web/src/lib/api.ts` + `web/src/lib/queries.ts` — TanStack Query hooks; M3 adds `useJobProgress`, M5 adds upstream-creds hooks.

### Project patterns
- `internal/httperr/*.go` — error envelope helpers. All new handler error paths use `httperr.Forbidden`, `httperr.Conflict`, etc. per Phase 6 protocol-redaction rule.
- `internal/httpx/middleware_*.go` — middleware placement; M1 adds `mirror_guard.go` in this pattern.
- `internal/metadata/migrations/*.sql` — migration file naming (sequence-prefixed). M1 adds `00NN_mirror_and_progress.sql` with the next sequence number.
- `web/e2e/*.spec.ts` — existing Playwright spec structure. M6 adds `mirror.spec.ts` (or extends an appropriate existing spec).

</canonical_refs>

<specifics>
## Specific Ideas

### Migration sequence (D-13)
```sql
ALTER TABLE repos ADD COLUMN is_mirror              INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repos ADD COLUMN mirror_upstream_url    TEXT;
ALTER TABLE repos ADD COLUMN mirror_filter_json     TEXT;
ALTER TABLE repos ADD COLUMN mirror_cred_id         INTEGER REFERENCES upstream_creds(id) ON DELETE SET NULL;
ALTER TABLE repos ADD COLUMN scan_on_sync           INTEGER NOT NULL DEFAULT 0;

ALTER TABLE sync_jobs ADD COLUMN progress_bytes     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_jobs ADD COLUMN total_bytes        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_jobs ADD COLUMN current_step       TEXT;
```

### ProgressWriter helper (D-12)
```go
// internal/jobs/progress.go
type ProgressWriter struct {
    jobID   int64
    repo    metadata.SyncJobsRepo
    lastAt  time.Time
    lastStep string
    lastDone int64
    lastTotal int64
}
// Set is throttled: persists only if ≥ 200 ms since last write AND values changed.
func (p *ProgressWriter) Set(ctx context.Context, step string, done, total int64) error { ... }
```

### Docker clone modal progress render (D-16)
```
Pulling library/nginx:1.27 …
Layer 3 of 7 · 42 MiB / 103 MiB · 41%
[████████████░░░░░░░░░░░░░░░░░░]
                                        [Cancel pull]
```

### Sync-endpoint branch logic (D-02, D-06, D-14)
```go
// if repo.is_mirror && bodyEmpty: read config from repo, enqueue
// if repo.is_mirror && bodyNotEmpty: 400 mirror_overrides_not_allowed
// if !repo.is_mirror: keep today's body-driven path (back-compat for tests + API users who prefer stateless form)
```

</specifics>

<deferred>
## Deferred Ideas (not this phase)

- **Drift purge / stale-artifact tracking** (v1.2) — option C from brainstorm. Requires `mirror_sources` table + per-artifact `last_seen_at` + purge endpoint + UI stale badge. Explicitly deferred to keep v1.1 shipping on time.
- **Recurring / scheduled sync** (v2.0 per user direction) — external cron curl satisfies the v1.1 story.
- **Git mirror** (v1.2) — go-git backend needed; no upstream support scaffolded yet.
- **Pull-through proxy cache semantics** — lazy-fetch on first client request. Different architecture.
- **Air-gap enforcement** — `AllowExternalActions` config flag exists but unused. Enforcing it against sync endpoints is v1.2 work.
- **Raw / S3 upstream support** — no enumerable upstream index; feature doesn't fit.
- **Change-upstream-URL flow** — forbidden by design; delete+recreate is the escape hatch.

</deferred>

---

*Phase: 08-upstream-mirror-and-docker-clone*
*Context gathered: 2026-04-19 from brainstorming session → design spec*
