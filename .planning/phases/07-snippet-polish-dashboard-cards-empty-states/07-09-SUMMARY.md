---
phase: 07-snippet-polish-dashboard-cards-empty-states
plan: 09
status: complete
tasks_completed: 1
tasks_total: 1
---

# Plan 07-09 Summary — Codex Rescue Sweep

## What shipped

End-of-phase Codex rescue pass per global CLAUDE.md "Codex verification" rule. Codex was invoked via `Agent(subagent_type="codex:codex-rescue", ...)` (not the `/codex:rescue` slash-command — WSL PATH issue documented in global rules) with a 15-min hard time-box and the full Phase 7 file list.

## Findings by severity

| Severity | Count | Fixed | Rebutted | Noted |
|----------|-------|-------|----------|-------|
| blocker | 0 | — | — | — |
| real-issue | 2 | 2 | 0 | 0 |
| minor | 6 | 0 | 1 | 5 |
| noise | 0 | — | — | — |
| **Total** | **8** | **2** | **1** | **5** |

## Fixes shipped

- **`b29fc62`** `fix(07): codex-identified — pool_release falls back to InRelease (clearsigned)` — `internal/protocol/deb/pool_release.go` now reads `dists/<suite>/InRelease` when `Release` is absent, strips the PGP clearsign wrapper, and re-parses the inner RFC822 headers. New `TestResolvePoolPath_InReleaseFallback` test.
- **`43a1c78`** `fix(07): codex-identified — disabled EmptyState CTA focusable for keyboard` — `web/src/components/common/EmptyState.tsx` wrapper span now has `tabIndex={0}` + `role="button"` + `aria-disabled="true"` + `aria-label`; inner `<Button>` gets `tabIndex={-1}`. Keyboard focus now reaches the Tooltip trigger.

## Rebutted

- **EmptyState warn logic for `primaryCTA.disabled`** — Codex suggested warning on invalid `to`/`onClick` combinations even when `disabled` is true. Rebutted: a disabled CTA is a tooltip-surface-only element; `to` and `onClick` are intentionally optional in that branch. Current guard is correct.

## Noted / deferred

- **admin_jobs.go multi-query reads (5 collapsed findings)** — handler uses 5 separate COUNT/time lookups instead of a single read-only transaction. Documented as deliberate trade-off in plan 07-05 SUMMARY; dashboard refetches every 30s so per-query skew is not observable. Re-evaluate if v1.2 adds a strict-consistency requirement for admin reads.

## Full gate status

- `go test ./internal/...` — 32/32 packages pass
- `make lint-typography` — clean
- `make lint-spacing-carveout` — clean
- `make lint-protocol-redaction` — clean
- `cd web && npx vitest run` — 63/63 tests pass
- `cd web && npm run build` — green
- Playwright specs (snippet-copy, dashboard-composition, empty-states) — parse clean (`--list`); full headless run deferred because the pre-existing webServer shell-syntax issue documented in `deferred-items.md` still blocks CI-style runs.

## Phase 7 readiness

Phase 7 closes ship-ready. All real-issues resolved, all tests green, v1.1 merge gate satisfied.

## Artifacts

- `.planning/phases/07-snippet-polish-dashboard-cards-empty-states/07-CODEX-FINDINGS.md`
- `internal/protocol/deb/pool_release.go` (fixed)
- `internal/protocol/deb/pool_release_test.go` (new test)
- `web/src/components/common/EmptyState.tsx` (fixed)
