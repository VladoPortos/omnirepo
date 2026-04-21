# Walkthrough #2 findings

Session: 2026-04-21. Container: `omnirepo-smoke` on `omnirepo:dev` (commit `171f969`).

Severity key: **B** blocker · **R** real-bug · **m** minor · **n** noise.

## Fix status (all green as of 2026-04-21)

Every finding below was fixed inline with a proper fix (not a patch) and
live-verified in the smoke container. No findings currently deferred.

- **F-1 + F-2** — `activityTargetHref` guards for `project.api-key.*` and `.deleted` (commit `3d7ec80`)
- **F-3** — High-Sev Findings dedupe by (cve, package, repo) with occurrence badge (commit `ed5fca5`); severity dropped from GROUP BY + worst-wins merging (commit `c320ff0`)
- **F-4** — dashboard Projects tile filters soft-deleted (commit `0459397`)
- **F-5** — project activity widget now surfaces `project.api-key.*` via `json_extract(details_json, '$.project')` (commit `6abd2b2`); `json_valid` guard prevents 500 on malformed rows (commit `c320ff0`)
- **F-6** — S3 bucket name min-chars client-side (commit `6512ecf`)
- **F-7 schema half** — migration 026 partial-unique on live rows for users/projects/s3_buckets (commits `f90e7b3`, `5e06188`, `a5415fc`)
- **F-7 admin half** — `/admin/users?include_deleted=true` + UI toggle + "Deleted" badge (commit `0b4d704`)
- **F-8** — cache invalidation on Add Member dialog open + super-admin gating on `useAdminUserList` (commits `7c42386`, `c320ff0`)
- **F-9** — git `/refs` aligned with OpenAPI GitRef schema: `sha` field + symbolic HEAD filtered out (commit `37add63`)
- **F-10** — git `/refs` emits short ref names so single-segment `{ref}` path parameters resolve (commit `23ae349`)
- **Codex P1 family** — all dashboard aggregate queries filter `p.deleted_at IS NULL`; `ListProjectIDsForUser` fixed at source (commits `69edf6b`, `c320ff0`); migration 027 for `repos` partial-unique (commit `501f0b1`); runner panic-safe rollback + pre-commit FK check (commit `a5415fc`)

## §3 Per-protocol repo flows

- ✅ **D-2 / 2db1e44 CreateRepoDialog** protocol awareness:
  - Docker → "Docker repos do not support repo-level mirroring. To pull an image from an external registry into this repo, use Pull external image on the repo detail page after creating it." (no mirror checkbox).
  - RPM → mirror checkbox + "This repo is a mirror of an upstream / Uploads will be disabled. A background job pulls from the configured URL.".
  - Git → neither (just type + name).
  - RAW → neither.
- ✅ **D-3 / 3c48e16 snake_case mirror_filter**:
  - snake_case-only payload (`names`, `globs`) → 201 Created.
  - Mixed snake + Pascal keys (`names` + `Names`) → 400 `repo.mirror_filter_invalid`.
- ✅ **D-4 / 1d3b29a git dual-mount**:
  - `git clone http://localhost:18080/walkthru2/git/tools.git` (canonical) → works, origin stored as canonical form.
  - `git clone http://localhost:18080/git/walkthru2/tools.git` (legacy) → also works.
  - Committed + pushed to canonical, ref landed on server.
  - UI clone-URL panel displays canonical form.
