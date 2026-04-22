# Batch 11 — Git mirrors (NEW in v1.3+)

**Status:** ✅ Passed clean (after fixes)
**Prereqs:** Batch 10 ✅
**State produced for later batches:**
- `acme/git/click-mirror` mirroring a small public git repo (pallets/click per live test)
- Confirmed mirror badge, receive-pack 403 gate, LFS detection, sync progress

## Scope — what's new

From git log and GITMIRROR-01…11:
- `mirrorSupportedTypes` widened to include `git` (cc5fda6)
- Sync handler with PlainClone/Fetch + LFS detect + progress sink (5889581)
- `git_refs.ReplaceAllTx` for atomic prune+insert (dd0ecaa)
- Skip InitBare on mirror repo create (7920876) — mirror bare is created by first clone
- Receive-pack 403 gate on mirrors (10cf4ce / GITMIRROR-03)
- LFS batch 501 gate on all git repos (db2bbbb / GITMIRROR-04 / D-12)
- CreateRepoDialog widened for git mirror (d4c74c8 / GITMIRROR-09)
- Read-only badge on repo detail + hide push button (093cc37 / GITMIRROR-10)
- Playwright specs: create-repo-git-mirror.spec.ts + repo-detail-git-mirror-badge.spec.ts
- `/sync` allow-list widened for git + generalized 409 envelope (b659c9a)
- `mirror.not_yet_synced` 503 envelope on Git Smart HTTP before backend dispatch (21d064d)
- Live mirror E2E behind `live_git` tag (39d95a3)

## Pre-flight

- [ ] `git` CLI available
- [ ] A small reliable public repo for mirror (default: `https://github.com/pallets/click.git`)
- [ ] If the host cannot reach GitHub, use a local fixture git repo
- [ ] Logged in as alice
- [ ] Server log tail open

## Test cases

