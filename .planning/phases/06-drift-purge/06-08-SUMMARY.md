# Plan 06-08 — UI: drift_purge checkbox in MirrorConfigSection

**Phase:** 06-drift-purge
**Plan:** 06-08 (UI / Playwright smoke)
**Status:** Complete
**Date:** 2026-04-25

## What shipped

A maintainer-gated `drift_purge` checkbox in `MirrorConfigSection.tsx`,
threaded end-to-end through the TypeScript surface, with a Playwright
smoke covering toggle persistence and the viewer-disabled gate.

### Files changed (5)

- `web/src/api/types.ts` — extended `Repo`, `MirrorConfigValue`, `RepoCreate`, `RepoPatch` with `drift_purge: boolean` (or optional on the request shapes).
- `web/src/components/MirrorConfigSection.tsx` — new import `useRoleFor` from `@/hooks/useAuth`; new `myRole` + `driftGateDisabled` derivation; new `<label>` block with `<Checkbox>` rendering D-16-locked tooltip copy verbatim, placed AFTER the `scan_on_sync` block and BEFORE the info box.
- `web/src/components/CreateRepoDialog.tsx` — `EMPTY_MIRROR` extended with `drift_purge: false` so the type is satisfied at create time.
- `web/src/components/settings/RepoSettingsTab.tsx` — `deriveInitial` accepts a 6th param `driftPurge`; the `useEffect` reads `repoQ.data.drift_purge`; `handleSave` now includes `drift_purge: localCfg.drift_purge` in the PATCH body alongside the other three editable fields.
- `web/e2e/drift-purge.spec.ts` (NEW) — two tests: maintainer toggle round-trip + viewer disabled gate.

### Tooltip copy (D-16, verbatim)

> Auto-remove mirror rows whose upstream entry vanished. Purged rows
> go to Trash for the configured retention window.

Checkbox label (D-15-derived):

> Auto-purge rows that vanish from upstream

## Verification

| Gate | Result |
|------|--------|
| `cd web && npx tsc --noEmit` | ✓ green |
| `cd web && npm run build` | ✓ green (chunked output unchanged from baseline) |
| `cd web && npx playwright test drift-purge.spec.ts --reporter=line` | ✓ **2 passed (26.9s)** |
| `git diff --stat web/package.json web/package-lock.json` | empty (zero new npm deps) |

The Playwright e2e suite drives the real backend (`./bin/omnirepo serve`) under the same self-signed TLS cert + `ignoreHTTPSErrors: true` that production uses. Verification details from the green run:

- Maintainer test: checkbox visible, unchecked by default (D-17 default off), enabled, click → checked, save → reload → still checked; uncheck → save → reload → still unchecked.
- Viewer test: checkbox visible, **disabled** (D-15 maintainer gate via `useRoleFor` returning `'viewer'`).

## Commits

- `1de2618` feat(06-08): add drift_purge checkbox to MirrorConfigSection (DRIFTPURGE-04, D-15/D-16)
- `8a920c7` test(06-08): playwright smoke for drift_purge maintainer toggle + viewer gate

## Phase-close note

Phase 6 UI surface is complete; no further UI work is required for v1.5.

- Sync History Dialog `drift_purged` line — DEFERRED per Discretion #8 in 06-07 (visual design needed; not blocking).
- TrashPage badge for drift kinds — DEFERRED per Discretion #5 in 06-04 (existing Kind column already renders the new kinds; sufficient for v1.5).

## Deviations from plan

- The plan listed only `MirrorConfigValue` + a single PATCH-request type, but the actual codebase has four interfaces in play (`Repo` for response, `MirrorConfigValue` for the form state, `RepoCreate` for POST, `RepoPatch` for PATCH). All four were extended for type-soundness — TS errors at the consumer sites (CreateRepoDialog.EMPTY_MIRROR, RepoSettingsTab.deriveInitial + handleSave) were resolved with the minimum drift_purge wiring. No scope creep beyond the type surface.
- The plan suggested helpers `seedMirrorRepo` + `seedUserWithProjectRole` with positional args; the actual `seedUserWithProjectRole` in `web/e2e/helpers/auth.ts` has a different signature (`request, login, role, projectName` returning the OTP). The spec was adapted to call the real helper signature; a local `seedPypiMirrorRepo` was added inside the spec (pattern: existing `seedAptMirrorRepo` in `mirror-settings.spec.ts`).
- Manual Playwright MCP visual screenshot was skipped — the MCP browser does not accept the self-signed dev cert (no equivalent of `ignoreHTTPSErrors`). The Playwright e2e test, which DOES accept it, exercises the real-rendered checkbox against assertions that include the visible aria-label (D-16 tooltip copy) and the gated disabled state. The visible label text is a literal React child (`<span className="font-semibold">Auto-purge rows that vanish from upstream</span>`) — Tailwind classes are static, no risk of render divergence from JSX.

## Wire-shape sanity

- PATCH body when checkbox toggles on: `{ mirror_filter, mirror_cred_id, scan_on_sync, drift_purge: true }` (server validates mirror-only via plan 06-02's `repo.drift_purge_mirror_only` 400 envelope; no UI rejection because the section only renders `isOpen` when the repo is already a mirror).
- GET response: `repoResponse.drift_purge: boolean` (always emitted per plan 06-02).
