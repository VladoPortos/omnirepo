# Batch 08 — Helm (HTTP index.yaml)

**Status:** ✅ Closed — Codex clean (1 blocker fixed, 5 findings deferred, 1 Codex-surfaced cross-cutting tracked)
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/helm/local` with `mychart-0.2.0.tgz` (uploaded) + `vulny-0.1.0.tgz` (intentionally misconfigured fixture kept for cross-batch gate retests)
- `acme/helm/http-mirror` mirroring `https://grafana.github.io/helm-charts` filtered to `grafana-agent-operator` (66 versions, all scan-clean)

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

### F-08.1 Helm UI missing per-version Delete row action (deferred)
- **Severity:** R / real-bug
- **Repro:** `acme/helm/local` → Content tab → click `mychart` row → two versions appear in the expanded panel with Scan-report + Rescan buttons only; no Delete.
- **Root cause:** Same cross-cutting pattern as F-05.4 / F-06.3 / F-07.2. Backend `DELETE /<project>/helm/<repo>/charts/<filename>` works (204 + index regen verified via curl). UI side just never wired a mutation hook + confirm dialog.
- **Decision:** Defer — part of the tracked REST-shim + row-action bundle with F-05.4 siblings (promote, wipe, sync) and F-06.3 (RPM/APT). Single focused PR after batch 15 close.
- **Status:** 🟨 Open (deferred)

### F-08.2 Trivy misconfig findings dropped — severity gate defeated for Helm (BLOCKER)
- **Severity:** B / blocker
- **Repro:**
  1. Upload `vulny-0.1.0.tgz` (privileged pod + hostNetwork + runAsUser:0 + SYS_ADMIN, ~25 k8s misconfigs) to `acme/helm/local`.
  2. `sqlite3 ... "SELECT severity_summary_json FROM scans WHERE artifact_id='vulny-0.1.0.tgz'"` → `{"critical":0,"high":0,"low":0,"medium":0,"unknown":0}` — scan status "clean".
  3. `PATCH block_on_severity=high` on the repo.
  4. `curl /acme/helm/local/charts/vulny-0.1.0.tgz` → **200 OK** (should be 403 blocked_by_scan).
- **Root cause:** `internal/scan/parse.go`'s `trivyReportBlock` declared only `Vulnerabilities`, never `Misconfigurations`. Trivy is invoked with `--scanners vuln,secret,misconfig` (per `trivy.go:54`), and the handler correctly comments "Helm chart scans in particular light up the misconfig scanner on templates/*.yaml" — but every misconfig finding was silently dropped at parse time. Summary counts therefore stayed zero for Helm (whose only meaningful findings are misconfigs), and `repos.block_on_severity` never had a non-zero severity to compare against. Manual `trivy fs` against the server's materialized tmp layout found HIGH:16 MEDIUM:10 LOW:24 on the same chart; the parser ate all of them.
- **Fix (commit `14773c7` + Codex follow-up `bd3d8dc`):**
  - `parse.go`: added `Misconfigurations []trivyReportMisconf` to the block; new `trivyReportMisconf` struct (ID, AVDID, Title, Description, Severity, Resolution, Status); `ParseTrivyJSON` now iterates misconfigs, buckets **active findings (Status="FAIL" OR empty)** into Summary by severity, and appends each as a synthetic `Vuln{CVEID: m.ID, Package: block.Target, …}`. PASS / EXCEPTION statuses skipped so resolved rules don't inflate the gate.
  - Codex follow-up: if both `ID` and `AVDID` are absent the CVEID is synthesized as `MISCONF-<target>-<i>` so no row lands with `cve_id=""` (NOT NULL accepts empty strings and would silently accumulate "ghost" vulnerability rows).
  - `parse_test.go`: three tests pin the behavior — `TestParseTrivyMisconfigurationsCountInSummary` (FAIL counts, PASS excluded, resolution folded into Description), `TestParseTrivyMisconfigEmptyStatusCountsAndSynthesizesID` (empty Status counted + empty-ID fallback synthesis), `TestParseTrivyVulnsPlusMisconfigsAdditive` (CVEs + misconfigs coexist in the summary).
- **Retest:**
  - Rescan vulny → `severity_summary_json = {"critical":0,"high":16,"low":24,"medium":10,"unknown":0}`.
  - Pull with `block_on_severity=high` → **403** `{"error":"blocked_by_scan","severity":"high","scan_id":385}`.
  - Clean chart (`mychart-0.2.0.tgz`) → 200 under the same gate.
  - Lower gate to `critical` (vulny has 0 critical), wait for 30 s cache TTL, pull again → 200. Confirms the gate threshold comparison is additive and correct.
  - `go test ./internal/scan/... -count=1` → green (9/9, incl. 3 new misconfig cases).
- **Codex verify:** ✅ Clean — 2 minors applied (empty-Status + ID-fallback), 1 cross-cutting surfaced as F-08.6 (materialize_pkg double-counts same misconfig across tgz + extracted targets; gate still fires correctly either way, defer to batch-15).
- **Status:** ✅ Closed

