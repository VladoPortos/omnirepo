# Phase 7 Codex Rescue Findings

**Invoked:** 2026-04-18
**Time-box:** 15 min (returned in ~2 min)
**Scope:** Phase 7 commits `3a4323c..5842a26` + carried 2026-04-17 batch
**Files reviewed:** 35 new/modified (18 Go, 17 frontend)
**Codex invocation:** `Agent(subagent_type="codex:codex-rescue", ...)`

## Findings

| # | File:Line | Severity | Finding | Disposition | Commit |
|---|-----------|----------|---------|-------------|--------|
| 1 | internal/api/admin_jobs.go:74 | minor | Read all summary fields in one statement/read-tx so counts and timestamps snapshot consistently | noted | — |
| 2 | internal/api/admin_jobs.go:85 | minor | (same) | noted | — |
| 3 | internal/api/admin_jobs.go:95 | minor | (same) | noted | — |
| 4 | internal/api/admin_jobs.go:105 | minor | (same) | noted | — |
| 5 | internal/api/admin_jobs.go:118 | minor | (same) | noted | — |
| 6 | internal/protocol/deb/pool_release.go:45 | real-issue | Fall back to reading `dists/<suite>/InRelease` when `Release` is absent and parse its signed header block | fixed | b29fc62 |
| 7 | web/src/components/common/EmptyState.tsx:112 | real-issue | Make the tooltip trigger focusable for the disabled CTA (`tabIndex=0`/ARIA around disabled button) | fixed | 43a1c78 |
| 8 | web/src/components/common/EmptyState.tsx:76 | minor | Warn on invalid `primaryCTA` combinations even when `disabled` is true if `to` and `onClick` are both set or both unset | rebutted | — |

## Dispositions

### Fixed (2)

- **#6 pool_release.go InRelease fallback** (`b29fc62`) — `ResolvePoolPath` now tries `InRelease` after `Release`, strips the PGP clearsign wrapper, and re-parses the inner RFC822 headers. Added `TestResolvePoolPath_InReleaseFallback` with a realistic signed-body fixture.
- **#7 EmptyState disabled-CTA keyboard focus** (`43a1c78`) — added `tabIndex={0}`, `role="button"`, `aria-disabled="true"`, `aria-label` to the wrapper span so keyboard users can tab to it and base-ui's Tooltip fires on focus. Inner `<Button>` gets `tabIndex={-1}` so the wrapper is the single tab stop; `focus-visible` utilities provide the outline.

### Rebutted (1)

- **#8 EmptyState warn on invalid `primaryCTA` when disabled** — the current guard `if (!primaryCTA.disabled && hasTo === hasOnClick)` is intentional. A disabled CTA does not need a `to` or `onClick` — the tooltip surface shows `disabledHint` and the button is non-interactive. Warning on absence of action handlers for a disabled CTA would be a false positive. Keep as-is.

### Noted, not fixed (5)

- **#1–#5 admin_jobs.go multi-query reads** — all five findings are the same observation about five different query lines: the handler executes 5 separate COUNT/time lookups against the reader pool instead of one read-only transaction snapshot. The 07-05 SUMMARY already documents this as a deliberate deviation (the alternative was a single `FILTER (WHERE status=?)` aggregate whose portability under modernc.org/sqlite v1.48.2 was untested). For an admin dashboard poll, the minor per-query skew is not observable — the dashboard refetches every 30s. Logged here; re-evaluate if v1.2 adds a "strict consistency" requirement.

### Noise (0)

None this pass.

## Summary

- **0 blocker** findings
- **2 real-issue** findings — both fixed
- **5 minor** findings (4 collapsed to one root cause) — deferred
- **1 minor** finding — rebutted
- **0 noise** findings

## Verification after fixes

```
go test ./internal/...        — all 32 packages green
make lint-typography          — clean
make lint-spacing-carveout    — clean
make lint-protocol-redaction  — clean
cd web && npx vitest run      — 63/63 passed
cd web && npm run build       — green
```

## Phase 7 readiness

Phase 7 is ready to close. All plan-level acceptance criteria green; Codex-identified real-issues fixed; minor findings documented as deliberate trade-offs. v1.1 ship-ready pending the usual verify/update-roadmap gates.
