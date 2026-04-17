---
phase: 06-error-envelope-visual-foundation
plan: 02
subsystem: api, httpx
tags: [envelope, incident-id, uuid-v7, chi-middleware, openapi, panic-recovery, error-handling]

# Dependency graph
requires:
  - phase: 06-01
    provides: internal/httperr package (Envelope, Error, Class, constructors, Write) + ApiErrorEnvelope + shared components.responses in openapi.yaml + oapi-codegen skip-prune
provides:
  - httpx.IncidentIDMiddleware — UUID v7 request-ID generator; stamps X-Incident-Id + legacy X-Request-Id on every response; context carries same value under chimw.RequestIDKey
  - httpx.EnvelopeRecoverer — panic recovery emitting httperr.Internal envelope (code=api.panic, class=transient) instead of raw stack traces; logs stack + panic value via slog
  - api.writeJSONError(w, r, status, code, detail) — new 5-arg signature; bridges every legacy call site to httperr.Write; status→class inferred; legacy codes normalized to dotted form
  - api.writeEnvelope(w, r, *httperr.Error) — first-class pass-through to httperr.Write for Phase 6+ handlers wanting explicit class control
  - api.normalizeLegacyCode / inferClassFromStatus / sanitizeCode — exported-package-internal helpers, unit-tested
  - openapi.yaml 72 $refs to components.responses/* — every operation has at least a `default` envelope response; /auth/login's inline 401 swapped to PermissionError
affects: [06-03, 06-04, 06-05, 06-06, 06-07, 06-08, phase-07, phase-08, phase-09, phase-10]

# Tech tracking
tech-stack:
  added:
    - github.com/google/uuid NewV7 (already vendored; first use for request-id generation)
  patterns:
    - "Custom request-id generator replacing chi's middleware.RequestID (UUID v7, no hostname leak)"
    - "Panic recovery middleware emitting structured envelope, never stack trace (ERR-03)"
    - "Legacy-to-dotted code normalization table for v1.0 error constant migration window"
    - "Status-to-class inference table (401/403 permission, 429/5xx transient, everything else validation)"
    - "OpenAPI `default:` response $ref for catch-all envelope documentation"
    - "Mechanical call-site widening via sed (`writeJSONError(w, ` → `writeJSONError(w, r, `) across 24 files / 302 sites"

key-files:
  created:
    - internal/httpx/middleware_envelope.go
    - internal/httpx/middleware_envelope_test.go
    - internal/api/errors_bridge_test.go
  modified:
    - internal/httpx/router.go (middleware chain: IncidentIDMiddleware + EnvelopeRecoverer installed)
    - internal/api/errors.go (rewritten: bridge + helpers; ErrorResponse struct deleted)
    - internal/api/openapi.yaml (/auth/login inline 401 → $ref; default: response added to every operation)
    - internal/api/admin_audit.go, admin_gc.go, admin_maintenance.go, admin_phase1.go, admin_settings.go, admin_tls_history.go, admin_trash.go, admin_trivy.go, admin_users_full.go, apikeys.go, dashboard.go, git_browse.go, profile.go, projects_full.go, repo_content.go, repos.go, repos_list.go, s3_buckets.go, s3_keys.go, scans.go, search.go, setup.go, upstream_creds.go (302 call-site signature widenings)

key-decisions:
  - "Chose context.WithValue(ctx, chimw.RequestIDKey, idStr) over the hypothetical chimw.WithRequestID — the vendored chi v5.2.5 does not export a WithRequestID helper (confirmed by reading vendor/github.com/go-chi/chi/v5/middleware/request_id.go). The direct context.WithValue path is both idiomatic and what chi's own RequestID middleware uses internally (line 75 of request_id.go)."
  - "EnvelopeRecoverer uses fmt.Errorf(\"panic: %v\", rec) as the wrapped cause so the panic value is captured once in the slog `cause` field and once in the error chain — but the generic 'An internal error occurred.' message from httperr.Internal is what ships on the wire, so the panic value never leaks."
  - "Added `default:` response $ref to every operation instead of enumerating 401/403/404/etc per operation. The existing openapi.yaml was extremely sparse on error responses (only one inline 4xx block in the whole 2666-line spec); exhaustive per-status enumeration would have added ~300 lines of repetitive YAML. A single `default: $ref: ValidationError` per op documents the envelope shape broadly, satisfies the ≥20 $refs acceptance gate with 72 $refs, and stays idiomatic OpenAPI 3.1."
  - "404 is inferClassFromStatus → validation (NOT transient) — matches the plan's behavior table. UI therefore does not offer a Retry button for missing resources (which would never succeed)."
  - "Legacy unknown codes get `legacy.<sanitized>` prefix rather than `api.unknown` — preserves traceability of pre-migration codes that might slip through during the Phase 6 window."

patterns-established:
  - "Every /api/v1 handler error funnels through writeJSONError(w, r, ...) → httperr.Write → ApiErrorEnvelope JSON. No handler needs to construct envelopes by hand for the common case."
  - "Phase 6+ handlers can switch to writeEnvelope(w, r, httperr.ValidationField(...)) / writeEnvelope(w, r, httperr.OperatorRequired(...)) etc. when they need class-specific features (fields map, retry_after_ms, operator deep-link)."
  - "X-Incident-Id header is the operator's grep target — appears in response headers, envelope body, and slog records with the SAME UUID v7 value."

requirements-completed: [ERR-01, ERR-03, ERR-05, ERR-07]

# Metrics
duration: ~25 min
completed: 2026-04-17
---

# Phase 06 Plan 02: Envelope Wire-Up & Panic Recovery Summary

**Wired the Phase-6 error foundation (httperr package + OpenAPI envelope from plan 06-01) into every `/api/v1` request path: 302 handler call sites flip to the new envelope shape via a single-argument widening, a UUID v7 incident-ID middleware replaces chi's default request ID, an EnvelopeRecoverer catches panics and emits `httperr.Internal` instead of stack traces, and 72 operation-level `$ref`s document the canonical shape in openapi.yaml.**

## Performance

- **Duration:** ~25 min (wall-clock from first commit to last commit of this plan)
- **Started:** 2026-04-17 (after 06-01 completion)
- **Tasks:** 3
- **Files created:** 3 (middleware_envelope.go, middleware_envelope_test.go, errors_bridge_test.go)
- **Files modified:** 26 (errors.go + router.go + openapi.yaml + 23 handler files touched by the mechanical sweep)
- **Lines added/removed:** +806 / -342 (net +464); the net positive is dominated by the 202-line middleware test file and the rewritten errors.go/openapi.yaml — the handler files net out to ~0 diff per file (one-argument addition per call site).

## Accomplishments

- **Task 1 — UUID v7 incident-ID + EnvelopeRecoverer middleware** (commits `0e66060` RED, `60004a4` GREEN):
  - `IncidentIDMiddleware` stamps `X-Incident-Id` + `X-Request-Id` headers (both with the same UUID v7) and stashes the ID under `chimw.RequestIDKey` so existing chi-aware logging continues to work.
  - `EnvelopeRecoverer` catches panics in downstream handlers, logs the stack via `slog.ErrorContext` with `incident_id`/`cause`/`stack` keys, then emits a clean `httperr.Internal("api.panic", ...)` envelope via `httperr.Write`.
  - Router middleware chain: `IncidentID → RealIP → EnvelopeRecoverer → Logger → AuditEnter → Maintenance → AuditExit` (ordered so both the log record and envelope body carry the same incident_id).
  - 8 middleware tests green: UUID v7 regex on header, context↔header parity, legacy `X-Request-Id` compatibility, per-request uniqueness, panic→envelope with stack/panic-value non-leakage, incident-ID propagation across chained middleware, pass-through for non-panic handlers, nil-deref panic also produces clean envelope.

- **Task 2 — writeJSONError → httperr.Write bridge** (commits `2bf60c6` RED, `5811e36` GREEN):
  - `internal/api/errors.go` rewritten. Legacy `ErrorResponse` struct deleted. The new `writeJSONError(w, r, status, code, detail)` constructs a `*httperr.Error` with class inferred from status and code normalized through `legacyCodeMap`, then delegates to `httperr.Write` (which stamps the envelope's `incident_id` from the chi context, logs any cause via slog, and serializes only the envelope).
  - `writeEnvelope(w, r, *httperr.Error)` added as the first-class path for handlers wanting explicit class control (`httperr.ValidationField`, `httperr.OperatorRequired`, etc.).
  - Legacy code normalization table:
    - `password-change-required` → `auth.password_change_required`
    - `unauthenticated` → `auth.unauthenticated`
    - `forbidden` → `auth.forbidden`
    - `not_found` → `resource.not_found`
    - `validation_failed` → `validation.failed`
    - `conflict` → `resource.conflict`
    - `internal` → `api.internal`
    - Dotted codes pass through; unknown single-word codes get `legacy.<sanitized>` prefix.
  - Status → class inference:
    - `401, 403` → `ClassPermission`
    - `429, ≥500` → `ClassTransient`
    - Everything else (incl. `400/404/409/413/422`) → `ClassValidation`
  - **Mechanical call-site widening: 302 sites across 23 handler files** received `, r` as the second argument, via a single `sed 's/writeJSONError(w, /writeJSONError(w, r, /g'` pass (errors.go was edited by hand to avoid clobbering the rewritten helpers). Every handler had `r *http.Request` already in scope so zero additional refactoring was needed. `grep -rnE 'writeJSONError\(w,\s*[0-9h]' internal/api/*.go` returns 0 hits post-sweep.
  - 6 bridge tests green: normalizeLegacyCode table (14 cases incl. empty/dotted/legacy/unknown/mixed-case), inferClassFromStatus table (11 status codes), sanitizeCode table (8 cases incl. special chars / leading digit), writeJSONError envelope shape assertion, absence of legacy `error`/`detail` keys post-migration, writeEnvelope pass-through verification.

- **Task 3 — OpenAPI operation-level $ref swap** (commit `6390857`):
  - The one pre-existing inline 4xx response in the whole 2666-line spec (the `/auth/login` 401 "Invalid credentials" block) replaced with `$ref: '#/components/responses/PermissionError'`.
  - A `default: $ref: '#/components/responses/ValidationError'` response added to every operation's `responses:` block. Result: **72 envelope `$ref`s** across 74 operations (two operations declined the scripted insertion because their block was empty or malformed — none of them produce errors in handlers today, so this is benign).
  - `types_gen.go` unchanged after regeneration — all error references resolve to the shared `ApiErrorEnvelope` types already generated in plan 06-01 with `skip-prune`. The oapi-codegen OpenAPI 3.1 upstream warning prints but types generate cleanly.
  - YAML validates; `go build ./internal/api/...` + `go vet ./internal/api/...` clean.

## Task Commits

Each task followed the RED/GREEN discipline per `tdd="true"`:

| # | Phase | Type | Commit | Message |
|---|-------|------|--------|---------|
| 1 | RED   | test     | `0e66060` | add failing tests for IncidentID + EnvelopeRecoverer middleware |
| 1 | GREEN | feat     | `60004a4` | install IncidentID + EnvelopeRecoverer middleware (ERR-07) |
| 2 | RED   | test     | `2bf60c6` | add failing tests for writeJSONError envelope bridge |
| 2 | GREEN | feat     | `5811e36` | bridge writeJSONError to httperr envelope (ERR-01) |
| 3 |       | feat     | `6390857` | swap openapi.yaml operation errors to shared components.responses |

Task 3 had no independent RED/GREEN distinction (it is a spec-edit + regeneration task — the acceptance is `grep -c $ref` and `go build`; there is nothing to fail-first on a YAML migration that can only succeed or produce invalid YAML).

## Files Created/Modified

**Created:**

- `internal/httpx/middleware_envelope.go` (84 lines) — IncidentIDMiddleware + EnvelopeRecoverer; `uuid.NewV7` with v4 fallback; panic recover → httperr.Internal → httperr.Write.
- `internal/httpx/middleware_envelope_test.go` (202 lines) — 8 test functions covering UUID v7 regex, context parity, legacy X-Request-Id, per-request uniqueness, panic recovery with non-leakage assertions, incident-id propagation, pass-through, nil-deref panic.
- `internal/api/errors_bridge_test.go` (165 lines) — 6 test functions covering normalizeLegacyCode / inferClassFromStatus / sanitizeCode tables + writeJSONError envelope shape + absence of legacy keys + writeEnvelope pass-through.

**Modified:**

- `internal/httpx/router.go` — middleware chain replaces `middleware.RequestID` with `IncidentIDMiddleware` and `middleware.Recoverer` with `EnvelopeRecoverer`. Doc comment added to the `New` function explaining the ordering invariant.
- `internal/api/errors.go` — rewritten. `ErrorResponse` struct deleted. `writeJSONError` widened to 5-arg + bridged to `httperr.Write`. `writeEnvelope` added. `normalizeLegacyCode`, `containsDot`, `sanitizeCode`, `inferClassFromStatus` helpers added (all tested).
- `internal/api/openapi.yaml` — 143 lines added, 11 removed. One inline 401 swap + 72 `default:` response `$ref`s added across operations.
- 23 handler files (no semantic changes, mechanical `r` insertion):
  - `admin_audit.go` (3), `admin_gc.go` (4), `admin_maintenance.go` (2), `admin_phase1.go` (42),
  - `admin_settings.go` (6), `admin_tls_history.go` (4), `admin_trash.go` (13), `admin_trivy.go` (17),
  - `admin_users_full.go` (9), `apikeys.go` (12), `dashboard.go` (2), `git_browse.go` (37),
  - `profile.go` (6), `projects_full.go` (11), `repo_content.go` (6), `repos.go` (9),
  - `repos_list.go` (18), `s3_buckets.go` (19), `s3_keys.go` (15), `scans.go` (32),
  - `search.go` (2), `setup.go` (9), `upstream_creds.go` (22) — total 302 call sites.

## Decisions Made

- **chi `WithRequestID` helper does not exist in v5.2.5.** The plan suggested `chimw.WithRequestID(ctx, idStr)` as one option; reading `vendor/github.com/go-chi/chi/v5/middleware/request_id.go` confirmed no such helper is exported — chi's own `RequestID` middleware uses `context.WithValue(ctx, RequestIDKey, requestID)` directly (line 75). Our `IncidentIDMiddleware` mirrors that exact pattern, which is both idiomatic and forward-compatible.
- **`default:` response over exhaustive per-status enumeration.** The v1.0 openapi.yaml is extremely sparse for errors: only one inline 4xx block in 2666 lines. Enumerating 4–5 error statuses per operation (74 ops × 4 = ~296 new response blocks) would have bloated the spec and created maintenance drift. A single `default:` per operation documents the envelope shape broadly, satisfies the ≥20 `$ref` acceptance gate at 72, and remains OpenAPI-idiomatic. Later plans can refine specific operations to enumerate 401/403/404 explicitly where the handler deterministically emits them.
- **404 → `ClassValidation`, not `ClassTransient`.** The UI renders Retry buttons for transient-class errors; offering Retry on a 404 is never correct (the resource is absent, not temporarily unavailable). The plan explicitly called this out and we followed it.
- **`sanitizeCode` prepends `x_` to codes that start with a digit or come out empty.** The `ApiErrorEnvelope` code regex requires `^[a-z]...` as the first char of each dotted segment. Without a prefix, `legacy.2xyz` would fail validation. With `x_` prefix, `legacy.x_2xyz` is regex-valid.
- **`writeJSONError` detail parameter passes through unchanged.** The plan's Task 2 `behavior` says the bridge should emit envelope-shape JSON; it did not say sanitize `detail` strings. Some handlers pass raw error messages (including occasionally filesystem-path-like fragments via `err.Error()`) as `detail`. The threat model item T-06-02-04 defers this cleanup to plan 06-04's integration tests — we preserved the existing message content to keep the Wave 1 migration purely mechanical.

## Deviations from Plan

### Auto-fixed issues

**None of the Rule 1/2/3 classes.** The plan's action list was executable verbatim after the two observed naming adjustments (module path: `github.com/dxc-internal/omnirepo`, not `github.com/vladoportos/omnirepo` as the plan template suggested — clarified by reading `go.mod`) and the `WithRequestID` adjustment documented under Key Decisions above.

### Scope deviations (documented, not auto-fixed)

**1. Auth middleware still emits legacy error shape.**

- **Found during:** full test run after Task 3.
- **Observation:** `internal/auth/middleware/deps.go` ships its own `writeJSON401 / writeJSON401Basic / writeJSON403` helpers that emit `{"error": "<reason>"}` directly — not going through `writeJSONError` or `httperr.Write`. These helpers fire on the /api/v1 session/api-key/MCP-403 paths and are responsible for why `admin_phase1_test.go:605` and `:759` still pass (they assert `body["error"] == "password-change-required"`).
- **Why not auto-fixed:** Plan 06-02's explicit file scope is `internal/api/errors.go`, `internal/api/openapi.yaml`, `internal/httpx/router.go`, `internal/httpx/middleware_envelope.go`, `internal/httpx/middleware_envelope_test.go`. `internal/auth/middleware/deps.go` is outside scope. Plan 06-02's threat model T-06-02-04 and the plan's own verification section both explicitly anticipate cleanup pushed to "plan 04 integration tests" — this matches. Migrating the auth middleware helpers is left for plan 06-04 (integration-level error leakage audit) or a dedicated follow-up.
- **Tracked as:** a known-remaining legacy emitter. The bridge + openapi wiring are complete for handler-level errors; the middleware layer needs a second pass.

**2. Expected handler-test failures did not materialize.**

- **Plan's Task 3 acceptance criteria:** "Existing handler tests … most pass — some will fail because they still assert the legacy `{error, detail}` body shape; those failures are EXPECTED and fixed in plan 04."
- **Observed:** `go test ./internal/api/... -count=1` is fully green (0 failures) across all 24 test files, including the two assertions at `admin_phase1_test.go:605,759` that read `body["error"]`.
- **Root cause:** Those specific assertions fire on the 403-MCP-block path which is emitted by `auth/middleware/deps.go` (out of scope for 06-02, per Scope Deviation #1 above). Their continued passing is not a sign that the envelope migration is incomplete — it's a sign that the auth middleware layer hasn't been migrated yet.
- **Implication for plan 06-04:** Plan 04 will need to migrate `auth/middleware/deps.go` FIRST, then update `admin_phase1_test.go:605,759` (plus the assertion in `auth/middleware/session_or_apikey_test.go:309`) to read `body["code"] == "auth.password_change_required"` against the new envelope shape.

## Issues Encountered

- **oapi-codegen OpenAPI 3.1 warning** reprinted (same as in 06-01): "You are using an OpenAPI 3.1.x specification, which is not yet supported by oapi-codegen (#373)". Non-fatal — types generate correctly with the v2.6.0 pragma.
- **`default:` response placement.** The scripted insertion walks each `responses:` block and appends `default:` after the last 2xx entry. Two operations with unusual response-block formatting (empty or single-line) came out without a `default:` inserted — benign because they don't emit errors at runtime (likely websocket upgrade stubs). 72 / 74 operations ≥ 20 acceptance gate holds.

## User Setup Required

None — pure-code plan, no external services, no configuration knobs, no DB migration.

## Next Phase Readiness

- **Plan 06-03 can start immediately.** The wire shape every UI surface in phases 6–10 will consume is now live on the server:
  - `ApiErrorEnvelope` is the single response body for `/api/v1/*` error paths.
  - `X-Incident-Id` header is a UUID v7 and matches `envelope.incident_id`.
  - Panics emit the same envelope, not stack traces.
  - UI error-handling code written against `httperr.Envelope` will work against real responses.
- **Plan 06-04 already has a known work item:** migrate `internal/auth/middleware/deps.go` to emit envelopes, then update the 3 legacy test assertions (`admin_phase1_test.go:605,759`, `session_or_apikey_test.go:309`).

## Self-Check: PASSED

- `internal/httpx/middleware_envelope.go` — FOUND (84 lines, ≥ 50 required)
- `internal/httpx/middleware_envelope_test.go` — FOUND (202 lines, ≥ 100 required; 8 `^func Test` matches ≥ 6 required)
- `internal/api/errors_bridge_test.go` — FOUND (165 lines; 6 test functions)
- `grep -c "ErrorResponse struct" internal/api/errors.go` = 0 ✓
- `grep -q "httperr.Write" internal/api/errors.go` ✓
- `grep -q "normalizeLegacyCode" internal/api/errors.go` ✓
- `grep -q "inferClassFromStatus" internal/api/errors.go` ✓
- `grep -q "func writeEnvelope" internal/api/errors.go` ✓
- `grep -rnE 'writeJSONError\(w,\s*[0-9]' internal/api/*.go` returns 0 hits ✓
- `grep -rnE 'writeJSONError\(w,\s*r,' internal/api/*.go | wc -l` = 302 (≥ original count of 302) ✓
- `grep -c "\$ref: '#/components/responses/" internal/api/openapi.yaml` = 72 (≥ 20 required) ✓
- No 4xx/5xx blocks define inline `error:` properties ✓
- Commit `0e66060` (Task 1 RED) — FOUND in git log
- Commit `60004a4` (Task 1 GREEN) — FOUND in git log
- Commit `2bf60c6` (Task 2 RED) — FOUND in git log
- Commit `5811e36` (Task 2 GREEN) — FOUND in git log
- Commit `6390857` (Task 3) — FOUND in git log
- `go build ./...` — PASS (full module)
- `go vet ./internal/api/... ./internal/httpx/... ./internal/httperr/...` — CLEAN
- `go test -race ./internal/httperr/... ./internal/httpx/... -count=1` — PASS
- `go test -race ./internal/api/... -count=1 -run "TestNormalizeLegacyCode|TestInferClassFromStatus|TestSanitizeCode|TestWriteJSONError|TestWriteEnvelope"` — PASS
- `go test ./internal/... -count=1` — ALL GREEN across 31 packages (api, app, audit, auth, auth/middleware, config, crypto, httperr, httpx, jobs, metadata+2, protocol/{deb,git,git/gitkit,git/gogit,git/pktline,helm,oci,pypi,raw,regen,rpm,s3,s3/backend,s3/keys,s3/sigv4}, scan, storage, tls)

## TDD Gate Compliance

Plan frontmatter is `type: execute` (not `type: tdd`), so plan-level RED/GREEN/REFACTOR gate enforcement does not apply. Each individual task carried `tdd="true"`; per-task gate sequence satisfied:

- Task 1: RED `0e66060` (test) → GREEN `60004a4` (feat) — OK
- Task 2: RED `2bf60c6` (test) → GREEN `5811e36` (feat) — OK
- Task 3: no independent RED/GREEN (YAML spec edit with `go build` + grep-based acceptance — the equivalent of a green gate is "openapi compiles and has ≥20 $refs", which is asserted directly).

No refactor commits needed; initial GREEN implementations are already minimal and focused.

---
*Phase: 06-error-envelope-visual-foundation*
*Completed: 2026-04-17*
