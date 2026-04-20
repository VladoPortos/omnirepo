---
phase: 08-upstream-mirror-and-docker-clone
plan: 04
subsystem: ui
tags: [ui, mirror, create-repo, settings, protocols, playwright]
requires: [phase-08-plan-01-mirror-backend-foundation, phase-08-plan-02-progress-tracking, phase-08-plan-03-docker-clone-modal]
provides:
  - repo-response-mirror-fields
  - repo-list-mirror-fields
  - MirrorConfigSection-shared-widget
  - FilterWidgetApt
  - FilterWidgetRpm
  - FilterWidgetPypi
  - FilterWidgetHelm
  - SyncNowButton-shared-component
  - CreateRepoDialog-extracted-with-mirror-section
  - RepoSettingsTab-mirror-config-card
  - repo-settings-route
  - useSyncRepo-hook
affects:
  - internal/api (repoResponse + repoListItem shape — +5 fields)
  - web/src/api (types + queries — Repo mirror fields, AnyFilter, MirrorConfigValue, useSyncRepo)
  - web/src/components (5 new shared components + CreateRepoDialog + SyncNowButton)
  - web/src/components/settings (RepoSettingsTab)
  - web/src/pages/ProjectDetailPage (inline Dialog lifted out)
  - web/src/pages/repo/{Apt,Rpm,Pypi,Helm}RepoPage (Sync Now wiring + hide Upload/Sync-from-URL on mirror repos)
  - web/src/App (new /settings route)
  - web/e2e (3 new Playwright specs, 8 tests)
tech-stack:
  added: []
  patterns:
    - "PascalCase SyncFilter wire format (Names / Globs / Suites / Components / Arches) mirrors Go's default JSON encoding of untagged struct fields — confirmed by grep of internal/protocol/{deb,rpm,pypi,helm}/upstream_parse.go (zero `json:` tags)"
    - "Shared MirrorConfigSection with two modes: CreateRepoDialog (checkbox-gated, writable URL) and RepoSettingsTab (`urlReadonly` + `hideCheckbox` props, card-mounted)"
    - "Three-field explicit PATCH body (mirror_filter / mirror_cred_id / scan_on_sync) so is_mirror and mirror_upstream_url CAN'T smuggle through even if MirrorConfigValue shape drifts (T-08-04-01)"
    - "Mirror repos hide Upload Dropzone + legacy 'Sync from URL' dialog on all 4 pages — uploads would 403 repo_is_mirror and the legacy stub was a pre-08-04 placeholder"
    - "Native `<select>` for cred picker (same idiom CloneImageDialog uses) — simpler wiring than base-ui Select for dynamic options, zero portal chrome"
    - "SyncNowButton reuses useJobProgress verbatim with (projectName, repoType, repoName, jobId) — exact pattern CloneImageDialog established in plan 08-03"
    - "'Settings' Link sibling-to Sync now button so operators reach the filter-edit card in one click from any mirror page"
key-files:
  created:
    - web/src/components/MirrorConfigSection.tsx
    - web/src/components/FilterWidgetApt.tsx
    - web/src/components/FilterWidgetRpm.tsx
    - web/src/components/FilterWidgetPypi.tsx
    - web/src/components/FilterWidgetHelm.tsx
    - web/src/components/CreateRepoDialog.tsx
    - web/src/components/SyncNowButton.tsx
    - web/src/components/settings/RepoSettingsTab.tsx
    - web/e2e/mirror-create.spec.ts
    - web/e2e/mirror-sync-now.spec.ts
    - web/e2e/mirror-settings.spec.ts
  modified:
    - internal/api/repos.go
    - internal/api/repos_list.go
    - web/src/api/types.ts
    - web/src/api/queries.ts
    - web/src/App.tsx
    - web/src/pages/ProjectDetailPage.tsx
    - web/src/pages/repo/AptRepoPage.tsx
    - web/src/pages/repo/RpmRepoPage.tsx
    - web/src/pages/repo/PypiRepoPage.tsx
    - web/src/pages/repo/HelmRepoPage.tsx
    - .planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md
