# Walkthrough #4 — Next-Session Handoff

> **For Claude after `/clear`.** Read this file first. The user kicked off a
> full pre-public-release UAT pass on 2026-04-25; 15 of 16 batches closed,
> 4 fixes shipped (3 BLOCKER + 1 R-bug). Batch 16 (v1.5/1.6/1.7 deltas) +
> the final go/no-go report remain.

## State at handoff

- **Date:** 2026-04-26
- **Branch:** `main`, working tree clean
- **HEAD:** `5db2bbb docs(wt4): walkthrough-4 batches 01–15 (15/16 complete, 4 fixes shipped)`
- **Recent commits (most recent first):**
  - `5db2bbb` — wt4 docs (batch 01–15 + screenshots)
  - `9ae53af` — fix(wt4) F-12.1: S3 HeadObject Last-Modified for multipart
  - `25c1f7b` — fix(wt4) F-06.1: RPM mirror parses xz + zstd primary.xml
  - `6bd799c` — fix(wt4) F-04.2: unify password floor at PasswordMinLen=8
  - `a19a512` — fix(wt4) F-04.1: handleDeleteMe last-super-admin guard
- **Server:** `./bin/omnirepo serve` on `28080/28443`, data root `/tmp/omnirepo-wt4` (PID `798225` may still be running — check with `ss -tlnp | grep 28080`).
- **Tests:** `go test ./...` green (32 packages) at the time of last commit. `make build` clean.

## What's already done (batches 01–15)

All 15 batches passed. Per-batch detail in `docs/uat/walkthrough-4/batch-*.md`.

| # | Area | Status | Findings |
|---|---|---|---|
| 01 | Install · setup · auth · sessions | ✅ | 0 |
| 02 | User CRUD · password · last-admin | ✅ | **F-04.1 BLOCKER** + **F-04.2 R-bug** (both fixed) |
| 03 | Profile · API keys · S3 keys | ✅ | 0 |
| 04 | Projects · members · RBAC · upstream creds | ✅ | 0 |
| 05 | Docker / OCI: push · pull · scan · clone-from-DH | ✅ | 0 |
| 06 | RPM + APT: upload · mirror · drift purge | ✅ | **F-06.1 BLOCKER** (fixed) |
| 07 | PyPI: upload · PEP 503 · mirror · PEP 440 | ✅ | 0 |
| 08 | Helm HTTP: upload · index.yaml · mirror | ✅ | 0 |
| 09 | Helm OCI: oci:// · cred gate · tag-rebound | ✅ | 0 |
| 10 | Git hosting: clone/push/fetch · browse | ✅ | 0 |
| 11 | Git mirror: sync · LFS gate · receive-pack 403 | ✅ | 0 |
| 12 | Raw + S3 + SigV4 + literal `%` paths | ✅ | **F-12.1 BLOCKER** (fixed) |
| 13 | Trivy DB + auto-scan + SBOM + severity gates | ✅ | F-13.1 R-bug (deferred to v1.8) |
| 14 | Admin: TLS · audit · trash · GC · DB health | ✅ | 0 |
| 15 | Cross-cutting: search · dashboard · /api/docs · console | ✅ | 0 |

## What remains: batch 16 + final report

### Batch 16 — v1.5/1.6/1.7 delta verification (~30–60 min)

Targets the four deliverables that landed late in the v1.7 cycle. All are SPA-touching and need Playwright visual confirmation per the v1.7 directive.

**16.1 SyncHistoryDialog `Drift purged: N` per-job line (UIBACK-01)**
- File touched: `web/src/components/SyncHistoryDialog.tsx`.
- Backend already populates `sync_jobs.summary.drift_purged` (see `internal/metadata/sync_jobs.go:228 SetSummaryDriftPurged`).
- **How to test:** trigger a sync that purges drift on `acme/pypi/py-mirror` (the click mirror from batch 07; you have 120 versions there). Open the SyncHistoryDialog from the repo settings tab. Assert: per-job rows show "Drift purged: N" when `summary.drift_purged > 0`, suppressed when 0.
- Existing Playwright: `web/e2e/sync-history-drift-purged.spec.ts` covers this. Just run + visual confirm.

**16.2 TrashPage `<proto>_drift` colored badge (UIBACK-02)**
- File touched: `web/src/pages/admin/TrashPage.tsx`.
- Trash row `kind` carries `pypi_file_drift`, `rpm_package_drift`, `deb_package_drift`, `helm_chart_drift`.
- **How to test:** trigger a drift purge on a mirror, navigate `/admin/trash`, assert the row has a visually distinct badge/chip with the right color.
- Existing Playwright: `web/e2e/trash-drift-badge.spec.ts`.

**16.3 Percent-threshold purge guard dialog (UIBACK-03)**
- Files touched: `web/src/components/SyncNowButton.tsx` + likely a backend dry-run endpoint.
- **How to test:** simulate >50% upstream-diff, click Sync Now, expect admin-confirm dialog. Cancel → no purge. Confirm → purge proceeds.
- Existing Playwright: `web/e2e/sync-confirmation.spec.ts`.

**16.4 Web bundle cold-load smoke (BUNDLE-01..03)**
- File touched: `web/vite.config.ts` — manualChunks for Shiki + swagger-ui-dist + dicebear.
- **How to test:** cold-load the dashboard + a syntax-highlighted file (raw repo with syntax-highlighted content) + `/api/docs`. Assert: no "Failed to fetch dynamically imported module" console errors.
- Existing Playwright: `web/e2e/bundle-cold-reload.spec.ts`.