### 11.1 CreateRepoDialog — type=git, mirror=true
- [ ] Open CreateRepoDialog, pick type `Git`
- [ ] **Expected (GITMIRROR-09 / d4c74c8):** the mirror checkbox appears for Git; selecting it reveals:
  - Upstream URL field
  - Optional upstream credential dropdown
  - (no filters for git — it's all-or-nothing)
- [ ] Name: `click-mirror`; Upstream: `https://github.com/pallets/click.git`; no creds
- [ ] Submit
- [ ] **Expected:** 201; redirect to repo detail

### 11.2 Mirror repo initial state — "not yet synced" envelope
- [ ] Immediately (before first sync), try to clone: `git clone http://alice:<key>@localhost:18080/acme/git/click-mirror.git`
- [ ] **Expected (21d064d):** 503 with `mirror.not_yet_synced` envelope; git client surfaces the body in a readable way
- [ ] Audit log: structured WARN (not ERROR)

### 11.3 InitBare skipped on mirror create (7920876)
- [ ] Verify that the repo's on-disk bare dir does **not** exist yet OR is empty
- [ ] Path check: `ls $OMNIREPO_DATA_ROOT/git/acme/click-mirror.git/` — should be absent or empty before first sync
- [ ] This proves 7920876 holds

### 11.4 First sync
- [ ] Click Sync now
- [ ] **Expected (5889581):** progress stream shows PlainClone progress (counting objects, receiving, resolving deltas); final "Sync complete · N files · X MB"
- [ ] Bare repo now materialized on disk; refs populated
- [ ] Audit log events: `mirror.sync.start`, `mirror.sync.success`
- [ ] If any LFS object is detected upstream: `mirror.sync.lfs_detected` (EvtMirrorSyncLFSDetected) — **does not** fail the sync, just records

### 11.5 Clone after sync
- [ ] `git clone http://alice:<key>@localhost:18080/acme/git/click-mirror.git /tmp/click-mirror`
- [ ] **Expected:** clone succeeds; working tree matches upstream HEAD of click
- [ ] Refs (`git branch -a`) include at least `main`/`master` plus tags

### 11.6 Read-only mirror badge (GITMIRROR-10 / 093cc37)
- [ ] On `/projects/acme/git/click-mirror` UI:
  - "Read-only mirror" badge visible near the repo title
  - Push / upload actions hidden
  - Clone snippet visible; no push snippet
- [ ] Playwright spec `repo-detail-git-mirror-badge.spec.ts` should still pass after these interactions
- [ ] Console clean

### 11.7 Receive-pack 403 gate (GITMIRROR-03 / 10cf4ce)
- [ ] `cd /tmp/click-mirror && git commit --allow-empty -m "push attempt" && git push origin HEAD`
- [ ] **Expected:** 403 from Smart HTTP receive-pack; structured envelope
- [ ] Git client surfaces an error; local repo unchanged
- [ ] Audit log: `git.receive_pack.denied` on mirror

### 11.8 LFS batch gate on mirror (GITMIRROR-04 / db2bbbb)
- [ ] `curl -X POST http://alice:<key>@localhost:18080/acme/git/click-mirror.git/info/lfs/objects/batch -H 'Content-Type: application/vnd.git-lfs+json' -d '{"operation":"download","transfers":["basic"],"objects":[{"oid":"deadbeef","size":1}]}'`
- [ ] **Expected:** 501 `lfs.not_supported` envelope
- [ ] Same behavior as non-mirror repo (Batch 10 case 10.13) — confirmed from global LFS gate

### 11.9 Re-sync — atomic refs update (dd0ecaa)
- [ ] Wait (or simulate) an upstream change by syncing again
- [ ] **Expected:** second sync completes; refs updated via `ReplaceAllTx` (atomic prune+insert — no window where refs are missing)
- [ ] `GET /refs` always returns a consistent snapshot (not an empty list during sync)

### 11.10 Sync allow-list (b659c9a) / 409 envelope
- [ ] Start a sync, immediately click Sync again
- [ ] **Expected:** second request returns 409 with generalized envelope (not a duplicate job)
- [ ] Audit log: one sync started, one rejected

### 11.11 Credential-gated private mirror (optional)
- [ ] Create a git mirror against a private repo using the `gh` upstream credential
- [ ] First sync uses the stored credential (Basic over HTTPS)
- [ ] **Expected:** success if creds are valid; structured envelope if not
- [ ] If no private repo available, skip and document

### 11.12 Mirror vs non-mirror dual-path
- [ ] `git clone http://alice:<key>@localhost:18080/acme/git/click-mirror.git` (canonical)
- [ ] `git clone http://alice:<key>@localhost:18080/git/acme/click-mirror.git` (legacy)
- [ ] Both succeed with identical content (D-4 consistency)

### 11.13 Soft-delete + restore a mirror repo
- [ ] Delete `click-mirror` → Trash
- [ ] Restore via `/admin/trash` (full test in Batch 14)
- [ ] After restore: mirror metadata preserved; new sync succeeds from same upstream

### 11.14 Live test: `make test-live-git`
- [ ] `make test-live-git` from repo root
- [ ] **Expected:** passes green (Playwright spec `create-repo-git-mirror.spec.ts` + `repo-detail-git-mirror-badge.spec.ts` + the live `pallets/click` mirror spec behind `live_git` tag)
- [ ] If fails, paste output into finding

### 11.15 Console + network sweep
- [ ] Create dialog, repo detail (with badge), sync detail, settings
- [ ] Zero errors/warnings
- [ ] Outbound fetches go only to the configured upstream (no other external origins)

## Findings

### F-11.1 — Creating a git mirror returned 400 "mirror_filter JSON does not match the protocol SyncFilter shape" (blocker)
- **Symptom:** POST `/api/v1/projects/{n}/repos` with `type=git, is_mirror=true` (from UI or REST) returned 400. UI dialog surfaced the validation envelope; the repo row was never created.
- **Root cause:** `validateMirrorFilter` in `internal/api/mirror_validate.go` switch had cases for `deb`/`rpm`/`pypi`/`helm` only; default branch rejected everything else. Git is in `mirrorSupportedTypes` (MIRROR_PROTOCOLS) but had no case. Frontend `CreateRepoDialog` always posted `mirror_filter: {}` regardless of protocol → hit the default branch.
- **Fix:** `f877e91` —
  - Frontend: `CreateRepoDialog.tsx` omits `mirror_filter` when `repoType==='git'` (git mirrors are all-refs; no filter API per GITMIRROR-01).
  - Backend: add `case "git":` that accepts the empty-object placeholder `{}` only (struct{} + DisallowUnknownFields), normalises to nil.
- **Retest:** ✅ mirror create now returns 200 via both UI and REST.
- **Codex verdict:** Clean (Q1).

### F-11.3 — InitBare-skip for mirrors never actually shipped (real-issue)
- **Symptom:** Commit 7920876 added `*git.Handler.OnRepoCreate` with a `repoIsMirror` short-circuit that skips InitBare + HEAD-seed for mirror repos. But on disk, a freshly-created mirror DID have a full bare skeleton (HEAD, config, objects/, refs/) — 11.3 test failed immediately.
- **Root cause:** The RepoCreateHook wired into `api.Deps` is `composedRepoCreateHook` in `internal/app/app.go`, which has its OWN inline git block that unconditionally calls InitBare. The OnRepoCreate method on git.Handler is dead code — never registered anywhere.
- **Consequences (cascading):**
  - On-disk bare skeleton was already populated → handler.go:193 `mirror.not_yet_synced` 503 guard never fired (F-11.2). Pre-sync clone returned 200 with an empty upload-pack advertisement.
  - First `PlainCloneContext` was fighting `ErrTargetDirNotEmpty` — masked because pallets/click happens to tolerate the bare remnants, would bite on larger mirrors.
- **Fix:** `f877e91` — port the `is_mirror` check from OnRepoCreate into the inline block in `composedRepoCreateHook`. Reads `is_mirror` from the same writer tx (SQLite WAL visibility handled correctly per admin_phase1.go call site).
- **Retest:** ✅
  - Mirror create: no `<repo>.git/` on disk
  - Pre-sync clone: 503 `mirror.not_yet_synced` envelope returned as designed
  - First sync: 65 MB bare repo populated cleanly
- **Codex verdict:** Clean on fix (Q2, Q3, Q4 noise) — noted that dead OnRepoCreate method should be cleaned up pre-v1.0 (tracked for batch-15 cross-cutting).

### F-11 Codex follow-up — unsynced git mirror was unrestorable from trash (real-issue, Codex Q6)
- **Symptom:** Deleting a git mirror before its first sync left the DB row soft-deleted but created NO trash sidecar (Trash.Move silently swallowed `os.ErrNotExist`). The row was orphaned — admin_trash.handleRestoreTrash had no entry to target.
- **Root cause:** Historically `Trash.Move` always expected a source directory to rename. admin_phase1.handleDeleteRepo tolerated `ErrNotExist` as "tree never existed" but the DB row was already flipped to soft-deleted. Restore was then impossible.
- **Fix:** `1688974` — `Trash.Move` now writes a metadata-only sidecar when the source is absent (`trashMetadata.Empty=true`) and returns `os.ErrNotExist` as a sentinel. `admin_trash.handleRestoreTrash` branches on `e.Empty` — restores the DB row, cleans the holder dir, no rename attempt. Downstream repo list shows the restored mirror with upstream_url intact; subsequent sync populates content normally.
- **Retest:** ✅ create `empty-git-mirror` → delete pre-sync → trash list includes entry → POST restore → 200 + DB row back + upstream_url preserved.
- **Codex verdict:** Applied.

### F-11 Codex follow-up — stale e2e assertion (real-issue, Codex Q5)
- **Symptom:** Existing `web/e2e/create-repo-git-mirror.spec.ts:206` asserted `body.mirror_filter === {}` for git mirror POST body — would fail after the F-11.1 frontend fix omitted the field entirely.
- **Fix:** `1688974` — update assertion to `expect(body.mirror_filter).toBeUndefined()`.

## Sign-off

- [x] All cases passed
- [x] Final state:
  - [x] `acme/git/click-mirror` synced at least once successfully (65 MB bare repo, 72 refs)
  - [x] `empty-git-mirror` exercises the metadata-only trash branch
  - [x] Badge visible ("Read-only mirror"); push denied (403 mirror.push_rejected); LFS blocked (501 lfs.not_supported)
  - [x] Pre-sync 503 `mirror.not_yet_synced` envelope works as designed
  - [x] Dual-mount canonical + legacy paths return identical SHAs
  - [x] Concurrent sync returns 202 then 409 `mirror.sync.in_flight`
  - [x] Refs stay consistent (72) during re-sync via atomic ReplaceAllTx
  - [x] `make test-live-git` green (13.5s against pallets/click)
- [x] All F-11.* closed (F-11.1, F-11.3, + two Codex follow-ups)
- [x] **Codex verify complete** — 0 blockers, 2 real-issues found + fixed, Q1-Q4 clean
- [x] README.md batch 11 status flipped to ✅
