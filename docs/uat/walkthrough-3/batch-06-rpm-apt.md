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

_(F-06.N)_

## Sign-off

- [ ] All cases passed
- [ ] Final state:
  - [ ] `acme/rpm/local` has at least one package
  - [ ] `acme/rpm/epel-mirror` has synced at least once successfully
  - [ ] `acme/deb/local` has at least one .deb, InRelease signed
  - [ ] `acme/deb/debian-mirror` synced once
- [ ] All F-06.* closed
- [ ] Codex pass on any fixes applied
- [ ] README.md batch 06 status flipped to ✅
