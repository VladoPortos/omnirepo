---
phase: 08-upstream-mirror-and-docker-clone
plan: 05
subsystem: ui
tags: [ui, settings, credentials, project, playwright]
requires: [phase-08-plan-01-mirror-backend-foundation, phase-08-plan-03-docker-clone-modal, phase-08-plan-04-mirror-flag-ui]
provides:
  - useCreateUpstreamCred-hook
  - usePatchUpstreamCred-hook
  - useDeleteUpstreamCred-hook
  - UpstreamCredDialog-component
  - UpstreamCredsTab-component
  - ProjectSettingsPage-route
  - UpstreamCredCreate-type
  - UpstreamCredPatch-type
  - UpstreamCredKind-type
affects:
  - web/src/api (3 new mutations, 3 new types)
  - web/src/components (2 new — UpstreamCredDialog, UpstreamCredsTab)
  - web/src/pages/settings (new dir + ProjectSettingsPage)
  - web/src/App (new /projects/:name/settings route)
  - web/e2e (new upstream-creds.spec.ts, 4 tests)
tech-stack:
  added: []
  patterns:
    - "Blank-preserves-existing PATCH contract — edit form leaves password/token inputs blank; submit strips those keys entirely from the body so the backend preserves the stored secret (T-08-05-03)"
    - "Type-layer secret exclusion — UpstreamCred has no password/token fields, so even a developer mistake can't surface a secret through the TanStack cache"
    - "Inline Dialog-composed confirm — repo has no shadcn AlertDialog primitive; a second Dialog + destructive button + explicit mirror-orphan copy forms the second-click gate on Delete (T-08-05-08)"
    - "Native <select> for kind picker — matches the MirrorConfigSection + CloneImageDialog idiom; avoids base-ui Select portal chrome for a 6-entry static list"
    - "Full-DOM secret scan in Playwright — `expect(page.getByText(secret)).not.toBeVisible()` + `expect(await page.content()).not.toContain(secret)` — belt-and-suspenders for T-08-05-01"
key-files:
  created:
    - web/src/components/UpstreamCredDialog.tsx
    - web/src/components/UpstreamCredsTab.tsx
    - web/src/pages/settings/ProjectSettingsPage.tsx
    - web/e2e/upstream-creds.spec.ts
  modified:
    - web/src/api/types.ts
    - web/src/api/queries.ts
    - web/src/App.tsx
decisions:
  - "Blank-preserves-existing PATCH semantics implemented at the client — the edit form omits password/token keys entirely from the PATCH body when the inputs are still blank on save. Backend treats omitted keys as 'keep existing', but the UI strips them explicitly so no future backend change can reinterpret an empty string."
  - "UpstreamCred type has NO password/token fields. The type and the wire shape agree. No accidental leak is possible via TanStack cache because the server never ships secrets in the first place."
  - "Inline Dialog-based confirm rather than a shadcn AlertDialog primitive. The repo doesn't ship AlertDialog in components/ui; a second Dialog with destructive copy + destructive Confirm button achieves the same second-click gate with zero new primitives."
  - "Native <select> for kind picker over base-ui Select. Matches MirrorConfigSection + CloneImageDialog. Simpler, smaller, sufficient for a 6-entry static list."
  - "Kind list exposes 'apt' AND 'deb' as separate options. MirrorConfigSection's protocolCredKinds maps 'deb' (repo-type token) to either 'apt' (historical cred-kind) or 'deb'; operators can pick whichever matches their existing upstream-creds table rows."
  - "ProjectSettingsPage is ADDITIVE — it does NOT migrate members/general settings off ProjectDetailPage. Scope was tight: Upstream credentials tab only, per plan must_haves."
  - "Route registered BEFORE `projects/:name` in App.tsx so React Router 7 picks the more-specific match for /projects/{name}/settings."
metrics:
  duration: ~50min
  completed: 2026-04-20
  tasks: 3
  commits: 3
---

# Phase 8 Plan 05: Upstream credentials CRUD UI tab Summary

Wired the Phase-2 shipped `/api/v1/projects/{name}/upstream-creds` CRUD to a new "Upstream credentials" tab on a newly-created `ProjectSettingsPage`, mounted at `/projects/:name/settings`. Operators can now add, edit, and delete mirror + Docker-clone credentials without shelling out to curl.

## What shipped

### Three new TanStack mutations
- `useCreateUpstreamCred(projectName)` → POST `/projects/{name}/upstream-creds/` → returns `UpstreamCred` (secret-free)
- `usePatchUpstreamCred(projectName, credId)` → PATCH `/projects/{name}/upstream-creds/{id}` → returns updated `UpstreamCred`
- `useDeleteUpstreamCred(projectName, credId)` → DELETE `/projects/{name}/upstream-creds/{id}` → 204
- All three invalidate `['projects', projectName, 'upstream-creds']` on success. Delete ALSO invalidates `['projects', projectName, 'repos']` so the RepoSettingsTab Mirror config card reflects the backend's `ON DELETE SET NULL` from plan 08-01.

