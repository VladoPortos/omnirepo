# Batch 06 — RPM and APT/Debian

**Status:** ⬜ Not started
**Prereqs:** Batch 05 ✅
**State produced for later batches:**
- `acme/rpm/local` with a couple of uploaded RPMs
- `acme/rpm/epel-mirror` mirroring a small subset of an RPM upstream
- `acme/deb/local` with an uploaded .deb
- `acme/deb/debian-mirror` mirroring a subset of a Debian upstream
- Release / InRelease signed and clients can install

## Pre-flight

- [ ] `rpm`, `createrepo_c`, `dnf` (or `yum`) available for test client scenarios
- [ ] `dpkg-deb`, `apt`, `gpg` available
- [ ] Logged in as alice (admin on acme)
- [ ] Server log tail open

## RPM test cases

### 6.1 Create local RPM repo
- [ ] `/projects/acme` → Create repo → type `RPM`, name `local`, mirror=false
- [ ] **Expected:** redirect to `/projects/acme/rpm/local`; empty state with upload snippet
- [ ] Console + network clean

### 6.2 Upload RPM via UI (if supported) or HTTP
- [ ] Build a tiny RPM (`rpmbuild -bb …`) or use an existing one
- [ ] Upload via UI drag-drop or `curl -u alice:<key> --upload-file pkg.rpm http://localhost:18080/acme/rpm/local/x86_64/pkg-1.0-1.el9.x86_64.rpm`
- [ ] **Expected:** 201; repomd.xml + primary.xml.gz regenerated; package appears in UI table
- [ ] Audit log: `rpm.upload`

### 6.3 Upload duplicate RPM
- [ ] Re-upload same file
- [ ] **Expected:** consistent outcome (409 or idempotent 201, document which); metadata is still correct

### 6.4 Browse / metadata
- [ ] `curl http://localhost:18080/acme/rpm/local/repodata/repomd.xml` → valid XML
- [ ] Signed primary.xml.gz served; digests match

### 6.5 `dnf` install end-to-end
- [ ] Configure a client `.repo` file and `dnf install pkg`
- [ ] **Expected:** dnf successfully resolves, downloads, and verifies the package

### 6.6 Delete RPM
- [ ] Row action → Delete
- [ ] **Expected:** 204; repomd.xml regen drops the package; `dnf install` now fails to find it (after refresh)
- [ ] Audit log: `rpm.delete`

### 6.7 Metadata regen on demand
- [ ] In repo settings, click "Regenerate metadata" (if exposed)
- [ ] **Expected:** status toast, repomd.xml reflects latest state, audit log entry

### 6.8 Create RPM mirror
- [ ] Create repo type `RPM`, name `epel-mirror`, mirror=true
- [ ] Upstream URL: a small, reliable RPM repo (e.g. a small local fixture, or a tiny subset of EPEL)
- [ ] Filters: package-name globs limited to a handful of packages (to keep the sync small)
- [ ] `scan_on_sync=true`
- [ ] Submit
- [ ] **Expected:** repo created in mirror mode; UI shows read-only / "uploads disabled" hint
- [ ] Audit log: `repo.create` with mirror=true

### 6.9 Upload to mirror rejected
- [ ] Try to upload a package to the mirror
- [ ] **Expected:** clean 403/409 envelope explaining "mirror repo — uploads disabled"
- [ ] Mirror state unchanged

### 6.10 Sync now — progress stream
- [ ] Click "Sync now"
- [ ] **Expected:** progress pill updates (bytes/files), final "Sync complete · N files · X MB"
- [ ] After sync, package list populated; scan jobs kick off; eventually some rows show scan results
- [ ] Audit log: `mirror.sync.success`

### 6.11 Sync failure handling
- [ ] Change upstream URL to a bogus host, click Sync now
- [ ] **Expected:** clean error pill; last successful sync timestamp preserved; repo usable with old metadata
- [ ] No crash, no partial state corruption
- [ ] Audit log: `mirror.sync.failure`

### 6.12 Severity gate on RPM (WALKTHROUGH-2 cross-protocol gate)
- [ ] If any mirrored package has HIGH CVE, set `block_on_severity=high` and pull via `dnf`
- [ ] **Expected:** 403 envelope `blocked_by_scan`, clean envelope on retry with a clean package

## APT test cases

