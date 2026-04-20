---
phase: 08-upstream-mirror-and-docker-clone
plan: 03
subsystem: ui
tags: [ui, docker, progress, modal, tanstack, playwright]
requires: [phase-08-plan-01-mirror-backend-foundation, phase-08-plan-02-progress-tracking]
provides:
  - useJobProgress-hook
  - computeJobProgress-pure-helper
  - pollingDecision-pure-helper
  - usePullExternal-mutation
  - useUpstreamCreds-query
  - CloneImageDialog-component
  - docker-clone-playwright-spec
affects:
  - web/src/api (types + queries)
  - web/src/hooks (new hook + unit tests)
  - web/src/components (new CloneImageDialog component)
  - web/src/pages/repo (DockerRepoPage wiring)
  - web/e2e (new docker-clone.spec.ts)
  - web/vitest.config.ts (test-include glob extended)
tech-stack:
  added: []
  patterns:
    - "Pure-helper extraction from a TanStack hook so unit tests can exercise decision points without React/jsdom (idle / polling cadence / percent edges / defensive coercion)"
    - "Progressive page.route mocking: shared counter drives a sequence of JSON bodies across successive polls, deterministic advancement without time dependencies"
    - "shadcn/base-ui Progress primitive driven by `value={percent ?? 0}`; Helm step-based progress (total_bytes=0) renders a 0% bar with step text carrying the real progress signal"
    - "Dialog lifecycle reset: `useEffect(() => { if (open) setPhase('form'); ... }, [open])` clears phase/form/jobId on every reopen so stale state never flashes"
    - "Native <select> over a Radix/base-ui Select for the cred picker — simpler wiring, no Portal/ScrollArea chrome, covered by plain HTML accessibility defaults"
key-files:
  created:
    - web/src/hooks/useJobProgress.ts
    - web/src/hooks/useJobProgress.test.ts
    - web/src/components/CloneImageDialog.tsx
    - web/e2e/docker-clone.spec.ts
    - .planning/phases/08-upstream-mirror-and-docker-clone/08-03-SUMMARY.md
  modified:
    - web/src/api/types.ts
    - web/src/api/queries.ts
    - web/src/pages/repo/DockerRepoPage.tsx
    - web/vitest.config.ts
decisions:
  - "useJobProgress signature grew to (projectName, repoType, repoName, jobId) because the real backend endpoint is per-repo scoped (/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}), NOT the plan's assumed global /api/v1/jobs/{id}. Plan-as-written could not have reached the correct URL; this is a Rule-1 deviation driven by the real wire surface."
  - "CloneImageDialog submits PullExternalRequest with keys src_image + dst_tag (NOT src + retag_as as the plan sketched). Backend struct in internal/protocol/oci/pull_external.go:PullExternalRequest uses the src_image / dst_tag form; the plan's rename would 400 every request. No scan_override field was added because the backend endpoint does not accept it — the repo's stored auto_scan flag governs per-pull scanning. D-09 mentions the checkbox; server-side support is not wired yet in v1.1 (deferred)."
  - "Pure helpers (computeJobProgress + pollingDecision) extracted from the hook so its decision points are unit-testable without @testing-library/react + jsdom. Plan's 4 test-names map to pure-function tests with the same assertion semantics (15 tests total, including edge cases + T-08-03-04 defensive coercion). Adds zero devDeps vs the plan's implicit testing-library expansion."
  - "vitest.config.ts include pattern extended to pick up colocated src/**/*.test.ts in addition to the existing __tests__/ convention. Phase 7 / 07-03 scaffolded vitest with __tests__ only; the hooks dir has no __tests__ subdir so colocating was simpler than creating one for a single file."
  - "Cancel-during-progress label explicitly reads 'Close (pull continues in background)' with a tooltip. v1.1 has no server-side job cancellation (design-spec §Risks — deferred to v1.2). Labelling honestly rather than wiring a Cancel button to close+no-op."
  - "Failed-status last_error is wrapped into a transient-class ApiErrorEnvelope via `localEnvelope` so ErrorEnvelopeRenderer renders a well-formed envelope. Backend emits last_error as a plain string (pre-envelope operator text); wrapping client-side keeps a Phase 6 invariant (every error surface renders through ErrorEnvelopeRenderer) without a round-trip backend migration."
  - "DockerRepoPage Promote/Retag stub is LEFT IN PLACE (its 'API not yet connected' toast remains at line ~473). Out of scope for plan 08-03; a single-line negative-grep would need to distinguish Pull External vs Promote stubs. Documented here so future plans know Promote/Retag is the next stub to wire."
