# Batch 05 — Docker / OCI

**Status:** ✅ Closed (6 fixes landed, 1 withdrawn)
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

| ID | Sev | Area | Summary | Status |
|----|-----|------|---------|--------|
| F-05.1 | **B** blocker | auth/membership across 10 sites (9 middleware + 1 REST helper) | User-owned API keys could not auth any project-scoped OCI/RPM/DEB/PyPI/Helm/RAW/Git/admin action — token 403 `not_a_project_member`. Root cause: membership-resolver branches only covered `ActorKindUser` + project-scoped keys; user-owned keys fell through. Fix pass 1 extracted `auth.ResolveMembership` and used it in 9 sites. Codex pass 2 caught a tenth copy in `api/scans.go Deps.actorIsProjectMember` that also rejected `Kind != User` — patched to match protocol-handler pattern. | ✅ Fixed — commits `d8d11d0` + `f0f6131` |
| F-05.2 | R | `blobGet` error envelope | 404 BLOB_UNKNOWN `detail` echoed `os.PathError.Error()`, leaking absolute CAS path. Generic internal-error leaked it too. Sanitised + slog-path for diagnostics. Regression test `TestBlobGet_UnknownDigest_DoesNotLeakFSPath`. | ✅ Fixed — commit `b942943` |
| F-05.3 | R | Multi-arch scan aggregation | Tags pointing at an OCI image index showed "Not scanned" forever — scan worker skips indexes and scans child manifests, but UI queried `scans.artifact_id = tag.digest` and the index digest has no row. All common Docker Hub images (hello-world, alpine, nginx, …) are multi-arch. Fix: `aggregateIndexScan` parses the index body, rolls up latest-per-child scans into a synthetic envelope the UI consumes identically to a direct scan row. | ✅ Fixed — commit `0ab9b54` |
| F-05.4 | R | `DockerRepoPage.tsx:308-313` + new REST shim | Delete-tag icon button had **no `onClick` handler**. UI can't call OCI DELETE `/v2/.../manifests/<ref>` directly (needs a Bearer from `/v2/token` the session cookie can't mint). Fix: session-authed shim `DELETE /api/v1/projects/{name}/repos/docker/{repo}/tags/{tag}` in `rest_tags.go` mirroring `manifestDelete`'s tag-form branch; UI wired with confirm dialog + `useDeleteDockerTag`. | ✅ Fixed — commit `c18e84c` |
| ~~F-05.5~~ | ~~R~~ | ~~`CloneImageDialog`~~ | **Withdrawn after Codex pass.** `useJobProgress` does poll and `CloneImageDialog.tsx:285` renders `ErrorEnvelopeRenderer` once `progress.status === 'failed'`. My 20 s probe didn't wait for retry backoff to exhaust — the UI does eventually surface the failure. Moved to observations. | 🟥 Rejected |
| F-05.6 | R | `DockerRepoPage.tsx:473` | Promote/Retag button toasted "API not yet connected." despite `POST /api/v1/projects/{name}/repos/docker/{repo}/promote` being fully implemented. Fix: new `usePromoteDockerTag` hook + real form state, server error envelope surfaced via `ErrorEnvelopeRenderer`, src + dst cache invalidation. | ✅ Fixed — commit `c18e84c` |

### Observations (not filed as findings)
- Repo header `Docker repository · 2 tags · 311.6 MB` after multi-arch push + delete shows orphan-blob storage; legitimate — blobs remain until GC sweeps. Accuracy bug if observed long-term, but matches the documented CAS + GC model.
- Rescan audit event kind is `scan.started` (with `Details.reason = "manual_rescan"`) not the doc-drafted `artifact.rescan`. Event IS recorded. Doc drift only.
- Cloning/pull-external audit event kind is `oci.pull_external.finished` not the doc-drafted `docker.clone.success`. Doc drift only.
- Each retried scan emits one extra `scan.started` entry on every worker dequeue attempt (3× for an initially-failed scan before DB seed). Minor audit noise — not a correctness issue.
- CLI-snippet shape `localhost:18080/acme/docker/demo/<image>:<tag>` expects a sub-image path; single-image form `localhost:18080/acme/docker/demo:tag` also works (crane verified). Batch doc assumed the latter; both are accepted.
- HEAD on `/v2/` returns `405 Method Not Allowed`. OCI Distribution spec only mandates GET for the ping endpoint, so this is spec-compliant; some clients may still probe HEAD first.

## Sign-off

- [x] Happy-path tests 5.1–5.3, 5.5, 5.10–5.13, 5.16–5.19 pass.
- [x] 5.4 verified via `crane tag` (OCI side-channel): both tags listed, same manifest digest.
- [x] 5.6 manifest detail panel shows mediaType / Image / Tag / Digest / Layers / Size / Uploaded / Scan findings.
- [x] 5.7 per-tag size row populated; no per-layer accordion (present only in manifest panel).
- [x] 5.8 rescan round-trip Pending → Clean; audit row `scan.started (reason=manual_rescan)` + `scan.finished`.
- [x] 5.9 deletion exercised via OCI v2 DELETE; data-flow correct; UI button itself broken (F-05.4).
- [x] 5.11 severity gate blocks alpine:3.10 (1 critical CVE) with `blocked_by_scan` envelope; hello-world clean pull still works.
- [x] 5.15 Promote backend route exists; UI admits disconnection (F-05.6).
- [x] 5.17 OCI conformance: GET /v2/ `registry/2.0`, tags/list JSON, manifest GET+HEAD with Docker-Content-Digest header.
- [x] 5.18 public_read toggle: anon GET manifest → 200 on, 401 with WWW-Authenticate on off.
- [x] 5.19 soft-delete clone → trash sidecar `/tmp/omnirepo-wt3/trash/1776868409-repo-2`; push to same path → 404 NAME_UNKNOWN (no auto-resurrect).
- [x] Final state:
  - [x] `acme/docker/demo` has `latest` (clean) + `concA`, `concB` (clean) + `vuln` (1 critical CVE, blocked by gate).
  - [x] HIGH/critical CVE coverage via `acme/docker/demo:vuln` (alpine:3.10).
  - [x] `acme/docker/clone` soft-deleted.
- [x] Fixes landed: F-05.1 (`d8d11d0` + `f0f6131`), F-05.2 (`b942943`), F-05.3 (`0ab9b54`), F-05.4 + F-05.6 (`c18e84c`).
- [x] F-05.5 withdrawn after Codex invalidated the "UI hangs" observation.
- [x] Codex pass 1 clean on F-05.1/.2; Codex pass 2 caught a 10th site for F-05.1 (`f0f6131`).
- [x] Codex pass 3 clean on F-05.3/.4/.6 — no blockers, no real issues flagged.
- [x] README.md batch 05 status flipped to ✅.
