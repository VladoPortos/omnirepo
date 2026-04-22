# Batch 13 — Vulnerability scanning (Trivy)

**Status:** ⬜ Not started
**Prereqs:** Batches 05–09 ✅ (artifacts exist in docker, rpm, apt, pypi, helm)
**State produced for later batches:** None — this batch exercises the scan/report UI against existing artifacts.

## Pre-flight

- [ ] Trivy binary bundled in the server image/binary
- [ ] Trivy DB present under `$OMNIREPO_DATA_ROOT/trivy/db/` (baked or pre-uploaded)
- [ ] Artifacts exist with known CVEs (e.g. `requests==2.0.1` from Batch 07, older alpine from Batch 05)

## Test cases

### 13.1 Trivy DB age display
- [ ] `/admin/trivy` renders
- [ ] Age in days displayed; warning banner if >30 days old (or whatever the threshold is)
- [ ] Last-updated timestamp visible
- [ ] Console + network clean

### 13.2 Trivy DB tarball upload
- [ ] Download a fresh tarball from `https://github.com/aquasecurity/trivy-db/releases` to a local file (or use a pre-prepared one)
- [ ] Upload via form
- [ ] **Expected:** validation (format check), extraction, DB rotation; age resets
- [ ] Audit log: `trivy.db.upload`
- [ ] Network: no outbound fetches during the upload (air-gap)

### 13.3 Trivy DB upload — invalid file
- [ ] Upload a random non-tarball file
- [ ] **Expected:** 400 envelope explaining format issue; existing DB untouched

### 13.4 Auto-check trigger (if exposed as button)
- [ ] Click "Check DB status" (or similar)
- [ ] **Expected:** synchronous status check; no outbound fetch; response describes current DB metadata

### 13.5 Single-artifact rescan
- [ ] Go to `/projects/acme/docker/demo` → row action Rescan on a tag
- [ ] **Expected:** status `Pending` → running → `Clean` / `Vulnerable`
- [ ] Scan report accessible via row click
- [ ] Audit log: `artifact.rescan`

### 13.6 Repo-wide rescan
- [ ] Repo settings → Rescan all artifacts
- [ ] **Expected:** all artifacts re-scan; progress indicator; final summary
- [ ] Audit log: `repo.rescan`

### 13.7 Scan report page
- [ ] Click a vulnerable artifact → scan report
- [ ] **Expected:** table of CVEs with severity, package, version, fix version (if any); sortable by severity
- [ ] SBOM Download button works (JSON); SBOM parseable
- [ ] Links to external CVE pages are either inert (air-gap: no auto-fetch) or `rel="noopener"` if clicked

### 13.8 block_on_severity — per-protocol
- [ ] For each of: Docker, RPM, APT, PyPI, Helm
  - [ ] Set repo `block_on_severity=high`
  - [ ] Pull a vulnerable artifact via native client
  - [ ] **Expected:** 403 with `blocked_by_scan` envelope (WALKTHROUGH-2 §3a proven live for PyPI; verify rest through code path + one other protocol live)
- [ ] Second pull with clean artifact → 200
- [ ] Audit log: `scan.gate.blocked` with `source:"db"` first, then `source:"cache"` on repeats

### 13.9 Scan-on-sync toggle
- [ ] Toggle `scan_on_sync=true` on a mirror repo (e.g. `acme/pypi/pypi-upstream`)
- [ ] Run Sync now
- [ ] **Expected:** scans auto-trigger on newly synced artifacts; scan_status populated by sync-end
- [ ] Toggle off → subsequent syncs don't auto-scan

### 13.10 Scan prune
- [ ] If API/admin has a scan prune action, trigger it
- [ ] **Expected:** old scan records dropped; current scans preserved
- [ ] Audit log: `scan.prune`

### 13.11 High-severity findings widget (dashboard)
- [ ] Dashboard → High-Sev Findings card
- [ ] **Expected (WALKTHROUGH-2 F-3):** entries deduped by (cve, package, repo) with occurrence badges; severity dropped from GROUP BY + worst-wins merging

### 13.12 Scan cache behavior
- [ ] Trigger the same gate twice in quick succession
- [ ] **Expected:** first audit `source:"db"`, second `source:"cache"`
- [ ] Cache invalidates after a fresh rescan

### 13.13 Console + network sweep
- [ ] Every scan-related page
- [ ] Zero errors/warnings
- [ ] No outbound network calls during any scan operation (air-gap gate)

## Findings

_(F-13.N)_

## Sign-off

- [ ] All cases passed
- [ ] No outbound scan-related network calls observed
- [ ] All F-13.* closed
- [ ] README.md batch 13 status flipped to ✅