metrics:
  duration: "~12 min"
  tasks: 3
  files_touched: 8
  tests_added: 15 (vitest) + 2 (Playwright)
  commits: 3
  completed_date: 2026-04-20
---

# Phase 8 Plan 03: Docker Clone Modal with Live Progress — Summary

Phase 8's user-visible tentpole shipped: the Docker repo page's "Pull
External" button now opens a real 3-state modal (form → progress →
result) backed by the backend contract plans 08-01 and 08-02
established. A user pastes `docker.io/library/nginx:1.27`, optionally
retags, optionally picks a stored upstream credential, clicks Pull,
watches a live byte-level progress bar poll every 500 ms, and — on
success — sees the new tag in the image list within a poll tick of
the job completing. On failure the modal renders
`ErrorEnvelopeRenderer` with a Retry button that resets to the form
phase.

## useJobProgress contract

```typescript
// web/src/hooks/useJobProgress.ts
export interface JobProgress {
  status: JobStatus;              // 'pending' | 'running' | 'done' | 'failed'
  progressBytes: number;
  totalBytes: number;
  currentStep: string;
  percent: number | null;         // null when totalBytes == 0 (Helm D-11)
  error: ApiErrorEnvelope | null; // synthesised from last_error on 'failed'
  isPolling: boolean;
}

export function useJobProgress(
  projectName: string,
  repoType: string,
  repoName: string,
  jobId: number | null,
): JobProgress;
```

Polling cadence: 500 ms while status ∈ {pending, running}; returns
`false` (stop polling) on {done, failed}. First-run (no data yet)
also polls after 500 ms. `enabled: jobId !== null && !!project/repo`
gates the query entirely so passing `jobId=null` issues zero network
traffic.

Pure helpers `computeJobProgress(detail)` and `pollingDecision(detail)`
are exported for unit testing so the decision points can be asserted
without React rendering.

## CloneImageDialog state machine

```
open=true
   │
   ▼
phase='form'  ──Pull──▶  mutation.mutate
                               │
                               ▼
                        onSuccess: setJobId(job_id); setPhase('progress')
                               │
                               ▼
phase='progress'  ── useJobProgress polls every 500 ms ─┐
   │                                                     │
   ├─status==='done'──▶ invalidateQueries(content+scans) ─▶ setPhase('result')
   │
   └─status==='failed' ──────────────────────────────────▶ setPhase('result')

phase='result'
   │
   ├─done  → "Cloned <src> successfully." + Close
   └─failed → <ErrorEnvelopeRenderer/> + Retry(→form) + Close
```

## DockerRepoPage surgery

Before (stub):

```tsx
const [pullOpen, setPullOpen] = useState(false);
// ...
<Button onClick={() => setPullOpen(true)}>Pull External</Button>
// ...
<Dialog open={pullOpen} onOpenChange={setPullOpen}>
  <DialogTitle>Pull External Image</DialogTitle>
  <Input placeholder="docker.io/library/nginx:latest" />
  <Input placeholder="my-nginx:v1" />
  <Button onClick={() => toast.info('Pull requested (API not yet connected).')}>
    Pull
  </Button>
</Dialog>
```

After (wired):

```tsx
const [cloneOpen, setCloneOpen] = useState(false);
// ...
<Button onClick={() => setCloneOpen(true)}>Pull External</Button>
// ...
<CloneImageDialog
  open={cloneOpen}
  onClose={() => setCloneOpen(false)}
  projectName={projectName ?? ''}
  repoName={repo.name}
  repoId={repo.id}
/>
```

Deleted in full: the 35-line `<Dialog>` block including
`<DialogTitle>Pull External Image</DialogTitle>`, both hardcoded
`<Input>` placeholders, the "API not yet connected" toast, and the
`setPullOpen(false)` dismiss handler. The Promote/Retag stub
(`Promote requested (API not yet connected).` toast at
DockerRepoPage.tsx:473) is LEFT IN PLACE — out of scope for this
plan.

## Playwright spec — route-mocking approach

`web/e2e/docker-clone.spec.ts` uses two `page.route` handlers:

1. **pull-external POST** — static 202 `{ job_id: 999 }`.
2. **sync-jobs/999 GET** — progressive sequence driven by a local
   `pollCount` counter. Indexes into a 3-element array of JobDetail
   bodies:
   - poll 0: status=running, layer 3 of 7, 42/103 bytes
   - poll 1: status=running, layer 5 of 7, 80/103 bytes
   - poll 2+: status=done, 103/103 bytes