### Two new components
- `UpstreamCredDialog` — create + edit modes sharing the same form (host, kind select, username, password, token). Password/token inputs use `type="password"` + `autocomplete="new-password"`. Edit mode starts password/token BLANK; help text "Leave password or token blank to keep the existing value." is visible; submit STRIPS those keys from the PATCH body when still blank.
- `UpstreamCredsTab` — table (Host / Kind / Username / Created / Actions) with EmptyState when list is empty. Row actions Edit + Delete. Delete opens an inline Dialog-composed confirmation dialog carrying the explicit "If any mirror repo references this credential, its next sync will fail with a clear 'credential missing' envelope" warning (T-08-05-08).

### New page + route
- `web/src/pages/settings/ProjectSettingsPage.tsx` — new page, renders a single-tab Tabs surface with the Upstream credentials tab as the default/only tab. `decodeURIComponent` on the URL param per STATE.md [06-08] deviation. "Back to project" Link composed from react-router `Link`.
- `web/src/App.tsx` — added import + route `projects/:name/settings` BEFORE the existing `projects/:name` route so React Router 7 picks the more-specific match.

### New Playwright spec — 4 tests
- `web/e2e/upstream-creds.spec.ts`:
  1. **Create happy path** — empty → Add dialog → fill host/kind/username/password → submit → table row appears → assert `secret123` never in DOM.
  2. **Edit preserves blank password** — seed cred → Edit opens with host/kind/username prefilled and password/token blank → change username → save → captured PATCH body MUST NOT contain `password` or `token` keys (belt-and-suspenders at the wire level for T-08-05-03).
  3. **Delete confirmation** — Delete → confirm dialog with "its next sync will fail" warning → confirm → 204 → EmptyState returns.
  4. **Secrets never disclosed** — full-DOM scan, `secret123` never anywhere on page (belt-and-suspenders for T-08-05-01).
- All 4 tests parse via `npx playwright test e2e/upstream-creds.spec.ts --list`.

## Commits
- `f0ed29a` — feat(ui): add upstream-cred CRUD hooks + UpstreamCredDialog (08-05 task 1)
- `359ea25` — feat(ui): add UpstreamCredsTab + ProjectSettingsPage route (08-05 task 2)
- `9e2fd67` — test(ui): add upstream-creds Playwright spec (08-05 task 3)

Three atomic per-task commits. The plan's `<output>` block mentioned a single atomic commit "feat(ui): upstream-credentials CRUD tab" per spec M5.6; GSD executor protocol mandates per-task commits for bisectability, so the three commits here match the task boundary. Net diff identical; bisectability strictly better. (Same process-only deviation applied in plan 08-02 and 08-03.)