### F-08.3 Helm mirror empty-state still offers upload snippet (deferred)
- **Severity:** m / minor
- **Repro:** `acme/helm/http-mirror` (mirror=true) before first sync → content tab title "No artifacts yet — Upload your first artifact using the snippet below" + `helm push`/`helm pull` (OCI) snippets.
- **Root cause:** Same class as F-06.4 (fixed for RPM/APT) but Helm's empty-state component never got the is_mirror-aware copy.
- **Decision:** Defer — trivial copy tweak, batch with F-08.4/F-08.5 minor UI nits when closing the tracked-open bundle after batch 15.
- **Status:** 🟨 Open (deferred)

### F-08.4 Helm repo header pluralizes versions as "charts" (deferred)
- **Severity:** m / minor
- **Repro:** `acme/helm/local` with `mychart-0.1.0.tgz` + `mychart-0.2.0.tgz` → header reads "Helm repository · 2 charts · 9.6 KB". That's *one* chart with two versions.
- **Decision:** Defer — one-line frontend fix (group-by chart name for the count). Low impact.
- **Status:** 🟨 Open (deferred)

### F-08.5 Helm expanded-version download links drop `/charts/` prefix (deferred)
- **Severity:** m / minor
- **Repro:** `acme/helm/local` → expand chart row → version link hrefs are `/acme/helm/local/mychart-0.1.0.tgz`, but the canonical upload+download path (and the path in `index.yaml`) is `/acme/helm/local/charts/mychart-0.1.0.tgz`. Both shapes return 200 today (backend accepts either), so the link works, but it's inconsistent.
- **Decision:** Defer — either the UI switches to the canonical shape or the protocol settles on one and 404s the other. Non-breaking.
- **Status:** 🟨 Open (deferred)

## Sign-off

- [x] All cases exercised
- [x] Final state:
  - [x] `acme/helm/local` has charts (`mychart-0.2.0.tgz` + `vulny-0.1.0.tgz`)
  - [x] `acme/helm/http-mirror` synced once (66 grafana-agent-operator versions)
- [x] F-08.2 fixed + Codex-clean after follow-up (`bd3d8dc`)
- [x] README.md batch 08 status flipped to ✅

## Test cases — actuals

- **8.1** ✅ Create `local` via Helm tab → combobox preselects "helm", name field accepts `local`, mirror off. Empty-state snippet renders `helm repo add local http://localhost:18080/acme/helm/local/`. No console/network errors.
- **8.2** ✅ `curl -u alice:<api-key> --upload-file mychart-0.1.0.tgz` → **201**. ~2 s later index.yaml shows `mychart 0.1.0` with correct digest. Audit log: `helm.upload helm_chart mychart-0.1.0.tgz ok`.
- **8.3** ✅ Authenticated GET on `/acme/helm/local/index.yaml` returns valid YAML; digest matches uploaded SHA; `urls` are relative (`charts/<file>`) — helm CLI resolves them against base URL, confirmed in 8.4. (Unauthenticated GET returns 401 by design — private project; batch spec's "curl with no auth" shorthand was ambiguous.)
- **8.4** ✅ `helm repo add acme … --username alice --password <api-key>` → OK. `helm repo update` OK. `helm search repo acme` shows `mychart 0.1.0`. `helm show chart acme/mychart` returns full Chart.yaml. `helm pull` downloads 4918 B tgz — SHA256 matches upload.
- **8.5** ✅ Package `mychart-0.2.0.tgz`, upload (201), coalescer regens. `helm repo update` + `helm search repo acme` shows `0.2.0`; `--versions` lists both `0.2.0` + `0.1.0`.
- **8.6** ⚠ (F-08.1) UI has no per-version Delete — used curl path. `DELETE /acme/helm/local/charts/mychart-0.1.0.tgz` → **204**. Index regenerated with only 0.2.0. `helm repo update && helm pull --version 0.1.0` → "chart not found in acme index". Audit `helm.delete helm_chart mychart-0.1.0.tgz ok`.
- **8.7** ✅ Create `http-mirror` with mirror=true, upstream `https://grafana.github.io/helm-charts`, filter `grafana-agent-operator`. Dialog hints "Uploads are disabled on mirror repos (403 repo_is_mirror)" + "Upstream URL cannot be changed after creation". Dropdown offers the existing credentials list (none applicable to helm).
- **8.8** ✅ Sync-now → job 12 `helm_sync done`, 66 files synced in ~7 s. `helm repo add http-mirror` + `helm search repo http-mirror/grafana-agent-operator` → `0.5.2` latest; `helm pull` downloads 19550 B 0.5.2 tgz. `helm install --dry-run testrel http-mirror/grafana-agent-operator --version 0.5.2` renders successfully (upstream deprecation warning is a chart concern, not ours).
- **8.9** ✅ `POST /api/v1/projects/acme/repos` with `{type:"helm", is_mirror:true, upstream_url:"oci://registry-1.docker.io/bitnamicharts"}` → **400** `{"code":"repo.mirror_url_invalid","message":"upstream URL must be http(s) with a host"}`. `http-mirror` item-count unchanged at 66, no new repo created.
- **8.10** ✅ Positive + negative gate paths. See F-08.2 retest above.
- **8.11** ✅ Sweep — 0 console errors/warnings on `/projects/acme/helm/local` and `/projects/acme/helm/http-mirror` post-navigation. Backend log scanned for `ERROR|panic|FATAL` in the batch window → 0 matches.
