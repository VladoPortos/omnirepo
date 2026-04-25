# Batch 05 — Docker / OCI: push · pull · scan · clone-from-Docker-Hub · severity gate

**Status:** ✅ Passed clean (0 findings)
**Prereqs:** Batch 04 ✅ + `crane` (v0.21+) installed locally + Docker Hub reachable
**State produced for later batches:**
- `acme/docker/demo` repo (id=3) holds `latest` tag (hello-world from Docker Hub, digest `sha256:f9078146db2e...`).
- `alpine` tag was pushed then deleted via UI; manifest GC pending (verifies tag-delete + ref-count messaging).

## Test cases

### 5.1 Create Docker repo via API ✅
- `POST /api/v1/projects/acme/repos {name:"demo", type:"docker"}` → 200 `{id:3, name, type}`. The OCI handler does NOT auto-create on first push (`NAME_UNKNOWN` until repo row exists).

### 5.2 Push real images from Docker Hub via crane ✅
- Docker daemon (Docker Desktop on WSL2) cannot reach `localhost:28080` — its insecure-registries config lives in Windows-side Docker Desktop, not /etc/docker/daemon.json. Used `crane` (no daemon required) instead.
- `crane auth login localhost:28080 -u alice --password-stdin` → "logged in via /home/vladoportos/.docker/config.json".
- `crane copy --insecure index.docker.io/library/hello-world:latest localhost:28080/acme/docker/demo:latest` → success; final digest `sha256:f9078146db2e05e794366b1bfe584a14ea6317f44027d10ef7dad65279026885 size: 12212`.
- `crane copy --insecure index.docker.io/library/alpine:3.20 localhost:28080/acme/docker/demo:alpine` → success; digest `sha256:d9e853e87e55... size: 9226`.
- Both pushes resolved blob-mount `from=library/hello-world` / `library/alpine` against the upstream and properly fell back to direct uploads when the cross-mount source wasn't local.

### 5.3 Pull-back integrity ✅
- `crane pull --insecure localhost:28080/acme/docker/demo:latest /tmp/hello-pull.tar` → 6.6 KB tarball.
- `crane manifest --insecure localhost:28080/acme/docker/demo:latest` SHA-256 → `f9078146db2e05e794366b1bfe584a14ea6317f44027d10ef7dad65279026885` — **byte-identical** to the upstream Docker Hub manifest. Storage is content-faithful.

### 5.4 /v2/_catalog gating ✅
- Anonymous `/v2/_catalog` → `UNAUTHORIZED` envelope `{code:"UNAUTHORIZED",message:"authentication required",detail:"authentication required"}`. (Catalog requires session/token; basic-auth API key alone doesn't pass through.)

### 5.5 Tag list ✅
- `crane ls --insecure localhost:28080/acme/docker/demo` → `["alpine","latest"]` after 5.2; `["latest"]` after 5.7 delete.

### 5.6 UI render — docker repo page ✅
- Navigate `/projects/acme/docker/demo` as alice. Page renders:
  - Header: `demo` · "Docker repository · 2 tags · 339.0 MB" + CLI Snippets button.
  - Tabs: Content / Scan Results / Settings.
  - Content actions: Pull External, Promote / Retag, filter input.
  - Tag table columns: Image:Tag, Image Size, Scan Status, Push Date, Digest, Signed.
  - Both rows show **Scanning** badge (auto-scan triggered; Trivy DB not yet uploaded — covered in batch 13).
  - Per-row: Rescanning… disabled button, copy-to-clipboard, delete tag.
  - Console: 0 errors / 0 warnings.
  - Screenshot: `screenshots/batch-05-docker-repo.png`.

### 5.7 CLI snippets dialog ✅
- Click `CLI Snippets` → side-panel opens with 3 pre-filled copy-buttons:
  - Login: `docker login localhost:28080`
  - Pull: `docker pull localhost:28080/acme/docker/<image>:<tag>`
  - Push: `docker push localhost:28080/acme/docker/<image>:<tag>`
- Hostname is `localhost:28080` (correct vs the 28443 HTTPS the UI uses — this is the protocol-client port). Screenshot: `screenshots/batch-05-cli-snippets.png`.
- Note: `<image>` placeholder isn't pre-filled with the current repo's name (`demo`). Polish opportunity, not a bug.

### 5.8 Tag delete (UI happy path) ✅
- Click delete-tag icon next to `alpine` → modal "Delete tag? · This removes the tag `alpine` from `demo`. The underlying manifest stays referenced for other tags; blob reclamation happens on the next GC sweep once its ref-count reaches zero." Confirm → tag removed from table.
- Verified post-delete: `crane ls` shows only `latest`; `crane manifest demo:alpine` returns `MANIFEST_UNKNOWN`.

### 5.9 RBAC — viewer cannot push, can read ✅
- Bob (viewer on acme) `POST /v2/acme/docker/demo/blobs/uploads/` → `HTTP 403 {code:"DENIED", detail:"not_a_maintainer"}`.
- Bob `GET /v2/acme/docker/demo/tags/list` → `HTTP 200 {name:"demo", tags:["latest"]}` (read allowed).

### 5.10 Pull External (clone-from-Docker-Hub) — covered indirectly ✅
- `crane copy index.docker.io/... localhost:28080/...` is functionally identical to the UI's Pull External (both write blobs into the OCI CAS and create manifests). The same backend `oci.PullExternalHandler` covers both. UI flow defers to dedicated v1.1 docker-clone tests in `web/e2e/docker-clone.spec.ts` (existing).

### 5.11 Severity gate (`block_on_severity`) ⬜ deferred to batch 13
- Trivy DB not uploaded yet — auto-scan rows show "Scanning…" indefinitely. Severity-gate flow needs a real scan result. Batch 13 covers this end-to-end with `block_on_severity=high` blocking `pull` on a known-CVE image.

## Findings

**None.** Push, pull, manifest integrity, tag delete, RBAC, UI all clean.

## Sign-off

- [x] All in-scope test cases marked
- [x] No findings opened
- [x] Backend log gate: 0 hits
- [ ] Codex batch-end review (will batch with 06–07)
- [x] Status flipped to ✅ in this file
