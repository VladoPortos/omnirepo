---
phase: 06-error-envelope-visual-foundation
plan: 01
subsystem: api
tags: [openapi, oapi-codegen, error-handling, slog, chi, json, envelope, incident-id]

# Dependency graph
requires:
  - phase: milestone-v1.0
    provides: internal/api hand-written ErrorResponse + writeJSONError helper, chi router with middleware.RequestID, oapi-codegen v2.6.0 pipeline
provides:
  - ApiErrorEnvelope + ApiErrorClass OpenAPI schemas with $ref-able components.responses (ValidationError, PermissionError, NotFoundError, TransientError, OperatorActionRequired)
  - internal/httperr package — Envelope, Error, Class constants, constructors (Validation, ValidationField, ValidationFields, Permission, Transient, OperatorRequired, Internal), Options (WithHint, WithStatus, WithCause, WithDetail), Write helper, IsInternalString, As
  - skip-prune flag on oapi-codegen generate so shared components schemas survive regen even before plan 02 swaps operation $refs
affects: [06-02, 06-03, 06-04, 06-05, 06-06, 06-07, 06-08, phase-07, phase-08, phase-09, phase-10]

# Tech tracking
tech-stack:
  added:
    - log/slog (already in stdlib; first use inside httperr)
    - github.com/go-chi/chi/v5/middleware (consumed inside httperr for GetReqID)
  patterns:
    - "Functional-options error constructors (WithHint/WithStatus/WithCause/WithDetail)"
    - "Class-driven default HTTP status in Write (zero-value Status → switch on Class)"
    - "Panic-on-invalid-code at construction; runtime safe because codes are compile-time constants"
    - "Internal() always emits generic 'An internal error occurred.' and logs cause via slog — never interpolates cause into wire body (ERR-03)"
    - "oapi-codegen skip-prune keeps unreferenced shared schemas in types_gen.go; use when a later plan will add the $refs"

key-files:
  created:
    - internal/httperr/envelope.go
    - internal/httperr/envelope_test.go
    - internal/httperr/write.go
    - internal/httperr/write_test.go
  modified:
    - internal/api/openapi.yaml (added ApiErrorClass, ApiErrorEnvelope, components.responses)
    - internal/api/types_gen.go (regenerated; new ApiErrorEnvelope + ApiErrorClass + ApiErrorEnvelope_Details + 5 response type aliases)
    - internal/api/generate.go (added skip-prune to oapi-codegen directive)

key-decisions:
  - "oapi-codegen -generate types,skip-prune — unreferenced shared schemas must survive regeneration so plan 02 can cut over incrementally without breaking builds"
  - "httperr.Envelope is a hand-written mirror of the generated ApiErrorEnvelope, not an alias — keeps package dependency-free of internal/api and lets httperr live closer to httpx/middleware"
  - "Internal() maps to ClassTransient + HTTP 500 — the client envelope says 'transient' so UI retry logic applies; slog logs the actual cause under the matching incident_id"
  - "Plan's TestWrite_IncidentIDFromChiMiddleware expected an X-Request-Id response header; chi middleware does not echo it. Test corrected to capture the assigned request id via a channel inside the handler and assert envelope body carries the same value. No behavior change in Write."

patterns-established:
  - "httperr.Write(w, r, *Error) is the only way errors leave /api/v1 going forward — plan 02 replaces writeJSONError call sites with this"
  - "Error codes are dotted snake_case, max 80 chars, enforced by regex at construction"
  - "internal_* markers (paths, .go:line, goroutine/runtime., sqlite, sql:/read:/open:/stat:) are what IsInternalString flags"

requirements-completed: [ERR-01, ERR-03, ERR-07]

# Metrics
duration: ~5 min
completed: 2026-04-17
---

# Phase 06 Plan 01: Error Envelope & httperr Package Summary

**Canonical JSON error envelope: `ApiErrorEnvelope` schema in OpenAPI plus an internal/httperr Go package (Envelope/Error/Class + 7 constructors + Write helper) wired to chi request-id and slog, with 24 test functions green and no handler call sites touched.**

## Performance

