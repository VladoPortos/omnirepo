# Batch 09 — Helm OCI (NEW in v1.3+)

**Status:** ⬜ Not started
**Prereqs:** Batch 08 ✅, `dockerhub` upstream credential on acme (Batch 04)
**State produced for later batches:**
- `acme/helm/bitnami-oci` mirror repo against a Bitnami OCI chart
- At least one chart synced with tag-rebound metadata (.tgz + version entry in index.yaml)

## Scope — what's new

From git log `55fb523` and OCIHELM-02…08:

- Upstream classification: `MirrorConfigSection` now accepts `oci://` prefix and routes to OCI client
- Docker Hub credential gate: OCI charts from Docker Hub require an upstream credential; without one, the sync fails cleanly with a structured envelope
- Real OCI pull: the helm OCI client wrapper fetches the manifest, unwraps the config/layers, stores the chart tarball under the repo's charts dir
- Tag-rebound: after pull, the sync emits `EvtOciTagRebound` in the audit log, updates index.yaml with the new version, and commits the data **before** any audit/trash operations (fix commit `55fb523`)
- Live E2E exists at `make test-live-oci` / `web/e2e` against real Bitnami OCI

## Pre-flight

- [ ] `helm` CLI available
- [ ] `dockerhub` upstream cred stored on `acme` (Batch 04) with valid Docker Hub credentials (real username + password / PAT)
- [ ] If credentials unavailable, document and skip live-OCI cases; fall back to a local OCI fixture
- [ ] Logged in as alice
- [ ] Server log tail open

## Test cases

### 9.1 Create repo — Helm OCI with `oci://` upstream
- [ ] Create Helm repo, name `bitnami-oci`, mirror=true
- [ ] Upstream URL: `oci://registry-1.docker.io/bitnamicharts/nginx`
- [ ] Upstream credential: `dockerhub`
- [ ] Submit
- [ ] **Expected:** repo created; CreateRepoDialog's MirrorConfigSection correctly accepted the `oci://` URL (OCIHELM-02 widen)
- [ ] Audit log: `repo.create` with classify result for oci upstream

### 9.2 Create repo — `oci://` without creds (Docker Hub gate)
- [ ] Create another Helm OCI repo, name `bitnami-oci-nocred`, upstream `oci://registry-1.docker.io/bitnamicharts/mysql`, credential = (none)
- [ ] Submit
- [ ] **Expected:** repo creation allowed OR blocked with envelope explaining "Docker Hub OCI requires credentials" — document observed behavior and confirm it matches OCIHELM-03 design
- [ ] If allowed: sync will fail clearly in 9.4; if blocked at create: envelope is structured

### 9.3 Sync — Bitnami OCI with creds
- [ ] On `bitnami-oci`, click Sync now
- [ ] **Expected:** progress stream (manifest fetch, layer pull, tag-rebound), final "Sync complete · N files · X MB"
- [ ] Audit log events (in order):
  1. `mirror.sync.start`
  2. `helm.oci.pull.success`
  3. `helm.oci.tag_rebound` (EvtOciTagRebound) — **per fix commit 55fb523, this must come after the commit** so retries don't observe missing DB state
  4. `mirror.sync.success`
- [ ] UI: chart `nginx` with one or more versions in the table
- [ ] `.tgz` file visible in storage under `acme/helm/bitnami-oci/charts/`
- [ ] `index.yaml` rewritten with entries for the synced versions

### 9.4 Sync — no creds path
- [ ] On `bitnami-oci-nocred`, click Sync now
- [ ] **Expected:** clean failure envelope surfaced in the UI, not a crash
- [ ] Audit log: `mirror.sync.failure` with a `reason` field explaining missing creds
- [ ] Repo state unchanged

### 9.5 helm install end-to-end
- [ ] `helm repo add acme-oci http://localhost:18080/acme/helm/bitnami-oci --username alice --password <api-key>`
- [ ] `helm repo update`
- [ ] `helm search repo acme-oci/nginx`
- [ ] `helm pull acme-oci/nginx --version <synced-version>` → succeeds
- [ ] Digest on server matches digest of upstream for that version (smoke proof of tag-rebound integrity)

### 9.6 Tag-rebound ordering regression (fix 55fb523)
- [ ] Simulate a mid-sync failure: start a sync, kill the server after the layer pull but before the audit event (or use a test hook if available)
- [ ] Restart server, sync again
- [ ] **Expected:** no orphan audit row referencing a chart version that doesn't exist in index.yaml
- [ ] If hard to reproduce live, verify the fix via unit test coverage by inspecting `internal/protocol/helm/oci/sync.go` (or wherever the fix landed): the commit order must be commit → audit → trash, never audit first
- [ ] Codex prompt must explicitly ask whether the commit ordering is preserved in the current code

### 9.7 Second sync is idempotent
- [ ] Click Sync now again on `bitnami-oci`
- [ ] **Expected:** no new chart versions (since upstream is stable for the duration of this test); audit log shows `mirror.sync.success` but no duplicate `tag_rebound` events
- [ ] Storage unchanged

### 9.8 Delete mirrored chart version
- [ ] UI row action → Delete on a synced chart version
- [ ] **Expected:** 204; index.yaml updated; `helm install` for that version fails after refresh
- [ ] Next sync re-adds the chart (if still upstream) — document behavior

### 9.9 Severity gate on OCI-synced Helm
- [ ] If the synced chart bundles vulnerable images, set `block_on_severity=high`; `helm pull` the chart → 403 envelope

### 9.10 Live E2E (make test-live-oci)
- [ ] Run `make test-live-oci` from repo root
- [ ] **Expected:** passes green; the live-OCI Playwright spec exercises real Bitnami pull + tag-rebound
- [ ] If it fails, copy the failure into a finding

### 9.11 Console + network sweep
- [ ] Repo detail, sync detail, settings
- [ ] Zero errors/warnings
- [ ] No outbound requests to anywhere except the configured upstream origin

## Findings

_(F-09.N)_

## Sign-off

- [ ] All cases passed
- [ ] Final state:
  - [ ] `acme/helm/bitnami-oci` has synced at least one version
  - [ ] `make test-live-oci` green (or skipped-with-reason documented)
- [ ] All F-09.* closed
- [ ] **Codex MUST be run** on the OCIHELM-02…08 commits + 55fb523 — this is new code, high risk
- [ ] README.md batch 09 status flipped to ✅