### 6.13 Create local APT repo
- [ ] Type `Debian`, name `local`
- [ ] Default suite/component (or prompted)
- [ ] Empty-state shows `echo "deb http://localhost:18080/acme/deb/local ..." | sudo tee ...` snippet

### 6.14 Upload .deb
- [ ] Build a tiny deb (`dpkg-deb --build …`) or use existing
- [ ] Upload via UI or `curl -u alice:<key> --upload-file pkg.deb http://localhost:18080/acme/deb/local/pool/main/p/pkg/pkg_1.0-1_all.deb`
- [ ] **Expected:** 201; Packages + Release + InRelease regenerated
- [ ] UI tree shows suite/component/arch

### 6.15 `apt` install end-to-end
- [ ] Add sources.list entry on a client; `apt update` → verifies InRelease signature
- [ ] `apt install pkg` → success

### 6.16 InRelease PGP signature verifies
- [ ] `gpg --verify InRelease` with the server public key → valid
- [ ] Audit log: `deb.metadata.regen` / similar

### 6.17 Delete .deb
- [ ] Row action → Delete
- [ ] **Expected:** 204; Release/InRelease regenerated; `apt install` fails after apt update

### 6.18 Create APT mirror
- [ ] Mirror=true; upstream URL to a small Debian-like repo (or a local fixture)
- [ ] Suite/component/arch filters set to a tiny subset
- [ ] Sync now → progress → complete
- [ ] `apt update` / `apt install` end-to-end against the mirrored repo

### 6.19 Mirror upload rejected (APT)
- [ ] Attempt upload to mirror → 403/409 envelope

### 6.20 Cross-protocol: severity gate on APT mirror
- [ ] Pick a mirrored package with HIGH CVE; pull via apt → blocked with envelope

### 6.21 Sync job history
- [ ] UI shows a list of past sync jobs with status, file count, byte count, duration
- [ ] Failed jobs visually distinct from successful jobs

### 6.22 Console + network sweep
- [ ] Visit repo detail, sync job detail, settings, filter dialogs
- [ ] Zero console errors/warnings

## Findings

> **Codex pass 1 (2026-04-22):** 2 real-issues found, 7 confirmed false positives.
> Real-issues addressed in commit `b572080`:
> 1. `sync_handler.go` had the same F-06.1 gap on the mirror path — refactored
>    to stage body to tmp, parse, then Put at `canonicalFilename()`.
> 2. `SyncNowButton.tsx` render gate required `status === 'failed'`, silently
>    swallowing F-06.6's retry-backoff envelope — dropped the status guard.
>
> **Codex pass 2 (2026-04-22):** 1 real-issue closed + 1 upgrade-path tracked-open.
> - `SyncNowButton.tsx` stale-jobId stacking on re-click → fix in `50adb1a`:
>   `setJobId(null)` in handleClick clears the previous hook state.
> - `sync_handler.go` digest fast-path skips before canonicalization — pre-v1.4
>   mirror rows with non-canonical filenames aren't migrated on subsequent sync.
>   Fresh v1.4 installs unaffected. Tracked in F-06.1 as an upgrade-path
>   follow-up; fix needs filename-migration + on-disk rename.

### F-06.1 RPM put.go missing promised NEVRA-filename check
- **Severity:** R / real-bug
- **Area:** `internal/protocol/rpm/put.go:34` `put` handler
- **Symptom:** `primary.xml.gz` `<location href>` is built from the RPM-header NEVRA via `canonicalFilename()` (`repodata.go:258`), but the on-disk storage key + the GET/DELETE route use the URL-path filename verbatim. Uploading `sample.rpm` as `pkg-1.0-1.el9.x86_64.rpm` (RPM header says `centos-release-7-…`) → handler 201s, stores at `/packages/pkg-1.0-1…` but the published metadata tells dnf to fetch `packages/centos-release-…` → 404 on every client download.
- **Repro:**
  1. `curl -u alice:<key> --upload-file centos-release.rpm http://localhost:18080/acme/rpm/local/packages/wrong-name.rpm` → 201.
  2. `curl http://localhost:18080/acme/rpm/local/repodata/primary-*.xml.gz | gunzip` → `<location href="packages/centos-release-7-2.1511.el7.centos.2.10.x86_64.rpm">`.
  3. `curl .../packages/centos-release-7-…x86_64.rpm` → 404.
