# Walkthrough #3 — Consolidated findings

All findings across all batches. Source of truth for release gate.

Severity: **B** blocker · **R** real-bug · **m** minor · **n** noise.
Status: 🟨 Open · ✅ Closed · 🟥 Rejected (disputed)

| ID | Sev | Area | Title | Fix commit | Codex | Retest | Status |
|----|-----|------|-------|-----------|-------|--------|--------|
| F-01.1 | R | httperr.Write | All 4xx logged at slog ERROR — floods alerting + trips backend-log gate | `0f2dfd1` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-01.2 | n | browser console | Browser-native "Failed to load resource" logs on every 4xx fetch | _(no fix — cure worse than disease)_ | — | ✅ Accepted | ✅ Closed |
| F-01.3 | R | AuthGuard + LoginPage | Deep-link lost across login redirect | `12ac7e1` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-01.4 | m | openapi.yaml | `/setup/status` + `/setup/superadmin` undocumented | `3d06d11` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-01.5 | R | auth pages | Cards render squished — motion.div flex-item has no width | `fa179e9` | ⬜ Pending | ✅ Passed | ✅ Closed |
| F-02.1 | R | main.tsx | Toaster never mounted — all `toast.*` silent across the app | `bdca441` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-02.2 | m | handleChangePassword | Wrong-current-password on self-service change not audited | `ddc6d81` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-02.3 | **B** | handleDeleteUser | Self-delete + last-super-admin delete both succeed → instance soft-brick | `7c8daea` + `88caa0c` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-03.1 | R | handleMe | `GET /api/v1/me` dropped `avatar_seed`; UI reverted to login-string fallback on every reload | `4aff6df` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-03.2 | R | apikeys.go | User API-key create + revoke emitted no audit events | `3d68953` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-03.3 | R | apikeys.go + project_apikeys.go | Unlimited API-key name length accepted (300+ bytes) | `31fb799` + `be474f8` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-03.4 | R | apikeys.go + project_apikeys.go + migration 028 | Duplicate live key names accepted; race-safe partial unique index added | `31fb799` + `be474f8` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-03.5 | **B** | OptionalSessionOrAPIKey | Invalid Basic/Bearer API-key credentials silently 200-null instead of 401 | `517c23f` + `be474f8` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-03.6 | R | DeleteAccountSection + handleDeleteMe | Post-delete UI stuck on /profile (logout 401'd); orphan session + api_key rows never cleaned | `5f27d48` + `be474f8` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-04.1 | R | ProjectsPage Create dialog | Stale name + stale error banner on reopen after Esc-close | `1082324` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-04.2 | **B** | internal/audit + admin_audit + migrations 029+030 | `audit_log.occurred_at` stored as Go `%v` string → from/to filters return 0, keyset pagination returns page-1 rows; RFC3339Nano variable-width trap found on Codex pass | `610d01e` + `cd6618a` | ✅ Clean | ✅ Passed | ✅ Closed |
| F-04.3 | m | sessions + apikeys metadata | `sessions.{issued_at,last_seen_at,expires_at}` + `api_keys.last_used_at` share the F-04.2 storage bug; session expiry off by ≤1 s at sub-second boundary. Latent; tracked for Batch 14. | _follow-up_ | — | — | 🟨 Open |
| F-05.1 | **B** | auth/membership across 10 protocol/middleware/REST sites | User-owned API keys could not auth any project-scoped action: JWT token exchange succeeded but downstream Can check returned `DENIED not_a_project_member`. Refactor: `auth.ResolveMembership` dispatches on Actor shape once; 9 open-coded blocks now delegate. Codex pass caught a 10th site — `api/scans.go Deps.actorIsProjectMember` had `actor.Kind != user` early-return — fixed in pass 2 to match the protocol-handler pattern. | `d8d11d0` + `f0f6131` | ✅ Clean (pass 1); pass 2 needs follow-up review | ✅ Passed (`crane copy` with alice-dev key pushes/pulls; `TestResolveMembership_*` + `TestScansREST_Rescan_UserOwnedAPIKey_Accepted`) | ✅ Closed |
| F-05.2 | R | `internal/protocol/oci/blobs.go` blobGet | 404 BLOB_UNKNOWN `detail` echoed `os.PathError.Error()` verbatim, leaking absolute CAS filesystem path; generic internal-error fallback leaked it too. Fix: canonical `errors.New("blob unknown")` + slog-logged raw err with digest for diagnostics. | `b942943` | ✅ Clean | ✅ Passed (`TestBlobGet_UnknownDigest_DoesNotLeakFSPath`) | ✅ Closed |
| F-05.3 | R | listDockerContent scan aggregation | Tag pointing at multi-arch image index showed `Not scanned` forever — child manifests ARE scanned, but UI queried `scans.artifact_id = tag.digest` and the index digest has no row. All Docker Hub official images are multi-arch → universally broken UX. Fix: `aggregateIndexScan` parses the stored index body, pulls the latest scan per child in one grouped `MAX(id)` query, synthesises a status + severity-summary envelope `deriveScanSeverity` / `severityCounts` consume identically to a native row. Status precedence: scanning > failed > done > "". | `0ab9b54` | ✅ Clean | ✅ Passed (`TestRepoContent_DockerIndexTag_*` + crane-verified `:multi` shows Clean end-to-end) | ✅ Closed |
| F-05.4 | R | `DockerRepoPage.tsx:308` + new REST shim | Delete-tag icon button had no `onClick`. UI can't call OCI DELETE `/v2/.../manifests/<ref>` directly — that needs a Bearer from `/v2/token` the session cookie can't mint. Fix: session-authed shim `DELETE /api/v1/projects/{name}/repos/docker/{repo}/tags/{tag}` in `internal/protocol/oci/rest_tags.go` reusing the OCI handler's repo/auth/tx helpers; mirrors `manifestDelete`'s tag-form branch exactly (tag unlink → ref_count check → cascade to manifest-delete only when orphan). UI wired with confirm dialog + `useDeleteDockerTag`. | `c18e84c` | ✅ Clean | ✅ Passed (`TestDeleteTag_*` 4 cases + UI "Tag 'vuln' deleted" end-to-end) | ✅ Closed |
| ~~F-05.5~~ | ~~R~~ | ~~CloneImageDialog~~ | **Withdrawn.** Initial finding claimed the Pull-External dialog hangs on upstream 401/404. Codex review showed `useJobProgress` does poll and `CloneImageDialog.tsx:285` does render `ErrorEnvelopeRenderer` once `progress.status === 'failed'`. The "hang" was me sampling before retry backoff exhausted — not a UI bug. Moved to observations in batch-05-docker-oci.md. | — | — | — | 🟥 Rejected |
| F-05.6 | R | `DockerRepoPage.tsx:473` | Promote/Retag button toasted "API not yet connected." despite `POST /api/v1/projects/{name}/repos/docker/{repo}/promote` being fully implemented. Fix: new `usePromoteDockerTag` mutation hook + real form state, `indexOf('/')` parse of `project/repo`, `ErrorEnvelopeRenderer` for server errors, src + dst cache invalidation. Siblings (`RpmRepoPage` Sync, `AptRepoPage` Sync, `RepoPageLayout` Wipe) deferred to batches 06+ per domain. | `c18e84c` | ✅ Clean | ✅ Passed (UI "Promoted multi → acme/demo:promoted" + new row visible) | ✅ Closed |
| F-06.1 | R | `internal/protocol/rpm/put.go` + `sync_handler.go` | NEVRA-filename check promised by step-5 doc comment never implemented. Uploads (and mirror syncs) with mismatched filename published `primary.xml <location href>` from canonicalFilename() but stored under the URL name → dnf 404s. Fix: `put.go` rejects 400 `filename_mismatch`; `sync_handler.go` stages to tmp, parses, then Puts at `canonicalFilename()` so mirrored rows also honour the invariant. Codex-clean. Upgrade-path tracked-open for pre-v1.4 rows whose digest fast-path skips re-download. | `563a7ee` + `b572080` | ✅ Clean | ✅ Passed (`TestRPMPut_RejectsFilenameNEVRAMismatch` + curl 400 + canonical 201; sync retest confirms href matches served file) | ✅ Closed |
| F-06.2 | m | `web/src/lib/snippets.ts` | Every snippet hard-coded `https://` regardless of served scheme — copy-paste-and-run failed on HTTP-served UIs. Fix: optional `scheme` param (default `'https'`); SnippetList reads `window.location.protocol` and passes matching value. | `9f860f3` | ✅ Clean | ✅ Passed (HTTP UI renders `baseurl=http://…`) | ✅ Closed |
| F-06.3 | R | `RpmRepoPage` / `AptRepoPage` row actions | No Delete row button; backend `DELETE /<project>/<t>/<r>/packages/{filename}` works via curl (204) but UI never wired it — same pattern as F-05.4. Closing needs a session-authed REST shim (protocol handler auth is BasicOrAPIKey-only). **Deferred — tracked-open** in batch-06.md. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-06.4 | m | `RpmRepoPage.tsx:289` + `AptRepoPage.tsx:343` | Mirror empty state said "Upload your first artifact using the snippet below" — but mirror uploads 403 and the snippet is pull-only. Fix: is_mirror-aware copy — title "Mirror not yet synced" + sync-hint description. | `dd982e2` | ✅ Clean | ✅ Passed (`/empty-mirror-check` renders sync-hint copy) | ✅ Closed |
| F-06.5 | R | `internal/api/sync_actions.go` SyncActorBridge | F-05.1's 11th site: user-owned API keys dropped the owning-user id on the bridge → `POST .../sync` with `-u alice:<user-key>` 403'd "not a project member" for every CI / cron / script caller. Fix: when `ProjectScope==nil && OwnerKind==user`, set `UserID = a.ID` (matches `auth.ResolveMembership` shape). | `2b12a70` | ✅ Clean | ✅ Passed (`TestSyncActorBridge_*` + curl 202) | ✅ Closed |
| F-06.6 | R | `web/src/hooks/useJobProgress.ts` + `SyncNowButton.tsx` | Retry-backoff silently swallowed (`status=pending, attempts>=1, last_error!=""` showed "Preparing…" for 96 min); repo-delete mid-sync left polling running every 500ms on 404. Fix: `computeJobProgress` wraps retry-backoff as `transient/job.retrying`; `pollingDecision` halts on 4xx; hook's TanStack `retry` short-circuits 4xx. Codex follow-ups: SyncNowButton render gate dropped `status==='failed'` guard + handleClick clears stale jobId to prevent envelope stacking. | `f487e2e` + `b572080` + `50adb1a` | ✅ Clean | ✅ Passed (5 unit tests + Playwright retest: retry envelope + "Try again" affordance visible during DNS-failure backoff) | ✅ Closed |
| F-06.7 | m | `web/src/lib/snippets.ts` (rpm) | `dnf config` snippet used `gpgcheck=1` → dnf rejected every non-OmniRepo-signed package with "wrong key(s)?". OmniRepo signs repomd, not packages (pass-through mirror). Fix: emit `repo_gpgcheck=1 gpgcheck=0` — verify the signed index, trust the packages. | `9f860f3` | ✅ Clean | ✅ Passed (dnf reaches transaction-test end-to-end) | ✅ Closed |
| F-06.8 | m | Mirror repo pages | Sync-job history backend endpoint exists (`GET .../sync-jobs`) but no UI surface lists past jobs with status/bytes/duration. **Deferred — tracked-open** in batch-06.md. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-07.1 | R | `internal/metadata/pypi_files.go` + `protocol/pypi/upload_legacy.go` + `upload_pep694.go` + `handler.go` | Re-uploading an existing `<filename>` via twine returned `200 {"status":"ok"}` and silently **overwrote** the stored row/blob/digest — PyPI semantics forbid re-upload of released versions (supply-chain integrity). Fix: (a) new `FindByFilenameTx` read-through-tx; (b) pre-check dup at the handler BEFORE PathStore.Put; (c) per-(repo, filename) `sync.Mutex` on the Handler (stored in a `sync.Map`) held across pre-check→Put→commit/rollback so two concurrent first-uploads can't both Put at the same path. Sync path still uses the UPSERT-y `PyPIFilesRepo.Insert` directly (idempotent re-sync). Codex #1 + #2 both caught real issues (blob-unlink on rollback; concurrent first-upload last-rename-wins); final state ships the per-key lock + skip-blob-delete-on-inner-race defense-in-depth. | `0f0a68a` + `1d225bd` + `c9febdb` | ✅ Clean (2 passes) | ✅ Passed (3 unit tests incl. `TestUpload_ConcurrentFirstUploads_ExactlyOneWins` under `-race`; twine retest on /tmp/omnirepo-wt3: first 200, dup 409, blob sha256 unchanged) | ✅ Closed |
| F-07.2 | R | `web/src/pages/.../PyPIRepoPage.tsx` expanded rows | Backend `DELETE /<project>/pypi/<repo>/packages/{filename}` works (204 + trash-move + regen), but the PyPI content panel shows only "Scan report" link per file — no Delete row action. Same pattern as F-05.4 / F-06.3 (deferred). **Deferred — tracked-open** in batch-07.md. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-07.3 | R | `web/src/pages/repo/PypiRepoPage.tsx:369` | Wheel/sdist filename links pointed at `/<project>/pypi/<repo>/simple/<norm>/<filename>` — backend doesn't serve that deeper path; SPA catch-all rendered "Page Not Found". Fix: swap href to the canonical `/<project>/pypi/<repo>/packages/<filename>` (backend already serves it; session cookie passes auth same-origin) and add `download={filename}` so the browser writes the artefact to disk. | `22bc935` | ✅ Clean | ✅ Passed (Playwright: both hello-1.0.tar.gz + hello-1.0-py3-none-any.whl now link to `/acme/pypi/local/packages/<filename>` with `download` attribute) | ✅ Closed |
| F-07.4 | R | `internal/protocol/pypi/sync_handler.go:184-195` + `upstream_parse.go:315-339` | Mirror sync's inline version parser stripped only `.gz/.tar/.zip`, so legacy `.egg` filenames still served by pypi.org (e.g. `requests-2.23.0-py2.7.egg`) landed in `pypi_files` with `version="py2.7.egg"`. Fix: new `isInstallableExt` whitelists the four extensions pip actually installs from a simple/ index (`.whl/.tar.gz/.tgz/.zip`, case-insensitive); sync's collect loop calls it right after `FilterFile` so rejected upstream entries never reach the downloader. Codex flagged a latent version-parse corner case for `foo-1.0.0-rc1.tar.gz` (dashed pre-release) — **pre-existing** bug in the same inline parser, out of scope for batch-07; tracked for batch-15 cross-cutting. | `77a4269` | ✅ Clean | ✅ Passed (`TestIsInstallableExt` + live mirror re-sync on /tmp/omnirepo-wt3: `files_synced=0` after deleting the stale egg row, no re-pull) | ✅ Closed |
| F-07.5 | R | `internal/protocol/pypi/sync_handler.go` inline parser | **Deferred — batch-15 cross-cutting.** `foo-1.0.0-rc1.tar.gz` (PEP 440 dashed pre-release) stores `version="rc1"` because `LastIndex("-")` wins over the release-segment boundary. Pre-existing bug; Codex #1 surfaced it while reviewing F-07.4's whitelist. Fix direction: replace the inline parser with a call through `parseSdistFilename` / `parseWheelFilename` from `parse.go` and drop files that fail the canonical parse. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-07.6 | m | PyPI mirror sync ingest filename validation | **Deferred — batch-15 cross-cutting.** Upstream-fed filenames reach PathStore without a strict allowlist (`^[A-Za-z0-9._-]+$`). React + `encodeURIComponent` block XSS; `download` attribute browsers sanitize path separators. But a hostile upstream could engineer filenames with disposition-header-influencing bytes. Codex #1 surfaced while reviewing F-07.3. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-08.1 | R | `HelmRepoPage` expanded rows | Backend `DELETE /<project>/helm/<repo>/charts/{filename}` works (204 + index regen), but expanded chart-version rows show only Scan-report + Rescan — no Delete. Same class as F-05.4 / F-06.3 / F-07.2. **Deferred — tracked-open** in batch-08.md. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-08.2 | **B** | `internal/scan/parse.go` | Trivy `--scanners vuln,secret,misconfig` runs for every Helm scan, but `trivyReportBlock` only decoded `Vulnerabilities` — every misconfiguration finding was silently dropped. Helm scans summarized all zeros, so `repos.block_on_severity` never had a non-zero severity to compare against — gate effectively a no-op for Helm. Fix: parse `Misconfigurations`; count active findings (Status="FAIL" OR empty, since some Trivy v0.69 outputs omit the field on active rules) into Summary by severity; fold each into `Vulnerabilities` with `CVEID=ID → AVDID → synthetic MISCONF-<target>-<i>` + `Package=block.Target`. | `14773c7` + `bd3d8dc` | ✅ Clean (Codex pass: 2 real-minors applied — empty-Status counted + synthetic ID fallback + deferred cross-cutting noted as F-08.6) | ✅ Passed (3 parse tests; vulny-0.1.0.tgz rescans as `{high:16,medium:10,low:24}`; pull@gate=high → 403 blocked_by_scan; clean chart → 200) | ✅ Closed |
| F-08.3 | m | `HelmRepoPage.tsx` empty-state | Mirror empty state still offers upload snippets ("Upload your first artifact…") even though mirror uploads 403. Same copy class as F-06.4 (fixed for RPM/APT). **Deferred — tracked-open** in batch-08.md. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-08.4 | m | `HelmRepoPage.tsx` header subtitle | Header renders "N charts" when there are N versions of 1 chart — should be "1 chart · N versions" (or just "N versions"). One-line grouping fix. **Deferred — tracked-open** in batch-08.md. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-08.5 | m | `HelmRepoPage.tsx:369` expanded-row links | Download links omit the canonical `/charts/` segment (`/acme/helm/local/<file>`) while `index.yaml` uses `charts/<file>`. Both work today — backend accepts either — but UI drift from canonical shape. **Deferred — tracked-open** in batch-08.md. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-08.6 | m | `internal/scan/materialize_pkg.go:150-183` | Package materialization copies the raw archive AND extracts it under `extracted/`, so Trivy's fs scan surfaces every Helm misconfig twice (once targeted at `<file>.tgz:templates/<y>` and once at `extracted/<pkg>/templates/<y>`). Summary counts and `vulnerabilities` rows double; FTS dedup mask hides it downstream but the raw counts are 2×. Gate still fires correctly (any > 0 at threshold). Codex caught this reviewing F-08.2 — pre-existing, cross-cutting. **Deferred — batch-15 cross-cutting.** | _deferred_ | — | — | 🟨 Open (deferred) |
| F-09.1 | **B** | `web/src/components/CreateRepoDialog.tsx:160` | Client-side validator rejected `oci://` URLs with "URL must use http(s)" even though backend `validateMirrorUpstreamURL` accepts them for helm mirrors (OCIHELM-03). Users could not create a helm OCI mirror via the UI at all. Fix: widen the regex guard to accept `oci://` when `repoType === 'helm'`. | `0a2ea2c` | ✅ Clean | ✅ Passed (Playwright: bitnami-oci creates via UI with oci:// URL + helm-kind cred) | ✅ Closed |
| F-09.2 | **B** | `internal/httpx/sync_rest.go:247` + `internal/protocol/helm/sync_handler.go:201` | Two layered gaps: (a) sync-trigger REST rejected oci:// with the same http(s)-only guard as the frontend; (b) SyncHandler.Handle unconditionally called ParseUpstream (HTTP GET `<url>/index.yaml`) even for oci://. Plan 11-03 marked pure-oci:// "out of scope" despite OCIHELM-03 widening the validator — so the feature was half-shipped. Fix: (a) widen sync_rest scheme check for `repoType=='helm' && repo.IsMirror` + require non-empty path for oci://; (b) add `ParseOCIUpstream` that uses `ociclient.ListTags` to enumerate semver tags and yield one UpstreamEntry per version; (c) SyncHandler.Handle dispatches on URL scheme. Codex follow-up: case-insensitive `normalizeRef` in ociclient (Cam/ORAS couldn't parse `OCI://…`). | `0a2ea2c` + `d4bb000` | ✅ Clean (3 real-issues flagged — 2 fixed, 1 deferred as F-09.3) | ✅ Passed (POST `/sync` returns 202; pipeline reaches `auth.docker.io/token` 401 for test creds — live pull blocked only by absent DH PAT, covered by `make test-live-oci`) | ✅ Closed |
| F-09.3 | m | `internal/httpx/upstream_err.go:21` + `internal/jobs/pool.go:54` | `SanitizeUpstreamErr` only redacts `Authorization:` headers — OCI token endpoints and scope query params leak through to `sync_jobs.last_error` and the UI envelope (e.g. `https://auth.docker.io/token?scope=repository%3Abitnamicharts%2Fnginx%3Apull&service=registry.docker.io`). No credential leak, just routing + scope details. Deferred: sanitizer change needs broader scope across protocols (rpm/deb/pypi/helm all share this path). **Deferred — batch-15 cross-cutting.** | _deferred_ | ✅ Codex-flagged | — | 🟨 Open (deferred) |
| F-09.4 | n | `web/src/components/MirrorConfigSection.tsx:76` | UX wart: existing `docker`-kind cred from Batch 04 not selectable for helm OCI mirror — users add a second cred with kind=`helm`/`basic`. Design-intentional per `Phase 11 / D-06` (Helm SDK's ClientOptBasicAuth path). Documented in batch-09.md. | _no fix_ | — | — | ✅ Accepted |
| F-09.5 | **B** | `internal/protocol/helm/upstream_parse.go` + `sync_handler.go:fetchAndCommitOCI` | Live sync against `oci://registry-1.docker.io/bitnamicharts/nginx` aborted mid-batch with "manifest does not contain minimum number of descriptors (2), descriptors found: 1". Bitnami publishes `<version>-metadata` OCI artifacts alongside every chart tag (scan/SBOM sidecars, single-layer manifests); semver accepts the pre-release suffix, so the naive filter yielded them — Helm SDK PullChart rejects + error propagates via downloadErrors aborting 239 good tags. Fix at two layers: `isNonChartOCISidecarTag("*-metadata")` prefilter in ParseOCIUpstream (cheap, intentional) + `isNonChartManifestErr` in fetchAndCommitOCI (defence in depth, catches future single-layer conventions). | `3bf5d04` | ✅ Clean — Codex suggested keeping narrow + relying on error-match for unknown sidecars | ✅ Passed (166 charts synced before DH rate limit; upstream issue, not code) | ✅ Closed |
| F-09.6 | m | `internal/config/config.go:274` + `internal/protocol/helm/sync_handler.go:91` | `UpstreamHTTPTimeout` documented as per-request but applied to the whole sync job via `context.WithTimeout(ctx, timeout)`; 60s default too tight for 200-tag OCI batches. Retry loop + F-09.8 failure-path Kick converge on the right state eventually. **Deferred — batch-15.** Split into per-request + per-job or bump default. | _deferred_ | — | — | 🟨 Open (deferred) |
| F-09.7+F-09.8 | **B** | `internal/protocol/helm/sync_handler.go:287` + rpm/deb/pypi equivalents | Per-file commits flip `metadata_state=dirty` but regen coalescer Kick lived only on success path. Under Docker Hub 100/6h auth-rate-limit, partial success (166/203) left index.yaml empty and UI showing "0 charts" for 6h+ until next successful sync. `helm repo add` returned empty catalogue. Fix: Kick on failure path too — no `filesAdded > 0` gate; regen is idempotent + coalesced. Cross-cutting fix applied to rpm/deb/pypi sync handlers (Codex Q6). | `3bf5d04` + `0cfcc67` | ✅ Clean (Codex Q3a regen/sync lock race pre-existing, deferred → batch-15) | ✅ Passed (index.yaml grew 27 B → 228 KB after fix; helm repo add + search + pull end-to-end with digest parity) | ✅ Closed |
| F-09.9 | m | `internal/protocol/helm/sync_handler.go:fetchAndCommitOCI` | Dedup gate runs AFTER `OCIClient.Resolve(ref)`, so 166 already-synced charts each cost one Docker Hub API call before dedup-skip. Burns the 200/6h auth rate limit on syncs that should be no-ops. Fix direction: pre-Resolve lookup on `(repo_id, name, version)` + freshness-window skip. **Deferred — batch-15 sync efficiency.** | _deferred_ | — | — | 🟨 Open (deferred) |
| F-09.10 | m | `internal/httpx/mirror_guard.go:87` | 403 envelope message "uploads to mirror repos are disabled" but MirrorGuard also rejects DELETE (design-intentional: mirrors are read-only; use `filter.Names`/`filter.Globs` to exclude). Fix: copy → "writes to mirror repos are disabled (uploads + deletes)"; Playwright E2E stub updated (Codex Q4). | `3bf5d04` + `0cfcc67` | ✅ Clean | ✅ Passed | ✅ Closed |

---

## Detail

### F-01.1 All error responses logged at slog ERROR level, regardless of HTTP status
- **Severity:** R / real-bug
- **Area:** `internal/httperr/write.go` (Write) — affects every handler funneling through `writeJSONError`
- **Symptom:** Every 4xx (validation / 401 / 403 / 404) emitted `level=ERROR msg=api.error ...`, polluting alert streams and tripping the testing-protocol §4 backend-log gate on every normal rejection.
- **Repro:**
  1. Start server clean.
  2. `curl -X POST http://localhost:18080/api/v1/setup/superadmin -d '{"login":"x","email":"x@y","password":"123"}'` → 422.
  3. `grep ERROR $OMNIREPO_DATA_ROOT/server.log` → one hit for a perfectly normal validation response.
- **Root cause:** `internal/httperr/write.go:51` unconditional `slog.ErrorContext(...)` ignoring class/status.
- **Fix:** commit `0f2dfd1` — level routes by status: 5xx or `operator_action_required` → ERROR, 4xx → WARN. Added `status` field in structured log. `TestWrite_LogLevelByStatus` pins the rule.
- **Codex verify:** ✅ Clean (batched)
- **Retest:** ✅ Re-triggered 422; log now `WARN ... status=422 class=validation`; grep-ERROR returns 0 hits.
- **Status:** ✅ Closed

### F-01.2 Browser-native "Failed to load resource" console entries on 4xx fetch (noise)
- **Severity:** n / noise
- **Area:** LoginPage + any UI that calls `fetch()` against an endpoint that may legitimately return 4xx
- **Symptom:** `ERROR  Failed to load resource: the server responded with a status of 401/404` surfaces in the browser console on wrong-password login / repo-not-found page / etc.
- **Root cause:** Chromium emits this for every non-2xx fetch; it's a platform log, not a JS error. The app handles both 401 and 404 correctly.
- **Fix:** None proposed. Any workaround (200-with-failure-body, XHR-with-custom-plumbing, `console.error` shim) would mask real bugs. Console-cleanliness gate in the testing protocol should be interpreted as "no JS-app errors" — browser-native network logs are exempt.
- **Status:** ✅ Closed as accepted noise

### F-01.3 Deep-link lost across login redirect
- **Severity:** R / real-bug
- **Area:** `web/src/App.tsx` (AuthGuard), `web/src/pages/LoginPage.tsx`
- **Symptom:** Visiting e.g. `/projects/acme/docker/demo` while logged out → /login → after sign-in lands on `/`, not the original path.
- **Root cause:** AuthGuard's `<Navigate to="/login" replace />` didn't pass `state={{ from: location }}`; LoginPage hardcoded `navigate('/')` on success.
- **Fix:** commit `12ac7e1` — AuthGuard attaches `state.from`; LoginPage navigates to `state.from.pathname` (+ search + hash) on success. Path must start with `/` and not `//` (open-redirect guard); otherwise falls back to `/`.
- **Codex verify:** ✅ Clean (batched)
- **Retest:** ✅ Retried repro — lands on `/projects/acme/docker/demo` (NotFound page, correct for now).
- **Status:** ✅ Closed

### F-02.3 Self-delete + last-super-admin delete both succeed (BLOCKER)
- **Severity:** B / blocker
- **Area:** `internal/api/admin_phase1.go:565` `handleDeleteUser`
- **Symptom:** Super-admin deleted itself via the admin API; zero live super-admins remained. Instance loses all admin-surface access; recovery needs direct SQL on the data volume.
- **Fix:** commit `7c8daea` — two safety checks in `handleDeleteUser`: actor ≠ target (else 409, point user to `/me`); if target is super-admin, require `CountLiveSuperAdmins > 1` (else 409 "promote another user first"). New `Users.CountLiveSuperAdmins` helper. Regression tests pin both rules.
- **Retest:** ✅ Post-fix `DELETE /admin/users/superadmin` as superadmin → 409 with correct envelope; row stays live.
- **Status:** ✅ Closed

### F-02.2 Wrong-current-password on self-service change not audited
- **Severity:** m / minor (observability gap)
- **Area:** `internal/api/admin_phase1.go:419` `handleChangePassword`
- **Symptom:** Failing `/auth/change-password` (wrong current password) returned 401 but emitted no audit row. Same threat surface as login brute-force, but untracked.
- **Fix:** commit `ddc6d81` — failure branch now writes `auth.password.changed / outcome=wrong_password`; success branch now sets `outcome=ok` explicitly.
- **Retest:** ✅ `auth_log` carries both outcomes after retrying the repro.
- **Status:** ✅ Closed

### F-02.1 Toast host never mounted — every `toast.*` call silent
- **Severity:** R / real-bug (affects the whole app)
- **Area:** `web/src/main.tsx` (app root)
- **Symptom:** `<Toaster>` component defined in `components/ui/sonner.tsx` but never mounted anywhere. All `toast.success` / `toast.error` calls across UsersPage, TLSPage, DockerRepoPage, MaintenancePage, TrashPage, GCPage, PypiRepoPage were silent no-ops. Observed in Batch 02 case 2.4 (dup-create → form clears, zero feedback).
- **Fix:** commit `bdca441` — `main.tsx` now mounts `<Toaster richColors position="top-right" />` as a sibling of RouterProvider. DOM probe after fix confirms a toast element with text `login exists` appears on dup-create within the 50ms poll.
- **Retest:** ✅ Confirmed.
- **Status:** ✅ Closed

### F-01.5 Auth cards render squished — motion.div flex-item has no width
- **Severity:** R / real-bug
- **Area:** `web/src/pages/LoginPage.tsx`, `web/src/pages/SetupPage.tsx` (both branches), `web/src/pages/ChangePasswordPage.tsx`
- **Symptom:** Every unauthenticated card (login, setup form, "Setup complete", change-password) renders at ~137-272px instead of the intended `max-w-md` (448px). "Setup complete" title wraps to two lines; description wraps to four.
- **Root cause:** `motion.div` wraps the `Card` inside a `flex items-center justify-center` parent. With no width class on the motion.div and no intrinsic-min from the content, the flex item shrinks below `max-w-md` and the Card's `w-full` then resolves against the collapsed parent.
- **Fix:** Add `className="w-full max-w-md"` to every `motion.div` wrapper. Pinned four call sites. Card width measured at 448px after fix.
- **Retest:** ✅ Screenshots show the card at proper width on /setup (setup-complete) and /login.
- **Status:** ✅ Closed

### F-03.1 `GET /api/v1/me` drops `avatar_seed` from the response
- **Severity:** R / real-bug
- **Area:** `internal/api/admin_phase1.go:501` `handleMe`
- **Symptom:** DB row stores the user's regenerated avatar seed, but `/me` JSON omits the field. Profile page's `me.avatar_seed || me.login` fallback silently reverts to the login-string avatar on every page load — user's customisation invisible until the next `PATCH /me` (which re-sends the seed and re-renders locally).
- **Root cause:** `handleMe` manually built `MeResponse{}` without populating `AvatarSeed`, even though the PATCH handler did. Two construction sites drifted.
- **Fix:** commit `4aff6df` — mirror the PATCH-path behaviour (emit when non-empty). Regression guard: `TestProfile_GetMeIncludesAvatarSeed`.
- **Codex verify:** ✅ Clean (confirmed no other `MeResponse` construction sites drop the field).
- **Retest:** ✅ Hash-compare of rendered avatar SVG proves the seed from `/me` is used across reload.
- **Status:** ✅ Closed

### F-03.2 User-scope API key create + revoke emit no audit events
- **Severity:** R / real-bug (observability gap)
- **Area:** `internal/api/apikeys.go` handleCreateAPIKey + handleRevokeAPIKey; `internal/audit/events.go`
- **Symptom:** Creating or revoking a user-owned API key through `POST/DELETE /api/v1/me/api-keys` writes the row but emits zero audit entries. Operator grepping `audit_log` for per-user key timelines gets a hole where project keys (which DO emit `project.api-key.{create,revoke}`) are fully traced.
- **Root cause:** The project-scoped handler in `project_apikeys.go` called `d.recordAudit(...)`; the user-scoped twin was authored without it.
- **Fix:** commit `3d68953` — add `EvtUserAPIKey{Created,Revoked}` kinds alongside the project variants; emit with `target_kind="user_api_key"` and the numeric id as `target_id`. Tests: `TestUserAPIKey_CreateEmitsAudit`, `TestUserAPIKey_RevokeEmitsAudit` — both also assert `details_json` never contains the plaintext secret.
- **Codex verify:** ✅ Clean (naming asymmetry vs. project keys flagged as operator-side noise, not a code issue).
- **Retest:** ✅ `audit_log` now records create + revoke entries per key id.
- **Status:** ✅ Closed

### F-03.3 API key names accepted at arbitrary length
- **Severity:** R / real-bug
- **Area:** `internal/api/apikeys.go` + `internal/api/project_apikeys.go`
- **Symptom:** `POST /api/v1/me/api-keys` with a 300-byte name → 201 accepted. Long names break the profile + admin tables, bloat `audit_log.details_json`, and invite low-effort abuse vectors.
- **Fix:** commit `31fb799` — introduce `maxAPIKeyNameLen = 128` on both user and project handlers; return 422 `"name too long"` past the cap. Audit + response now emit the trimmed name. Regression: `TestUserAPIKey_RejectsOverlongName`.
- **Codex verify:** ✅ Clean.
- **Retest:** ✅ Boundary test: 128-char name → 201; 129-char → 422.
- **Status:** ✅ Closed

### F-03.4 Duplicate live API-key names accepted; app-layer check racy
- **Severity:** R / real-bug
- **Area:** `internal/api/apikeys.go` + `internal/api/project_apikeys.go`; `internal/metadata/migrations/028_api_keys_live_name_unique.up.sql`
- **Symptom:** Creating a second API key with the same name as an existing live key succeeded, producing two indistinguishable rows in the profile table.
- **Root cause:** No app-layer check AND no DB-level uniqueness. Even after adding the app-layer list-then-insert, two concurrent POSTs could both pass.
- **Fix:**
  - commit `31fb799` — app-layer guard rejects duplicates (409 `"name already in use"`). Revoked names remain reusable so rotation isn't painful.
  - commit `be474f8` (Codex pass) — migration 028 adds partial unique indexes on `api_keys(owner_user_id, name)` and `(owner_project_id, name)` WHERE `revoked_at IS NULL`. Both handlers translate the resulting SQLITE_CONSTRAINT error back to the same 409 envelope.
- **Codex verify:** ✅ Clean.
- **Retest:** ✅ Serial duplicate → 409; unique index present on live server; concurrent-duplicate race now eliminated at DB level.
- **Status:** ✅ Closed

### F-03.5 `OptionalSessionOrAPIKey` 200-nulls on invalid API-key creds (BLOCKER)
- **Severity:** B / blocker
- **Area:** `internal/auth/middleware/session_or_apikey.go` `OptionalSessionOrAPIKey`
- **Symptom:** `curl -k -u alice:omr_u_wrongkey https://.../api/v1/me` → **200** with empty body. Identical to "no auth supplied". Protocol CLIs and credential-probe detectors can't tell rejected keys from success; the strict `SessionOrAPIKey` twin correctly 401s on the same inputs.
- **Root cause:** The optional-middleware branch dropped failed Basic / Bearer auth and flowed through as anonymous. Only a completely valid-but-stale session cookie should demote silently.
- **Fix:**
  - commit `517c23f` — Basic/Bearer credentials that fail → 401 via the canonical envelope; session cookies keep silent-200 behaviour (matches `TestLogout` contract). Guard against a present-but-unshaped Basic password.
  - commit `be474f8` (Codex pass) — also 401 when `Authorization:` carries an empty/whitespace Bearer token or any unknown scheme (previously fell through as anonymous).
- **Tests:** `TestOptionalMiddleware_BasicAuth_WrongKey_401`, `TestOptionalMiddleware_BasicAuth_MalformedKey_401`, `TestOptionalMiddleware_Bearer_WrongKey_401`, `TestOptionalMiddleware_Bearer_EmptyToken_401`, `TestOptionalMiddleware_UnknownScheme_401`, `TestOptionalMiddleware_NoCreds_200`.
- **Codex verify:** ✅ Clean (two tightenings applied from review).
- **Retest:** ✅ Live server returns `401` envelope on every invalid-creds probe; `200 null` only for no-header case.
- **Status:** ✅ Closed

### F-03.6 Delete-account leaves UI stuck + orphans DB rows
- **Severity:** R / real-bug
- **Area:** `web/src/pages/ProfilePage.tsx` DeleteAccountSection; `internal/api/admin_phase1.go` handleDeleteMe
- **Symptom:** After successful `DELETE /api/v1/me` (200), the frontend called `POST /auth/logout` which 401'd because the session cookie had already been cleared. The logout hook's redirect lived on `onSuccess` so it never fired — user saw the confirmation dialog still open on `/profile`. Separately, `Users.Delete` is a soft-delete, so FK cascades on `sessions` and `api_keys` never fired — orphan rows accumulated.
- **Fix:**
  - commit `5f27d48` — DeleteAccountSection drops the logout call and redirects directly (`qc.clear()` + `window.location.href = '/login'`) after the DELETE succeeds.
  - commit `be474f8` (Codex pass) — handleDeleteMe explicitly calls `Sessions.DeleteAllForUser` + new `APIKeys.RevokeAllByUser` best-effort before clearing the cookie. Middleware already rejects soft-deleted users, but the cleanup keeps the partial unique index slots freed and ensures a future regression can't resurrect the departed user's keys / sessions.
- **Tests:** `TestDeleteMe_DropsSessionsAndRevokesAPIKeys` asserts `sessions=0` + `live-keys=0` post-delete.
- **Codex verify:** ✅ Clean (cleanup was Codex's recommendation).
- **Retest:** ✅ `testdel` throwaway account end-to-end in Playwright: confirm dialog → redirect to `/login` → no 401 console error → DB shows orphan rows gone.
- **Status:** ✅ Closed

### F-01.4 Setup endpoints undocumented in OpenAPI spec
- **Severity:** m / minor
- **Area:** `internal/api/openapi.yaml`
- **Symptom:** `/setup/status` + `/setup/superadmin` missing from the spec served at `/api/v1/openapi.yaml`. Swagger UI and generated clients never surface the bootstrap flow — one of the first endpoints a new operator needs.
- **Root cause:** Spec drift — handlers shipped without corresponding spec entries.
- **Fix:** commit `3d06d11` — added schemas + paths; types regenerated; hand-written Go types now alias the generated names; email field kept plain `string` so handler's non-empty check stays authoritative.
- **Retest:** ✅ Both paths present; full Go tests green.
- **Status:** ✅ Closed

### F-04.1 Create-project dialog retains stale name + error banner on Esc-reopen
- **Severity:** R / real-bug
- **Area:** `web/src/pages/ProjectsPage.tsx`
- **Symptom:** Typed an invalid name (e.g. `ACME`), submitted → 422 surfaced the error banner. Pressed Esc, then reopened the dialog — previous name AND stale error banner were still shown. Submitting after success correctly cleared the form, but dismiss-paths (Escape, overlay click) didn't.
- **Root cause:** `setName('')` / `setDescription('')` lived only in the `handleCreate` success branch. `onOpenChange={setDialogOpen}` short-circuited to just the boolean setter, so React local state survived across close/reopen.
- **Fix:** Wrap `onOpenChange` — when `open=false`, also reset `name`, `description`, and `errorEnvelope`.
- **Retest:** ✅ Submitted `BAD_NAME` → banner visible. Esc → reopen → fields empty, no banner.
- **Codex verify:** ⬜ Pending
- **Status:** ✅ Closed

### F-04.2 Audit timestamps stored in unparseable Go-`%v` format — filters + pagination broken
- **Severity:** **B** / blocker (audit review endpoint effectively unusable past page 1 or for any time-range query)
- **Area:** `internal/audit/audit.go` (write); `internal/api/admin_audit.go` (filter + cursor); `internal/metadata/migrations/029_audit_occurred_at_rfc3339.*` (data migration)
- **Symptom:**
  1. `GET /api/v1/admin/audit?from=2026-04-22T10:00:00Z` returned 0 items even though every row was from 2026-04-22 after 10:00.
  2. Keyset pagination returned the same first-page rows on page 2 (the `occurred_at < cursor OR (... = ... AND id < ...)` predicate never excluded anything).
- **Root cause:** `audit.Record` bound `e.OccurredAt` (a `time.Time`) directly to `ExecContext`. `modernc.org/sqlite`'s default conversion uses Go's `time.Time.String()` — `"2026-04-22 12:43:05.123456789 +0000 UTC"`. That format (a) is not parseable by SQLite's `datetime()` / `strftime()`, and (b) sorts lexicographically AFTER RFC3339 strings when the date is equal (space `0x20` < `T` `0x54` at index 10). The admin endpoint formatted its `from` / `to` / cursor as RFC3339, so every same-day comparison lost.
- **Fix (initial, commit `610d01e`):**
  - `internal/audit/audit.go` wrote `e.OccurredAt.UTC().Format(time.RFC3339Nano)` — ISO-8601, parseable by SQLite.
  - `internal/api/admin_audit.go` formatted `from` / `to` and the cursor `SortValue` as `time.RFC3339Nano`.
  - `migrations/029_audit_occurred_at_rfc3339.up.sql` rewrote legacy rows: `substr(s, 1, 10) || 'T' || substr(s, 12, length(s)-21) || 'Z'`.
- **Codex verdict:** Found a residual blocker — `time.RFC3339Nano` strips trailing zeros (Go's `.999999999` format verb), so the stored text has variable width. Lex comparison then breaks on a zero-ns row in second S vs a sub-second row in the same second: `"...:05Z"` vs `"...:05.1Z"` — `'Z' (0x5A) > '.' (0x2E)`, wrong order. `ORDER BY`, keyset cursors, and range filters all affected in the narrow window.
- **Fix (Codex-pass, commit `cd6618a`):**
  - Introduced `audit.DBTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"` — fixed-width 30-char ISO-8601.
  - `audit.go` now binds via `DBTimestampLayout`; `admin_audit.go` binds filter values, cursor `SortValue`, and response `timestamp` via the same layout (end-to-end format symmetry).
  - `migrations/030_audit_occurred_at_fixed_width.up.sql` pads legacy RFC3339Nano rows: short-fraction rows get `.X...Z` → `.XYYYYYYYYZ`; zero-fraction rows get `.000000000Z` inserted.
- **Retest (final):**
  - DB: 136/136 rows at length 30; sample row id=135 padded from `...50:54.79858302Z` (29 chars) to `...50:54.798583020Z` (30 chars).
  - Filter `from=2026-04-22T10:00:00Z` → 50 items; `to=2026-04-22T10:30:00Z` → 30 items; `from=2099-01-01T00:00:00Z` → 0 items.
  - Pagination: p1 `[142,141,140]`, p2 `[139,138,137]`, p3 `[136,135,133]` — strict monotonic across same-second events (139 `.489`, 138 `.423`).
  - Fresh write emitted at 30 chars (`2026-04-22T13:06:21.283318398Z`).
  - `go test ./internal/audit/... ./internal/api/... ./internal/metadata/...` all green.
- **Codex verify:** ✅ Clean (follow-up applied).
- **Status:** ✅ Closed

### F-04.3 (carried forward) Session + API-key timestamps have the same Go-`%v` storage format
- **Severity:** m / minor (surfaced by Codex during F-04.2 review)
- **Area:** `internal/metadata/sessions.go:38` (INSERT), `internal/metadata/sessions.go:58` (`expires_at > CURRENT_TIMESTAMP`), `internal/metadata/apikeys.go:239` (`last_used_at`)
- **Symptom:** Same storage bug as F-04.2 — modernc binds `time.Time` via `.String()`. Sessions expiry check works today only because Go-`%v` happens to lex-compare correctly against SQLite's `CURRENT_TIMESTAMP` for typical date values; sub-second boundary behaviour is wrong but not observable (session expires ≤1 second late).
- **Fix:** Not in batch 04 scope; the user-facing consequence is latent. Recorded here so Batch 14 (admin + session management) picks it up, or a dedicated audit pass converts all `time.Time`-bound columns to `audit.DBTimestampLayout` in one change.
- **Status:** 🟨 Open (tracked for follow-up)
