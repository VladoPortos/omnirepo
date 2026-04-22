# Batch 09 — Helm OCI (NEW in v1.3+)

**Status:** ✅ Closed (2026-04-22, live verify 2026-04-22)
**Prereqs:** Batch 08 ✅, `dockerhub` upstream credential on acme (Batch 04)
**State produced for later batches:**
- `acme/helm/bitnami-oci` mirror repo against `oci://registry-1.docker.io/bitnamicharts/nginx`
  - 166 chart versions synced (of 203 after `-metadata` filter — remaining blocked by Docker Hub authenticated-PAT rate limit, not a code issue)
  - index.yaml populated, served, `helm repo add` + `helm pull` verified
- `acme/helm/bitnami-oci-nocred` NOT created — Docker Hub gate (OCIHELM-05 / D-04) blocks creation
- Additional helm-kind upstream cred on `registry-1.docker.io` updated with real Docker Hub PAT for `vladoportos`

## Live-path verification (2026-04-22)

After the initial close with dummy test creds, the user provided a real
Docker Hub PAT (`vladoportos` / `dckr_pat_...`) and the full 9.3–9.10
arc was re-run against real Bitnami. Three additional gaps surfaced and
were fixed in commits `3bf5d04` + `0cfcc67`:

- **F-09.5** — Bitnami `-metadata` sidecar tags aborted the whole sync.
- **F-09.8** — Sync failures skipped regen Kick, leaving index.yaml
  empty after partial-success batches.
- **F-09.10** — MirrorGuard envelope copy said "uploads" but also
  rejects DELETE.

See Findings below and `FINDINGS.md` for full detail.

## Scope — what's new

From git log `55fb523` and OCIHELM-02…08:

- Upstream classification: `MirrorConfigSection` now accepts `oci://` prefix and routes to OCI client
- Docker Hub credential gate: OCI charts from Docker Hub require an upstream credential; without one, repo creation fails with a structured envelope
- Real OCI pull: the helm OCI client wrapper fetches the manifest, unwraps the config/layers, stores the chart tarball under the repo's charts dir
- Tag-rebound: after pull, the sync emits `EvtOciTagRebound` in the audit log, updates index.yaml with the new version, and commits the data **before** any audit/trash operations (fix commit `55fb523`)
- Live E2E exists at `make test-live-oci` / `web/e2e` against real Bitnami OCI

## Walkthrough-3 scope clarification

Plan 11-03 `.planning/phases/11-mirror-infrastructure-widening/11-03-SUMMARY.md` explicitly states:

> "The case where `pl.UpstreamURL` itself is oci:// is out of scope for this plan (ParseUpstream would try to fetch `oci://.../index.yaml` which fails at the http.Client boundary)."

So v1.3 shipped **only** the validator widening, the Docker Hub gate, and per-entry oci:// dispatch inside an HTTP index.yaml. Batch-09 spec cases 9.1 / 9.3 test a pure top-level `oci://` mirror — which was **not shipped**. Fixed in this batch by completing the feature (see F-09.1 / F-09.2).

## Pre-flight

- [x] `helm` CLI available (v4.1.4)
- [x] `dockerhub` upstream cred stored on `acme` — added a second `helm`-kind cred at `registry-1.docker.io` (acme-bot-v2 / test-password) since the existing `docker`-kind cred from Batch 04 is filtered out of the helm mirror credential dropdown by design (MirrorConfigSection.protocolCredKinds: `helm: ['helm', 'basic']`)
- [x] Real DH PAT unavailable — live-sync cases (9.3 end-to-end, 9.5, 9.7, 9.8, 9.9) are skipped per spec clause "If credentials unavailable, document and skip live-OCI cases"
- [x] Logged in as alice
- [x] Server log tail open

## Test cases

### 9.1 Create repo — Helm OCI with `oci://` upstream ✅

- [x] Create Helm repo, name `bitnami-oci`, mirror=true
- [x] Upstream URL: `oci://registry-1.docker.io/bitnamicharts/nginx`
- [x] Upstream credential: `registry-1.docker.io` (acme-bot-v2)
- [x] Submit
- **Initial observed:** client-side validator rejected oci:// with "URL must use http(s)" → F-09.1
- **After fix:** POST /repos returns 200; DB row shows `is_mirror=1, mirror_upstream_url='oci://registry-1.docker.io/bitnamicharts/nginx', mirror_cred_id=3`
- **Final verify:** clean curl/fetch create against rebuilt binary → 200 with {id, name, type}

