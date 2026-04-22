# Batch 14 — Admin operations (TLS, audit, trash, GC, maintenance, DB health)

**Status:** ⬜ Not started
**Prereqs:** Batches 01–12 ✅ (populated data + deleted items in trash + audit history)
**State produced for later batches:** None

## Scope — what's new from v1.3+

- DBHEALTH-01..07: boot-time PRAGMA integrity_check, `POST /admin/db/health/check` with 429 rate-limit, 409 concurrent lease, panic-safe lease, DSN pragma snapshot display
- Trash system: soft-delete retention, deleted_by + original_location, restore capability
- Maintenance: global read-only with user-visible banner

## Pre-flight

- [ ] Logged in as superadmin
- [ ] Some soft-deleted items exist from earlier batches (closed project, acme/docker/clone, etc.)
- [ ] Server log tail open

## TLS test cases

### 14.1 TLS page renders
- [ ] `/admin/tls` → current cert metadata (fingerprint, SANs, NotBefore/After), history table, upload form, self-signed regen action
- [ ] Console + network clean

### 14.2 Upload new cert
- [ ] Generate a throwaway cert + key (openssl), upload both files
- [ ] **Expected:** 200; hot-reload — no server restart; new cert active for subsequent HTTPS handshakes
- [ ] Previous cert archived in history
- [ ] Audit log: `tls.upload`

### 14.3 Hot-reload verification
- [ ] After upload, open a fresh TLS connection (new browser tab or `openssl s_client -connect localhost:18443`)
- [ ] **Expected:** cert served is the new one; fingerprint matches upload
- [ ] Existing connections (current browser) may still have the old cert in the keepalive — acceptable, document

### 14.4 Upload invalid cert
- [ ] Upload mismatched cert/key pair, or a malformed file
- [ ] **Expected:** 400 envelope; existing cert untouched; UI shows the error
- [ ] Audit log: `tls.upload.failure`

### 14.5 History + rollback
- [ ] Rollback to previous cert via history table
- [ ] **Expected:** active cert returns to previous; audit log entry

### 14.6 Self-signed regen
- [ ] Click "Regenerate self-signed"
- [ ] **Expected:** new self-signed pair generated; fingerprint rotates; audit log entry
- [ ] Subsequent browser hit requires re-trust of new cert

## Audit log test cases

### 14.7 Audit page renders
- [ ] `/admin/audit` → event table with action, actor, target, timestamp, outcome
- [ ] Default sort: newest first
- [ ] Console + network clean

### 14.8 Filters
- [ ] Filter by action (e.g. `auth.login.success`), actor, outcome, time range
- [ ] **Expected:** each filter narrows the list correctly
- [ ] URL reflects filter state (deep-linkable)

### 14.9 Event coverage
- [ ] Verify the following events from earlier batches are present:
  - [ ] `user.create`, `user.delete`, `user.password_change`, `user.password_reset`
  - [ ] `auth.login.success`, `auth.login.failure`, `auth.logout`
  - [ ] `project.create`, `project.delete`, `project.member.add`, `project.member.remove`
  - [ ] `api-key.create`, `api-key.revoke`
  - [ ] `upstream-cred.create`, `upstream-cred.delete`
  - [ ] Protocol events: `oci.push`, `rpm.upload`, `deb.upload`, `pypi.upload`, `helm.upload`, `raw.upload`
  - [ ] `mirror.sync.start`, `mirror.sync.success`, `mirror.sync.failure`
  - [ ] `helm.oci.tag_rebound` (EvtOciTagRebound — new)
  - [ ] `mirror.sync.lfs_detected` (EvtMirrorSyncLFSDetected — new, if LFS was seen on any git mirror)
  - [ ] `git.receive_pack.denied` (new — mirror push attempts)
  - [ ] `scan.gate.blocked`
  - [ ] `tls.upload`

### 14.10 Audit detail
- [ ] Click a row → detail view with full `details_json`
- [ ] json_valid guard holds (WALKTHROUGH-2 F-5 — no 500 on malformed rows)

## Trash test cases

### 14.11 Trash page renders
- [ ] `/admin/trash` → table of soft-deleted items with kind, name, deleted_at, deleted_by, original_location, retention remaining
- [ ] Console + network clean

### 14.12 Restore project
- [ ] Restore the `closed` project deleted in Batch 04
- [ ] **Expected:** project reappears in `/projects`; all its prior repos/members still linked (or restored)
- [ ] Audit log: `project.restore`
- [ ] If there's a live `closed` project created after delete (Batch 04 case 4.19): restore must either refuse (409) or rename — document