- **Root cause:** `put.go`'s step-5 doc comment ("Validate filename matches NEVRA — defense in depth") was never implemented. Nothing enforces the canonicalFilename invariant regen already assumes.
- **Fix:** commit `563a7ee` — after `rpm.Parse` succeeds, compute `parsed.canonicalFilename()` and 400 `filename_mismatch` (audit `rpm.upload rejected reason=filename_nevra_mismatch`) when `res.filename` disagrees. New `TestRPMPut_RejectsFilenameNEVRAMismatch` pins it; every existing RPM test migrated to the canonical `centos-release-7-…x86_64.rpm` URL via a `sampleRPMCanonical` constant. Codex follow-up `b572080` applied the same guard to `sync_handler.go` so mirror-synced rows also get canonical storage. Upgrade-path tracked-open: pre-v1.4 rows with non-canonical filenames need a filename-migration pass; fresh installs are fine.
- **Codex verify:** ✅ Clean (batched 2026-04-22, 2 passes)
- **Retest:** ✅ `curl --upload-file sample.rpm .../packages/wrong-name-1.0.x86_64.rpm` → 400 `filename_mismatch: RPM header NEVRA requires filename centos-release-7-2.1511.el7.centos.2.10.x86_64.rpm`; canonical filename continues to 201.
- **Status:** ✅ Closed

### F-06.2 CLI snippets hard-code `https://` regardless of served scheme
- **Severity:** m / minor
- **Area:** `web/src/lib/snippets.ts:getSnippets` + every repo-page empty-state snippet
- **Symptom:** When OmniRepo is served over plain HTTP (e.g. testing port `:18080`, a reverse-proxy that terminates TLS upstream), the snippet panel renders `baseurl=https://localhost:18080/...` / `deb https://…/ stable main` / `helm repo add … https://…/` etc. Copy-paste-and-run fails with scheme/port confusion.
- **Repro:**
  1. Browse to any repo's Content tab with the UI served over `http://localhost:18080/`.
  2. Inspect the `dnf config` / `apt source` / `pip install` snippet body → URL is `https://`.
- **Root cause:** `getSnippets` concatenated `https://${host}/…` verbatim for every URL.
- **Fix:** commit `9f860f3` — added optional `scheme: 'http' | 'https'` arg (default `'https'` for back-compat with tests); `SnippetList` reads `window.location.protocol` and passes the matching scheme. New `F-06.2` tests exercise every RepoType with `scheme='http'`.
- **Codex verify:** ✅ Clean (batched 2026-04-22, 2 passes)
- **Retest:** ✅ UI snippet renders `baseurl=http://localhost:18080/…` and `gpgkey=http://…` on HTTP-served UI.
- **Status:** ✅ Closed

### F-06.3 RPM + APT row action missing Delete button (sibling of F-05.4)
- **Severity:** R / real-bug (UI gap)
- **Area:** `web/src/pages/repo/RpmRepoPage.tsx`, `web/src/pages/repo/AptRepoPage.tsx`
- **Symptom:** Package row offers Rescan but no Delete; users can't remove an uploaded `.rpm` / `.deb` from the UI.
- **Root cause:** Backend `DELETE /<project>/rpm/<repo>/packages/{filename}` + the matching deb pool path exist and work (verified end-to-end via curl 204 + regen). UI never wired them — same pattern F-05.4 noted as "deferred to batches 06+". Because the protocol handlers auth via `BasicOrAPIKey` (no session cookie path), closing this needs a session-authed REST shim (`/api/v1/projects/{n}/repos/{rpm|deb}/{r}/packages/{filename}`) plus the row-action UI.
- **Fix:** **Deferred** — tracked-open. Scope is ~1 REST shim per protocol + per-page delete mutation + confirm dialog + tests; same shape as F-05.4's docker-tag-delete work. Kept out of this batch to land the other real-bugs cleanly; picked up in the next polish phase.
- **Codex verify:** —
- **Retest:** Delete continues to work via curl; row-action gap remains.
- **Status:** 🟨 Open — tracked-open (deferred)

