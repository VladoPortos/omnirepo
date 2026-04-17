---
phase: 06-error-envelope-visual-foundation
plan: 04
subsystem: api, httpx, auth/middleware, web/e2e
tags: [testing, envelope, playwright, integration-tests, err-01, err-02, err-03, err-04, err-05, err-06, err-07, incident-id]

# Dependency graph
requires:
  - phase: 06-01
    provides: httperr.Envelope + httperr.Write + httperr.IsInternalString + ApiErrorEnvelope schema
  - phase: 06-02
    provides: writeJSONError(w,r,status,code,detail) bridge + IncidentIDMiddleware + EnvelopeRecoverer + 72 openapi $refs
  - phase: 06-03
    provides: ApiError envelope migration + useApiError hook + ErrorEnvelopeRenderer + /_dev/error-class-story page + /api/v1/_dev/error/:class canned routes
provides:
  - internal/api/errors_envelope_test.go — 6 unit tests pinning the normalize/sanitize/class-inference surface + regex/leakage gates
  - internal/api/handlers_envelope_integration_test.go — 14 integration tests forcing every class through real handlers, asserting envelope invariants + incident-id parity + no-leakage
  - web/e2e/error-envelope.spec.ts — 9 Playwright scenarios driving the dev story page in inline + page + live modes, covering countdown / operator deep-link / incident chip
  - VITE_OMNIREPO_DEV build flag — opt-in SPA inclusion of /_dev/error-class-story for Playwright (T-06-03-04 tree-shake invariant preserved for regular prod builds)
  - OMNIREPO_DEV_PROXY=0 runtime flag — embedded SPA + backend dev routes without the Vite reverse proxy (lets e2e run against a standalone Go binary)
  - Envelope-shape migration of internal/auth/middleware/deps.go (writeJSON401/writeJSON403 → httperr.Write with Reason→code + static user-facing messages)
  - Envelope-shape migration of internal/httpx/spa.go writeAPINotFound + internal/httpx/middleware_maintenance.go MaintenanceMode (replaces the last two legacy {"error":...} emitters on /api/v1)
  - writeJSONError default-message fallback: every empty detail now ships a static developer-authored sentence so the wire envelope always has a non-empty message (OpenAPI schema gate)
  - Tightened normalizeLegacyCode: passthrough now requires the full wire regex match (^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$) — "errors.go:123" / "/home/…" / multi-dot inputs are redirected through the sanitize path instead of leaking verbatim
affects: [06-05, 06-06, 06-07, 06-08, phase-07, phase-08, phase-09, phase-10]

# Tech tracking
tech-stack:
  added:
    - Playwright @playwright/test 1.56+ (already in devDeps; first use for error-envelope coverage)
  patterns:
    - "Integration-test helper assertEnvelope(t, body) running universal ERR-01 / ERR-03 / ERR-07 invariants — code regex, class enum, no internal-marker substrings — in one place so per-class tests only assert the class-specific extras"
    - "Dev-surface env-var split: OMNIREPO_DEV=1 (backend canned routes) and OMNIREPO_DEV_PROXY=0 (keep embedded SPA) are independent so e2e gets dev routes without Vite"
    - "Build-time dev-route gate (VITE_OMNIREPO_DEV=true) as a second opt-in alongside import.meta.env.DEV — Playwright suite builds with the flag, regular prod builds do not, tree-shake invariant from 06-03 preserved"
    - "writeJSONError default-message-per-status fallback pattern — callers pass \"\" when they want the server to pick a safe class-appropriate sentence, never when they want to emit a real user-facing string. Avoids the 100+ individual per-call-site edits that would be required to satisfy the schema's required-non-empty message field"
    - "Policy-reason → dotted code mapper (reasonCode + reasonMessage in auth/middleware/deps.go) — single lookup point for every auth denial; unknown reason falls through to auth.forbidden so wire shape stays schema-valid"