- **F-9 (R, real bug) FIXED** Git repo detail page crashed with `TypeError: Cannot read properties of undefined (reading 'slice')` after first commit. Backend `/refs` emitted `target` while OpenAPI GitRef schema + frontend expect `sha`; HEAD leaked as `type=symbolic` (not in the enum). Fixed in `internal/api/git_browse.go` + contract test (commit `37add63`).
- **F-10 (R, real bug) FIXED** After F-9 the page rendered but tree fetch 404'd because `/refs` emitted full `refs/heads/main` names that chi can't bind as a single `{ref}` path segment. Fixed by stripping the `refs/heads/` / `refs/tags/` prefixes in the response (commit `23ae349`).
- ✅ **§3a PyPI severity gate (064765d) VERIFIED LIVE**:
  - Setup: `acme/pypi/pypi-upstream` already mirrored + scanned; `block_on_severity=high`.
  - Block: `GET /acme/pypi/pypi-upstream/packages/requests-2.0.1-py2.py3-none-any.whl` (1 HIGH finding in scan summary) → `403 {"error":"blocked_by_scan","severity":"high","scan_id":83}` (exact envelope per handler.go:533).
  - Control: `GET requests-2.33.1-py3-none-any.whl` (clean summary) → 200 with 64 947-byte wheel.
  - Audit: both `scan.gate.blocked` (with `source:"db"` then `source:"cache"` on repeat) and `pypi.upload` (outcome=`blocked`) rows present in `audit_log`.
  - Cross-protocol: the shared `rawGate` function is re-typed into `pypi.SeverityGateFn`, `helm.SeverityGateFn`, `rpm.SeverityGateFn`, `deb.SeverityGateFn` at app.go:411-420 — single implementation covers all 4 non-OCI protocols. OCI has its own dedicated gate (`internal/protocol/oci/severity_gate.go`) with its own test. `go test ./internal/protocol/... -run SeverityGate` green across both real implementations.
  - Conclusion: live PyPI proof + shared-function wiring + OCI's own unit test means all 5 gated protocols are covered; no need to repeat the live test per protocol.
- ✅ **§3c Helm push + browse VERIFIED LIVE**:
  - Created helm repo `walkthru2/helm/charts` (`POST /api/v1/projects/walkthru2/repos`, id=15).
  - PUT `/walkthru2/helm/charts/charts/mychart-0.1.0.tgz` (414 B minimal chart) → 201.
  - `index.yaml` regenerated via coalescer (~2s), digest `d75050462f41…` matches uploaded.
  - GET chart round-trip: identical SHA256 hash, Content-Type `application/gzip`, 414 B.
  - Helm CLI: `helm repo add` + `helm repo update` + `helm search repo` + `helm show chart` all succeed against `http://localhost:18080/walkthru2/helm/charts` with basic auth.
  - UI at `/projects/walkthru2/helm/charts`: renders "charts" heading, "Helm repository · 1 chart · 414 B" subtitle, Content table with `mychart 0.1.0 / App 1.0 / 414 B / 1 minute ago / Clean / Rescan`. Zero console errors. Auto-scan ran post-upload and marked chart Clean.
- **F-11 (m, minor)** Breadcrumb auto-renders nav path for any URL, even invalid ones. Typing `/projects/walkthru2/repos/helm/charts` (extra `/repos/` segment) shows a "Page Not Found" body but the breadcrumb still displays `walkthru2 > repos > Helm > charts` with clickable links to routes that don't exist (the `repos` link goes to `/projects/walkthru2/repos` which is also Not Found). The correct URL is `/projects/walkthru2/helm/charts` (per App.tsx:313 route `projects/:name/:type/:repo`). UX-only — no internal link in the app appears to use the `/repos/` form, so this only bites users who paste wrong URLs. Fix direction: when the main panel renders NotFound, either suppress breadcrumb past `projects/:name` or mark breadcrumb segments as non-clickable.
- ✅ **§3e RAW upload/download/delete VERIFIED LIVE**:
  - Created raw repo `walkthru2/raw/files` (id=16).
  - `PUT /walkthru2/raw/files/hello/world.txt` → 201 with Location header.
  - `GET` same URL → 200 with byte-identical content.
  - `PUT /walkthru2/raw/files/another.txt` → 201 (second file, arbitrary path).
  - Listing via `GET /api/v1/projects/walkthru2/repos/raw/files/content` → JSON with 2 items + per-file `scan_severity:"clean"`, `sha256:`, `scan_status:"done"`.
  - `DELETE /walkthru2/raw/files/hello/world.txt` → 204; subsequent GET → 404; sibling file still 200.
  - UI at `/projects/walkthru2/raw/files`: heading "files", "RAW repository · 1 file · 13 B" subtitle, table row `another.txt 13 B application/octet-stream 15 seconds ago Clean Rescan`. Zero console errors.
