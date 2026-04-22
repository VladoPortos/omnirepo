# Batch 08 — Helm (HTTP index.yaml)

**Status:** ⬜ Not started
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/helm/local` with an uploaded chart
- `acme/helm/http-mirror` mirroring an HTTP Helm repo (not OCI — Batch 09 covers OCI)

## Pre-flight

- [ ] `helm` CLI available
- [ ] A tiny `mychart-0.1.0.tgz` built locally (or the one from WALKTHROUGH-2 §3c)
- [ ] Logged in as alice
- [ ] Server log tail open

## Test cases

### 8.1 Create local Helm repo
- [ ] Type `Helm`, name `local`, mirror=false
- [ ] Empty state shows `helm repo add acme http://localhost:18080/acme/helm/local …` snippet

### 8.2 Upload chart
- [ ] `curl -u alice:<api-key> --upload-file mychart-0.1.0.tgz http://localhost:18080/acme/helm/local/charts/mychart-0.1.0.tgz`
- [ ] **Expected:** 201; index.yaml regenerated (coalescer ~2s); UI lists `mychart` with version 0.1.0
- [ ] Audit log: `helm.upload`

### 8.3 index.yaml content
- [ ] `curl http://localhost:18080/acme/helm/local/index.yaml` → valid YAML
- [ ] Chart entry has `digest` matching uploaded, `urls` absolute

### 8.4 `helm` CLI end-to-end
- [ ] `helm repo add acme http://localhost:18080/acme/helm/local --username alice --password <api-key>`
- [ ] `helm repo update`
- [ ] `helm search repo acme`
- [ ] `helm show chart acme/mychart`
- [ ] All succeed

### 8.5 Upload second version
- [ ] Build mychart-0.2.0.tgz (or any second version), upload
- [ ] index.yaml has both versions; `helm search repo acme` shows latest

### 8.6 Delete chart
- [ ] UI row action → Delete 0.1.0
- [ ] **Expected:** 204; index.yaml regenerated; `helm install mychart --version 0.1.0` fails after repo update
- [ ] Audit log: `helm.delete`

### 8.7 Create HTTP mirror
- [ ] Type `Helm`, name `http-mirror`, mirror=true
- [ ] Upstream URL: a small HTTP Helm repo (or a local fixture we host)
- [ ] Filters: chart-name glob

### 8.8 Mirror sync — HTTP upstream
- [ ] Sync now → progress → complete
- [ ] UI lists mirrored charts
- [ ] `helm repo add local-mirror http://localhost:18080/acme/helm/http-mirror` and `helm install` a mirrored chart — success

### 8.9 Graceful handling of `oci://` upstream in HTTP mirror mode
- [ ] (If mirror type forces http but upstream is `oci://` URL) — **Expected:** friendly error or delegation to Helm OCI repo type (Batch 09)
- [ ] No partial data corruption on the repo

### 8.10 Severity gate on Helm
- [ ] If a mirrored chart contains a vulnerable image, set `block_on_severity=high`; `helm pull` that chart → 403 envelope
- [ ] Clean chart → success

### 8.11 Console + network sweep
- [ ] Zero errors/warnings

## Findings

_(F-08.N)_

## Sign-off

- [ ] All cases passed
- [ ] Final state:
  - [ ] `acme/helm/local` has a chart
  - [ ] `acme/helm/http-mirror` synced once
- [ ] All F-08.* closed
- [ ] README.md batch 08 status flipped to ✅