key-files:
  created:
    - internal/api/errors_envelope_test.go
    - internal/api/handlers_envelope_integration_test.go
    - web/e2e/error-envelope.spec.ts
  modified:
    - internal/api/errors.go (codeShapeRegex passthrough gate, sanitizeCode leading-non-letter x_ prefix, writeJSONError default-message fallback, defaultMessageForStatus helper)
    - internal/api/errors_bridge_test.go ("some.dotted.code" expectation updated; multi-dot codes no longer pass through)
    - internal/api/admin_phase1.go (login + change-password empty-detail call sites now pass a static user-facing sentence)
    - internal/api/admin_phase1_test.go (newTestServer installs httpx.IncidentIDMiddleware + calls api.MountDevErrorRoutes; doRaw helper added; 2 legacy body["error"] assertions flipped to body["code"] + body["class"])
    - internal/auth/middleware/deps.go (writeJSON401 / writeJSON401Basic / writeJSON403 → httperr.Write; reasonCode + reasonMessage mappers; RequireCanWith threads r through to writers)
    - internal/auth/middleware/basic_or_apikey.go (writeJSON401Basic call sites widened to (w, r))
    - internal/auth/middleware/session_or_apikey.go (writeJSON401 call sites widened to (w, r))
    - internal/auth/middleware/session_or_apikey_test.go (legacy password-change-required substring assertion → envelope code + class assertion)
    - internal/httpx/spa.go (writeAPINotFound → httperr.Write; validation class for 404)
    - internal/httpx/spa_test.go (legacy "error":"not_found" assertion → envelope code + class)
    - internal/httpx/middleware_maintenance.go (MaintenanceMode → httperr.OperatorRequired with deep link to /admin/maintenance)
    - internal/httpx/devproxy.go (OMNIREPO_DEV_PROXY=0 escape hatch on IsDevMode)
    - web/src/App.tsx (DEV_ROUTES_ENABLED unions import.meta.env.DEV with VITE_OMNIREPO_DEV build flag)
    - web/playwright.config.ts (webServer command + env pass VITE_OMNIREPO_DEV=true + OMNIREPO_DEV=1 + OMNIREPO_DEV_PROXY=0)
    - web/.gitignore (test-results/, playwright-report/, blob-report/)

key-decisions:
  - "Migrated internal/auth/middleware/deps.go in plan 04 (the deferred-from-02 item) instead of leaving it for a separate plan. Rationale: the plan's explicit must-have requires the legacy {error, detail} body shape tests to flip to envelope assertions, and three of those assertions (admin_phase1_test.go:605,759 + session_or_apikey_test.go:309) fire on paths emitted by auth middleware. Migrating deps.go here kept the scope aligned with the tests this plan was required to fix."
  - "Did the same for internal/httpx/spa.go writeAPINotFound and internal/httpx/middleware_maintenance.go MaintenanceMode — both were still emitting legacy {\"error\":\"...\"} on /api/v1 responses. Cleaning them up in plan 04 means ZERO legacy-shape emitters remain on the /api/v1 surface; the integration-test sweep TestEnvelope_NoInternalLeakage_AcrossHandlers is now a meaningful gate."
  - "Added writeJSONError default-message-per-status fallback instead of editing ~100 individual call sites to supply a non-empty detail. The schema's ApiErrorEnvelope.message is required and non-empty, but the v1.0 convention of passing \"\" for 500 paths is intentional (don't leak raw err.Error() to wire). Static per-status default is the bridge between the two."
  - "Split OMNIREPO_DEV (backend dev routes) from OMNIREPO_DEV_PROXY (Vite reverse proxy) so the Playwright suite can run against a real production binary serving the embedded SPA. Without this split, enabling dev routes forced the dev proxy, which Playwright can't satisfy without a parallel Vite process."
  - "Introduced VITE_OMNIREPO_DEV as a build-time opt-in. Vite only sees import.meta.env.DEV when running in dev mode, so the existing guard excludes the story page from every production build. Adding a custom flag that Playwright sets at build time keeps the tree-shake invariant for regular production builds while enabling the story-page route for the e2e run."
  - "Tightened normalizeLegacyCode to require the full wire regex for passthrough, not just 'contains a dot'. The old behavior would let 'errors.go:123' or '/home/omnirepo/foo.db' pass through verbatim and ship as the envelope code — violating both the OpenAPI schema regex and ERR-03's no-leakage invariant. New behavior redirects those through the sanitize path, producing e.g. 'legacy.errorsgo123'."