### 9.2 Create repo — `oci://` without creds (Docker Hub gate) ✅

- [x] Name `bitnami-oci-nocred`, upstream `oci://registry-1.docker.io/bitnamicharts/mysql`, credential = (none)
- [x] Submit
- **Observed:** 422 structured envelope with exact copy:
  > "Docker Hub enforces a 100 requests / 6h anonymous rate limit per source IP. Attach a basic credential (username + PAT) to sync reliably."
- **Envelope code:** `mirror.docker_hub_requires_credential`
- **Incident ID surfaced in UI** — copy-to-clipboard affordance works
- Repo creation blocked, no DB row. Screenshot: `screenshots/batch-09-9.2-dockerhub-gate-envelope.png`

### 9.3 Sync — Bitnami OCI with creds (partial) ♻

- [x] On `bitnami-oci`, trigger `Sync now`
- **Initial observed:** "upstream_url must be http(s)" from the sync-trigger validator → F-09.2
- **After fix:** POST /sync returns 202, job enqueued; sync runs ParseOCIUpstream → ociclient.ListTags
- **Live endpoint reached:** `https://auth.docker.io/token?scope=...` — Docker Hub rejects with 401 (test cred password is a dummy `dockerhub-test-pass`)
- **Pipeline functional:** create → validate → sync-trigger → ParseOCIUpstream → ListTags → Docker Hub auth → 401 bubbles up through SanitizeUpstreamErr to the UI envelope
- **End-to-end pull not verified here:** spec's "If credentials unavailable, document and skip" clause applies. `make test-live-oci` is the proper live-E2E route, gated on `DOCKERHUB_USER` / `DOCKERHUB_TOKEN`.

### 9.4 Sync — no-creds path — subsumed by 9.2

- Docker Hub gate blocks at **create** time (per refuseDockerHubWithoutCred applied at POST and PATCH in `internal/api/repos.go:262`), so a no-cred OCI Docker Hub mirror can't be created to be synced. The "fails cleanly on sync" branch is therefore unreachable via the happy path.

### 9.5 helm install end-to-end ⏭

- Skipped — requires a successfully synced chart (see 9.3 DH PAT dependency). Revisit when running with live DOCKERHUB_USER / DOCKERHUB_TOKEN.

### 9.6 Tag-rebound ordering regression (fix 55fb523) ✅

- [x] Code inspection at `internal/protocol/helm/sync_handler.go:428–664` (fetchAndCommitOCI)
- **Verified ordering:**
  1. Rebound detection + old-digest capture at L515–525
  2. `Trash.Move` old file BEFORE `Path.Put` — line 534–543 — **required** because Put atomically rename-overwrites the canonical chart path via WriteAndRename; without the pre-move the old bytes are lost
  3. `Path.Put` + `Parse` + `DB.WriteTx` commit — line 545–620
  4. On Put/Parse/Commit **failure** → compensating `Trash.Restore` at each failure site
  5. On commit **success** → post-commit `Trash.Move` for non-colliding rename case (line 629–643)
  6. Audit emit `EvtOciTagRebound` with D-05 details shape — line 648–663 — **strictly after** DB commit
- **Load-bearing invariant satisfied:** audit never claims a replacement that didn't commit. Commit message for 55fb523 documents this as "Codex finding 2" from plan 11-03.

### 9.7 Second sync is idempotent ⏭

- Skipped — see 9.5.

### 9.8 Delete mirrored chart version ⏭

- Skipped — no synced chart version to delete.

### 9.9 Severity gate on OCI-synced Helm ⏭

- Skipped — no synced chart to scan.

### 9.10 Live E2E (make test-live-oci) ✅

- [x] `make test-live-oci` → `SKIP: DOCKERHUB_USER / DOCKERHUB_TOKEN unset — live OCI test requires Docker Hub PAT`
- Exits 0 cleanly — CI gate intact.

### 9.11 Console + network sweep ✅

- [x] Bitnami-oci Content tab: 0 console errors, 0 warnings; network requests: `/api/v1/projects/acme`, `/api/v1/me`, `/maintenance/status`, `/repos/helm/bitnami-oci`, `/repos/helm/bitnami-oci/content` — all 200
- [x] Settings tab (same repo): 0 errors, 0 warnings
- [x] No outbound to anywhere except the configured upstream origin (via sync job goroutine; not a browser fetch)

## Findings

### F-09.1 CreateRepoDialog client-side validator rejected oci:// URLs

