---
phase: 07-snippet-polish-dashboard-cards-empty-states
plan: 03
subsystem: ui
tags: [snippets, vitest, playwright, a11y, clipboard, tdd]

# Dependency graph
requires:
  - phase: 06-error-envelope-visual-foundation
    provides: CopyButton aria-live polite contract (Phase 6 / VISUAL-08)
  - phase: 07-snippet-polish-dashboard-cards-empty-states
    provides: SnippetList primitive + CopyButton aria-label override (07-02)
provides:
  - Corrected per-protocol CLI snippets for all 8 RepoTypes (S-01..S-09)
  - Vitest infrastructure (web/vitest.config.ts + `npm test` script) usable by future phases for pure-TS unit tests
  - Playwright spec asserting SNIPPET-09 aria-live + clipboard round-trip on the SnippetPanel Sheet
affects: [07-08, 07-09, v1.2]

# Tech tracking
tech-stack:
  added:
    - vitest ^4.1.4 (devDep) — pure-TS unit-test runner, peer-compatible with Vite 6.x
  patterns:
    - "vitest with node environment + `@/` alias mirrors Vite config for frontend pure-TS modules"
    - "Playwright spec grants clipboard-read + clipboard-write permissions at `test.use` scope so every test inherits them without per-test grantPermissions calls"
    - "TDD RED/GREEN split across two commits — failing tests first, then implementation — preserves a clean git-log audit of the contract and how code changed to satisfy it"

key-files:
  created:
    - web/src/lib/__tests__/snippets.test.ts
    - web/vitest.config.ts
    - web/e2e/snippet-copy.spec.ts
  modified:
    - web/src/lib/snippets.ts
    - web/package.json
    - web/package-lock.json

key-decisions:
  - "Git Authenticate form = `git config --global credential.helper store` (NOT `-c http.extraHeader=Authorization: Bearer …`) — simpler for users, avoids teaching the extraHeader mechanic; both forms work against BasicOrAPIKey middleware which accepts API-key-as-password in any Basic-auth username field"
  - "Vitest 4.1.4 added as devDep (was absent); `npm test` wires `vitest run`; test file at `web/src/lib/__tests__/snippets.test.ts` follows a `__tests__/` colocation convention (not `*.test.ts` alongside source)"
  - "Defensive `default: return []` branch added to `getSnippets` switch so unknown RepoType strings fall through safely rather than returning `undefined` (was a latent bug pre-07-03)"

patterns-established:
  - "Pattern 1: `web/src/lib/__tests__/*.test.ts` colocated under `lib/__tests__/` — vitest config includes `src/**/__tests__/**/*.test.ts`"
  - "Pattern 2: Playwright clipboard spec grants permissions via `test.use({ permissions: ['clipboard-read', 'clipboard-write'] })` at the `describe` scope"
  - "Pattern 3: Snippets with auth hints lead with a `# shell comment` inside the `cmd` body so copy-pasting the full block still runs as a shell script — `#` is a valid shell comment line"
  - "Pattern 4: snippets emit dual-variant APT keys (signed-by + legacy trusted.gpg.d) as SEPARATE labeled entries so users pick the right one for their Debian/Ubuntu version — not a single dual-purpose block"

requirements-completed: [SNIPPET-01, SNIPPET-02, SNIPPET-03, SNIPPET-04, SNIPPET-05, SNIPPET-06, SNIPPET-07, SNIPPET-08, SNIPPET-09]

# Metrics
duration: 5m21s
completed: 2026-04-18
---

# Phase 07 Plan 03: Snippet Polish & Copy Contract Summary

**`getSnippets` rewrite per S-01..S-09 — APT dual-variant signing + literal `stable main`, PyPI `.pypirc` block, Helm 4-entry traditional+OCI, Git Clone+Authenticate with no inline userinfo, RAW `-u` on both directions, S3 `<region>` placeholder — plus vitest scaffold + 9 shape tests and a Playwright aria-live/clipboard spec.**

## Performance

- **Duration:** 5m21s
- **Started:** 2026-04-18T00:20:58Z
- **Completed:** 2026-04-18T00:26:19Z
- **Tasks:** 2 (Task 1 TDD split: RED + GREEN)
- **Files modified:** 6 (3 created, 3 modified)

## Accomplishments

- **Per-RepoType snippet correctness.** All 8 RepoTypes in `web/src/lib/snippets.ts` reviewed; 6 rewritten per S-01..S-09 (deb/pypi/helm/git/raw/s3), 2 kept verbatim (docker/rpm). Every currently-shipping snippet is now correct, complete, and free of deprecated forms (`apt-key add` removed; inline userinfo URLs forbidden; auth hints surfaced symmetrically on RAW).
- **Vitest scaffold in place.** Added `vitest ^4.1.4` devDep, `web/vitest.config.ts` (node env + `@/` alias), `"test": "vitest run"` script, and a `web/src/lib/__tests__/snippets.test.ts` covering all 9 per-RepoType contracts plus the default-branch empty-array case. `npm test` runs them in ~100ms.
- **Playwright aria-live + clipboard spec.** `web/e2e/snippet-copy.spec.ts` asserts the SNIPPET-09 / S-10 wire contract: click a CopyButton inside the SnippetPanel Sheet, expect `aria-live="polite"` region to announce "Copied to clipboard" within 1s, and `navigator.clipboard.readText()` round-trip contains the snippet body. Spec parses cleanly under `npx playwright test --list`.

## Task Commits

1. **Task 1 RED: failing shape tests + vitest scaffold** — `bcd14b6` (test)
2. **Task 1 GREEN: `getSnippets` rewrite per S-01..S-09** — `7f9e865` (feat)
3. **Task 2: SnippetPanel aria-live + clipboard e2e spec** — `5b42059` (test)