patterns-established:
  - "Every /api/v1 error response now emits the canonical ApiErrorEnvelope regardless of which middleware or handler fired it. Integration tests enumerate representative failure paths and run assertEnvelope(body) on each; any future regression that emits a legacy shape fails TestEnvelope_NoInternalLeakage_AcrossHandlers."
  - "Playwright scenarios drive the dev story page with data-story-class / data-story-mode selectors (from plan 06-03). Live-wire tests prove the end-to-end server → client wire-parse pipeline works for every class, not just canned fixtures."
  - "Auth-middleware Reason tokens are mapped to dotted envelope codes in one place (reasonCode) so future auth additions just extend the switch; no new call sites to audit."
  - "Countdown e2e test uses a 5-second timeout for a 3-second retry_after_ms countdown — generous enough to tolerate CI scheduling jitter. If flakes appear, widen the timeout; don't reduce retry_after_ms (the 3s value is a UI-SPEC spec, not a test knob)."

requirements-completed: [ERR-01, ERR-02, ERR-03, ERR-04, ERR-05, ERR-06, ERR-07]

# Metrics
duration: ~25 min
completed: 2026-04-17
---

# Phase 06 Plan 04: Integration & E2E Tests Summary

**Shipped the Wave 1 test suite: 20 Go tests (6 unit + 14 integration) pinning every ApiErrorClass through real /api/v1 handlers, 9 Playwright scenarios driving the dev story page in inline + page + live modes, and migrated the last three legacy {"error": ...} emitters (auth middleware, SPA 404, maintenance middleware) so ZERO legacy-shape emitters remain on /api/v1.**

## Performance

- **Duration:** ~25 min (wall-clock from first commit to docs commit)
- **Started:** 2026-04-17T14:00Z (after 06-03 completion + reading task)
- **Tasks:** 2 (per-task TDD; Task 1 RED→GREEN, Task 2 direct GREEN with manual Playwright verification)
- **Files created:** 3 (errors_envelope_test.go, handlers_envelope_integration_test.go, error-envelope.spec.ts)
- **Files modified:** 14 (errors.go, errors_bridge_test.go, admin_phase1.go, admin_phase1_test.go, auth/middleware/deps.go, auth/middleware/basic_or_apikey.go, auth/middleware/session_or_apikey.go, auth/middleware/session_or_apikey_test.go, httpx/spa.go, httpx/spa_test.go, httpx/middleware_maintenance.go, httpx/devproxy.go, web/src/App.tsx, web/playwright.config.ts + web/.gitignore)
- **Go test functions added:** 20 (6 in errors_envelope_test.go, 14 in handlers_envelope_integration_test.go)
- **Playwright scenarios added:** 9

## Accomplishments

