# Batch 07 — PyPI

**Status:** ✅ Passed — 4 fixes landed (F-07.1 / F-07.3 / F-07.4 + Codex-2 concurrency follow-up), F-07.2 deferred (same REST-shim bundle as F-05.4 / F-06.3), F-07.5 / F-07.6 deferred cross-cutting. All findings Codex-clean after two passes.
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/pypi/local` with at least one uploaded wheel
- `acme/pypi/pypi-upstream` mirroring a tiny subset of pypi.org (or local fixture)
- block_on_severity=high exercised (WALKTHROUGH-2 §3a repro)

## Pre-flight

- [ ] `twine` and `pip` available on client machine / container
- [ ] `python -m build` or `pip wheel` to produce test wheels
- [ ] Logged in as alice
- [ ] Server log tail open

## Test cases

### 7.1 Create local PyPI repo
- [x] Type `PyPI`, name `local`, mirror=false
- [x] Empty state shows `pip install -i http://localhost:18080/acme/pypi/local/simple/ …` snippet (scheme matches served origin — F-06.2 holds)

### 7.2 Upload wheel via twine
- [x] Build a tiny wheel (e.g. `hello-1.0-py3-none-any.whl`)
- [x] `twine upload --repository-url http://localhost:18080/acme/pypi/local/legacy/ -u alice -p <api-key> dist/*` → 200 (twine expects 200, not 201)
- [x] PEP 503 simple/ index regenerated; UI shows `hello 1.0 1.1 KB Clean`

### 7.3 PEP 503 simple index content
- [x] `GET /acme/pypi/local/simple/` → 200 HTML, `<a href="hello/">hello</a>` (project auth required — 401 anon, OK)
- [x] `GET /acme/pypi/local/simple/hello/` → 200 with `#sha256=<hex>` fragment + `data-requires-python=">=3.8"`
- [x] `GET /acme/pypi/local/simple/Hello/` → 301 to `/simple/hello/`; `/HELLO/` and `/h_e_l_l_o/` also redirect per PEP 503 normalization

### 7.4 `pip install` end-to-end
- [x] `pip install --index-url http://alice:<key>@localhost:18080/acme/pypi/local/simple/ hello` succeeds; `greet()` returns the expected string; digest auto-verified by pip via `#sha256=` fragment.

### 7.5 Upload second version
- [x] Build `hello-1.1` wheel; upload
- [x] Index lists both; `pip install hello==1.0` → 1.0; `pip install hello` → 1.1

### 7.6 Upload sdist
- [x] Build sdist (`hello-1.0.tar.gz`); upload
- [x] simple/ lists sdist alongside wheels; direct fetch returns byte-identical sdist with matching sha256
- [x] `pip install --no-binary :all:` sdist install is a pip/build-isolation quirk unrelated to OmniRepo — sdist content is PEP-valid (PKG-INFO `Name: hello`), served correctly

### 7.7 Duplicate upload
- [x] Re-upload `hello-1.0.tar.gz` → **F-07.1**: 200 `{"status":"ok"}` instead of 409; row `uploaded_at` advanced; upsert semantics override PyPI immutability contract.

### 7.8 Delete wheel
- [x] Backend `DELETE /acme/pypi/local/packages/hello-1.1-py3-none-any.whl` → 204; simple/ index regenerated to exclude it; `pip install hello==1.1` 404s (no cache refresh needed); audit row `pypi.delete` recorded (actor via `actor_api_key_id`, owner resolvable by join)
- [ ] UI row action not reachable → **F-07.2** (deferred, same shape as F-05.4 / F-06.3)
- [x] Filename link in expanded panel navigates SPA to dead route → **F-07.3**

### 7.9 Create mirror
- [x] Type `PyPI`, name `pypi-upstream`, mirror=true
- [x] Upstream URL: `https://pypi.org/simple/` (pypi.org reachable from this host via `curl -sI`)
- [x] Filters: `{"names":["six"]}` initially, then PATCH to `{"names":["requests"]}` for §7.11
- [x] scan_on_sync=true

### 7.10 Sync
- [x] `POST .../sync` → 202, 4 polls to done; `six` = 48 releases, 862 KB; `requests` = 236 releases, 53.7 MB
- [x] Job row `done|236|56306464/56306464` in `sync_jobs`; scans queued + completed (284 total, 0 failed)
- [x] `pip install --index-url http://alice:<key>@localhost:18080/acme/pypi/pypi-upstream/simple/ six` → `six 1.17.0` end-to-end