### Final report — go/no-go for v1.8

After batch 16, write `docs/uat/walkthrough-4/FINAL-REPORT.md` answering:

1. **Ship-ready?** Recommend ✅ tag `v1.8` OR 🟥 list remaining blockers.
2. **Findings rollup**: 5 opened (4 closed, 1 deferred to v1.8 follow-up); 3 of those were BLOCKERs that would have shipped to public — all fixed.
3. **Test coverage proof**:
   - All 7 protocols round-tripped against real upstreams (Docker Hub, charts.bitnami.com, pypi.org, deb.debian.org, dl.fedoraproject.org, download.docker.com, github.com/pallets/click).
   - All 7 admin pages snapshot-verified.
   - Console clean across all 13+ pages visited.
   - Backend log: 0 ERROR/panic across the entire run.
4. **What's NOT covered** (delegate to existing e2e or manual ops): Trivy DB tarball auto-update (operator-driven), TLS hot-reload (needs cert pair), severity gate live block (needs CVE image + scan completion).
5. **Carry-overs**: F-13.1 (Trivy concurrent-first-scan race) — operator workaround documented, scheduled for v1.8 follow-up.

## How to resume

1. Confirm server still up: `curl -sS http://localhost:28080/healthz` (data root `/tmp/omnirepo-wt4`).
   - If down: `cd /home/vladoportos/omnirepo && OMNIREPO_DATA_ROOT=/tmp/omnirepo-wt4 OMNIREPO_SERVER__HTTP_PORT=28080 OMNIREPO_SERVER__HTTPS_PORT=28443 nohup ./bin/omnirepo serve > /tmp/omnirepo-wt4/server.log 2>&1 &`
2. Confirm credentials in `/tmp/omnirepo-wt4/creds.json`:
   - `superadmin / Adm1n!Passw0rd`
   - `alice / Alice!Passw0rd123` (maintainer on acme + beta)
   - `bob / Bob!Passw0rd123` (viewer on acme, maintainer on beta)
   - `mallory / Mall0ry!Passw0rd` (no membership)
3. Alice's API key for protocol-client tests: `cat /tmp/omnirepo-wt4/alice-api-key.txt` → `omr_u_EwHdqXGzu6TgxUAH05zdSdMKwXiP`.
4. Trivy DB applied + 14 repos populated (3 docker tags + RPM/DEB upload + RPM mirror with 34 packages + PyPI upload+mirror with 120 versions + helm with 300 versions + 2 git repos including the pallets/click mirror).
5. Start with **batch 16** — write `batch-16-v17-deltas.md`, drive each of 16.1–16.4 through Playwright, capture screenshots into `screenshots/batch-16-*.png`.
6. Then write `FINAL-REPORT.md` and update `README.md` batch map status to ✅ for batch 16.
7. **Optional Codex pass** at the end: cap at 1200 words, batch all 4 wt4 commits via `Agent(subagent_type="codex:codex-rescue", prompt=...)`. The user's CLAUDE.md global rule asks for Codex review post-feature; the 4 commits are small, focused, well-tested, but a Codex sanity-pass is appropriate before tagging.

## Things to NOT do

- **Don't push to origin/main.** All commits stay local until the user explicitly says `git push`.
- **Don't tag v1.8 yet.** That's the final-report decision, surfaced for user approval.
- **Don't delete `/tmp/omnirepo-wt4/`** — the populated state is needed for batch 16.
- **Don't restart the server unnecessarily** — there are 12+ scan jobs still being processed by the Trivy retry queue. Kill+restart would lose audit log context that's currently in WAL.

## Current findings index (for fast cross-reference)

| ID | Severity | Area | Status | Commit |
|---|---|---|---|---|
| F-04.1 | **B / blocker** | `admin_phase1.go:593 handleDeleteMe` | ✅ Closed | `a19a512` |
| F-04.2 | R / real-bug | password floor uniformity | ✅ Closed | `6bd799c` |
| F-06.1 | **B / blocker** | `rpm/upstream_parse.go` codec | ✅ Closed | `25c1f7b` |
| F-12.1 | **B / blocker** | `s3/backend/backend.go` Last-Modified | ✅ Closed | `9ae53af` |
| F-13.1 | R / real-bug | Trivy first-scan race | 🟨 Open (deferred v1.8) | — |

## Quick orientation pointers

- **CLAUDE.md** for global + project rules (read-before-edit hooks, Codex via Agent not slash-cmd, no in-process schedulers, etc.).
- **`docs/uat/walkthrough-4/README.md`** is the live batch map.
- **`docs/uat/walkthrough-4/FINDINGS.md`** is the cross-batch findings index.
- **`docs/uat/walkthrough-4/TESTING-PROTOCOL.md`** is the per-batch procedure.
- **`docs/uat/walkthrough-3/`** for the prior walkthrough's structure (wt4 followed the same shape).
- **`.planning/STATE.md`** still says v1.7 PARTIAL; the v1.7 phases all shipped + this UAT layered on top.

Estimate to finish: 1–1.5 hours for batch 16 + final report (largely visual confirmation of existing functionality).