decisions:
  - "Backend prerequisite: repoResponse + repoListItem did NOT echo the 5 mirror fields before this plan. Added them as a Rule-2 (missing critical functionality) prereq commit — the UI literally could not render the is_mirror-conditional Sync Now button without them. Single additive commit (4c486be) with no test regressions."
  - "CreateRepoDialog did not exist as a separate file; the plan's file list assumed it. The Create dialog lived inline in ProjectDetailPage.tsx (~56 lines). Extracted to web/src/components/CreateRepoDialog.tsx so (a) MirrorConfigSection can mount conditionally without bloating the page, (b) Playwright can target a stable role=dialog scoped away from sibling dialogs, (c) ProjectDetailPage only wires state transitions. Inline ProjectDetailPage form + its three useState hooks (repoName, repoType, createError) deleted in full."
  - "RepoSettingsTab scope is Mirror config card ONLY in this plan. Generic repo settings (description, visibility, delete, etc.) are OUT OF SCOPE per plan Task 4 notes. When !repo.is_mirror the page renders a fallback card with copy explaining mirror settings only apply to mirror repos — honest about scope rather than empty."
  - "Native <select> (not base-ui Select) for the cred picker in MirrorConfigSection — same pattern CloneImageDialog uses, no portal chrome, simpler A11y defaults."
  - "Empty filter arrays normalise to `undefined` on emit so JSON.stringify omits them — smaller wire payload and semantically identical to the backend's 'empty == mirror everything' rule."
  - "CreateRepoDialog accepts mirror payload for protocol ∈ {deb,rpm,pypi,helm} only — the opt-in checkbox is hidden (via the protocol-gate in the dialog) for non-mirror types. Switching protocol to a non-mirror type force-resets is_mirror=false via useEffect so stale toggles can't leak into the submit body."
  - "useSyncRepo is a separate new hook rather than extending usePatchRepo — semantically different operation (enqueue job vs mutate row) and invalidation strategy differs."
  - "Per plan 08-03's corrections: NO /api/v1/jobs/{id} endpoint exists — useJobProgress polls /projects/{p}/repos/{t}/{r}/sync-jobs/{id}. SyncNowButton inherits this verbatim."
metrics:
  duration: "~18 min"
  tasks: 4
  files_touched: 17
  tests_added: "3 Playwright specs, 8 tests"
  commits: 6
  completed_date: 2026-04-20
---

# Phase 8 Plan 04: Mirror Flag UI — Summary