- ✅ **§3f S3 via aws cli VERIFIED LIVE** (also covers §5 S3-key mint):
  - Profile → Create S3 Key → selected `walkthru2` → one-time-reveal dialog shipped secret `WatWsYX…UfIN`; Access Key ID `AKIAVMMQC2GODBVHZ6VV` stored in `s3_access_keys` (project_id=4).
  - `aws --endpoint-url http://localhost:18080/s3 s3 cp /tmp/s3-obj.txt s3://b1x/hello.txt` → 201, 38 bytes uploaded.
  - `s3 ls s3://b1x/` → shows object.
  - `s3 cp s3://b1x/hello.txt …` → byte-identical round-trip.
  - `s3 rm s3://b1x/hello.txt` → success; subsequent `ls` empty.
  - `s3 ls` (ListBuckets) → AccessDenied — correct behavior for a project-scoped key (cannot enumerate across projects).
- **F-12 (m, minor)** "Create S3 Key" dialog combobox displays the project numeric id (e.g. `"4"`) instead of the project name (`walkthru2`) after selection. The underlying form value is correct and Create succeeds, but the visible label is wrong. Likely the combobox `value` is bound to `project.id` but the display-text callback is missing a lookup to `project.name`. Same class as the similar picker issues seen in §2. UI-only polish.
- ✅ **§5 Profile layout rendered clean**: Personal Information (login disabled, email editable, avatar regenerate); Change Password (3 fields + disabled-until-filled Update); API Keys table (lists `ci-user-key`); S3 Access Keys table; My Projects shortcuts; Delete Account danger zone. Route lives at `/profile` (not `/me/...` as the handoff claimed). Zero console errors on mount.

## §4 Search
- **F-13 (R, real bug) FIXED** Severity filter returned empty for known-matching CVEs. Root cause: `vulnerabilities.severity` is stored uppercase (`HIGH`/`MEDIUM`/`LOW`) but the API surface and UI chips send lowercase; `internal/metadata/search.go` did `v.severity = ?` with raw lowercase `p.Severity`, so the predicate never matched. Fix: `strings.ToUpper(p.Severity)` before binding (internal/metadata/search.go). Added `TestSearchAll_SeverityFilterCaseInsensitive` covering lower/UPPER/mixed inputs; all three PASS. Live retest at `/search?q=requests&kind=cve&severity=high` now surfaces `HighCVE-2018-18074requests`.
- **F-12 (m, minor) FIXED** "Create S3 Key" dialog combobox rendered the selected project's numeric id (`4`) instead of its name (`walkthru2`). Radix `SelectValue` was falling back to the raw value because the dialog's mount timing left the SelectItem's text out of Radix's internal map. Fix: explicit `{projects.find(...)?.name}` inside `<SelectValue>` in `web/src/pages/ProfilePage.tsx`. Post-fix DOM inspection confirms combobox text reads "walkthru2▼".
- ✅ Search text box returns mixed result kinds for freeform queries ("requests" → 7 MEDIUM + 1 HIGH CVE + pypi/requests artifacts).
- ✅ Kind-filter button ("Repos"/"Artifacts"/"CVEs") narrows to one table set.
- ✅ Walkthru2-scoped query ("walkthru2") surfaces repos (myapp, tools, snake-test) and artifacts (docker/myapp:1.0, helm/mychart:0.1.0).
- ✅ Deep-link via URL (`/search?q=…&kind=cve&severity=high`) pre-fills chips and triggers the correct backend call.