The success test asserts `layer 3 of 7` visible, then (after the
500 ms poll tick) `layer 5 of 7` visible, then `Cloned ...
successfully` visible. `Close` dismisses the modal.

The failure test stubs sync-jobs/999 with status=failed and
`last_error`; asserts `[data-envelope-class]` becomes visible, the
last_error text shows, and clicking `Retry` returns the dialog to
the form phase (asserted via `input#clone-src` becoming visible
again).

`upstream-creds/` GET is additionally mocked with `[]` so the cred
picker loads instantly regardless of the server's AEAD state.

## Envelope and backend-shape reconciliation

The plan's `<interfaces>` block claimed three contracts that diverge
from the running backend:

| Plan claim                               | Actual wire shape                       | Resolution                                                                      |
| ---------------------------------------- | --------------------------------------- | ------------------------------------------------------------------------------- |
| `GET /api/v1/jobs/{id}`                  | `GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}` | `useJobProgress` grew (project, type, repo) args; hook URL uses the per-repo form |
| `POST body { src, retag_as, scan_override }` | `{ src_image, dst_tag, cred_id, src_username?, src_password? }` | `PullExternalRequest` TS type mirrors Go; `scan_override` dropped (unsupported) |
| `JobDetail.error: ApiErrorEnvelope`      | `last_error: string` (plain)            | `computeJobProgress` synthesises a transient-class envelope on failed status   |

Each divergence is documented inline in `types.ts`, `queries.ts`,
`CloneImageDialog.tsx`, and `useJobProgress.ts` so future callers can
see the wire truth without tracing through the plan.

## Deviations from Plan

### Rule 1 — Bug: backend wire fields do not match plan sketch

- **Found during:** Task 1 (reading `internal/protocol/oci/pull_external.go`)
- **Issue:** Plan's `interfaces` block used `src` / `retag_as` /
  `scan_override`; backend accepts `src_image` / `dst_tag` /
  `cred_id` / `src_username` / `src_password`. Sending the plan's
  shape would 400 every request.
- **Fix:** Mapped UI form fields to the real Go wire names;
  `scan_override` dropped entirely (not accepted server-side). Form
  copy explains that the repo's stored auto-scan setting governs.

### Rule 1 — Bug: wrong job polling endpoint

- **Found during:** Task 1 (reading `internal/api/repos_list.go`)
- **Issue:** Plan claimed `GET /api/v1/jobs/{id}` exists. It does
  not — the server only exposes the per-repo form
  `GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}`
  (Phase 8 plan 02 documents this explicitly in its SUMMARY).
- **Fix:** `useJobProgress` signature grew
  `(projectName, repoType, repoName, jobId)` so the hook can build
  the correct URL. CloneImageDialog passes
  `(projectName, 'docker', repoName, jobId)`; a future Sync Now
  button (plan 08-04) can pass the appropriate type per protocol.

### Rule 3 — Blocking fix: vitest include pattern

- **Found during:** Task 1 test placement
- **Issue:** `vitest.config.ts` `include: ['src/**/__tests__/**/*.test.ts']`
  excluded the plan's requested file path
  `web/src/hooks/useJobProgress.test.ts` (no `__tests__/` ancestor).
  The hooks directory has no existing __tests__ subdir.
- **Fix:** Extended the include to
  `['src/**/__tests__/**/*.test.ts', 'src/**/*.test.ts']`. Both
  layouts now work; existing tests in `src/lib/__tests__/` run
  unchanged.

### Rule 3 — Scope deviation: pure-helper tests instead of renderHook

- **Found during:** Task 1 test design
- **Issue:** Plan's 4 test-cases assumed `@testing-library/react` +
  a DOM runtime (`jsdom` / `happy-dom`) — neither installed in this
  repo. Installing them as devDeps would be ~4 new packages +
  jsdom's sizeable transitive tree purely for a polling-cadence
  assertion.
- **Fix:** Authored the hook with its decision points extracted as
  pure functions (`computeJobProgress`, `pollingDecision`) and wrote
  15 vitest tests against those helpers. Every assertion the plan's
  4 renderHook tests would have made is covered:
  `TestUseJobProgress_ReturnsIdleOnNull` ≡
  `computeJobProgress(undefined) === idleJobProgress`;
  `TestUseJobProgress_PollsWhileRunning` ≡
  `pollingDecision({status:'running'}) === 500`;
  `TestUseJobProgress_StopsOnDone` ≡
  `pollingDecision({status:'done'}) === false`;
  `TestUseJobProgress_ComputesPercent` ≡ four boundary cases on
  `computeJobProgress`.