- **Task 1 — Go unit + integration tests + envelope-bridge tightening** (commits `a2c25d4`, `456a31d`, `438cb99`):
  - `TestNormalizedCodesPassRegex` + `TestNormalizeLegacyCode_Table` + `TestInferClassFromStatus_Table` + `TestSanitizeCode_LocalSegmentRegex` + `TestNormalizeLegacyCode_NeverLeaksShapeInternals` + `TestContainsDot` — 6 unit tests in errors_envelope_test.go covering the bridge surface.
  - `assertEnvelope(t, body []byte) envelope` — universal invariant helper: asserts code matches wire regex, class is one of the 4 known values, message/hint don't trip `httperr.IsInternalString`, and body is free of path / source-location / stack / sqlite driver substrings.
  - 14 integration tests in handlers_envelope_integration_test.go: validation class via change-password empty body + malformed JSON; permission class via login unknown-user + admin-without-admin + no-cookie; not-found via missing project; incident-id parity with X-Incident-Id header; dev-route classes with details.retry_after_ms / operator_route / operator_label / fields map; no-internal-leakage sweep across 5 representative failure paths; never-echoes-user-input (`<script>` tag test); Content-Type is application/json; transient class retry_after_ms non-empty; operator class route is same-origin (T-06-03-03); belt-and-braces `api.Mount` regression guard.
  - Tightened `internal/api/errors.go`:
    - `codeShapeRegex` gate on passthrough (multi-dot / paths / source locations / stack markers no longer leak).
    - `sanitizeCode` x_ prefix now fires on any leading non-letter (was only on leading digits + empty).
    - `writeJSONError` default-message-per-status fallback covers the ~100 empty-detail call sites on 500 paths.
  - Migrated `internal/auth/middleware/deps.go` writeJSON401 / writeJSON401Basic / writeJSON403 to `httperr.Write`, with `reasonCode` + `reasonMessage` mappers that convert `auth.Reason*` tokens to dotted envelope codes with developer-authored static messages.
  - Migrated `internal/httpx/spa.go` `writeAPINotFound` and `internal/httpx/middleware_maintenance.go` `MaintenanceMode` — the last two legacy-shape emitters on /api/v1.
  - Updated 3 legacy-asserting tests (plan 02's deferred items):
    - `internal/api/admin_phase1_test.go:605,759` — `body["error"] == "password-change-required"` → `body["code"] == "auth.password_change_required"` + `body["class"] == "permission"`.
    - `internal/auth/middleware/session_or_apikey_test.go:309` — substring `"password-change-required"` → `"auth.password_change_required"` + `"class":"permission"`.
    - `internal/httpx/spa_test.go:100` — `"error":"not_found"` → `"code":"resource.not_found"` + `"class":"validation"`.
  - `newTestServer` installs `httpx.IncidentIDMiddleware` + calls `api.MountDevErrorRoutes` so integration tests get incident-id header/envelope parity and dev canned-routes when OMNIREPO_DEV=1.
  - Added `(*testServer).doRaw` helper returning raw body bytes for assertEnvelope decoding.
  - All Go tests green: `go test -race ./internal/api/... ./internal/httperr/... -count=1` 97.6s + 1.0s both pass. `go vet ./internal/api/...` clean.

- **Task 2 — Playwright e2e tests + dev-surface flag plumbing** (commits `c198f87`, `adcd160`):
  - 9 Playwright scenarios in `web/e2e/error-envelope.spec.ts`:
    - all 4 classes render in inline mode with ARIA role (role=alert or role=status + data-envelope-class hook).
    - all 4 classes render in page mode.
    - transient inline section shows "Try again in Ns" disabled button.
    - transient countdown reaches zero, button re-enables, click increments `data-story-retry-count` counter.
    - operator_action_required shows "Go to Admin → Trivy" CTA navigating to `/admin/trivy`.
    - incident_id chip + CopyButton (`aria-label="Copy to clipboard"`) render for every class.
    - live-fetched envelopes render end-to-end for every class (server → wire → UI parse).
    - validation class surfaces "Some fields need your attention." (UI-SPEC §Copywriting).
    - permission class copy renders (Lock icon + hint).
  - `web/src/App.tsx` — `DEV_ROUTES_ENABLED = import.meta.env.DEV || VITE_OMNIREPO_DEV === 'true'` unions the Vite-dev-mode signal with a build-time opt-in flag. Regular production builds keep the story-page route tree-shaken (T-06-03-04 invariant preserved); the Playwright suite builds with `VITE_OMNIREPO_DEV=true` to include it.
  - `internal/httpx/devproxy.go` — `IsDevMode()` gains an `OMNIREPO_DEV_PROXY=0` escape hatch so the server can enable dev backend routes while still serving the embedded SPA (no Vite sidecar required).
  - `web/playwright.config.ts` — webServer command builds the SPA with `VITE_OMNIREPO_DEV=true` and starts the Go binary with `OMNIREPO_DEV=1 OMNIREPO_DEV_PROXY=0`. Env vars propagate via `env:` block.
  - Manual verification: ran the 9-scenario suite against the rebuilt binary on the main working tree; all 9 passed in 8.7s on first execution.

## Task Commits

| # | Phase | Type | Commit      | Message |
| - | ----- | ---- | ----------- | ------- |
| 1 | RED→GREEN | test+feat | `a2c25d4` | add failing envelope-bridge unit tests + tighten normalize |
| 1 | GREEN | feat | `456a31d` | migrate auth middleware + SPA 404 + maintenance to envelope |
| 1 | GREEN | feat | `438cb99` | integration tests + non-empty envelope messages (ERR-01/03/07) |
| 2 | GREEN | test | `c198f87` | Playwright e2e coverage for ErrorEnvelopeRenderer |
| — | chore | chore | `adcd160` | ignore Playwright runtime artifacts |

## Files Created/Modified

**Created:**
- `internal/api/errors_envelope_test.go` (213 lines) — 6 unit tests covering normalize/sanitize/class-inference/leakage gates.
- `internal/api/handlers_envelope_integration_test.go` (515 lines) — 14 integration tests covering every class + incident-id parity + no-leakage sweep.
- `web/e2e/error-envelope.spec.ts` (203 lines) — 9 Playwright scenarios covering the 4 classes × 3 modes + countdown + deep-link + incident chip.

**Modified:**
- `internal/api/errors.go` — codeShapeRegex passthrough gate; sanitizeCode leading-non-letter x_ prefix; writeJSONError default-message fallback; defaultMessageForStatus helper.
- `internal/api/errors_bridge_test.go` — "some.dotted.code" expectation flipped from passthrough to sanitized.
- `internal/api/admin_phase1.go` — login + change-password handlers ship static user-facing sentences instead of "".
- `internal/api/admin_phase1_test.go` — IncidentIDMiddleware + MountDevErrorRoutes on the test server; doRaw helper; 2 legacy assertions updated.
- `internal/auth/middleware/deps.go` — envelope migration + reasonCode/reasonMessage mappers.
- `internal/auth/middleware/basic_or_apikey.go` — writeJSON401Basic call sites widened.
- `internal/auth/middleware/session_or_apikey.go` — writeJSON401 call sites widened.
- `internal/auth/middleware/session_or_apikey_test.go` — legacy substring assertion updated.
- `internal/httpx/spa.go` — writeAPINotFound envelope migration.
- `internal/httpx/spa_test.go` — legacy body shape assertion updated.
- `internal/httpx/middleware_maintenance.go` — MaintenanceMode envelope migration (operator_action_required class).
- `internal/httpx/devproxy.go` — OMNIREPO_DEV_PROXY=0 escape hatch on IsDevMode.
- `web/src/App.tsx` — DEV_ROUTES_ENABLED unions import.meta.env.DEV with VITE_OMNIREPO_DEV.
- `web/playwright.config.ts` — webServer env + command pass all three dev flags.
- `web/.gitignore` — Playwright runtime artifacts excluded.

## Decisions Made

- **Pre-existing handler tests that asserted legacy `{error, detail}` shape** — enumerated via `grep -nE 'body\[.error.\]|body\[.detail.\]'`: three hits (admin_phase1_test.go:605, :759 and session_or_apikey_test.go:309) all on the must_change_password 403 path emitted by auth middleware. A fourth hit was in spa_test.go on the /api/* NotFound path. Updated all four as part of the auth/spa/maintenance migration commit; they were the reason plan 02 left those migrations for plan 04.
- **Test-count delta for internal/api/...** — plan 06-02 shipped 6 bridge tests in errors_bridge_test.go; plan 06-03 shipped 6 dev-route integration tests; plan 06-04 adds 20 tests (6 unit + 14 integration). Running `grep -c "^func Test" internal/api/*_test.go | awk -F: '{s+=$2}END{print s}'` shows ~180+ test functions total in the package after this plan. `go test -race ./internal/api/... -count=1` — 97.6 seconds, all green.
- **Playwright web server env-var wiring** — chose the 3-flag opt-in pattern (`OMNIREPO_DEV=1` backend canned routes + `OMNIREPO_DEV_PROXY=0` embedded SPA + `VITE_OMNIREPO_DEV=true` build-time story-page inclusion) over more invasive alternatives: (1) a hard "dev binary" variant would require a second cmd/omnirepo-dev build target; (2) a runtime-hot-reloaded SPA would require Vite to run in the sidecar at test time, breaking standalone-binary testing. The flag split keeps regular production builds completely free of dev surfaces while giving the Playwright suite everything it needs.
- **Timing-dependent assertion in the countdown test** — canned transient envelope carries `retry_after_ms: 3000`. The test asserts the button reaches "Try again" text within 5s (2s margin over the 3s countdown) to absorb CI scheduler jitter. Not flaky on local runs (8.7s total suite, one clean pass); if CI flakes emerge, widen the per-assertion timeout to 8s rather than shortening retry_after_ms (that value is a UI-SPEC contract, not a test knob).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing critical functionality] Empty envelope messages violated the OpenAPI schema**

- **Found during:** Task 1 RED — integration tests asserting `envelope.Message != ""` failed on every login / internal-error path where `writeJSONError(w, r, status, code, "")` was called.
- **Issue:** The ApiErrorEnvelope schema declares `message` as required + string-type; 100+ call sites pass `""` as `detail` because they explicitly do not want to serialize internal cause strings (ERR-03). Wire bodies were shipping `"message":""` which fails the schema validator any downstream client runs.
- **Fix:** Added `defaultMessageForStatus(status)` helper to `internal/api/errors.go` emitting a static developer-authored sentence per canonical HTTP status. `writeJSONError` falls back to it when `detail == ""`. Static strings only — zero interpolation — so the ERR-03 no-leakage invariant holds.
- **Files modified:** `internal/api/errors.go`.
- **Verification:** All 14 integration tests assert `e.Code != "" && e.Message != "" && e.Class != ""` — green.

**2. [Rule 1 - Bug] normalizeLegacyCode passthrough leaked internals**

- **Found during:** Task 1 RED — `TestNormalizeLegacyCode_NeverLeaksShapeInternals` discovered that inputs like `"errors.go:123"` and `"/home/omnirepo/data/project.db"` passed through verbatim because the old `containsDot` helper accepted any "." and skipped sanitization.
- **Issue:** A future call site that accidentally threaded `err.Error()` (which might contain file paths or source locations) into the `code` argument of `writeJSONError` would ship the raw string as the wire envelope code, violating BOTH the OpenAPI code regex AND ERR-03.
- **Fix:** Replaced the lightweight `containsDot` gate with a full `codeShapeRegex` match (`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`). Inputs that don't match the wire regex now fall to the sanitize path and get the `legacy.` prefix.
- **Files modified:** `internal/api/errors.go`, `internal/api/errors_bridge_test.go` (one test case expectation flipped since `"some.dotted.code"` was actually malformed wire-shape).
- **Verification:** `TestNormalizeLegacyCode_NeverLeaksShapeInternals` green across all 6 malicious inputs.

**3. [Rule 1 - Bug] sanitizeCode could produce leading-underscore codes**

- **Found during:** Task 1 RED — `TestSanitizeCode_LocalSegmentRegex` failed on `"_leading"` input.
- **Issue:** Local-segment regex requires `^[a-z]`; sanitizeCode's x_ guard only fired on empty-result or leading-digit inputs, leaving leading-underscore inputs (e.g. `"_leading"` → `"_leading"`) as invalid local segments.
- **Fix:** Widened the x_ guard to "any leading character that is not a lowercase letter" (empty || digit || underscore || non-alpha).
- **Files modified:** `internal/api/errors.go`.
- **Verification:** 10/10 `TestSanitizeCode_LocalSegmentRegex` cases green.

**4. [Rule 3 - Blocking] Playwright couldn't drive /_dev/error-class-story against a production bundle**

- **Found during:** Task 2 — tried running the e2e suite; `/_dev/error-class-story` returned 502 because `make build-all` produces a production bundle with `import.meta.env.DEV === false`, which tree-shakes the story page route out.
- **Issue:** The Playwright webServer runs against a real Go binary that serves the embedded SPA. The story page is only available in Vite dev mode. Without a build-time opt-in, e2e tests couldn't exercise the UI renderer.
- **Fix:** Introduced `VITE_OMNIREPO_DEV` build-time flag — `DEV_ROUTES_ENABLED` in web/src/App.tsx unions it with `import.meta.env.DEV`. Playwright config sets the flag at build time so the story page ships in the Playwright bundle; regular production builds don't set it, so the tree-shake invariant (T-06-03-04) holds.
- **Files modified:** `web/src/App.tsx`, `web/playwright.config.ts`.
- **Verification:** `curl https://localhost:8443/_dev/error-class-story` returns 200 HTML against the Playwright-flavored binary; Playwright suite 9/9 green.

**5. [Rule 3 - Blocking] Dev-proxy conflict with embedded-SPA e2e**

- **Found during:** Task 2 — initial e2e attempt: setting `OMNIREPO_DEV=1` to enable backend canned routes ALSO flipped `IsDevMode()=true`, which replaced the SPAHandler with a reverse proxy to `localhost:5173`. Without Vite running, every SPA request returned 502.
- **Issue:** `OMNIREPO_DEV` had two orthogonal effects (backend routes + SPA proxy) bundled into one flag.
- **Fix:** Added `OMNIREPO_DEV_PROXY` as an independent runtime flag. `IsDevMode()` returns true only when BOTH `OMNIREPO_DEV=1` AND `OMNIREPO_DEV_PROXY != "0"`. Playwright sets `OMNIREPO_DEV_PROXY=0` to keep the embedded SPA while still registering dev backend routes.
- **Files modified:** `internal/httpx/devproxy.go`, `web/playwright.config.ts`.
- **Verification:** Server boot log shows `spa.mode handler=embedded` + dev routes still respond with canned envelopes.

### Scope Expansion (tracked)

**6. [Rule 2 - Missing critical functionality] SPA 404 + maintenance middleware + auth middleware were still emitting legacy shape**

- **Found during:** Grepping for `body["error"]` / `"error":` revealed three more emitter sites beyond deps.go: `internal/httpx/spa.go:writeAPINotFound` (SPA 404 for unknown /api/* routes) and `internal/httpx/middleware_maintenance.go:MaintenanceMode` (maintenance gate).
- **Issue:** The plan's "legacy {error, detail} body shape" cleanup only explicitly named auth middleware (`internal/auth/middleware/deps.go`). But two other /api/v1-surface emitters existed, and leaving them would mean `TestEnvelope_NoInternalLeakage_AcrossHandlers` couldn't actually sweep the whole surface.
- **Action:** Migrated all three in one commit (`456a31d`). SPA-404 ships `resource.not_found` / class=validation; maintenance ships `maintenance.enabled` / class=operator_action_required with deep link to /admin/maintenance (wires directly into plan 06-03's ErrorEnvelopeRenderer operator CTA).
- **Rationale:** In-scope per Rule 2 (missing critical functionality — envelope parity). After this plan, ZERO legacy-shape emitters remain on /api/v1.
- **Verification:** `go test ./internal/api/... ./internal/httpx/... ./internal/auth/...` all green; spa_test + session_or_apikey_test updated to assert envelope fields.

---

**Total deviations:** 6 auto-fixed (1 missing functionality default-message, 2 bugs in normalize/sanitize, 2 blocking issues for e2e, 1 scope expansion for envelope parity).

**Impact on plan:** All plan must-haves satisfied. Plan's deferred-from-02 items (`internal/auth/middleware/deps.go` + 3 legacy test assertions) are closed. `TestEnvelope_NoInternalLeakage_AcrossHandlers` is now a meaningful whole-surface gate because ZERO legacy-shape emitters remain.

## Issues Encountered

- **Background-task shell-state reset** — `kill $PID` where `$PID` came from a previous `&` spawn didn't work across Bash tool invocations because the subshell state doesn't persist. Worked around by capturing pids via `pgrep` fresh each time.
- **Playwright webServer timeout** — default 120s is tight when the command chain does `npm ci && vite build && make build` from scratch. Not hit in manual verification (server + existing binary), but may need widening if a cold CI run takes longer.

## User Setup Required

- **Playwright e2e:** requires `npx playwright install chromium` on the host (one-time per machine; ~112 MB).
- **To run the e2e suite locally:**
  ```
  # From repo root
  make build-all
  OMNIREPO_DEV=1 OMNIREPO_DEV_PROXY=0 ./bin/omnirepo serve -config /path/to/config.yaml &
  cd web && npx playwright test error-envelope.spec.ts
  ```
  Or just `cd web && npx playwright test error-envelope.spec.ts` — the webServer config handles the build + server boot automatically when no server is already listening on :8443.

## Next Phase Readiness

- **Wave 1 is now GREEN end-to-end.** ApiErrorEnvelope is the single response body for /api/v1 error paths; X-Incident-Id header matches envelope.incident_id; panics emit the envelope not stack traces; UI renders the class-specific icon/CTA; integration + e2e tests pin the contract.
- **Plan 06-05 (protocol redaction)** is unblocked. Its scope is the protocol handlers (internal/protocol/*) which by design do NOT use the envelope (they emit protocol-native error shapes). Redaction patterns from the httperr package (IsInternalString regex set) can be reused.
- **Plans 06-06, 06-07, 06-08 (visual foundation)** are unblocked. The ErrorEnvelopeRenderer they build on is stable; status tokens (bg-status-*) referenced in plan 06-03's class-style map will get their CSS-variable backing when plan 06-06 installs them.
- **No remaining legacy-shape emitters on /api/v1.** A future plan that introduces one will be caught by `TestEnvelope_NoInternalLeakage_AcrossHandlers`.

## Self-Check: PASSED

- `internal/api/errors_envelope_test.go` — FOUND (213 lines, ≥ 150 required)
- `internal/api/handlers_envelope_integration_test.go` — FOUND (515 lines, ≥ 200 required)
- `web/e2e/error-envelope.spec.ts` — FOUND (203 lines, ≥ 120 required)
- `grep -c "^func Test" internal/api/handlers_envelope_integration_test.go` = 14 (≥ 6 required)
- `grep -c "^\s*test(" web/e2e/error-envelope.spec.ts` = 9 (≥ 7 required)
- `grep -q "assertEnvelope" internal/api/handlers_envelope_integration_test.go` — FOUND
- `grep -q "IsInternalString" internal/api/handlers_envelope_integration_test.go` — FOUND
- `grep -q "X-Incident-Id" internal/api/handlers_envelope_integration_test.go` — FOUND
- `grep -q "uuidV7Regex" internal/api/handlers_envelope_integration_test.go` — FOUND
- `grep -q "TestValidationNeverEchoesUserInput" internal/api/handlers_envelope_integration_test.go` — FOUND as `TestEnvelope_ValidationNeverEchoesUserInput`
- `grep -q "/_dev/error-class-story" web/e2e/error-envelope.spec.ts` — FOUND
- `grep -q "Try again in" web/e2e/error-envelope.spec.ts` — FOUND
- `grep -q "Go to Admin → Trivy" web/e2e/error-envelope.spec.ts` — FOUND
- `grep -q "Incident " web/e2e/error-envelope.spec.ts` — FOUND
- `grep -q "data-story-class" web/e2e/error-envelope.spec.ts` — FOUND
- `grep -q 'data-story-mode="live"' web/e2e/error-envelope.spec.ts` — FOUND
- `! grep -E 'font-medium|font-bold|font-light' web/e2e/error-envelope.spec.ts` — CLEAN
- Commit `a2c25d4` (Task 1 RED+GREEN unit tests) — FOUND in `git log`
- Commit `456a31d` (Task 1 auth/spa/maintenance migration) — FOUND in `git log`
- Commit `438cb99` (Task 1 integration tests) — FOUND in `git log`
- Commit `c198f87` (Task 2 Playwright) — FOUND in `git log`
- Commit `adcd160` (chore: gitignore) — FOUND in `git log`
- `go test -race ./internal/api/... ./internal/httperr/... -count=1` — PASS (97.6s + 1.0s)
- `go vet ./internal/api/...` — CLEAN
- `go test ./internal/... -count=1` — all 32 packages PASS (jobs package has a known flaky timing-dependent test unrelated to this plan; passes individually)
- `cd web && npx tsc --noEmit` — PASS
- `cd web && npx playwright test error-envelope.spec.ts --reporter=line` — PASS (9/9 scenarios, 8.7s)
- Manual verification: server started with OMNIREPO_DEV=1 OMNIREPO_DEV_PROXY=0 + SPA built with VITE_OMNIREPO_DEV=true → /_dev/error-class-story returns 200 HTML; /api/v1/_dev/error/:class returns envelopes for all 4 classes.

## TDD Gate Compliance

Plan frontmatter is `type: execute` (not `type: tdd`). Per-task `tdd="true"`:

- Task 1: RED `a2c25d4` (tests that caught 3 real bugs + schema violations) → GREEN `438cb99` (integration tests + the bridge-side fixes to make them pass). A middle migration commit `456a31d` is the envelope parity for auth middleware / SPA / maintenance that closed the last legacy-shape emitters. Gate sequence satisfied.
- Task 2: No independent RED — Playwright specs are validation tests for already-implemented UI behavior (plan 06-03's renderer). The acceptance signal is "9/9 scenarios green against real binary + real SPA build"; achieved in one commit.

No refactor commits needed; initial GREEN implementations cover the `<behavior>` blocks cleanly.

---
*Phase: 06-error-envelope-visual-foundation*
*Completed: 2026-04-17*