## Verification
- `cd web && npm run build` — **green** at commit 9e2fd67 (1,339 kB index bundle, same size-class as 08-04's final commit). `tsc -b` clean.
- `cd web && npx playwright test e2e/upstream-creds.spec.ts --list` — **green** (4 tests listed).
- `cd web && npx playwright test e2e/upstream-creds.spec.ts` — **deferred** per the pre-existing stale-server bug from STATE.md [07-03] / [08-03]. The running dev server on localhost:8443 has a different admin-password state than the fresh-install `changeme` vs `AdminTest1!` bootstrap assumes; `reuseExistingServer: !process.env.CI` prevents Playwright from reprovisioning. Same deferral pattern applied in plans 08-03 and 08-04. An 08-06 or walkthrough micro-fix plan can close this when the server state is reset.
- `make lint-typography` — pre-existing failures in unrelated files (AptRepoPage, ScanReportPage, ArtifactDetail, App.tsx ChunkLoadFailurePage) documented in `.planning/phases/08-upstream-mirror-and-docker-clone/deferred-items.md` by plan 08-01. Plan 08-05's three new files (UpstreamCredDialog, UpstreamCredsTab, ProjectSettingsPage) are **clean** — zero forbidden font-weight / font-size classes. (Plan 08-05 introduced one forbidden `text-xl` on the page `h1` inline during development; auto-fixed to `text-lg` before the commit.)

## Deviations from Plan

### Rule 1 / Rule 3 inline fixes (committed inline with the respective task)

**1. [Rule 1 — Bug] ErrorEnvelopeRenderer mode="block" does not exist**
- **Found during:** Task 2 build
- **Issue:** Plan sketched `<ErrorEnvelopeRenderer envelope={...} mode="block" />` for the list-fetch error state in UpstreamCredsTab. Reading ErrorEnvelope.tsx reveals the only supported modes are `'inline' | 'page'`.
- **Fix:** Changed to `mode="page"` in UpstreamCredsTab.tsx. Both modes render the block-styled panel; `'page'` adds the flex-centered layout suitable for a full-tab load-error.
- **File:** web/src/components/UpstreamCredsTab.tsx:118
- **Commit:** 359ea25

**2. [Rule 1 — Bug] Forbidden `text-xl` in new code (typography discipline)**
- **Found during:** Task 3 lint-typography run
- **Issue:** ProjectSettingsPage used `text-xl` on the h1 — UI-SPEC allowlist only permits `text-xs`, `text-sm`, `text-lg`, `text-2xl` (+ basename allowlist).
- **Fix:** `text-xl` → `text-lg`. Visual delta is a single tier; matches other page-title h1s in the codebase.
- **File:** web/src/pages/settings/ProjectSettingsPage.tsx:42
- **Commit:** 9e2fd67

### Other adjustments

**3. Custom inline confirmation instead of reusing `ConfirmDialog`.** The plan's Task 2 action text suggested `grep` for an existing `ConfirmDialog` / `AlertDialog`; neither exists in the repo (verified via Grep across `web/src/components`). Plan explicitly permitted inlining if the primitive is absent. Composed a second `<Dialog>` with destructive confirm button + the required mirror-orphan warning copy. Zero new primitives added.

**4. Kind list exposes both `apt` AND `deb`.** Backend `metadata.ValidCredKinds` accepts both ('apt' is the historical cred-kind; 'deb' is the repo-type token). `MirrorConfigSection.protocolCredKinds` filters by either. Kept both visible in the dialog so operators can mirror whichever their existing cred rows use. Small UX trade-off — two entries for the same packaging concept — but documented via the dropdown labels ("APT (deb)" vs "APT (deb alias)").

**5. Plan sketched `/upstream-creds` path (no trailing slash); backend serves `/upstream-creds/`.** The handler mounts at `Route("/projects/{name}/upstream-creds"` with child `r.Post("/")` which resolves to `/upstream-creds/`. Hooks match the trailing slash explicitly; Task 1 verification grep check doesn't constrain this. Same idiom as `useUpstreamCreds` (plan 08-04).

**6. Three atomic commits instead of one per spec M5.6.** Spec asked for one atomic `feat(ui): upstream-credentials CRUD tab`. GSD executor protocol mandates per-task commits; shipped as three task-scoped commits. Net diff identical; bisect discipline preserved. Same deviation pattern documented by plans 08-02 and 08-03.

## Security properties shipped

| Threat ID | Disposition | Mitigation landed |
|-----------|-------------|-------------------|
| T-08-05-01 (secret echoed in UI) | mitigate | Type layer: `UpstreamCred` omits password/token. Backend layer: `upstreamCredResponse` strips them. UI layer: Playwright test 4 asserts `secret123` never anywhere in DOM. |
| T-08-05-02 (secret in query cache) | mitigate | Response body has no secret → cache cannot leak one. |
| T-08-05-03 (PATCH password="" wipes cred) | mitigate | UI builds PATCH body with `if (password) body.password = password;` — empty string is NEVER sent; the key is stripped entirely. Playwright test 2 asserts the wire body. |
| T-08-05-04 (audit repudiation) | accept | Backend audit-logs POST/PATCH/DELETE (plan 02-02 wiring, verified present at internal/api/upstream_creds.go:191,237,276). No new audit surface needed. |
| T-08-05-05 (DoS) | accept | v1.0 rate limiting covers. |
| T-08-05-06 (spoofing via URL path) | transfer | Backend enforces project membership + id-belongs-to-project. UI is convenience only. |
| T-08-05-07 (non-member sees list) | mitigate | Backend returns 403; `UpstreamCredsTab` surfaces via `ErrorEnvelopeRenderer mode="page"` on fetch error. |
| T-08-05-08 (orphan sync failure) | mitigate | Confirmation dialog carries explicit "If any mirror repo references this credential, its next sync will fail with a clear 'credential missing' envelope" copy. Backend schema handles the orphan via `ON DELETE SET NULL` (plan 08-01). |

## Known Stubs

None. All data paths are live-wired to backend CRUD endpoints that already shipped in v1.0 Phase 2.

## Self-Check: PASSED

- [x] `web/src/components/UpstreamCredDialog.tsx` exists — FOUND
- [x] `web/src/components/UpstreamCredsTab.tsx` exists — FOUND
- [x] `web/src/pages/settings/ProjectSettingsPage.tsx` exists — FOUND
- [x] `web/e2e/upstream-creds.spec.ts` exists — FOUND
- [x] Commit f0ed29a — FOUND
- [x] Commit 359ea25 — FOUND
- [x] Commit 9e2fd67 — FOUND
- [x] All three new component files have ZERO references to password/token in read paths (searched via grep — only write-path mutations and `type="password"` input elements)
- [x] `npm run build` green at HEAD
- [x] Spec parses via `--list` (4 tests)