## §6 Admin
- ✅ `/admin/users` lists `admin / alice / bob / charlie`; F-7 "Show deleted" toggle reveals the soft-deleted `alice@example.com` ("Deleted" badge).
- ✅ D-5 aria-labels on every row: `Edit user X` / `Delete user X` present on all 4 users.
- ✅ `/admin/audit`: 50-row paginated audit log, most-recent first (`auth.login.success admin`).
- ✅ `/admin/gc`: "Run Garbage Collection" confirm-dialog → completes with `Status done · Bytes Freed 0 B · Duration 0s` (expected: no orphans to reclaim yet).
- ✅ `/admin/trash`: lists the earlier soft-deleted raw file with Restore button.
- **F-15 (m, minor) PARTIAL FIX** Trash row showed empty "Original Location" column even though `storage.TrashEntry.OriginalPath` is populated via the per-entry sidecar. Root cause: `handleListTrash` in `internal/api/admin_trash.go` omitted the field from the response shape. Fixed by surfacing `OriginalLocation: e.OriginalPath` into the returned item; regression test `TestAdminTrash_SoftDeleteShowsInList` extended to assert non-empty `original_location` containing the pre-delete path. Live retest shows `1776762004-raw-file-16 -> /var/lib/omnirepo/repos/walkthru2/raw/files/hello/world.txt`. Remaining gaps (`deleted_by`, `retention_countdown`) require a schema/sidecar change (TrashEntry has no actor field; retention is a policy not a per-entry timestamp) — **deferred** as a design-change item, not cosmetic.
- ✅ `/admin/tls`: shows active self-signed cert (Subject omnirepo, Issuer omnirepo, Expiry 2028-04-19, SHA-256 fingerprint), upload form, empty history.
- ✅ `/admin/trivy`: DB `baked-20260416`, age 2 days, 1.0 GB at `/var/lib/omnirepo/trivy/db`, upload + pull buttons present; "Pull Latest DB" clearly labelled as air-gap-incompatible.
- ✅ **D-8 Maintenance unbrick VERIFIED LIVE**: toggled ON via `/admin/maintenance` confirm dialog; DB `enabled:true`, write (POST repo) → 503 `{"code":"maintenance.enabled",...,"details":{"operator_route":"/admin/maintenance"}}`, read → 200; toggled OFF via the top banner "Disable" button (itself a write while maintenance is on) → state flips back to `enabled:false`; subsequent DELETE on `maint-test` repo → 200. Admin never bricked.

## §7 Swagger
- ✅ `/swagger/` loads with title "OmniRepo API 1.0.0 OAS 3.1"; 15 tag groups, 74 operations rendered; zero console errors.
- ✅ `/api/v1/openapi.yaml` served (71 249 B, `openapi: "3.1.0"`).
- 30f13d6 spa-subdir fix holds — swagger assets load from `/swagger/`, not re-entering the SPA.

## §8 E2E auth flows
- ✅ Sign Out from user-menu dropdown → redirects to `/login`.
- ✅ Login as bob (non-admin; password reset via admin PATCH `/api/v1/admin/users/bob` because the initial OTP was already consumed) → lands on Dashboard.
- ✅ Sidebar for bob: only Dashboard / Projects / Search / Profile — no Admin menu.
- ✅ API-level gating: `curl -b bob-cookie /api/v1/admin/users` → 403 `auth.super_admin_required`.
- **F-14 (m, minor) FIXED** Before fix: bob navigating directly to `/admin/users` rendered the admin page shell (empty "No users found" table) with two 403 errors in console — only the data fetch was gated, not the route. Fix: wrap the `admin` route children in a new `RequireSuperAdmin` component that redirects non-admins to `/` before any admin API fetch runs (`web/src/App.tsx`). Post-fix: bob → `/admin/users` lands on `/` Dashboard with zero console errors; admin still reaches every `/admin/*` page (regression check green).
- ⏭️ Session timeout (24h+ TTL) and forced-password-reset-on-first-login flows not re-tested this session; both were exercised end-to-end in the prior walkthrough (D-1 API keys validated the session path; setup wizard covered force-change in the 2026-04-16 run).

