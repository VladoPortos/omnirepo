# Batch 16 — v1.5/1.6/1.7 UI delta verification

**Status:** ✅ Passed clean (0 findings)

> Targets the four UI deliverables that landed late in the v1.7 cycle:
> drift purge surfacing on history, drift kind in trash, percent-threshold
> override flow, and the manualChunks bundle split. All four are
> SPA-touching and require Playwright visual confirmation per the v1.7
> directive — both the conditional render (mocked spec) and the deployed
> surface (wt4 server, real data).

## Test approach

Each delta is verified two ways:

1. **Playwright spec** — runs against an ephemeral OmniRepo on
   `http://localhost:8080 / https://localhost:8443` (own data root,
   created via `mktemp -d`); `resetServerState` per `beforeEach`. The
   spec mocks the relevant API response so it covers conditional
   branches (drift_purged > 0 vs == 0 vs absent; drift_blocked > 0 vs
   absent; the four `<proto>_drift` kinds vs default) without requiring
   a live drift-purge to fire.
2. **Visual confirmation** — Playwright MCP drives the wt4 server on
   `http://localhost:28080` (the populated data root with 14 repos,
   real sync history, real Trivy DB). Confirms each surface is wired
   up in the deployed production-style build, takes a screenshot, and
   gates on console errors.

Drift purges did not fire naturally during wt4 (no upstream drift was
synthesised; pypi/click is monotonic; rpm/docker-ce was a one-shot sync).
The mocked spec proves conditional rendering; the visual confirms the
component is mounted on the live page.

## Test cases

### 16.1 SyncHistoryDialog "Drift purged: N" sub-line (UIBACK-01) ✅

- **Spec:** `web/e2e/sync-history-drift-purged.spec.ts:62` — passed in
  `1.8s`. Mocks three jobs (`drift_purged: 12`, `drift_purged: 0`,
  `summary: '{}'`); asserts:
  - `[data-testid="sync-history-drift-purged"]` count == 1
  - text matches `/Drift purged:\s*12/`
  - the two non-drift rows render no sub-line.
- **Visual:** opened dialog on `acme/pypi/py-mirror` (120 click
  releases, 17.6 MB synced 51m ago). Dialog renders the v1.7 layout —
  Status · Started · Duration · Files · Size · Last step columns. The
  one historical job shows `done · 51 minutes ago · 6s · 120 · 17.6 MB
  · done`; sub-line correctly suppressed (this sync didn't drift-purge
  — `summary.drift_purged` is absent). Screenshot:
  `screenshots/batch-16-sync-history-dialog.png`.
- **Backend wiring confirmed:** `internal/metadata/sync_jobs.go` —
  `SetSummaryDriftPurged` writes `summary.drift_purged` on every drift
  purge run; the dialog's lazy `GET /sync-jobs` query carries it.

### 16.2 TrashPage `<proto>_drift` colored badge (UIBACK-02) ✅

- **Spec:** `web/e2e/trash-drift-badge.spec.ts:33` — passed in `968ms`.
  Mocks five trash rows (`pypi_file_drift`, `rpm_package_drift`,
  `deb_package_drift`, `helm_chart_drift`, plain `repo`); asserts:
  - 4× `[data-testid="trash-drift-badge"]` with text `/Drift · (PyPI|RPM|APT|Helm)/`
  - `aria-label="Drift purge: PyPI"` (screen-reader spelling)
  - `tabindex="0"` (keyboard-focusable)
  - the plain `repo` row carries no drift-badge markup.
- **Visual:** navigated `/admin/trash` on wt4. Page loads with two
  current trash rows (both `repo` kind from earlier batch tests by
  alice — `1777151537-repo-8` / docker-ce, `1777151339-repo-7` /
  epel-jq). Default badge renders correctly (the `kind=repo` path);
  the drift-kind path is exercised by the spec only, since wt4 hasn't
  triggered drift naturally. Page surface, retention countdown, and
  Restore/Purge actions render clean. Screenshot:
  `screenshots/batch-16-trash-page.png`.

### 16.3 Percent-threshold purge guard / override flow (UIBACK-03) ✅

- **Spec:** `web/e2e/sync-history-drift-blocked.spec.ts:62` + `:175` —
  both passed (1.7s + 1.4s). The latest job's `summary.drift_blocked > 0`
  surfaces a banner with `Drift purge blocked: N rows pending
  confirmation` and an "Override and purge" button. Clicking it opens
  the confirm dialog `Override drift purge guard`; confirming POSTs
  `/sync` with body `{"force_drift_threshold":true}`. Asserts:
  - banner appears with `data-testid="drift-blocked-banner"`
  - confirm dialog opens with `/will purge\s*42/` text
  - POST body literally `{"force_drift_threshold":true}` (the v1.7
    contract for bypassing the percent-threshold guard on the next sync)
  - parent dialog closes intentionally so SyncNowButton's progress
    block surfaces.
  - the negative case (no `drift_blocked` in any job) renders no
    banner; the `drift_purged` sub-line from UIBACK-01 still works.