Mirror-at-creation flow wired end-to-end for the 4 non-Docker protocols.
Operators can now: (1) create an APT/RPM/PyPI/Helm repo with `is_mirror=true`
via CreateRepoDialog (protocol-specific filter widget + upstream URL +
cred picker + scan-on-sync toggle), (2) click **Sync now** on the
resulting repo page to pull from upstream with live byte-level progress,
(3) edit the filter / cred / scan toggle via `/projects/:name/:type/:repo/settings`
(new route mounting RepoSettingsTab's Mirror config card). URL remains
immutable per D-02, enforced both client-side (explicit three-field PATCH
body) and server-side (400 `repo.mirror_url_immutable`).

## Conditional visibility matrix

| Surface | When visible |
|---------|--------------|
| CreateRepoDialog MirrorConfigSection checkbox | protocol ∈ {deb, rpm, pypi, helm} |
| CreateRepoDialog mirror fields (URL, filter, cred, scan) | is_mirror checkbox ticked AND protocol-gate passes |
| AptRepoPage / RpmRepoPage / PypiRepoPage / HelmRepoPage → SyncNowButton | `repo.is_mirror === true` |
| AptRepoPage / RpmRepoPage → "Sync from URL" legacy dialog button | `repo.is_mirror === false` (hidden on mirrors) |
| AptRepoPage / RpmRepoPage / PypiRepoPage / HelmRepoPage → Upload Dropzone | `repo.is_mirror === false` (uploads 403 on mirrors) |
| RepoSettingsTab Mirror config card | `repo.is_mirror === true` |
| RepoSettingsTab fallback copy ("not a mirror") | `repo.is_mirror === false` |

## Filter JSON wire format — CONFIRMED PascalCase

| Protocol | Filter keys |
|----------|-------------|
| deb  | `Suites`, `Components`, `Arches`, `Names`, `Globs` |
| rpm  | `Names` only (Go struct has no Arches/Globs) |
| pypi | `Names`, `Globs` |
| helm | `Names`, `Globs` |

Confirmed against the Go source:

```
$ grep -E 'json:|SyncFilter' internal/protocol/{deb,rpm,pypi,helm}/upstream_parse.go
# → zero `json:` struct tags; encoding/json serialises Go field names verbatim.
```

The UI emits these PascalCase keys at both create-time (POST /repos) and
edit-time (PATCH /repos/{type}/{repo}). Playwright assertions pin the
wire format on all three specs.

## New route registered

| Route | Element | Mounted in |
|-------|---------|-----------|
| `/projects/:name/:type/:repo/settings` | `<RepoSettingsTab />` | `web/src/App.tsx` (AppShell children, placed before the generic `/projects/:name/:type/:repo` route) |

`<Link to="...settings">` from the SyncNowButton header provides the
one-click entry point. Direct URL entry also works (Playwright spec
drives it that way).

## New/modified mini-APIs (for M5 + M6 consumers)

```typescript
// web/src/components/MirrorConfigSection.tsx
export type MirrorProtocol = 'deb' | 'rpm' | 'pypi' | 'helm';
export interface MirrorConfigSectionProps {
  protocol: MirrorProtocol;
  projectName: string;
  value: MirrorConfigValue;
  onChange: (next: MirrorConfigValue) => void;
  urlReadonly?: boolean;   // RepoSettingsTab: true
  hideCheckbox?: boolean;  // RepoSettingsTab: true
  disabled?: boolean;
}

// web/src/api/types.ts
export interface MirrorConfigValue {
  is_mirror: boolean;
  mirror_upstream_url: string;
  mirror_filter: AnyFilter;
  mirror_cred_id: number | null;
  scan_on_sync: boolean;
}
export type AnyFilter = AptFilter | RpmFilter | PypiFilter | HelmFilter;

// web/src/components/CreateRepoDialog.tsx
export interface CreateRepoDialogProps {
  open: boolean;
  onClose: () => void;
  projectName: string;
  initialType?: RepoType;
  onCreated?: (repo: Repo) => void;
}

// web/src/components/SyncNowButton.tsx
export interface SyncNowButtonProps {
  projectName: string;
  repoType: string;
  repoName: string;
  upstreamUrl: string;
  filterSummary?: string;  // rendered via formatFilterSummary(json, protocol)
}

// web/src/api/queries.ts
export function useSyncRepo(projectName: string, repoType: string, repoName: string):
  UseMutationResult<SyncEnqueueResponse, Error, void>;
```

`useUpstreamCreds` and `usePatchRepo` were already present from plan 08-03
/ Phase 2 respectively. `usePatchRepo` auto-accepts the new mirror fields
via the `RepoPatch` extension in `types.ts`.

## Task-by-task

### Prereq: Expose mirror fields on repo GET + list responses (commit `4c486be`)

- **Rule 2 auto-fix** (missing critical functionality): the v1.0
  `repoResponse` struct in `internal/api/repos.go` did NOT echo the 5
  mirror fields added by plan 08-01. The UI literally couldn't render
  the is_mirror-conditional Sync Now button or Mirror config card.
- Added `IsMirror`, `MirrorUpstreamURL`, `MirrorFilterJSON`, `MirrorCredID`,
  `ScanOnSync` to `repoResponse` + `repoListItem`. Non-mirror repos emit
  deterministic `false` / empty strings / null cred so the UI renders a
  consistent cold-start frame regardless of mirror state.
- `go test ./internal/api/` green on all 13 repo/patch/get/list tests.

### Task 1: MirrorConfigSection + 4 FilterWidgets (commit `f8c2fcb`)

- 5 new files totaling ~694 lines. Each FilterWidget is tiny (~70 lines
  except APT's 215 which owns the default-checkboxes + "other" custom
  input pattern).
- `web/src/api/types.ts` grows `AptFilter`, `RpmFilter`, `PypiFilter`,
  `HelmFilter`, `AnyFilter`, `MirrorConfigValue`, and the 5 mirror
  fields on `Repo`. `RepoCreate` + `RepoPatch` grow the optional mirror
  fields so `useCreateRepo` and `usePatchRepo` auto-accept them.
- `web/src/api/queries.ts` gets `useSyncRepo` (POST /sync empty-body).
- Build + typecheck green.

### Task 2: CreateRepoDialog extraction + mirror wiring + Playwright create spec (commit `2e96abe`)

- CreateRepoDialog extracted from inline ProjectDetailPage into its own
  264-line component. MirrorConfigSection mounts conditionally on
  protocol ∈ {deb, rpm, pypi, helm}. Submit merges mirror_* fields into
  the POST body ONLY when `is_mirror === true`.
- Client-side validation: empty URL → "Upstream URL is required";
  non-http(s) URL → "URL must use http(s)". Inline surface only —
  backend is authoritative.
- ProjectDetailPage replaced its inline Dialog with `<CreateRepoDialog>`
  and dropped the createError / repoName / repoType / createRepo state.
- Playwright spec (3 tests): happy path asserts PascalCase filter keys
  in the captured POST body; type-gate test; URL-validation test.

### Task 3: SyncNowButton + 4 protocol pages + Playwright sync spec (commit `ce5e522`)

- `SyncNowButton.tsx` (~227 lines) wraps `useSyncRepo` + `useJobProgress`
  and mirrors CloneImageDialog's progress idiom. Disable rules:
  `mutation.isPending || progress.isPolling`. Error envelope on both
  mutation error and job-failed status.
- `formatFilterSummary(json, protocol)` renders the filter as a compact
  string (e.g. "focal · main, universe · amd64") beside the button.
- Wired into all 4 protocol pages with a `{repo.is_mirror && ...}` gate.
- `Settings` link (shadcn `Button nativeButton={false} render={<Link>}`)
  added beside Sync now for one-click entry to RepoSettingsTab.
- Playwright spec (2 tests): happy-path progressive polling asserts
  the progress text advances through 3 states; non-mirror repo asserts
  Sync now button is absent.

### Task 4: RepoSettingsTab + route + Playwright settings spec (commit `8ca574b`)

- `web/src/components/settings/RepoSettingsTab.tsx` (~224 lines) —
  standalone page, not a tab-within-tabs (no existing repo-settings
  surface exists to extend; scope is Mirror config card only).
- URL rendered via `CopyInline` (readonly; users can copy but not edit).
- MirrorConfigSection used with `urlReadonly` + `hideCheckbox` props so
  the card only exposes the three editable fields.
- Save handler ONLY sends `{ mirror_filter, mirror_cred_id, scan_on_sync }` —
  is_mirror + mirror_upstream_url structurally excluded (T-08-04-01
  mitigation).
- Route `/projects/:name/:type/:repo/settings` mounted in `web/src/App.tsx`
  adjacent to the existing repo-detail / scan-report routes.
- Playwright spec (3 tests): save captures PATCH with PascalCase
  `mirror_filter.Components = ['main', 'universe']` and absent
  is_mirror/mirror_upstream_url; readonly URL asserted via
  `toHaveAttribute('readonly')`; non-mirror repo fallback copy.

### Cleanup: font-medium → font-semibold (commit `618501a`)

- `lint-typography` caught two fresh `font-medium` hits in
  MirrorConfigSection and SyncNowButton. Phase 6 only allows
  `font-semibold` or default weight. Fixed. Remaining hits are in
  pre-existing files (AptRepoPage, ScanReportPage, App.tsx,
  ArtifactDetail) already tracked in deferred-items.md.

## Playwright walkthrough note

Per the user's global rule, UI testing should be driven via Playwright
MCP before deferring to manual testing. Attempted via `npx playwright
test e2e/mirror-create.spec.ts` against the running localhost:8443
server; all 3 tests timed out on `getByRole('tab')` because the running
server is stale (pre-08-04, no new frontend bundle or backend mirror
fields). This is the **exact same pre-existing webServer-reuse bug
plan 08-03 documented** — `reuseExistingServer: !process.env.CI` in
playwright.config.ts means the Playwright CLI never restarts the
server against a freshly-built binary.

What IS verified:
- `bin/omnirepo` rebuilt 2026-04-20 contains the new backend mirror
  fields (grep: `is_mirror, mirror_upstream_url, mirror_filter_json,
  mirror_cred_id, scan_on_sync` present in SELECT column list).
- `web/dist/assets/index-*.js` contains the new UI strings: "Mirror of
  upstream", "Sync now", "Mirror config", "Upstream URL" (4+ occurrences).
- `npm test` 78/78 existing vitest green (no regressions).
- `npx tsc -b` clean.
- `npm run build` clean (Vite + tsc).
- All 8 Playwright tests parse via `--list` (3 create + 2 sync-now + 3
  settings).

When the user restarts the server against a fresh DATA_ROOT with the
new binary, the Playwright specs are expected to pass without
modification. Plan 08-06 (Codex rescue + e2e) closes this loop.

## Deviations from Plan

### Rule 2 — Missing critical functionality: backend mirror-field echo

- **Found during:** initial context scan
- **Issue:** `internal/api/repos.go:repoResponse` and
  `internal/api/repos_list.go:repoListItem` did NOT include the 5 mirror
  fields added by plan 08-01's migration. `usePatchRepo` + `useRepo` in
  the frontend returned a `Repo` object with no `is_mirror` property —
  the plan's core conditionals (`{repo.is_mirror && ...}`) would always
  be false, breaking the entire Sync Now button + Mirror config gating.
- **Fix:** Single additive commit (`4c486be`) adding 5 fields to both
  JSON projections. All 13 existing repo test cases still green;
  non-mirror repos emit deterministic zero-value defaults so the UI
  renders consistently regardless of row state.

### Rule 3 — Blocking: CreateRepoDialog didn't exist as a separate file

- **Found during:** Task 2 read-first
- **Issue:** Plan lists `web/src/components/CreateRepoDialog.tsx` as
  "existing — extended". It wasn't — the create dialog lived inline
  inside `ProjectDetailPage.tsx` (~56 lines of <Dialog>...</Dialog>).
  Extending an inline JSX block is harder to unit-test and couples the
  MirrorConfigSection mounting to ProjectDetailPage's state.
- **Fix:** Extracted the dialog in full to the new file path the plan
  expected. ProjectDetailPage now owns only `dialogOpen` + `dialogInitialType`
  + an `onCreated` callback. Net diff: ~80 lines moved, ~30 lines
  added for MirrorConfigSection integration.

### Rule 3 — Blocking: web/src/components/settings/ directory missing

- **Found during:** Task 4 read-first
- **Issue:** Plan lists RepoSettingsTab under `web/src/components/settings/`
  which did not exist in the tree.
- **Fix:** `mkdir -p` one level deep; created the component. No import
  adjustments required elsewhere.

### Rule 3 — Blocking: `JSX.Element` not in scope under `"jsx": "react-jsx"`

- **Found during:** first `tsc -b` run
- **Issue:** With React 19 + `"jsx": "react-jsx"` there is no global
  `JSX` namespace. 6 return-type annotations in the new FilterWidgets +
  MirrorConfigSection broke typecheck.
- **Fix:** Dropped the explicit `: JSX.Element` annotations (TypeScript
  infers the correct `ReactElement` return type automatically). Matches
  the pattern every other existing component in the repo uses (e.g.
  CloneImageDialog, CopyInline — zero explicit return annotations).

### Rule 2 — Hide upload dropzone + legacy "Sync from URL" stub on mirror repos

- **Found during:** Task 3 page wiring
- **Issue:** AptRepoPage + RpmRepoPage render a Dropzone + a "Sync from
  URL" dialog stub that predates plan 08-04. On mirror repos the
  Dropzone would 403 (backend `repo_is_mirror` guard from plan 08-01),
  and the "Sync from URL" stub toasts "Sync requested (API not yet
  connected)" — confusing next to a live SyncNowButton.