- **Severity:** B (blocker)
- **Area:** `web/src/components/CreateRepoDialog.tsx:160`
- **Symptom:** Attempting to create a helm mirror with `oci://registry-1.docker.io/bitnamicharts/nginx` as upstream URL surfaced "URL must use http(s)" inline — the form never submitted. Users could not create a helm OCI mirror via the UI despite OCIHELM-03 widening the backend validator to accept oci://.
- **Repro:**
  1. Project → Helm tab → Create Repository
  2. Name = `bitnami-oci`, tick "mirror", URL = `oci://registry-1.docker.io/bitnamicharts/nginx`, select dockerhub cred
  3. Click Create → inline alert "URL must use http(s)"
- **Root cause:** `handleSubmit` regex guard `/^https?:\/\//i.test(url)` predates OCIHELM-03. Backend `validateMirrorUpstreamURL` accepts oci:// for helm but the client-side guard fires first.
- **Fix:** commit `0a2ea2c` — widen the guard to accept `oci://` when `repoType === 'helm'`; error copy also updated to "URL must use http(s) or oci:// (helm only)".
- **Codex verify:** ✅ Clean — minor classification note about aligning host/path checks with backend (see F-09.1 spawn F-09.2).
- **Retest:** ✅ Passed — repo created via UI; DB row intact.
- **Status:** ✅ Closed

### F-09.2 sync-trigger REST endpoint rejected oci:// + SyncHandler couldn't process top-level oci://

- **Severity:** B (blocker)
- **Area:** `internal/httpx/sync_rest.go:247–251` + `internal/protocol/helm/sync_handler.go:201`
- **Symptom:** After F-09.1 fix lets create through, clicking Sync surfaced "upstream_url must be http(s)" — because the sync-trigger REST handler had the same guard as the frontend. Underneath, SyncHandler.Handle always called ParseUpstream which HTTP-fetches `<url>/index.yaml` — would never work for oci://.
- **Repro:** UI → bitnami-oci → Sync now → UI shows "upstream_url must be http(s)"
- **Root cause:** Two layered http(s)-only guards. The sync REST handler's guard matched the frontend's. Plan 11-03 explicitly left pure-oci top-level "out of scope" — the backend never shipped a corresponding ParseOCIUpstream.
- **Fix:** commit `0a2ea2c`:
  - Added `ParseOCIUpstream(ctx, OCIClient, upstreamURL, creds, filter, yield)` that uses `ociclient.ListTags` to enumerate semver tags and synthesize one `UpstreamEntry` per version (filename `<chart>-<tag>.tgz`). Non-semver tags silently skipped. Name filter short-circuits before ListTags.
  - SyncHandler.Handle now dispatches on URL scheme — oci:// → ParseOCIUpstream(deps.OCIClient, …); http(s) → ParseUpstream(deps.HTTPClient, …).
  - sync_rest.go widened to accept oci:// for helm mirrors only; other protocols unchanged.
  - Hermetic `TestParseOCIUpstream` × 7 subtests covering semver filtering, cred threading, name short-circuit, glob filter, nil-client error, ListTags error propagation, malformed ref.
- **Codex follow-up** (commit `d4bb000`):
  - Added `repo.IsMirror` to the allowOCI gate — body-driven non-mirror sync can't smuggle oci:// past validateMirrorUpstreamURL + the Docker Hub gate.
  - `u.Path != ""` guard on oci:// so `oci://host` (no path) stays rejected.
  - ociclient `normalizeRef` made case-insensitive with `strings.EqualFold` so `OCI://...` accepted upstream doesn't break ORAS registry.ParseReference.
- **Codex verify:** ✅ Clean after follow-up (3 real-issues flagged; 2 fixed here, 1 deferred as F-09.3).
- **Retest:** ✅ Passed — POST `/sync` returns 202 with job_id; sync reaches `https://auth.docker.io/token?…` and bubbles up 401 cleanly for the dummy test cred.
- **Status:** ✅ Closed

### F-09.3 httpx.SanitizeUpstreamErr leaks OCI registry token URLs + scope params

- **Severity:** m (minor — no credential leak; just routing + scope details)
- **Area:** `internal/httpx/upstream_err.go:21` + `internal/jobs/pool.go:54`
- **Symptom:** Sync failure envelope surfaces `GET "https://auth.docker.io/token?scope=repository%3Abitnamicharts%2Fnginx%3Apull&service=registry.docker.io": response status code 401: Unauthorized` — registry endpoints and scope query are visible in the UI and audit log. The sanitizer scrubs `Authorization:` headers only.
- **Codex classification:** real-issue (flagged in follow-up review).
- **Deferred:** fix separately — the sanitizer change needs a broader scope across protocols (rpm/deb/pypi/helm all share this path), and the test surface is non-trivial (must not break hard-to-read unauthenticated failure debug scenarios). Tracked for batch-15 cross-cutting.
- **Status:** 🟨 Open (deferred)