_Note: Task 1 used TDD so the cycle split into two atomic commits — RED (failing tests first) and GREEN (implementation that passes them). REFACTOR phase was unnecessary; the implementation landed clean._

## Files Created/Modified

- `web/src/lib/snippets.ts` — rewritten; docker/rpm unchanged, deb/pypi/helm/git/raw/s3 corrected per S-01..S-09; defensive `default: return []` branch added.
- `web/src/lib/__tests__/snippets.test.ts` — NEW; 9 test cases asserting entry counts, label lists, and key correctness substrings per RepoType plus the default-branch case.
- `web/vitest.config.ts` — NEW; vitest pointed at `src/**/__tests__/**/*.test.ts` with node environment and `@/` alias matching Vite config.
- `web/package.json` + `web/package-lock.json` — added vitest 4.1.4 devDep and `"test": "vitest run"` script.
- `web/e2e/snippet-copy.spec.ts` — NEW; Playwright spec asserting aria-live announcement + clipboard write with auth bootstrap copied from error-envelope.spec.ts.

## Decisions Made

- **Git Authenticate form:** Went with `git config --global credential.helper store` over `-c http.extraHeader='Authorization: Bearer <api-key>'`. Both work against the `BasicOrAPIKey` middleware (verified in `internal/auth/middleware/basic_or_apikey.go` — the API-key-as-password branch accepts any username when the password matches `APIKeyRegex`). Helper-store is simpler for users: they run `git config` once, then `git push`/`fetch` prompt for user + key the first time and cache in `~/.git-credentials`. Avoids teaching the extraHeader mechanic.
- **Vitest location + colocation style:** `web/src/lib/__tests__/*.test.ts` (colocated tests under a `__tests__` folder) over `*.test.ts` alongside source. Keeps `lib/` directory listing clean, makes test files easy to grep, and matches how the codebase already colocates `web/src/hooks/__tests__` if future phases add hook tests.
- **Defensive `default: return []`:** The original `getSnippets` switch had no default, so passing an unknown RepoType returned `undefined` and would crash downstream `.map()` callers. Added a defensive default — caught by the new "unknown RepoType" test case.

## Deviations from Plan

**None — plan executed exactly as written.** The plan's contingency branches (vitest-not-installed, fallback snippet harness, clipboard permission fallback) were all checked; vitest installed cleanly against Vite 6.3.3 (vitest 4.1.4 is peer-compatible), and the Playwright clipboard permissions worked with the standard `test.use` permissions array — no fallback paths taken.

## Issues Encountered

- **Playwright webServer shell-syntax issue (pre-existing, OUT-OF-SCOPE).** When attempting to run `npx playwright test snippet-copy.spec.ts` for the optional full-run verification, the configured `webServer.command` (`cd .. && VITE_OMNIREPO_DEV=true (cd web && ...) && make build && ... serve`) fails with `/bin/sh: 1: Syntax error: "(" unexpected` — Playwright runs the webServer command under `/bin/sh` which doesn't support bash subshell syntax. Reproduces on existing `error-envelope.spec.ts` too, so this is not new with 07-03 — logged to `deferred-items.md` for a later 07-* micro-fix. The plan's acceptance criteria marks full-run verification as conditional ("When the dev server is running"); the mandatory `--list` parse check passes.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- Plan 07-04 (EmptyState call-site migrations) can land next without depending on snippets.ts internals.
- Plan 07-08 (end-to-end snippet-audit Playwright spec) now has a working vitest baseline to complement it — per-protocol shape is unit-tested, UI rendering stays in Playwright's lane.
- Vitest scaffold unlocks future pure-TS unit testing for `web/src/lib/*.ts` (format helpers, query-key builders, etc.) without adding per-phase setup cost.
- The pre-existing Playwright webServer shell-syntax bug (logged to `deferred-items.md`) will block anyone trying to execute a full e2e run locally until a later plan wraps the command in `bash -c '...'`. All spec files parse correctly via `--list`.

## Self-Check: PASSED

**Files verified on disk:**
- ✅ `web/src/lib/snippets.ts` (modified)
- ✅ `web/src/lib/__tests__/snippets.test.ts` (created)
- ✅ `web/vitest.config.ts` (created)
- ✅ `web/e2e/snippet-copy.spec.ts` (created)
- ✅ `web/package.json` (modified — vitest devDep + test script)

**Commits verified in git log:**
- ✅ `bcd14b6` test(07-03): add failing getSnippets shape tests + vitest scaffold
- ✅ `7f9e865` feat(07-03): rewrite getSnippets per S-01..S-09 corrections
- ✅ `5b42059` test(07-03): add SnippetPanel aria-live + clipboard e2e spec

**Verification gates:**
- ✅ vitest: 9/9 passed (was 2/9 at RED)
- ✅ make lint-spacing-carveout: clean
- ✅ make lint-typography: clean
- ✅ npm run build: green
- ✅ npx playwright test snippet-copy.spec.ts --list: 1 test discovered

## TDD Gate Compliance

Plan 07-03 used task-level TDD on Task 1. Git log shows:
1. `test(07-03): add failing getSnippets shape tests …` — **RED** gate (commit `bcd14b6`)
2. `feat(07-03): rewrite getSnippets per S-01..S-09 …` — **GREEN** gate (commit `7f9e865`)

REFACTOR phase was not required — the implementation passed cleanly and needed no further shape changes.

---
*Phase: 07-snippet-polish-dashboard-cards-empty-states*
*Completed: 2026-04-18*