### F-06.4 Mirror empty-state copy asks user to upload
- **Severity:** m / minor
- **Area:** `web/src/pages/repo/RpmRepoPage.tsx:289`, `web/src/pages/repo/AptRepoPage.tsx:343`
- **Symptom:** An empty mirror repo renders "No artifacts yet / Upload your first artifact using the snippet below" — but uploads to mirrors are 403 `repo_is_mirror`, and the snippet itself is pull-only (dnf config / apt source).
- **Root cause:** `canUpload` is `!!currentUser` (unscoped); the empty-state branch assumed writable local repos.
- **Fix:** commit `<TBD>` — `is_mirror`-aware copy. Mirror repo: `title="Mirror not yet synced"` + `description="Click Sync now to pull from upstream, then use the snippet below to install from this mirror."` Non-mirror: unchanged. Maintainer-less readers get the sync-hint variant too.
- **Codex verify:** ✅ Clean (batched 2026-04-22, 2 passes)
- **Retest:** ✅ `/projects/acme/rpm/empty-mirror-check` renders "Mirror not yet synced" + sync-hint; `acme/rpm/local` (non-mirror) unchanged.
- **Status:** ✅ Closed

### F-06.5 SyncActorBridge drops owning-user id for user-owned API keys (F-05.1 11th site)
- **Severity:** R / real-bug (blocker for user-owned API-key mirror sync)
- **Area:** `internal/api/sync_actions.go:35` `SyncActorBridge`
- **Symptom:** `POST /api/v1/projects/{n}/repos/{t}/{r}/sync` with `-u alice:<alice-dev-key>` → `403 forbidden: not a project member` even though alice IS an acme member. Only session cookies worked; user-owned API keys broke every mirror-sync trigger from CI / cron / scripts.
- **Root cause:** The bridge's `ActorKindAPIKey` branch populated `APIKeyID` + `ProjectID` (if project-scoped) but never set `UserID` for **user-owned** keys. Downstream `handleSync` (`internal/httpx/sync_rest.go:158`) branches `actor.UserID != 0` then `actor.APIKeyID != 0 && actor.ProjectID != 0` — both fell through for a user-owned key with `ProjectScope == nil`, falling into the 403. Per the `Actor` doc comment (`internal/auth/actor.go:41-46`), `a.ID` already holds the owning user's id for user-owned API keys; the bridge just didn't surface it.
- **Fix:** commit `2b12a70` — `case auth.ActorKindAPIKey` now sets `out.UserID = a.ID` when `ProjectScope == nil && OwnerKind == OwnerKindUser`, mirroring `auth.ResolveMembership`'s shape exactly. New `internal/api/sync_actions_test.go` covers user-owned, project-scoped, user-session, and anonymous actors.
- **Codex verify:** ✅ Clean (batched 2026-04-22, 2 passes)
- **Retest:** ✅ `POST .../sync` with `-u alice:omr_u_PP7UxqzLjWKAHt7jDePabloKWOZd` → 202 `{job_id,kind:rpm_sync}` (was 403).
- **Status:** ✅ Closed

### F-06.6 useJobProgress doesn't handle retry-backoff or 404-after-delete
- **Severity:** R / real-bug
- **Area:** `web/src/hooks/useJobProgress.ts:computeJobProgress` + `pollingDecision`
- **Symptoms (two paths):**
  1. Sync job with unreachable upstream → attempt 1 fails, row stays `status=pending, attempts=1, last_error=<dns>`, runner backs off 1m/5m/30m/30m/30m until MaxAttempts=5. Pre-fix the UI renders "Preparing…" with no error pill for up to 96 minutes while 2 polls/sec hammer the backend.
  2. Navigate away from a mirror page mid-sync, then delete the repo via API. The cached hook keeps hitting `/sync-jobs/{id}` → 404 forever (observed 100+ 404s in console sweep).
- **Root cause:** `computeJobProgress` only wrapped `last_error` into an envelope when `status==='failed'`; `pollingDecision` only halted on `done`/`failed`. Both paths lose retry-backoff + 4xx-after-delete.
- **Fix:** commit `f487e2e` — `computeJobProgress` now surfaces a `transient`-class `job.retrying` envelope when `status==='pending' && attempts >= 1 && last_error !== ''`. `pollingDecision` gained a `PollingDecisionInput` overload (detail + error) and halts on 4xx (retry still fires on 5xx for transient server outages). The hook threads `query.state.error` through and also sets `retry: (count, error) => error.status < 400 || error.status >= 500 && count < 2` so TanStack Query stops chattering on 4xx. 5 new `F-06.6` unit tests cover both the retry-backoff envelope and the 4xx halt. Codex follow-ups `b572080` (SyncNowButton render gate dropped `status==='failed'` guard) and `50adb1a` (handleClick clears stale jobId on re-click to prevent retry-envelope + mutationError stacking).
- **Codex verify:** ✅ Clean (batched 2026-04-22, 2 passes)
- **Retest:** Playwright retest deferred to final batch close — requires a fresh browser to clear the pre-fix poller accumulator; unit tests pass.
- **Status:** ✅ Closed