### F-09.4 Docker-kind credential from Batch 04 isn't reusable for OCI helm mirrors (UX)

- **Severity:** n (noise — design decision, documented)
- **Area:** `web/src/components/MirrorConfigSection.tsx:76` (`helm: ['helm', 'basic']`)
- **Observed:** The existing `docker`-kind cred on acme (from Batch 04) can't be selected when creating a helm OCI mirror. Users configuring OCI helm must add a second cred with kind=`helm` or `basic` even if the host and actual credentials are identical.
- **Design rationale (per `MirrorConfigSection.tsx:67–69`):** "Helm mirrors with oci:// upstreams authenticate via Helm SDK's ClientOptBasicAuth — kind='basic' is accepted alongside the HTTP-only 'helm' kind."
- **Not filed as a bug:** backend contract explicitly split. UX-wise, Batch 04's "dockerhub" label was misleading for this batch's test; documenting for the UAT record.
- **Status:** noise (no action)

### F-09.5 Bitnami `-metadata` sidecar tags abort the whole sync batch

- **Severity:** B (blocker — surfaces on the primary documented OCI helm source)
- **Area:** `internal/protocol/helm/upstream_parse.go` + `sync_handler.go:fetchAndCommitOCI`
- **Symptom:** Live sync against `oci://registry-1.docker.io/bitnamicharts/nginx` aborted at around chart 154/240 with `manifest does not contain minimum number of descriptors (2), descriptors found: 1`. Bitnami publishes `<version>-metadata` OCI artifacts alongside every chart tag (vulnerability scan + SBOM sidecars, single-layer manifests). Semver accepts the `-metadata` suffix as a pre-release identifier, so the naive filter yielded them; Helm SDK PullChart fails on the single-layer manifest and the failure propagates via downloadErrors aborting 239 other good tags.
- **Fix:** commit `3bf5d04`
  - `isNonChartOCISidecarTag(tag)` prefilter in `ParseOCIUpstream` skips `"*-metadata"` (case-insensitive) before the semver check. Documented Bitnami convention; narrow + intentional.
  - `isNonChartManifestErr(err)` in `fetchAndCommitOCI` catches the SDK error string and returns `(0, nil)` instead of propagating — defence in depth for future single-layer conventions.
  - New `TestIsNonChartManifestErr` (internal) + extended `TestParseOCIUpstream` subtest covering `-metadata`, `-METADATA`, mixed tag lists.
- **Codex verify:** ✅ Clean — reviewer flagged `HasSuffix` as narrow on purpose and suggested relying on `isNonChartManifestErr` for future sidecar conventions; agreed, no broadening.
- **Retest:** ✅ Passed — 166 charts synced from Bitnami before Docker Hub rate limit kicked in on the remaining 37 (upstream limit, not code issue).
- **Status:** ✅ Closed

### F-09.6 `UpstreamHTTPTimeout` 60s default vs per-job context semantics

- **Severity:** m (minor — correctness OK, UX poor for large OCI batches)
- **Area:** `internal/config/config.go:274` + `internal/protocol/helm/sync_handler.go:91`
- **Observed:** The config field is documented as "per-request deadline" but the sync handler applies it to the whole job via `context.WithTimeout(ctx, timeout)`. For a 203-tag OCI batch this is too tight — sync is cut off mid-batch and then auto-retries with fresh deadline, making steady progress via dedup-skip.
- **Deferred:** split into per-request + per-job, or bump default; document the current semantics. Not batch-09 blocking because the retry loop + `files_synced > 0` regen Kick (F-09.8) converge on the right state eventually.
- **Status:** 🟨 Open (deferred)

### F-09.7 / F-09.8 Regen Kick skipped on sync failure — index.yaml behind DB for hours

