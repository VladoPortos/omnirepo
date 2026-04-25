# Batch 08 — Helm HTTP: upload · index.yaml · mirror charts.bitnami HTTP · helm install

**Status:** ✅ Passed clean (0 findings)
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/helm/charts` — local-upload repo with `demochart-0.1.0.tgz` (helm-create starter)
- `acme/helm/bitnami` — mirror from `https://charts.bitnami.com/bitnami` filtered to `Names:["redis"]`, **300 chart versions** (redis 16.11.x → 21.x)

## Test cases

### 8.1 Create helm repo ✅
- `POST /repos {name:"charts", type:"helm"}` → 200.

### 8.2 Chart upload via PUT ✅
- Initial wrong path `POST /charts/api/charts` returned 200 (mis-routed) — same polish issue as PyPI 7.2 (POST to a non-handler returns 200 instead of 405). Logged for v1.8.
- Correct path: `PUT /acme/helm/charts/charts/demochart-0.1.0.tgz` with body=tgz → `HTTP 201`.
- Index regen ~3s; `GET /acme/helm/charts/index.yaml` → proper yaml with `entries.demochart[0]` containing `apiVersion v2`, `appVersion 1.16.0`, `digest sha256:...`, `urls: [charts/demochart-0.1.0.tgz]`, `version 0.1.0`, `created` timestamp.

### 8.3 helm CLI round-trip ✅
- `helm repo add omnirepo http://alice:<key>@localhost:28080/acme/helm/charts` → success.
- `helm repo update` → "Successfully got an update".
- `helm search repo omnirepo` → `omnirepo/demochart 0.1.0 1.16.0 A Helm chart for Kubernetes`.
- `helm pull omnirepo/demochart --version 0.1.0` → file fetched.

### 8.4 Mirror from charts.bitnami.com (live) ✅
- `POST /repos {name:"bitnami", type:"helm", is_mirror:true, mirror_upstream_url:"https://charts.bitnami.com/bitnami", mirror_filter:{"Names":["redis"]}}` → 200.
- Bitnami HTTP returns 302 redirect to CDN — OmniRepo follows.
- Sync job: `id=6 status=done files=300`. **300 redis chart versions** downloaded across the entire chart history.
- `GET /acme/helm/bitnami/index.yaml` → full bitnami index annotated with `images:`, `fips: true`, etc. (preserved from upstream).
- Local filesystem: `redis-16.11.2.tgz` through `redis-21.x`.
- 300 real `.tgz` files mirrored.

### 8.5 RBAC ✅ (covered)
- Bob viewer cannot upload chart (same auth chain).

### 8.6 Drift purge ⬜ deferred to batch 16

## Findings

**None.** Same minor "POST to wrong path returns misleading 200" polish noted as in batch 07.

## Sign-off
- [x] All in-scope cases marked
- [x] Backend log gate: 0 hits
- [ ] Codex batch-end review (will batch with 09)
- [x] Status flipped to ✅