- **Visual:** the SyncHistoryDialog opens, dialog chrome and history
  table render — surface for the banner is mounted. Banner did not
  fire on wt4 (no upstream-diff > 50% scenario was simulated; would
  require a synthesised drift situation). Note the handoff doc
  referenced `web/e2e/sync-confirmation.spec.ts` for this delta — that
  file is actually the v1.6 success-pill spec (POLISH-01); the
  UIBACK-03 spec is `sync-history-drift-blocked.spec.ts`. Both files
  exist; only the latter targets UIBACK-03.

### 16.4 Web bundle cold-load smoke (BUNDLE-01..03) ✅

- **Spec:** `web/e2e/bundle-cold-reload.spec.ts:26` — passed in `1.0s`.
  Cold-loads `/dashboard`, `/profile`, and `/api/docs` then asserts the
  console contains zero entries matching:
  - `Failed to fetch dynamically imported module`
  - `ChunkLoadError`
  - `Loading chunk \d+ failed`
- **Visual:** drove the same three routes on wt4:
  - `/` (Dashboard) — 3 Projects · 14 Repositories · 4 Users · 9396
    Scan Findings (630 high · 2424 medium · 6342 low). Storage 2.1 GB /
    95.9 GB · Background Jobs 1 running, 2 queued · Trivy DB Fresh ·
    SQLite Healthy 12 MB. Console: 0 errors / 0 warnings. Screenshot:
    `screenshots/batch-16-dashboard-cold.png`.
  - `/profile` — Personal Information card with dicebear avatar
    rendered (the SU initials chip), Email, Login, Save Changes,
    Change Password, API Keys panel. Confirms the dicebear chunk is
    embedded and tree-shaken correctly. Console: 0 errors.
    Screenshot: `screenshots/batch-16-profile-cold.png`.
  - `/api/docs` — 302 → `/swagger/`. Swagger UI renders with
    OmniRepo API 1.0.0 / OAS 3.1, /api/v1/openapi.yaml link, Authorize
    button, all sections expandable (setup, auth, me, …). Confirms
    the `swagger-ui-dist` static bundle ships from the Go embed.
    Console: 0 errors. Screenshot:
    `screenshots/batch-16-api-docs-cold.png`.
- **manualChunks split confirmed working:** zero
  `Failed to fetch dynamically imported module` errors across the
  three routes — proves the Phase 5 split (react-vendor / tanstack /
  ui-base / radix / lucide / dicebear / sanitize / vendor + per-language
  shiki dynamic imports) didn't introduce a stale-hash regression.

## Console / backend gate

Console errors recorded across all batch-16 navigation:

```
Total: 2 (both pre-existing inert noise)
[ERROR] Failed to load resource: 401 @ /api/v1/auth/login   (login retry — wt3 F-01.2 noise classification)
[ERROR] Failed to load resource: 401 @ /api/v1/auth/login   (idem)
```

Both are the pre-classified 401-on-pre-login-fetch noise documented in
walkthrough-3 F-01.2 and re-confirmed in batches 14 & 15. Zero
regression-relevant entries.

Backend log (`/tmp/omnirepo-wt4/server.log`) gate:

```
$ grep -E '\[ERROR\]|\bpanic\b|\bDATA\sRACE\b' /tmp/omnirepo-wt4/server.log
# (no output)
```

Zero ERROR / panic / data-race hits across the batch-16 traffic.

## Spec runtime summary

```
Running 5 tests using 1 worker

  ✓ bundle-cold-reload.spec.ts:26 (1.0s)
  ✓ sync-history-drift-blocked.spec.ts:62 (1.7s)
  ✓ sync-history-drift-blocked.spec.ts:175 (1.4s)
  ✓ sync-history-drift-purged.spec.ts:62 (1.8s)
  ✓ trash-drift-badge.spec.ts:33 (968ms)

  5 passed (7.5s)
```

Run via ephemeral server on 8080/8443 with `OMNIREPO_DEV=1`
(distinct from the wt4 server on 28080/28443). The wt4 data root was
not touched.

## Findings

**None.**

## Sign-off

- [x] All four sub-cases verified (spec + visual)
- [x] All four Playwright specs passed
- [x] Backend log gate: 0 ERROR / panic / data-race hits
- [x] Console clean (only pre-classified 401 noise)
- [x] Screenshots captured
- [ ] Codex batch-end review (deferred to final-report Codex pass on
      all 4 wt4 fix commits batched)
- [x] Status flipped to ✅