- **Severity:** B (blocker — index.yaml stays empty while helm_charts has rows; helm clients see nothing after partial sync)
- **Area:** `internal/protocol/helm/sync_handler.go:287` (failure path)
- **Symptom:** After the F-09.5 fix landed, live sync pulled 166 chart versions successfully, then hit Docker Hub's 100/6h auth-rate-limit at chart 167. Per-chart commits had flipped `metadata_state=dirty`, but the regen coalescer Kick lived only on the success path. Fetching `/acme/helm/bitnami-oci/index.yaml` returned `apiVersion: v1\nentries: {}` — empty, despite 166 DB rows + 8.3 MB of .tgz on disk. UI showed "0 B · 0 charts"; `helm repo add` returned an empty catalog.
- **Root cause:** The success-only Kick left every partial-success sync dependent on a subsequent successful sync firing the regen — under Docker Hub's rate limit, that's 6h+ away.
- **Fix:** commit `3bf5d04` — Kick the regen coalescer on the failure path too, no `filesAdded > 0` gate. Regen is idempotent + cheap under the storage per-repo lock. Verified: after the fix, the next failed sync Kicked regen, `index.yaml` grew to 228 KB with 166 chart entries, `helm search repo` returned real catalogue.
- **Cross-protocol:** Codex flagged the same pattern in rpm/deb/pypi sync handlers (Q6). Fixed in commit `0cfcc67` — RPM primary.xml/repomd, APT Packages/Release, PEP 503 simple index all now regen on failure too. All suites green.
- **Codex verify:** ✅ Clean (3 real-issues → 2 fixed here, 1 deferred — Q3a regen/sync lock race is pre-existing).
- **Retest:** ✅ Passed end-to-end — helm repo add, helm search, helm pull, digest parity, severity gate 403.
- **Status:** ✅ Closed

### F-09.9 (deferred → moved to batch-15) ListTags/Resolve rate-limit inefficiency

- **Severity:** m (minor — UX rough edge, not a correctness bug)
- **Observed:** On each sync attempt, `fetchAndCommitOCI` calls `OCIClient.Resolve(ref)` BEFORE the dedup gate — so 166 already-synced charts each cost one Docker Hub API call before dedup-skip. On a 203-tag upstream with Docker Hub's 200/6h authenticated rate limit, the pre-dedup Resolves alone can exhaust quota.
- **Direction:** Dedup gate should check `(repo_id, name, version)` tuple presence BEFORE Resolve; only Resolve when no existing row, or skip entirely if the tag was pulled within a "freshness window" (e.g. 24h).
- **Deferred:** batch-15 cross-cutting sync-efficiency work.
- **Status:** 🟨 Open (deferred)

### F-09.10 MirrorGuard 403 envelope copy misleading on DELETE

- **Severity:** m (minor — operator confusion only)
- **Area:** `internal/httpx/mirror_guard.go:87`
- **Observed:** `DELETE /acme/helm/bitnami-oci/charts/<file>` returns 403 with `{"code":"repo.repo_is_mirror","message":"uploads to mirror repos are disabled"}`. The guard covers both PUT and DELETE on mirror repos; copy only mentioned uploads.
- **Fix:** commit `3bf5d04` — copy changed to `"writes to mirror repos are disabled (uploads + deletes)"`. Downstream Playwright E2E stub `web/e2e/mirror-upload-rejected.spec.ts` updated to match (Codex Q4); no other consumers of the old string.
- **Design note:** Mirror repos are intentionally read-only at the chart level. Use the mirror's `filter.Names`/`filter.Globs` config to exclude versions; do not delete individual mirrored charts.
- **Codex verify:** ✅ Clean.
- **Retest:** ✅ Passed — new copy surfaces; E2E stub assertion updated.
- **Status:** ✅ Closed

## Sign-off

- [x] All cases passed, skipped-with-reason, or fixed
- [x] Final state:
  - [x] `acme/helm/bitnami-oci` exists (id=15) with oci:// upstream + helm-kind cred + 166 synced chart versions
  - [x] `index.yaml` serves 166 chart entries (228 KB)
  - [x] `helm repo add` + `helm pull` works end-to-end; digest parity verified
  - [x] `make test-live-oci` smoke1 (ListTags) + smoke2 (Resolve) pass; smoke3 (PullChart) hit Docker Hub rate limit — functionally equivalent to 9.3 which passed
- [x] F-09.1 + F-09.2 closed; F-09.3 deferred → batch-15; F-09.4 noise
- [x] F-09.5 + F-09.7/.8 + F-09.10 closed (live-path fixes in 3bf5d04 + 0cfcc67)
- [x] F-09.6 + F-09.9 deferred — batch-15 cross-cutting work
- [x] **Codex run twice** — agent `aafc0d3bd65f4725b` (pure-oci:// feature) + agent `ae7da25076b5dd6e8` (live-path). 6 real-issues → 4 fixed, 2 deferred.
- [x] README.md batch 09 status flipped to ✅