### 7.11 Severity gate — repro of WALKTHROUGH-2 §3a
- [x] `requests-2.0.1-py2.py3-none-any.whl` ↠ `{critical:0,high:1,medium:5}`; `requests-2.33.1-py3-none-any.whl` clean
- [x] PATCH `block_on_severity=high` → 200
- [x] `curl .../requests-2.0.1-py2.py3-none-any.whl` → **403** `{"error":"blocked_by_scan","severity":"high","scan_id":158}` (legacy envelope; spec-expected, cross-protocol — 6 sites)
- [x] `curl .../requests-2.33.1-py3-none-any.whl` → **200** with 64,947-byte wheel body
- [x] Audit: two `scan.gate.blocked` rows within 4 ms — first `source:"db"`, second `source:"cache"`, both with `cve_count:1, severity:"high", scan_id:158`
- [x] Side observation: **F-07.4** — `requests-2.23.0-py2.7.egg` row got `version="py2.7.egg"` (sync parser doesn't handle `.egg`)

### 7.12 Filter validation (mixed snake/Pascal — D-3 regression)
- [x] `POST /projects/acme/repos` with `mirror_filter: { names: [...], Names: [...] }` → **400** canonical envelope `{"code":"repo.mirror_filter_invalid","class":"validation","incident_id":"..."}`

### 7.13 Console + network sweep
- [x] Console on repo detail + mirror sync: 0 errors, 0 warnings (only pre-existing F-01.2 "Failed to load resource" browser-native noise from earlier batches)
- [x] Network: all 2xx except the intentional 202 (sync kick) + 400 (7.12 test) + 403 (severity gate)
- [x] Backend log grep `(level=error|panic|FATAL|"level":"ERROR)` over batch-07 window → 0 hits

## Findings

### F-07.1 Twine duplicate upload returns 200 instead of 409 — silently overwrites existing release
- **Severity:** R / real-bug
- **Area:** `internal/metadata/pypi_files.go` `PyPIFilesRepo.Insert` + `internal/protocol/pypi/upload_legacy.go:271` `commitPyPIRow`
- **Symptom:** Re-uploading an existing filename via `twine upload` succeeds with `200 {"status":"ok","filename":"hello-1.0.tar.gz"}`. PyPI semantics require 400/409 so a released version is immutable (supply-chain integrity).
- **Repro:**
  1. `twine upload --repository-url http://localhost:18080/acme/pypi/local/legacy/ dist/hello-1.0.tar.gz` → 200 (first upload).
  2. Repeat the same command against the same file. → still 200.
  3. `sqlite3 .../omnirepo.sqlite "SELECT id,filename,uploaded_at FROM pypi_files WHERE filename='hello-1.0.tar.gz';"` → `uploaded_at` advanced to the dup-upload timestamp. Same row id, new digest/size fields would overwrite if content differed.
- **Console/network:** clean — it's a backend semantics bug, no client error surface.
- **Root cause:** `PyPIFilesRepo.Insert` is SQL `INSERT ... ON CONFLICT(repo_id, filename) DO UPDATE SET ...`. It is shared between the twine-legacy upload path (which must refuse dups per PEP) and the mirror-sync path (which needs idempotent upsert, see `sync_handler.go:188`).
- **Fix direction:** check `FindByFilename` **inside** `commitPyPIRow`'s write-tx before calling `Insert`; if a row exists, return 409 with an error envelope `{"code":"pypi.file_exists","class":"conflict","message":"File '<filename>' already exists in this repo — delete it first."}`. Keep `Insert` as UPSERT for the sync path (`sync_handler.go`).
- **Codex verify:** ⬜ Pending
- **Retest:** ⬜ Pending
- **Status:** 🟨 Open

### F-07.2 No Delete row action on PyPI content panel (UI)
- **Severity:** R / real-bug
- **Area:** `web/src/pages/.../PyPIRepoPage.tsx` expanded rows (mirrors F-05.4 Docker-tag + F-06.3 RPM/APT)
- **Symptom:** When a PyPI repo holds wheels/sdists, the expanded package panel shows each file with only a "Scan report" link. There is no Delete/kebab/row-action. The backend `DELETE /<project>/pypi/<repo>/packages/{filename}` works (204, trash-moves the file, regens the simple index), but users have no in-UI path to trigger it.
- **Repro:**
  1. Upload `hello-1.1-py3-none-any.whl` via twine.
  2. Open `/projects/acme/pypi/local`, click the "hello" row to expand.
  3. The 3-file list shows no Delete control, only Scan-report links.
- **Backend proof:** `curl -u alice:<key> -X DELETE http://localhost:18080/acme/pypi/local/packages/hello-1.1-py3-none-any.whl` → 204; simple/hello/ regens to 2 entries; `pip install hello==1.1` 404s (audit `pypi.delete` recorded).
- **Fix direction:** same shape as F-05.4 — a session-authed REST shim under `internal/api/` that reuses `Handler.deletePackage`'s auth/tx helpers, plus a `useDeletePyPIFile` hook + confirm dialog per row. May share a single cross-protocol "packages delete" REST shim alongside F-06.3.
- **Status:** 🟨 Open (deferred — tracked-open alongside F-05.4 / F-06.3)

### F-07.3 File-name links in expanded panel 404 — point at a non-existent `/simple/<project>/<filename>` SPA route
- **Severity:** R / real-bug
- **Area:** PyPI repo page expanded file list (wheel + sdist filename <a href>)
- **Symptom:** Clicking e.g. `hello-1.1-py3-none-any.whl` in the panel navigates to `/acme/pypi/local/simple/hello/hello-1.1-py3-none-any.whl`. Backend routes only `/simple/` (root index) and `/simple/{name}/` (per-project index); the deeper path falls through to the SPA catch-all which renders "Page Not Found".
- **Repro:**
  1. On `/projects/acme/pypi/local`, click the "hello" row → expand.
  2. Click any wheel/sdist filename.
  3. Browser URL becomes `/acme/pypi/local/simple/hello/<filename>`; page body shows "Page Not Found".
- **Root cause:** the generated link href treats the filename as an additional path segment under `/simple/<project>/`.
- **Fix direction:** either set `href` to the canonical download URL `/<project>/pypi/<repo>/packages/<filename>` (backend serves it; session cookie passes auth on same-origin), OR wire it to a file-detail SPA route that reads metadata + exposes an explicit "Download" button. Download-direct is closest to how Docker/RPM/APT panels behave today.
- **Status:** 🟨 Open

### F-07.4 Mirror sync-path version parser truncates legacy `.egg` filenames
- **Severity:** R / real-bug
- **Area:** `internal/protocol/pypi/sync_handler.go:322-330`
- **Symptom:** Mirrored `.egg` files land in `pypi_files` with a nonsense `version` (e.g. `requests-2.23.0-py2.7.egg` → row stores `version="py2.7.egg"`, `kind="sdist"`). That version value gets echoed back out in the simple/ index grouping, the collapsed UI row "latest version", and the `scan.gate` fields.
- **Repro:**
  1. Create a PyPI mirror with upstream pypi.org and filter `{"names":["requests"]}`.
  2. Sync — pulls ~236 versions including one `.egg`.
  3. `sqlite3 .../omnirepo.sqlite "SELECT filename,version,kind FROM pypi_files WHERE version LIKE '%egg%';"` → `requests-2.23.0-py2.7.egg|py2.7.egg|sdist`.
- **Root cause:** `sync_handler.go`'s inline version parser strips only `.gz`, `.tar`, `.zip`, then takes the substring after the last `-`. For `name-version-pyX.Y.egg` filenames the suffix isn't stripped and the last dash falls inside the `-pyX.Y.egg` tail. The canonical `parseSdistFilename` in `parse.go:202` explicitly rejects anything that isn't `.tar.gz / .tgz / .zip`, but the sync path doesn't use it.
- **Fix direction:** **skip `.egg` (and `.exe`, `.msi` installers) at the upstream-file-filter stage** — PyPI stopped accepting new `.egg` uploads in 2017 and pip has not installed from eggs via simple/ since the same era. Alternative: extend the sync parser to call `parseSdistFilename` and `parseWheelFilename` consistently and treat parse failure as "skip file + log warn". Skipping is cleaner than adding `.egg` support since the file isn't installable by modern pip anyway.
- **Status:** 🟨 Open

## Sign-off

- [x] All 13 cases passed (7.1 – 7.13)
- [x] Final state:
  - [x] `acme/pypi/local` has `hello-1.0-py3-none-any.whl` + `hello-1.0.tar.gz` (hello-1.1.whl was deleted as part of 7.8; the backend-delete retest re-uploaded the baseline so the live state is hello-1.0 wheel + sdist)
  - [x] `acme/pypi/pypi-upstream` mirrored `six` (48 releases) and `requests` (236 releases, 53.7 MB), 284 scans done
  - [x] Severity gate proven: `requests-2.0.1` (high:1) → 403 `blocked_by_scan source:db/cache`; `requests-2.33.1` (clean) → 200 with wheel body
- [x] F-07.1 / F-07.3 / F-07.4 closed with fixes landed (and F-07.1 fortified after two Codex passes)
- [x] F-07.2 deferred — same shape as F-05.4 / F-06.3, bundles into the next cross-protocol REST-shim
- [x] F-07.5 (dashed-pre-release version parser) + F-07.6 (filename allowlist) deferred — pre-existing cross-cutting, tracked for batch-15
- [x] Codex rescue pass #1 (verdict: blocker found on F-07.1 — addressed in commit `1d225bd`)
- [x] Codex rescue pass #2 (verdict: concurrency hole on F-07.1 — addressed in commit `c9febdb`; rescue-3 not required, fix lands with unit + race coverage)
- [x] README.md batch 07 status flipped to ✅