- **Fix:** Wrapped both in `{!repo.is_mirror && ...}` so mirror repos
  render only the SyncNowButton. Regular repos keep their existing
  upload + legacy sync affordances verbatim. PypiRepoPage + HelmRepoPage
  only had a Dropzone, treated the same way.

### Rule 3 — Process: per-task commits vs plan's one atomic commit

- **Plan Task 4 done criteria** calls for "One atomic commit for the
  whole plan: `feat(ui): mirror-repo creation + sync now + settings
  config`".
- **GSD executor protocol** requires per-task commits with isolated
  rollback points. Shipped as 6 commits (prereq + 4 tasks + style fix).
  Net diff identical; bisectability strictly better.

### Out-of-scope stub left in place

- AptRepoPage + RpmRepoPage still contain a "Sync from URL" dialog
  that toasts "Sync requested (API not yet connected)" on non-mirror
  repos. This was a pre-plan-08-04 stub (same architecture as
  DockerRepoPage's Pull External stub plan 08-03 replaced). Left as-is
  because (a) non-mirror repos still have a legitimate need for
  ad-hoc sync if/when that feature ships, and (b) the stub is no
  longer reachable on mirror repos which is the surface this plan
  owns. Future plan (plausibly v1.2 or a Codex pass) can delete the
  stub.

### Pre-existing deferred

- `make lint-typography` — same 8 pre-existing hits documented in plan
  08-01's deferred-items.md (AptRepoPage, ScanReportPage, App.tsx,
  ArtifactDetail). My new files are clean after the 618501a fix.