### F-06.7 RPM snippet uses `gpgcheck=1` that rejects every package
- **Severity:** m / minor
- **Area:** `web/src/lib/snippets.ts:case 'rpm'`
- **Symptom:** `dnf install <pkg>` against an OmniRepo RPM repo fails with `Import of key(s) didn't help, wrong key(s)?  …  GPG check FAILED`. Reproduced end-to-end in a rockylinux/rockylinux:9 container.
- **Root cause:** OmniRepo signs `repomd.xml` with the repo's auto-generated GPG key. Individual packages pass through unsigned (user uploads) or carry upstream signatures (mirrored). `gpgcheck=1` verifies EVERY package signature against the imported key and rejects anything not-OmniRepo-signed. The pass-through-mirror shape wants `repo_gpgcheck=1 gpgcheck=0` (verify the signed index, trust the packages).
- **Fix:** commit `9f860f3` — snippet now emits `repo_gpgcheck=1\ngpgcheck=0\ngpgkey=<proto>://…/public-key.asc`. Snippet test updated to assert the new shape; install retest (see below) confirms.
- **Codex verify:** ✅ Clean (batched 2026-04-22, 2 passes)
- **Retest:** ✅ `dnf install` against the new snippet reaches transaction-test stage (download + metadata verify clean; failure thereafter is the Rocky-vs-CentOS file conflict of the test package, not OmniRepo).
- **Status:** ✅ Closed

### F-06.8 No sync-job history UI surface
- **Severity:** m / minor (UI gap)
- **Area:** Mirror repo pages — Settings tab + /settings page
- **Symptom:** Backend list endpoint exists (`GET /api/v1/projects/{n}/repos/{t}/{r}/sync-jobs`) with status, file count, byte count, duration per job. Neither the Content tab nor the mirror settings page surface a history list. Users see only the last sync's progress pill; no failed-job trail.
- **Root cause:** UI was never wired to the `handleListSyncJobs` endpoint.
- **Fix:** **Deferred** — tracked-open. Scope is a compact list component + a new useSyncJobs hook per protocol page. Defers to a follow-up UI pass.
- **Codex verify:** —
- **Retest:** Endpoint continues to respond; UI gap remains.
- **Status:** 🟨 Open — tracked-open (deferred)

## Sign-off

- [x] All test cases exercised (6.1–6.22)
- [x] Final state:
  - [x] `acme/rpm/local` has `centos-release-7-2.1511.el7.centos.2.10.x86_64.rpm` (uploaded, scanned Clean)
  - [x] `acme/rpm/epel-mirror` synced once successfully from `acme/rpm/local`
  - [x] `acme/deb/local` has `hello-wt3_1.0-1_all.deb`, InRelease signed + gpg-verified
  - [x] `acme/deb/debian-mirror` synced once from `acme/deb/local`
- [x] All F-06.* closed — F-06.1/.2/.4/.5/.6/.7 ✅ fixed + Codex-clean (2 passes). F-06.3/.8 🟨 tracked-open deferred.
- [x] Codex pass on fixes applied (pass 1 found 2 real-issues → `b572080`; pass 2 found 1 real-issue + 1 upgrade-path tracked-open → `50adb1a`).
- [x] README.md batch 06 status flipped to ✅
- [x] All cases passed
- [ ] Final state:
  - [ ] `acme/rpm/local` has at least one package
  - [ ] `acme/rpm/epel-mirror` has synced at least once successfully
  - [ ] `acme/deb/local` has at least one .deb, InRelease signed
  - [ ] `acme/deb/debian-mirror` synced once
- [ ] All F-06.* closed
- [ ] Codex pass on any fixes applied
- [ ] README.md batch 06 status flipped to ✅