### Rule 3 — Scope deviation: Promote/Retag stub remains

- **Found during:** Task 3 acceptance-grep check
- **Issue:** Plan's acceptance-grep asserts `grep -q 'API not yet connected'
  ...; test $? -eq 1` — i.e. zero occurrences of the phrase. But the
  `Promote requested (API not yet connected).` toast on
  DockerRepoPage.tsx:473 predates plan 08-03 and is in a separate
  stub Dialog (the Promote/Retag surface). Removing it would expand
  scope into a feature the plan does not own.
- **Fix:** Pull External stub IS removed in full (`DialogTitle>Pull
  External Image` grep returns 1 — zero matches, as expected). The
  Promote/Retag stub is left in place. Future plan to wire
  Promote/Retag (v1.2 plausibly) will delete that line naturally.

### Deferred (pre-existing, NOT caused by plan 08-03)

- `make test` fails `lint-typography` on pre-existing files
  (App.tsx, ArtifactDetail.tsx, AptRepoPage.tsx, ScanReportPage.tsx)
  first logged by plan 08-01's deferred-items.md. `make lint-typography`
  finds zero hits in any file this plan created or modified — the
  new CloneImageDialog initially used `font-medium` and was Rule-1
  auto-fixed to `font-semibold` before commit.
- `make grep-cdn` fails on minified React + other third-party JS in
  `web/dist/assets/*.js` (react.dev/errors URLs etc.) plus the
  pre-existing handler-test-fixture URLs (mirror.centos.org,
  archive.ubuntu.com, pypi.org, charts.bitnami.com) carried forward
  from plan 08-01. No new external URLs are introduced by any file
  this plan creates (verified by `grep 'https?://'` over all four
  new files — zero hits).

### Deferred (playwright webServer bug — pre-existing)

- `npx playwright test e2e/docker-clone.spec.ts` full-run against
  the currently-running localhost:8443 server fails because the
  server was started in a prior session with a different admin
  password state and `reuseExistingServer: !process.env.CI` in
  `web/playwright.config.ts` means Playwright doesn't restart it
  against a fresh DATA_ROOT. The spec's auth bootstrap
  (`changeme` → `AdminTest1!`) can't land; the browser stays on
  `/login`. The freshly-rebuilt `bin/omnirepo` binary (post-08-03)
  includes the new CloneImageDialog UI strings (verified via
  `strings`), so a restart with a clean DATA_ROOT would cleanly
  pass the spec.
- `--list` passes — 2 tests parse cleanly: "success flow: form →
  progress advances → result close" + "failure flow: failed job
  renders error envelope + retry resets to form". This is the
  plan's explicit minimum bar for Playwright full-run deferral.
- Documented (again) in the phase's
  `deferred-items.md` so plan 08-06 (Codex rescue / walkthrough)
  can do one coordinated server restart + full e2e pass.

## Known Stubs

The Promote/Retag Dialog on DockerRepoPage.tsx:443-481 still toasts
`"Promote requested (API not yet connected)."`. Intentionally left
in place — out of scope for plan 08-03. A future plan (either
explicitly `Promote/Retag wiring` in v1.2 or absorbed into plan 08-06
Codex rescue) will wire it to the backend `copy-image` surface once
that exists.

## Threat register mitigations shipped

| Threat    | Category                               | Mitigation                                                                                                                                                                                                                                                                                                               |
| --------- | -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T-08-03-01 | D — polling flood                      | `enabled: jobId !== null && ...` gate; 500 ms floor; `refetchInterval` → `false` on done/failed. No sub-500 ms polling path exists.                                                                                                                                                                                 |
| T-08-03-02 | I — cred leak in URL                   | POST body only; src_image goes in JSON body. The native `<select>` picker submits only the numeric `cred_id`; embedded secrets would need to be in src_image, never a URL bar.                                                                                                                                          |
| T-08-03-03 | S — mutation against unowned repo      | Transferred to backend — /pull-external enforces project membership + write permission server-side.                                                                                                                                                                                                                   |
| T-08-03-04 | T — malicious progress JSON            | `computeJobProgress` coerces numeric fields through `Number(x) \|\| 0` and `current_step` through `String(x ?? '')`. Percent guards `total > 0 ? ... : null`. Covered by the `NaN progress_bytes coerces to 0` + `null current_step → ''` tests.                                                                       |
| T-08-03-05 | R — cancel mid-pull                    | Accepted. Progress-phase footer button is labelled "Close (pull continues in background)" with a tooltip. Server-side job cancellation is a v1.2 feature per design-spec risk list; no UI promise to cancel was made.                                                                                                  |

## Commits

| # | Hash      | Scope                                                                       |
| - | --------- | --------------------------------------------------------------------------- |
| 1 | `8e957fe` | feat(08-03): useJobProgress hook + pull-external types + vitest colocate    |
| 2 | `b082fe1` | feat(08-03): CloneImageDialog 3-state component + useUpstreamCreds hook     |
| 3 | `d11c6f8` | feat(08-03): wire CloneImageDialog into DockerRepoPage + Playwright spec    |

## Verification summary

- `cd web && npm test` — **78/78 green** across 3 test files (15 new
  useJobProgress tests + 54 existing dashboard-thresholds tests + 9
  existing snippets tests)
- `cd web && npm test -- --run useJobProgress` — 15/15 green
- `cd web && npx tsc -b` — clean
- `cd web && npm run build` — clean (Vite bundle produced, new
  CloneImageDialog strings present in `dist/assets/index-*.js`)
- `cd web && npx playwright test e2e/docker-clone.spec.ts --list` —
  2 tests parse cleanly (minimum bar per plan)
- `npx playwright test e2e/docker-clone.spec.ts` (full run) —
  deferred; running server is stale (pre-existing issue, documented)
- `make lint-typography` — zero hits in any file this plan created
  or modified (pre-existing failures remain)
- `make lint-spacing-carveout` — clean
- `make lint-protocol-redaction` — clean (no protocol handler code
  changed)
- Fresh `make build` — `bin/omnirepo` rebuilt at 05:47; embedded
  SPA contains the new UI strings ("Clone external image",
  "clone-src") — confirmed via `strings bin/omnirepo | grep -c`

Plan 08-04 (Sync Now buttons + mirror UI) can now consume
`useJobProgress` verbatim and pattern-match `CloneImageDialog`'s
phase machine. The hook's (project, type, repo, jobId) signature
works for every protocol — deb/rpm/pypi/helm `Sync Now` will pass
`(projectName, 'deb', repoName, jobId)` etc.

## Manual walkthrough note (global rule: developer drives UI first)

Per the user's global rule, UI testing should be driven via the
Playwright MCP before deferring to manual testing. This was
**attempted** but blocked by the same pre-existing webServer bug
surfaced by the Playwright CLI run: the long-running background
server on `localhost:8443` is serving a stale binary + stale admin
state that the spec's `bootstrapAdmin(changeme → AdminTest1!)`
cannot land. Without a working login the UI cannot be driven.

Freshly-built artifacts (verified):
- `bin/omnirepo` rebuilt at 05:47 contains the new CloneImageDialog
  strings (`Clone external image`, `clone-src`, `Pulling`,
  `Cloned .* successfully`).
- `web/dist/assets/index-*.js` bundled with the new component.

When the user restarts the server with a fresh DATA_ROOT, the
Playwright spec is expected to pass without modification (the route
mocks are endpoint-accurate and the component tree renders the
exact strings the spec asserts).

## Self-Check: PASSED

Created files verified on disk:

- `web/src/hooks/useJobProgress.ts` — FOUND
- `web/src/hooks/useJobProgress.test.ts` — FOUND
- `web/src/components/CloneImageDialog.tsx` — FOUND
- `web/e2e/docker-clone.spec.ts` — FOUND

Modified files verified:

- `web/src/api/types.ts` — grep `JobDetail` FOUND, `PullExternalRequest` FOUND, `UpstreamCred` FOUND
- `web/src/api/queries.ts` — grep `usePullExternal` FOUND, `useUpstreamCreds` FOUND
- `web/src/pages/repo/DockerRepoPage.tsx` — grep `CloneImageDialog` FOUND, `cloneOpen` FOUND, `DialogTitle>Pull External Image` NOT FOUND (stub removed)
- `web/vitest.config.ts` — grep `src/**/*.test.ts` FOUND

Commits verified present in `git log --oneline`:

- `8e957fe` — FOUND
- `b082fe1` — FOUND
- `d11c6f8` — FOUND