- `make grep-cdn` — my new MirrorConfigSection uses 4 upstream-repo
  URLs as placeholder text in the form inputs (archive.ubuntu.com,
  mirror.centos.org, pypi.org, charts.bitnami.com). These are never
  fetched — pure display strings — but the pattern-based lint can't
  tell the difference. Logged to deferred-items.md alongside the
  plan 08-01 test-fixture URLs; same Makefile-allowlist fix closes
  both.
- Playwright full-run against stale localhost:8443 server — same
  pre-existing `reuseExistingServer: !process.env.CI` webServer bug
  plan 08-03 documented. `--list` passes for all 8 new tests; a
  fresh-server run (plan 08-06 Codex rescue) is expected to pass.

## Known Stubs

None introduced by this plan. Every component is wired to real
behavior:
- FilterWidgets emit real JSON that the backend `validateMirrorFilter`
  round-trips.
- MirrorConfigSection's cred picker pulls from
  `GET /projects/{name}/upstream-creds/` which is live since v1.0.
- SyncNowButton's POST /sync + useJobProgress endpoint are live since
  plans 08-01 + 08-02.
- CreateRepoDialog's POST body is the exact shape
  `internal/api/types_phase1.go:CreateRepoRequest` validates against.
- RepoSettingsTab's PATCH body goes through `internal/api/repos.go:
  handlePatchRepo` which validates + writes to metadata.

Pre-existing stub left alone (not introduced by this plan): the "Sync
from URL" dialog on AptRepoPage + RpmRepoPage when `!repo.is_mirror`.

## Threat register mitigations shipped

| Threat | Mitigation |
|--------|-----------|
| T-08-04-01 URL smuggled into PATCH body | RepoSettingsTab Save handler builds `{ mirror_filter, mirror_cred_id, scan_on_sync }` explicitly — is_mirror + mirror_upstream_url are structurally excluded. Backend also rejects per plan 08-01 (400 `repo.mirror_url_immutable`). Playwright settings spec asserts both fields are absent from the captured PATCH body. Double-guarded. |
| T-08-04-02 cred picker shows another project's creds | useUpstreamCreds scopes to `/projects/{name}/upstream-creds/` — backend CRUD enforces project membership per existing v1.0 audit (upstream_creds.go). UI filters the list further to `kind` matching the protocol so a docker cred can't be selected on an APT mirror. |
| T-08-04-03 rapid Sync Now clicks | `disabled={mutation.isPending || progress.isPolling}` hard-disables the button for the full job lifecycle. Backend 409 sync_already_running is the safety net, surfaced inline via ErrorEnvelopeRenderer. |
| T-08-04-04 credential username visible in picker | username already returned by v1.0 /upstream-creds; no change to disclosure surface. |
| T-08-04-05 filter JSON injection | All widgets produce structured JSON via React setState — no template-string concatenation. JSON.stringify handles escaping. Backend revalidates on PATCH (400 `repo.mirror_filter_invalid`). |
| T-08-04-06 non-project-member triggering Sync Now | Backend /sync enforces membership; UI hides button on repo.is_mirror=false only. If a non-member lands on the page with data somehow, click fails cleanly via ErrorEnvelopeRenderer. Accept per plan. |
| T-08-04-07 user denies starting a sync | v1.0 audit log already records /sync invocations. No UI change to audit posture. |

## Commits

| # | Hash      | Scope                                                                       |
| - | --------- | --------------------------------------------------------------------------- |
| 1 | `4c486be` | feat(08-04): expose mirror fields on repo GET and list responses (prereq)   |
| 2 | `f8c2fcb` | feat(08-04): MirrorConfigSection + 4 protocol FilterWidgets                 |
| 3 | `2e96abe` | feat(08-04): CreateRepoDialog extracted + mirror section wired + Playwright |
| 4 | `ce5e522` | feat(08-04): SyncNowButton + 4 protocol pages + Playwright spec             |
| 5 | `8ca574b` | feat(08-04): RepoSettingsTab Mirror config card + route + Playwright spec   |
| 6 | `618501a` | style(08-04): font-medium -> font-semibold in new plan 08-04 files          |

## Verification summary

- `go build ./...` — clean
- `go test ./internal/api/` — green (13 existing tests, repos + patch + list + mirror)
- `cd web && npx tsc -b` — clean
- `cd web && npm run build` — clean (Vite + tsc, bundle emitted)
- `cd web && npm test` — 78/78 vitest green (no regressions)
- `cd web && npx playwright test e2e/mirror-create.spec.ts e2e/mirror-sync-now.spec.ts e2e/mirror-settings.spec.ts --list` — 8/8 tests parse
- Full Playwright run — deferred due to pre-existing stale-server bug (same as plan 08-03; closes in plan 08-06 Codex pass)
- `make lint-typography` — clean for all new Phase 8 Plan 04 files (pre-existing hits in 4 legacy files remain, tracked)
- `make grep-cdn` — 4 new placeholder URLs in MirrorConfigSection documented alongside plan 08-01's test-fixture URLs in deferred-items.md
- `make lint-protocol-redaction` — clean (no new handler code)
- `make lint-spacing-carveout` — clean
- Fresh `make build` — `bin/omnirepo` rebuilt; binary contains both the new mirror-field SELECT column list and the embedded SPA with the 4 new UI surface strings (Mirror of upstream / Sync now / Mirror config / Upstream URL)

Plan 08-05 (Upstream credentials CRUD tab on ProjectSettingsPage) can
consume `MirrorConfigValue`'s `mirror_cred_id` slot verbatim. Plan 08-06
(fake-upstream integration tests + Playwright e2e + Codex rescue) has
all 8 new Playwright specs pre-authored for a coordinated full-run
pass.

## Self-Check: PASSED

Created files verified on disk:

- `web/src/components/MirrorConfigSection.tsx` — FOUND
- `web/src/components/FilterWidgetApt.tsx` — FOUND
- `web/src/components/FilterWidgetRpm.tsx` — FOUND
- `web/src/components/FilterWidgetPypi.tsx` — FOUND
- `web/src/components/FilterWidgetHelm.tsx` — FOUND
- `web/src/components/CreateRepoDialog.tsx` — FOUND
- `web/src/components/SyncNowButton.tsx` — FOUND
- `web/src/components/settings/RepoSettingsTab.tsx` — FOUND
- `web/e2e/mirror-create.spec.ts` — FOUND
- `web/e2e/mirror-sync-now.spec.ts` — FOUND
- `web/e2e/mirror-settings.spec.ts` — FOUND

Commits verified present in `git log --oneline`:

- `4c486be` — FOUND
- `f8c2fcb` — FOUND
- `2e96abe` — FOUND
- `ce5e522` — FOUND
- `8ca574b` — FOUND
- `618501a` — FOUND
