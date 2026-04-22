# Batch 05 — Docker / OCI

**Status:** ⬜ Not started
**Prereqs:** Batch 04 ✅ (acme project exists with alice admin + bob member + dockerhub creds)
**State produced for later batches:**
- `acme/docker/demo` repo with pushed `hello-world:latest` and `hello-world:v1`
- `acme/docker/clone` repo with a tag pulled from Docker Hub (alpine or busybox)
- Scan results exist for at least one tag (drives Batch 13)

## Pre-flight

- [ ] Logged in as alice (project admin on acme)
- [ ] Docker CLI available locally: `docker --version`
- [ ] `docker login http://localhost:18080 -u alice -p <alice-api-key>` succeeds
- [ ] Server log tail open

## Test cases

### 5.1 Create docker repo
- [ ] From `/projects/acme` → "Create repo" → type `Docker`, name `demo`
- [ ] **Expected:** repo created; CreateRepoDialog does NOT show mirror checkbox for docker (WALKTHROUGH-2 D-2)
- [ ] Redirect to `/projects/acme/docker/demo`
- [ ] Empty-state page shows snippets for `docker pull http://localhost:18080/acme/docker/demo:...` etc.

### 5.2 Snippet copy buttons
- [ ] Each snippet card has a Copy button
- [ ] Click copies the correct command with host pre-filled
- [ ] Console clean

### 5.3 Push a tiny image
- [ ] Local side:
  ```bash
  docker pull hello-world:latest
  docker tag hello-world:latest localhost:18080/acme/docker/demo:latest
  docker push localhost:18080/acme/docker/demo:latest
  ```
- [ ] **Expected:** push succeeds end-to-end; UI table eventually lists `latest` tag with size, created_at, digest
- [ ] Trivy scan auto-triggers; `Scan status` column transitions to `Clean` (hello-world has no CVEs)
- [ ] Backend log: push events, no ERROR

### 5.4 Push a second tag
- [ ] `docker tag hello-world:latest localhost:18080/acme/docker/demo:v1`
- [ ] `docker push localhost:18080/acme/docker/demo:v1`
- [ ] **Expected:** UI table lists both `latest` and `v1`; manifests distinct

### 5.5 Pull back
- [ ] `docker pull localhost:18080/acme/docker/demo:v1` on a new machine or `docker rmi` first
- [ ] **Expected:** pull succeeds; digest matches push

### 5.6 Manifest view
- [ ] Click `latest` row → manifest details panel (or modal)
- [ ] **Expected:** JSON blob with mediaType, config digest, layers, size
- [ ] No console errors on open/close

### 5.7 Tag history / size accordion (WALKTHROUGH-2 recovery)
- [ ] Size accordion expands per-layer or per-tag breakdown
- [ ] Sums correctly against the manifest

### 5.8 Rescan
- [ ] Click "Rescan" on `latest`
- [ ] **Expected:** status returns to `Pending` then `Clean`; scan_report_page accessible
- [ ] Audit log: `artifact.rescan`

### 5.9 Delete tag — happy path
- [ ] Row action → Delete on `v1`
- [ ] Confirmation dialog
- [ ] **Expected:** DELETE to `/v2/acme/docker/demo/manifests/<digest>` → 204/202; row disappears
- [ ] `docker pull localhost:18080/acme/docker/demo:v1` → manifest unknown
- [ ] Orphaned blobs: check `/admin/gc` (Batch 14) — blobs should be collectable

### 5.10 Delete tag — last tag
- [ ] Delete `latest`
- [ ] Repo becomes empty; UI returns to empty-state
- [ ] Push a new `latest` (re-seed for Batch 13 severity gate)

### 5.11 block_on_severity gate (setup for Batch 13)
- [ ] Settings tab → set `block_on_severity` to `high`
- [ ] Push an image known to have HIGH CVEs (e.g. an older alpine or nginx)
- [ ] `docker pull` of that tag → **Expected:** 403 with `blocked_by_scan` envelope
- [ ] UI shows the block reason on the tag row
- [ ] Clean image still pulls fine

### 5.12 Pull-external — happy path
- [ ] Create `acme/docker/clone`
- [ ] Click "Pull external image" → dialog → source `docker.io`, image `library/alpine`, tag `3.19`, credential `dockerhub` (Batch 04 setup)
- [ ] Submit → **Expected:** progress stream (per-blob), completion toast, tag appears in repo
- [ ] `docker pull localhost:18080/acme/docker/clone:3.19` succeeds
- [ ] Audit log: `docker.clone.success`

### 5.13 Pull-external — no credentials / rate-limit
- [ ] Remove the upstream cred from the dialog → submit again
- [ ] **Expected:** clean 401/403 envelope from upstream surfaced in the UI (not a crash)
- [ ] Retry with cred → succeeds

### 5.14 Pull-external — nonexistent image
- [ ] Source `docker.io`, image `nonexistent/does-not-exist`, tag `x`
- [ ] **Expected:** UI shows "image not found" / structured error, no partial data
- [ ] Backend cleans up any partial manifest/blobs

### 5.15 Promote tag (if exposed)
- [ ] If the UI exposes promote/cross-mount, test moving a tag to a new name within the repo
- [ ] Document behavior; file a finding if the action is present but broken

### 5.16 Concurrent pushes
- [ ] From two shells, push the same image with different tags at the same time
- [ ] **Expected:** both succeed; no manifest collision; UI eventually lists both tags
- [ ] Backend log: no race errors

### 5.17 OCI Distribution spec conformance basics
- [ ] `GET /v2/` → 200 with `Docker-Distribution-API-Version: registry/2.0`
- [ ] `GET /v2/acme/docker/demo/tags/list` → 200 with `{ "name": "...", "tags": ["latest", ...] }`
- [ ] `GET /v2/acme/docker/demo/manifests/latest` → manifest JSON
- [ ] `HEAD` variants match `GET` status

### 5.18 Anonymous pull (if repo supports public_read)
- [ ] If `public_read` toggle exists, enable on demo
- [ ] `curl -k http://localhost:18080/v2/acme/docker/demo/manifests/latest` without auth → 200
- [ ] Disable toggle → 401

### 5.19 Soft-delete repo → trash
- [ ] Delete `acme/docker/clone` via repo settings
- [ ] **Expected:** removed from repos list; entry in `/admin/trash` (tested in Batch 14)
- [ ] Push to the same repo path → 404 (or auto-resurrect, document behavior)

### 5.20 Console + network sweep across every page
- [ ] Repo list, repo detail (each tag), scan report, manifest view, clone dialog
- [ ] Zero errors / warnings

## Findings

_(F-05.N)_

## Sign-off

- [ ] All cases passed
- [ ] Final state:
  - [ ] `acme/docker/demo` has at least `latest` tag, clean scan
  - [ ] One tag in acme/docker/demo has HIGH CVE for severity gate test (if possible — otherwise rely on PyPI per WALKTHROUGH-2 §3a)
  - [ ] `acme/docker/clone` either deleted (in trash) or restored
- [ ] All F-05.* closed
- [ ] Codex run: "Review Docker batch commits for correctness" (include all fix commits from batch)
- [ ] README.md batch 05 status flipped to ✅