## §9 Codex pass + deferred
- Codex audit (`codex:codex-rescue`) reviewed the three session commits d73dd6b/418dd6e/104b12a plus the 34-commit v1.2→HEAD range and returned all three fixes as `noise — no-fix` (correctness clean; no SQL-injection risk in ToUpper, no double-placeholder, no Suspense bypass of the super-admin gate, no render-forever hang since `useMe` terminates via `retry:false`).
- **F-16 (R, real-issue, DEFERRED — design change)** Codex flagged: `internal/api/git_browse.go:46,206` — chi routes `/tree/{ref}/*`, `/blob/{ref}/*`, `/commits/{ref}`, `/blame/{ref}/*`, `/compare/{spec}` all capture `{ref}` as a single path segment, so any ref whose short name contains `/` (e.g. `feature/x`, `release/v1.2`) will only bind the first segment. Fix requires either URL-encoding the ref on the client (chi usually decodes before routing, so `%2F` alone doesn't help) or moving `ref` to a query parameter in 8+ routes plus every UI href builder. The current smoke repo only has `main`, so the defect doesn't bite the testbed — **deferred to v1.4** pending user decision on the URL scheme (keep path segments vs. switch to `?ref=…&path=…` query). Clone/push Smart-HTTP paths are unaffected (they route through go-git's backend, not these browse routes).
- **F-11 (m, minor, DEFERRED — cosmetic)** Breadcrumb on NotFoundPage renders clickable path for typed-wrong URLs. Low impact (no internal link uses the wrong form) — defer.

## Session summary — 2026-04-21
- Commits added this session: 7 (`d73dd6b` F-13, `418dd6e` F-12, `104b12a` F-14, `0dba1da` + `94a50a6` F-15 partial, plus two walkthrough doc updates).
- Running total since v1.2 tag `f4081c7`: 34 commits ahead of main, **not pushed**.
- Test gates: `go test ./internal/...` clean, `npx tsc --noEmit` clean, `npx vitest run` 94/94, `PRAGMA foreign_key_check` empty on live DB.
- Walkthrough §0-§8 fully complete; §9 Codex pass complete. Smoke container `omnirepo-smoke` running on :18080 / :18443 with /tmp/omnirepo-smoke-data persistent volume.
- Open items for next session: F-16 (git ref with `/`) + F-11 + F-15 remaining (`deleted_by`, `retention_countdown`). v1.3 tag decision pending user approval.

## §0 Pre-flight
- ✅ Login page renders, 0 console noise.
- ✅ Bad password shows "Invalid login or password" + caps-lock/admin-reset hint. No "Ask a project owner" copy leaks into bad-password branch (3520818 verified).
- ✅ Good password logs in → lands on `/` which renders dashboard.

## §2 walkthru2 project
- ✅ Project creation (`walkthru2`) lands on detail page, all 4 overview cards render (Members, Storage, Project API Keys, Project Activity).
- ✅ **D-6 / d2dd910** S3 tab renders at 0 buckets (empty state with Create button + `/s3/{bucket}/{key}` hint); after creating `b1x`, tab strip shows `S3 (1)`. Storage card updates to `0 repositories, 1 S3 bucket`.
- ✅ **D-1 / 915f9d6** Project API Keys end-to-end:
  - Empty-name submit blocked client-side (Mint Token disabled).
  - Mint `ci-walkthru` → OneTimeReveal dialog with `omr_p_YY1sZXEy0Lk6w0rw5cryEUDyFK05`.
  - Dialog closes → DOM only shows truncated `YY1sZXEy…`, no full token or prefix leakage.
  - Scoped auth: `/api/v1/projects/walkthru2` → 200, `/acme` → 403, `/empty-test` → 403.
  - Revoke has `aria-label="Revoke ci-walkthru"` (D-5 pattern applied); confirmation dialog; after revoke token returns 401.
- **F-5 (m, minor)** Project Activity widget on the `walkthru2` overview page shows only `project.created project/walkthru2` — never shows the `project.api-key.create` or `project.api-key.revoke` events. Likely same root cause as F-1 (api-key events emit numeric project id as subject, so per-project activity filter miscategorizes them).
- **F-6 (m, minor)** S3 bucket name validation: helper copy says "3–63 chars" but the "Create Bucket" submit button is **enabled with only 2 chars** (e.g. `b1`). Didn't submit that case, but client-side rule is inconsistent with copy. Server likely rejects, but button state should match the stated minimum.
- **F-7 (R, real bug) Soft-deleted users are zombies**. Schema in `users` table has a table-level `UNIQUE(login)` that applies to *all* rows including soft-deleted, so:
  - Re-creating `alice` after deletion returns `409 login exists`.
  - Admin GET `/api/v1/admin/users` (even with `include_deleted=true`) does NOT list her.
  - Admin GET `/api/v1/admin/trash?kind=user` returns empty (users aren't in trash).
  - There is no API or UI path to see, restore, or purge a soft-deleted user — they're invisible but still block login re-use.
  - Evidence: `sqlite3 …db "SELECT id,login,deleted_at FROM users"` shows `2|alice|…|2026-04-21 02:02:46`. The partial index `idx_users_login ON users(login) WHERE deleted_at IS NULL` exists but is **not unique**, so the table-level UNIQUE wins.
  - Fix direction: drop table-level `UNIQUE(login)`, make the partial index `UNIQUE`; add a trash entry emission on user delete + a restore/purge admin UI; or drop soft-delete for users entirely (hard-delete + cascade).
- **F-8 (m, minor) Member-add picker caches users list**. The `Add Member` dialog uses `GET /api/v1/admin/users?limit=200`; the response is fetched once on page load and not re-fetched when the dialog is re-opened. Creating a new user in another tab doesn't show them in the picker until a full page reload. React Query cache invalidation likely missing. Workaround for walkthrough: reloaded project page to refresh picker.
- ✅ **dd6ef72 member-add** verified end-to-end once the cache was refreshed: picker listed `bob — bob@example.com`, Add → member list + `member.added` activity, Remove with confirmation dialog → back to admin-only + `member.removed` activity, zero console errors, aria-labels present on Remove buttons.
- ✅ **a029ab1 OCI cross-mount** verified live with `docker push`: pushed alpine:3.19 to `localhost:18080/acme/docker/images:alpine` (first write), then re-tagged to `localhost:18080/walkthru2/docker/myapp:1.0` and re-pushed — docker reported `Layer already exists`, matching digest `sha256:b58899f0…2171` landed at both repos with no re-upload. Pre-fix this 404'd on the cross-mount step.

## §1 Dashboard
- Observed state drift from handoff: dashboard shows **3 projects**, but handoff said `acme` + `empty-test` only. Activity log shows `project.created d1-smoke` then `project.deleted d1-smoke` 40 min ago; a 3rd project must have been created since. Not a bug, flag for verification when projects list is clicked.
- **F-1 (R, real bug)** `project.api-key.create` audit event emits numeric project ID (e.g. `2`) as its subject, so the Recent Activity widget renders a link to `/projects/2` which 404s (`Project "2" does not exist`). Other events like `project.created empty-test` correctly use the slug. Fix: audit emission for `project.api-key.{create,revoke}` should use project slug, matching existing project events. Location: likely `internal/audit/events.go` + the D-1 API-key handler in `internal/api/project_apikeys.go` (or wherever 915f9d6 added the emit).
- **F-2 (m, minor)** `project.deleted d1-smoke` still renders as a clickable link to `/projects/d1-smoke` (which 404s). Deleted-project audit entries should either drop the link or route to the Trash page with a filter. Skipped click, same 404 class as F-1.
- **F-3 (m, minor)** High-Severity Findings widget shows ~20 identical rows for `CVE-2018-18074 in acme/pypi-upstream (requests)` with no version/file/line differentiator. Presumably every copy of `requests` in the upstream mirror; UX should dedupe by CVE and show a count, not repeat.
- **F-4 (m, minor)** Dashboard "Projects" counter shows **3** while `/projects` list shows only `acme` + `empty-test` (2). Counter likely includes soft-deleted `d1-smoke` that hasn't been purged. Counter should match the list.