### 14.13 Restore repo
- [ ] Restore the `acme/docker/clone` from Batch 05
- [ ] **Expected:** repo + tags/scans/jobs preserved

### 14.14 Permanent delete
- [ ] Pick a trashed item and hard-delete it
- [ ] **Expected:** row disappears from trash; data dir cleaned up (no orphan files)
- [ ] Audit log: `trash.purge` / equivalent

### 14.15 Retention countdown display
- [ ] Each trash row shows "Expires in X days"
- [ ] Manually age a trash row (via DB update in a controlled test) and re-load — countdown updates

### 14.16 Auto-purge on retention expiry
- [ ] If the background job purges expired rows, verify an expired row disappears after the next run
- [ ] If this is manual-only, document it

## GC test cases

### 14.17 GC page renders
- [ ] `/admin/gc` → current storage stats, trigger button, last-run timestamp

### 14.18 Trigger GC
- [ ] After deleting some OCI tags in Batch 05, click Trigger GC
- [ ] **Expected:** orphan blobs deleted; on-disk bytes drop
- [ ] Audit log: `gc.run`
- [ ] No in-use blobs deleted (cross-check by pulling a live tag post-GC)

### 14.19 GC while busy
- [ ] Trigger a mirror sync, then click GC
- [ ] **Expected:** GC waits, blocks, or queues behind the sync — whichever the design is, it must not corrupt data
- [ ] No race errors in backend log

## Maintenance test cases

### 14.20 Enable maintenance mode
- [ ] `/admin/maintenance` → toggle ON
- [ ] **Expected:** red banner visible on every page for every logged-in user (super-admin, alice, bob)
- [ ] Banner text explains "read-only"

### 14.21 Writes blocked
- [ ] As alice, try to upload a new artifact, create a repo, delete something
- [ ] **Expected:** 403 envelope `maintenance.read_only` (or similar)
- [ ] Reads still succeed

### 14.22 Super-admin writes during maintenance
- [ ] As superadmin, can still toggle maintenance off (bootstrap safety)
- [ ] Document whether superadmin bypasses maintenance for other writes (design choice)

### 14.23 Disable maintenance
- [ ] Toggle off → banner disappears; writes resume normally

## DB health test cases (NEW)

### 14.24 Boot-time integrity_check (DBHEALTH-01)
- [ ] Cold-start the server; check server.log
- [ ] **Expected:** `PRAGMA integrity_check` ran at boot; log line confirms `ok`
- [ ] If any error: server refuses to accept traffic

### 14.25 Manual DB health check endpoint
- [ ] `POST /api/v1/admin/db/health/check` (via UI button or curl)
- [ ] **Expected:** 200 with integrity result; DSN pragma snapshot returned
- [ ] Dashboard DB Health card reflects last-check time (DBHEALTH-07)

### 14.26 Rate-limit (429)
- [ ] Fire 10+ requests within a short window
- [ ] **Expected:** after N requests, 429 envelope with clear message; headers may include Retry-After
- [ ] Audit log or metric reflects the throttling

### 14.27 Concurrent lease (409)
- [ ] Fire two requests simultaneously from two shells
- [ ] **Expected:** exactly one returns 200, the other 409 `db.health.check_in_progress` envelope
- [ ] Panic-safe lease: if the lease-holder panics, the lease is released (no deadlock for subsequent checks)

### 14.28 Panic-safe lease integration
- [ ] If there's a test hook for inducing a panic mid-check, use it; otherwise reason from code
- [ ] Follow-up check after panic → succeeds normally

### 14.29 Dashboard DB Health card screenshots (spec-verified)
- [ ] `web/e2e/db-health-card.spec.ts` + `db-health-card-screenshots.spec.ts` exist — run them (`npx playwright test db-health-card`)
- [ ] **Expected:** green; if any snapshot drift is intended, update the stored snapshot and commit

## Console + network sweep
- [ ] Visit every admin page; zero errors/warnings
- [ ] No unexpected outbound traffic during admin ops

## Findings

_(F-14.N)_

## Sign-off

- [ ] All cases passed
- [ ] All F-14.* closed
- [ ] **Codex run** — DB health + maintenance + TLS hot-reload surfaces are high-risk; include DBHEALTH-01..07 commits in the prompt
- [ ] README.md batch 14 status flipped to ✅