- **Duration:** ~5 min (wall-clock from first task commit to last task commit)
- **Started:** 2026-04-17T11:15:50Z (approximate, first commit timestamp)
- **Completed:** 2026-04-17T11:21:00Z
- **Tasks:** 3
- **Files created:** 4 (internal/httperr/*.go)
- **Files modified:** 3 (internal/api/openapi.yaml, types_gen.go, generate.go)

## Accomplishments

- Added `ApiErrorClass` + `ApiErrorEnvelope` schemas + 5 reusable `components.responses` to the OpenAPI 3.1 contract (ERR-01, ERR-07).
- Regenerated `types_gen.go` with the new types, including `ApiErrorEnvelope_Details` and alias types for the shared response components — using `skip-prune` so unreferenced shared schemas survive regeneration.
- Built the hand-written `internal/httperr` package:
  - `Envelope` JSON-matches the OpenAPI schema exactly (`omitempty` on hint/incident_id/details).
  - 7 constructors covering validation / permission / transient / operator-required / internal cases.
  - Functional options: `WithHint`, `WithStatus`, `WithCause`, `WithDetail`.
  - `Write(w, r, *Error)` stamps `Envelope.IncidentID` from `chi/middleware.GetReqID`, logs the internal cause via `slog.ErrorContext`, and serializes **only** the envelope (never Cause or Status) — addresses ERR-03 (no internal leakage) and ERR-07 (incident correlation).
  - `IsInternalString` detector for filesystem paths, Go source locations, stack markers, and driver/syscall leaks — available for call-site screening in plan 02.
- 24 test functions green with `-race` (17 in envelope_test.go, 7 in write_test.go). Full module `go build ./...` clean. `go vet ./internal/httperr/...` clean.

## Task Commits

Each task TDD-committed (test RED → feat GREEN):

1. **Task 1: OpenAPI schema + regenerated types** — `3f12b8a` (feat)
2. **Task 2 RED: failing tests for Envelope package** — `26a847b` (test)
2. **Task 2 GREEN: Envelope, Error, Class, constructors** — `141b0f9` (feat)
3. **Task 3 RED: failing tests for Write** — `e03cc2f` (test)
3. **Task 3 GREEN: Write implementation + test correction** — `5df899e` (feat)

**Plan metadata:** (to follow — docs commit with SUMMARY + STATE + ROADMAP)

## Files Created/Modified

- `internal/httperr/envelope.go` (219 lines) — Envelope, Error, Class, CodeIsValid, constructors, options, IsInternalString, As.
- `internal/httperr/envelope_test.go` (445 lines) — 17 test functions: code regex valid/invalid, constructor-panic-on-bad-code, per-class constructor verification, retry-after-ms omit/present, operator route/label, internal cause never leaks, IsInternalString table, JSON marshal/roundtrip, errors.As chain, Options composition, verbatim-message invariant.
- `internal/httperr/write.go` (82 lines) — Write + defaultStatusForClass. Handles nil-error defensively (500 `api.unexpected`).
- `internal/httperr/write_test.go` (202 lines) — 7 test functions: envelope-only body, chi RequestID → incident_id, class-default status table (5 sub-tests), explicit status wins, nil-error 500, Content-Type, empty-context omits incident_id.
- `internal/api/openapi.yaml` — added `ApiErrorClass` + `ApiErrorEnvelope` under `components.schemas`; new `components.responses` with 5 reusable entries.
- `internal/api/types_gen.go` — regenerated; contains `ApiErrorClass`, `ApiErrorEnvelope`, `ApiErrorEnvelope_Details`, and typed aliases `ValidationError`/`PermissionError`/`NotFoundError`/`TransientError`/`OperatorActionRequired`.
- `internal/api/generate.go` — `go:generate` line now `oapi-codegen -generate types,skip-prune -o types_gen.go -package api openapi.yaml`.

## Decisions Made

- **skip-prune on oapi-codegen** — without it, schemas not referenced from any operation's `responses` are pruned from `types_gen.go`. Since plan 02 will add the `$ref`s later, we need the shared `ApiErrorEnvelope` types preserved now. Alternative considered: add throwaway `$ref` on one operation just to keep the schema alive — rejected because it couples task 1 to handler-level edits the plan explicitly defers to plan 02.
- **httperr.Envelope hand-written, not alias of api.ApiErrorEnvelope** — keeps `internal/httperr` free of a dependency on `internal/api` (which imports chi, middleware, and a dozen types). Struct tags and shape are asserted by `TestEnvelope_JSONMarshal` to match the OpenAPI schema; any drift shows up at test time.
- **Internal() is Class=Transient, Status=500** — matches the "opaque server failure, client may retry, but there's also an incident trail" semantics. If a future call site needs 500 without retry guidance, it can use `WithStatus(500)` on a Transient constructor directly.
- **Class-default status for unknown classes → 500** — Write receives `*Error`, not an interface; an unknown class value means either a typo constant or a manual `&Error{}` construction without the constructor. 500 is the safest fallback.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] oapi-codegen pruned unreferenced schemas**
- **Found during:** Task 1 (OpenAPI schema addition)
- **Issue:** After adding `ApiErrorEnvelope` + `ApiErrorClass` to `components.schemas`, regenerated `types_gen.go` did NOT contain them. Root cause: oapi-codegen's default behavior removes schemas no operation references. Plan 01 intentionally does not `$ref` them from any operation (that's plan 02's mechanical sed).
- **Fix:** Added `skip-prune` to the `-generate` flag in `internal/api/generate.go`. Verified `PaginatedList` (another pre-existing unreferenced schema) also now generates.
- **Files modified:** `internal/api/generate.go`
- **Verification:** `grep -c ApiError internal/api/types_gen.go` → 32; `go build ./internal/api/...` clean.
- **Committed in:** `3f12b8a` (Task 1 commit)

**2. [Rule 1 - Bug] Test expected a response header chi doesn't set**
- **Found during:** Task 3 (running Write tests)
- **Issue:** `TestWrite_IncidentIDFromChiMiddleware` asserted `X-Request-Id` on the response from `chi/middleware.RequestID`. Chi's middleware reads that header on inbound requests and writes the id into context but does NOT echo it on the response. Test failed.
- **Fix:** Replaced the header-read with a channel-based capture: the handler reads `chimw.GetReqID(ctx)` and sends it through a channel before calling `Write`. Test then asserts the envelope body `incident_id` equals the captured id. Write's actual behavior is unchanged.
- **Files modified:** `internal/httperr/write_test.go`
- **Verification:** `go test -race ./internal/httperr/... -count=1` green.
- **Committed in:** `5df899e` (Task 3 GREEN commit)

---

**Total deviations:** 2 auto-fixed (1 blocking, 1 bug)
**Impact on plan:** Both fixes necessary for correctness. Plan's `done` criteria for all three tasks remain fully satisfied. No scope creep.

## Issues Encountered

- `go generate` printed a stderr warning about OpenAPI 3.1 partial support in oapi-codegen v2.6.0 (upstream issue #373). Non-fatal — types generated correctly after `skip-prune`. No action; matches the project's established 3.1 spec + v2.x generator combo.

## User Setup Required

None — pure-code plan, no external services, no configuration knobs, no DB migration.

## Next Phase Readiness

- **Plan 02 can start immediately.** Public API of `internal/httperr` is complete:
  - Types: `Envelope`, `Error`, `Class`, 4 Class constants, `Option`.
  - Constructors: `Validation`, `ValidationField`, `ValidationFields`, `Permission`, `Transient`, `OperatorRequired`, `Internal`.
  - Options: `WithHint`, `WithStatus`, `WithCause`, `WithDetail`.
  - Helpers: `Write`, `CodeIsValid`, `IsInternalString`, `As`.
- **Plan 02's job:** replace `writeJSONError(...)` call sites under `internal/api` with `httperr.Write(...)` and swap operation-level `responses` inline schemas to `$ref` the shared components. No type churn.
- **No blockers.**

## Self-Check: PASSED

- `internal/httperr/envelope.go` — FOUND (219 lines)
- `internal/httperr/envelope_test.go` — FOUND (445 lines)
- `internal/httperr/write.go` — FOUND (82 lines)
- `internal/httperr/write_test.go` — FOUND (202 lines)
- `internal/api/openapi.yaml` contains `ApiErrorEnvelope` — FOUND
- `internal/api/types_gen.go` contains `ApiErrorEnvelope` + `ApiErrorClass` — FOUND
- `internal/api/generate.go` contains `skip-prune` — FOUND
- Commit `3f12b8a` (Task 1) — FOUND in `git log`
- Commit `26a847b` (Task 2 RED) — FOUND in `git log`
- Commit `141b0f9` (Task 2 GREEN) — FOUND in `git log`
- Commit `e03cc2f` (Task 3 RED) — FOUND in `git log`
- Commit `5df899e` (Task 3 GREEN) — FOUND in `git log`
- `go test -race ./internal/httperr/... -count=1` — PASS
- `go build ./internal/api/...` — PASS
- `go vet ./internal/httperr/...` — CLEAN
- `go build ./...` — PASS (full module)

## TDD Gate Compliance

Plan frontmatter is `type: execute` (not `type: tdd`), so plan-level RED/GREEN/REFACTOR gate enforcement does not apply. Each individual task carried `tdd="true"`; per-task gate sequence satisfied:

- Task 2: RED `26a847b` (test) → GREEN `141b0f9` (feat) — OK
- Task 3: RED `e03cc2f` (test) → GREEN `5df899e` (feat) — OK
- Task 1 was spec/regen work (no independent RED/GREEN meaningful; verification via generated artefacts + compile).

No refactor commits needed; initial GREEN implementations are already minimal and clean.

---
*Phase: 06-error-envelope-visual-foundation*
*Completed: 2026-04-17*
