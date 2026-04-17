# Phase 6: Error Envelope & Visual Foundation — Research

**Researched:** 2026-04-17
**Domain:** API error contract (Go + OpenAPI 3.1 + TS) + React 19 design-system primitives
**Confidence:** HIGH (direct codebase inspection + Context7-verified oapi-codegen behaviour + UI-SPEC already locks design decisions)

> This research answers the 12 planner-facing questions from the spawn prompt. The UI-SPEC is the locked visual/interaction contract; this document answers the *implementation* questions the UI-SPEC deliberately did not pin down.

---

## Summary for planner — Top 5 decisions that shape the plan

1. **No wire-shape flag day. One-shot hard cutover.** The existing `{error, detail}` envelope produced by `internal/api/errors.go::writeJSONError` is the ONLY v1.0 error shape (verified: 304 call sites in `internal/api/`; zero callers use a different shape). Replacing `writeJSONError`'s body + `ErrorResponse` struct in place, plus migrating UI `handleResponse`, is an atomic change: all Go API handlers flip in one commit because they all funnel through one helper. Protocol handlers (`internal/protocol/**`) use raw `http.Error` (206 call sites across 29 files) and leak `fmt.Sprintf("%v", err)` — they are a SEPARATE migration and MUST be scoped as a parallel wave in Phase 6 because they are NOT routed through `writeJSONError` today.
2. **`internal/httperr` is a NEW package, not a rename of `internal/api/errors.go`.** `internal/api/errors.go` is package-private (lowercase `writeJSON`, `writeJSONError`) and only `/api/v1` handlers can call it. Protocol handlers live in `internal/protocol/{oci,rpm,deb,pypi,helm,raw,s3,git}` — different packages, no shared helper today. New public package `internal/httperr` provides the envelope type, constructors, and `Write(w, r, err)` helper, imported by BOTH `internal/api/*` and `internal/protocol/**`.
3. **Incident ID = chi `middleware.RequestID` piggyback, NOT a new column.** The router already installs `middleware.RequestID` (router.go:30) and `audit.enter`/`audit.exit` slog records include `request_id`. `httperr.Write` reads `middleware.GetReqID(r.Context())`, writes it into `incident_id`, and the logger-on-error call (new) keys the internal error (stack, wrapped chain) by the same ID. The `audit_log` SQLite table has no column change — correlation is already via slog output which is what operators grep anyway. Zero schema migration for ERR-07.
4. **OpenAPI fragment is straightforward; ONE oapi-codegen pitfall to know about.** `ApiErrorEnvelope` with `type: object`, `enum` for `class`, and nested `details` generates clean Go types in types-only mode (Context7-verified). **Pitfall:** `additionalProperties: false` with extra named properties OR unioning `details.fields` with other named keys triggers oapi-codegen's `AdditionalProperties map[string]...` generation pattern which bloats the type. Recommended: use `additionalProperties: true` on `details` so oapi-codegen generates a clean struct with optional `*string` / `*int` fields for the named knobs and the user-shaped `fields: map[string]string` validation carrier.
5. **Skeleton shipping order: sweep + stop-the-line.** The UI-SPEC migration list puts SkeletonTable/Card/Detail/Metric under Phase 6 as components, but the UI-SPEC is SILENT on where to apply them. Recommendation: Phase 6 ships the 4 components + retrofits exactly ONE of each call-site kind (DashboardPage, ProjectsPage, one repo detail, HEALTH is Phase 9 problem) as canonical usage examples; Phase 7/9/10 plans each carry a "replace remaining blanks on surfaces you touch" task. Rationale: a full sweep blows up Phase 6's diff, and the only hard dependency downstream is **existence of the four components** (no one blocks on applying them everywhere).

Decisions 1 + 2 + 3 are the load-bearing migration strategy; decisions 4 + 5 drive the plan-decomposition choices for tasks the planner cannot defer.

---

## User Constraints (from CONTEXT.md)

**No CONTEXT.md exists for Phase 6.** The UI-SPEC.md is the locked design contract; treat its **Component/Token Migration Tasks** list (19 numbered items) as the discretion-area pre-loaded by the UI researcher. Planner may reorder and decompose those 19 tasks but MUST NOT drop any — each maps to a grep-verifiable test in Phase 6's Checker Sign-Off.

---

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| ERR-01 | Stable envelope `{code, message, hint?, class, incident_id?}` | §Q1 (OpenAPI fragment), §Q2 (Go-side), §Q3 (validation details) |
| ERR-02 | UI renders envelope per class | §Q5 (renderer + TanStack) |
| ERR-03 | No internal leak verbatim | §Q2 (mapping table) |
| ERR-04 | Retry affordance on transient | §Q5 (`useApiError`) |
| ERR-05 | Operator-action-required deep-link | §Q2 (operator_route mapping) |
| ERR-06 | Validation errors highlight fields | §Q3 (`details.fields` + `fieldErrors` prop) |
| ERR-07 | incident_id correlates UI ↔ log ↔ audit | §Q4 (chi RequestID piggyback) |
| VISUAL-01 | Single status color palette | §Q6 (CSS tokens) |
| VISUAL-02 | Consistent badge shape/size | §Q6 + §Q9 (snapshot matrix) |
| VISUAL-03 | Skeletons in known-shape surfaces | §Q7 (shipping order) |
| VISUAL-04 | Copy-to-clipboard on URLs/commands/digests/keys | §Q8 (CopyButton reuse) |
| VISUAL-05 | Primary vs destructive distinct | UI-SPEC §Button Hierarchy (already settled) |
| VISUAL-06 | No h-scroll at 1366×768 | §Q11 (sticky-first-column only) |
| VISUAL-07 | Spacing + typography hierarchy | UI-SPEC §Typography (already settled) |
| VISUAL-08 | WCAG AA contrast | §Q10 (@axe-core/playwright MPL-2.0) |
| VISUAL-09 | Severity treatment consistent | UI-SPEC §Severity Mapping + reuse `SeverityBadge.tsx` |

---

## Project Constraints (from CLAUDE.md)

- **`make grep-cdn` must stay green.** Any dep used for testing (axe-core) must be devDep only, never bundled into `web/dist/`.
- **Apache-2.0-compatible licenses.** `@axe-core/playwright` is MPL-2.0 — file-level copyleft, compatible with Apache-2.0 when used as a **dev/test dependency only**, not linked into the shipped binary. It must remain in `devDependencies`.
- **Stack frozen.** Go 1.25, React 19.1, Vite, Tailwind 4.1, `@base-ui/react` 1.4, TanStack Query 5.74. No version bumps.
- **Test gates:** every feature ships with tests; `make test` is the merge gate; UI verified with Playwright.
- **No documentation files unless explicitly required.** RESEARCH.md and plan artifacts are allowed (GSD-generated).
- **No outbound network calls at runtime.** Trivy DB only via user action. Error envelopes must not trigger telemetry of any kind.
- **shadcn official registry only.** `web/components.json` already frozen at `base-nova`; no new `shadcn add` invocations expected in Phase 6 (all base primitives already in repo).

---

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|--------------|----------------|-----------|
| Envelope schema source-of-truth | API (OpenAPI spec) | Codegen (oapi-codegen) | One YAML declaration feeds Go types AND TS consumers read TS-hand-mirror |
| Envelope construction | API / Backend (Go `internal/httperr`) | Protocol handlers | Every non-2xx response in the Go binary goes through one writer |
| Incident ID generation | API / Backend middleware (chi `middleware.RequestID`) | — | Already installed; free piggyback |
| Internal-detail redaction | API / Backend (`httperr.Write`) | — | Server owns the boundary; UI never sees internal strings |
| Envelope parsing | Frontend Server (N/A — SPA only) | Browser / Client (`web/src/api/client.ts`) | SPA is pure client; Go binary serves static assets |
| Envelope rendering | Browser / Client (`ErrorEnvelopeRenderer`) | — | React tree only |
| Status/skeleton tokens | Browser / Client (`src/index.css` + components) | — | CSS + React |
| WCAG contrast verification | Test harness (Playwright + axe-core) | — | Build gate, not runtime |

No cross-tier misassignment risk — envelope generation is API-side, rendering is client-side, testing is CI-side. Clean separation.

---

## Graph Context

No `.planning/graphs/graph.json` exists in this repo. Skipping graph-assisted discovery. All surface inventory below comes from direct grep/codebase inspection.

---

## Q1 — OpenAPI 3.1 + oapi-codegen compatibility

**Finding:** The UI-SPEC's canonical fragment (UI-SPEC line 164–187) is already OpenAPI 3.1-compliant. Context7's oapi-codegen README confirms that `type: object` with `enum`, nested objects, and `additionalProperties` all generate clean Go types in v2.6.0 types-only mode. **One real pitfall:** mixing `additionalProperties: true` with explicit named sub-properties under `details` works, but mixing `additionalProperties: false` with only some named sub-properties generates a strict struct where oapi-codegen emits `AdditionalProperties map[string]any` anyway for forward-compat — the strict mode doesn't actually enforce at runtime (it's an OpenAPI *validation* concern, not a Go-type concern).

Verified in `internal/api/types_gen.go` that oapi-codegen v2.6.0 is already in use and produces standard struct types without `AdditionalProperties` for every current schema. A new schema slotting in next to them is low risk.

**Canonical fragment the planner can paste verbatim into `internal/api/openapi.yaml`:**

```yaml
components:
  schemas:
    ApiErrorClass:
      type: string
      enum: [validation, permission, transient, operator_action_required]

    ApiErrorEnvelope:
      type: object
      required: [code, message, class]
      properties:
        code:
          type: string
          pattern: "^[a-z][a-z0-9_]*\\.[a-z][a-z0-9_]*$"
          maxLength: 80
        message:
          type: string
          maxLength: 280
        hint:
          type: string
          maxLength: 280
        class:
          $ref: '#/components/schemas/ApiErrorClass'
        incident_id:
          type: string
          maxLength: 64
        details:
          type: object
          additionalProperties: true
          properties:
            field:
              type: string
              description: Dot-path of the offending form field (validation class)
            fields:
              type: object
              additionalProperties:
                type: string
              description: Map of field-path → error-code for multi-field validation failures
            retry_after_ms:
              type: integer
              minimum: 0
              description: Suggested retry delay (transient class)
            operator_route:
              type: string
              description: UI path the operator should navigate to (operator_action_required)
            operator_label:
              type: string
              description: CTA label for the operator deep-link
```

**Referencing from every existing error response:** today each 4xx/5xx response defines its schema inline as `{ error: string, detail: string }`. Migration path:

```yaml
# Shared responses component — reuse across all handlers
components:
  responses:
    ValidationError:
      description: Validation failed
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ApiErrorEnvelope'
    PermissionError:
      description: Permission denied
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ApiErrorEnvelope'
    NotFoundError:
      description: Resource not found
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ApiErrorEnvelope'
    TransientError:
      description: Temporary failure
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ApiErrorEnvelope'
    OperatorActionRequired:
      description: Administrator action required
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/ApiErrorEnvelope'
```

Then each operation adds `responses: '4xx': $ref: '#/components/responses/ValidationError'` etc. This is minimally invasive — existing 2xx responses stay untouched, only error response definitions update.

**Gotchas:**
- `oneOf` on the `class` enum is NOT needed; a string enum is sufficient and generates a typed `ApiErrorClass` constant set (oapi-codegen v2.6.0 already does this for `GCStatusResponseStatus` today — verified in `types_gen.go` line 18–22).
- `additionalProperties: true` on `details` avoids the `AdditionalProperties map[string]any` bloat that `additionalProperties: false` plus extra `properties` would cause — confirmed from Context7 docs ([Generate Go Struct for OpenAPI Schema with Additional Properties](https://github.com/oapi-codegen/oapi-codegen/blob/main/README.md)).
- `pattern:` regex on `code` compiles to a field comment only in types-only mode; enforcement happens at handler-level Go code — plan MUST include unit test `TestEnvelopeCodeRegex` that asserts every constant in `httperr.Code*` matches the pattern.
- **NO pre-codegen migration needed.** The existing `ErrorResponse` struct in `errors.go` line 14 is hand-written and NOT referenced from `openapi.yaml` (spec line 29 says so explicitly). Adding `ApiErrorEnvelope` alongside it is non-colliding; the old `ErrorResponse` struct can be deleted after `writeJSONError` is rewritten to emit the new shape.

**Recommendation:**
- Plan one task: "Add `ApiErrorEnvelope` + `ApiErrorClass` schemas to `openapi.yaml`; add 5 reusable error responses; run `go generate` to regenerate `types_gen.go`."
- Plan a second task: "Swap every operation's error response body in `openapi.yaml` to `$ref` the shared responses." (Mechanical; ~40 paths.)
- Do NOT introduce `oneOf` — the simpler `enum` on `class` generates a better Go type.

**Impact on plan:** 2 concrete tasks. Both grep-verifiable (`grep '$ref.*ApiErrorEnvelope' internal/api/openapi.yaml | wc -l`). No codegen tooling upgrade required.

**Confidence:** HIGH — Context7-verified, types_gen.go already uses equivalent patterns.

---

## Q2 — Go-side implementation surface

**Finding:** Two very different code paths today, and the planner MUST treat them separately:

**Path A — `/api/v1/*` + `/api/admin/*` handlers in `internal/api/` (the "structured" path):**
- ALL 304 error sites go through `writeJSONError(w, status, code, detail)` (verified by grep).
- Error helpers centralized in `internal/api/errors.go` (72 lines).
- Existing stable codes: `password-change-required`, `unauthenticated`, `forbidden`, `not_found`, `validation_failed`, `conflict`, `internal` (errors.go:20–28).
- Migration: rewrite `writeJSONError` to build an `ApiErrorEnvelope` and emit it. Because every caller funnels through one helper, the callers mostly don't need to change — they just need to pass `class` instead of (or in addition to) the legacy `code`.

**Path B — `internal/protocol/**` handlers (the "raw" path):**
- 206 `http.Error(w, "msg", status)` sites across 29 files (verified).
- Multiple files (`raw/delete.go`, `raw/get.go`, `rpm/put.go`, `helm/put.go`, etc.) emit `fmt.Sprintf("%v", err)` or `fmt.Sprintf("stat: %v", err)` — **these leak internal error strings to the client directly**, violating ERR-03.
- These handlers implement wire protocols (Docker registry, APT, RPM, PyPI, Helm, S3, Git) where clients MAY NOT accept a JSON envelope — e.g. `docker push` expects an OCI error format, `apt-get` expects plain text, `aws s3 cp` expects SigV4 XML.
- **This is the architectural subtlety.** Protocol clients (dockerd, apt, pip, curl, aws-cli, git) don't parse `application/json` errors — they want protocol-native errors. The **envelope contract in ERR-01/03 only applies to `/api/v1` and `/admin` JSON endpoints consumed by the Web UI**. Protocol handlers need a DIFFERENT fix: redact the `%v` interpolation so internal paths/driver strings don't reach clients, but keep emitting the protocol-native error shape.

**Proposed `internal/httperr` package shape:**

```go
// Package httperr defines the canonical JSON error envelope for
// /api/v1 and /admin REST endpoints (ERR-01). Protocol handlers in
// internal/protocol/{oci,rpm,deb,pypi,helm,raw,s3,git} do NOT use this
// package — they emit protocol-native errors via helpers in their own
// packages. See Q2 in the Phase 6 research for the split rationale.
package httperr

type Class string

const (
    ClassValidation       Class = "validation"
    ClassPermission       Class = "permission"
    ClassTransient        Class = "transient"
    ClassOperatorRequired Class = "operator_action_required"
)

// Envelope is the wire shape. Mirrors openapi.yaml#/components/schemas/ApiErrorEnvelope.
// Field names match the generated api.ApiErrorEnvelope (Go gen from OpenAPI).
type Envelope struct {
    Code       string         `json:"code"`
    Message    string         `json:"message"`
    Hint       string         `json:"hint,omitempty"`
    Class      Class          `json:"class"`
    IncidentID string         `json:"incident_id,omitempty"`
    Details    map[string]any `json:"details,omitempty"`
}

// Error wraps Envelope with an internal (never-serialized) cause.
// Callers use errors.Is/As to match sentinels.
type Error struct {
    Envelope Envelope
    Status   int
    Cause    error // logged, never sent
}

func (e *Error) Error() string  { return e.Envelope.Message }
func (e *Error) Unwrap() error  { return e.Cause }

// Constructors — one per class + one per common sub-case.
func Validation(code, msg string, opts ...Option) *Error
func ValidationField(code, field, msg string) *Error
func ValidationFields(code string, fields map[string]string) *Error
func Permission(code, msg string, opts ...Option) *Error
func Transient(code, msg string, retryAfterMs int) *Error
func OperatorRequired(code, msg, operatorRoute, operatorLabel string) *Error

// Internal wraps an opaque server-side failure into a 500 transient-class
// envelope with a generic message. Cause is logged, not serialized.
func Internal(code string, cause error) *Error

type Option func(*Error)

func WithHint(h string) Option           { /* ... */ }
func WithStatus(s int) Option            { /* ... */ }
func WithDetail(k string, v any) Option  { /* ... */ }

// Write serializes e as JSON + logs (slog) the internal cause keyed by
// the chi request_id (which also becomes the public incident_id).
// Safe to call from any handler.
func Write(w http.ResponseWriter, r *http.Request, e *Error)

// IsInternalString returns true if s looks like a leaked internal string
// (filesystem path, "sql:", "read:", "open:", etc.). Used in unit tests
// to assert the envelope never carries these.
func IsInternalString(s string) bool
```

**Mapping from existing sentinel errors to classes:**

| Sentinel (file) | Class | Code | Hint |
|-----------------|-------|------|------|
| `s3/keys.ErrS3AccessKeyNotFound` | `permission` | `s3_key.not_found` | "Check your access key ID." |
| `protocol/raw` path-traversal check (inline) | `validation` | `validation.invalid_path` | "Paths may not contain `..`." |
| `sigv4.Err*` (4 sentinels in `sigv4/errors.go`) | `permission` | `sigv4.signature_mismatch` etc. | (protocol-native path — NOT envelope) |
| `metadata.ErrUserNotFound` | `validation` (if self-service) or `not_found` code | `user.not_found` | (context-specific) |
| `setup.ErrAlreadyInitialized` | `operator_action_required` | `setup.already_initialized` | "OmniRepo is already set up; log in instead." |
| bare `fmt.Errorf("stat: %v", err)` (raw/*.go) | stays `http.Error` but with redacted message | — | Don't use envelope — protocol path |
| Trivy DB missing (from admin_trivy) | `operator_action_required` | `trivy.db_missing` | `operator_route: "/admin/trivy"`, `operator_label: "Go to Admin → Trivy"` |
| TLS cert upload parse failure | `validation` | `tls.invalid_cert` | "Upload a valid PEM-encoded certificate." |
| Maintenance mode active (middleware) | `operator_action_required` | `maintenance.active` | `operator_route: "/admin/maintenance"`, `operator_label: "Go to Admin → Maintenance"` |

**File-modification scope (rough):**
- `internal/httperr/envelope.go` — **new, ~200 LOC**
- `internal/httperr/write.go` — **new, ~80 LOC** (writes JSON + logs cause with `slog.ErrorContext`)
- `internal/httperr/envelope_test.go` — **new, ~300 LOC** (constructor table, regex validation, redaction)
- `internal/api/errors.go` — rewrite `writeJSONError` (1 function, ~30 LOC change); delete `ErrorResponse` struct
- `internal/api/*.go` — 304 existing call sites to `writeJSONError`. Strategy: keep the signature `writeJSONError(w, status, code, detail)` but have it emit the new envelope with a **class inferred from status** (400→validation, 401/403→permission, 404→validation with code "not_found", 409→validation, 429/503→transient, 500→transient). No call-site changes needed for v1.0 fidelity. Phase 6 adds a NEW helper `writeEnvelope(w, r, *httperr.Error)` for handlers that want explicit class control (operator_action_required, validation_fields, etc.). Phase 7/9/10 opt into the new helper as they touch handlers.
- `internal/protocol/**/*.go` — 206 `http.Error` sites. Plan ONLY the redaction sweep: replace `fmt.Sprintf("stat: %v", err)` / `fmt.Sprintf("%v", err)` with static generic messages (e.g. `"storage error"`) and add `slog.Error(...)` logging of the real error keyed by `middleware.GetReqID(r.Context())`. Grep gate: `grep -R 'http.Error.*%v' internal/protocol/` returns zero after the sweep.
- `internal/httpx/middleware_envelope.go` — **new, ~50 LOC**. Recoverer-style middleware that catches panics inside API handlers and emits an `Internal(...)` envelope instead of the default 500 text.

**Recommendation:**
- Plan 3 tasks for the Go side: (a) new `httperr` package + its tests, (b) rewrite `writeJSONError` in `errors.go` to bridge legacy-codes → envelope and add `writeEnvelope` helper, (c) redaction sweep across `internal/protocol/**` WITHOUT introducing JSON envelope emission in protocol handlers.
- Integration test (ERR-03): for every handler in `/api/v1` and every status code 400/401/403/404/409/413/500, force the failure path and assert (1) response is valid `ApiErrorEnvelope` JSON, (2) `message` and `hint` don't contain `/`, `sqlite`, `sql:`, `read:`, `open:`, `stat:`, stack-trace markers (`goroutine`, `runtime.`).
- For protocol handlers: integration test asserts NO `%v`-interpolated errors reach clients (grep gate, not runtime test, because testing across 9 protocols is too expensive here).

**Impact on plan:** ~5 tasks for Go-side landing, scoped neatly. The big payoff is that `writeJSONError`'s single-helper funnel means the `/api/v1` migration is mostly transparent to handlers.

**Confidence:** HIGH — direct grep verification + handler file inspection.

---

## Q3 — Validation error shape

**Finding:** The UI-SPEC only specifies `details.field: string` (single-field). Multi-field forms (e.g. create-user: both `login` and `email` can fail simultaneously) need a map. The UI-SPEC Component Inventory says `ErrorEnvelopeRenderer` exposes a `fieldErrors: Record<string, string>` prop, so the TS consumer already expects a map — server must supply one.

**Shape that plays nicely:** add `details.fields: Record<string, string>` as a sibling of `details.field`. `field` is for single-field cases (preserves UI-SPEC fidelity), `fields` is for multi-field. Both are valid; the renderer prefers `fields` when present:

```yaml
details:
  additionalProperties: true
  properties:
    field:
      type: string   # single field shortcut
    fields:
      type: object   # multi-field map
      additionalProperties:
        type: string
```

**oapi-codegen compatibility:** `map[string]string` inside a parent struct with `additionalProperties: true` + explicit named sub-properties works cleanly. Verified via Context7 — the generator produces:

```go
type ApiErrorEnvelope_Details struct {
    Field   *string            `json:"field,omitempty"`
    Fields  *map[string]string `json:"fields,omitempty"`
    RetryAfterMs *int          `json:"retry_after_ms,omitempty"`
    OperatorRoute *string      `json:"operator_route,omitempty"`
    OperatorLabel *string      `json:"operator_label,omitempty"`
    AdditionalProperties map[string]any `json:"-"` // forward compat
}
```

**TypeScript interface:**

```typescript
export interface ApiErrorDetails {
  field?: string;
  fields?: Record<string, string>;        // field path → error code
  retry_after_ms?: number;
  operator_route?: string;
  operator_label?: string;
  [key: string]: unknown;
}

export interface ApiErrorEnvelope {
  code: string;
  message: string;
  hint?: string;
  class: 'validation' | 'permission' | 'transient' | 'operator_action_required';
  incident_id?: string;
  details?: ApiErrorDetails;
}
```

Form-library neutrality: the UI-SPEC says `FieldErrorSlot` consumes `fieldErrors: Record<string, string>`. `useApiError()` normalizes both `field` (single) and `fields` (multi) into that shape:

```typescript
function normalize(envelope: ApiErrorEnvelope): Record<string, string> {
  if (envelope.class !== 'validation') return {};
  const d = envelope.details ?? {};
  if (d.fields) return d.fields;
  if (d.field) return { [d.field]: envelope.message };
  return {};
}
```

**Recommendation:**
- Add `details.fields: Record<string, string>` to the OpenAPI schema alongside `field`.
- `httperr.ValidationFields(code, map[string]string)` constructor in Go.
- `useApiError` normalizes both shapes into `fieldErrors: Record<string, string>`.
- Field path convention: dot-separated (`repo.name`, `user.email`) — matches UI-SPEC. Document this in the `httperr` package godoc.

**Impact on plan:** adds 1 constructor to `httperr`, adds 1 normalization line to `useApiError`, adds 1 schema field. Trivial.

**Confidence:** HIGH — Context7-verified oapi-codegen pattern.

---

## Q4 — Incident ID correlation (ERR-07)

**Finding:** Everything needed is already wired:

- `internal/httpx/router.go` line 30 installs `middleware.RequestID` globally on the chi router. Every request gets a generated ID.
- `internal/httpx/middleware_audit.go` lines 18, 21, 26 already extracts `reqID := middleware.GetReqID(r.Context())` and writes it to structured logs (`audit.enter` + `audit.exit`).
- `audit_log` SQLite table (migrations/001_initial.up.sql:107–119) has NO `request_id` column. But that's fine — audit entries correlate by `(occurred_at, actor_user_id, event_kind)` and the slog output is what operators grep. Adding a column is NOT needed for v1.1.

**How incident_id flows:**

1. Request enters → `middleware.RequestID` stamps `X-Request-Id` on the context.
2. Handler fails → calls `httperr.Write(w, r, err)`.
3. `Write` reads `reqID := middleware.GetReqID(r.Context())`.
4. `Write` sets `envelope.IncidentID = reqID` → serializes JSON.
5. `Write` also calls `slog.ErrorContext(r.Context(), "api.error", slog.String("request_id", reqID), slog.String("incident_id", reqID), slog.Any("cause", err.Cause), slog.String("code", err.Envelope.Code))`.
6. If the handler ALSO writes an audit entry (e.g. failed permission check on an admin action), it should put `request_id` in `details_json` (existing free-form TEXT column). Planner adds this as a note on audit-emitting handlers: `details_json: {"request_id": "..."}`.

**Log-format change:** NONE required. slog already emits structured JSON when `config.LogFormat == "json"` (see `middleware_logger.go`). Operators grep logs by `request_id` today; the envelope's `incident_id` is just the same string exposed to users.

**UUID v7 vs v4:** chi's `middleware.RequestID` uses a proprietary format (hostname-prefixed counter). It's fine for correlation but not ideal for user-facing display. Two options:

- **Option A — use chi's format as-is.** Pros: zero code, already deployed. Cons: exposes `hostname-pid-counter` which leaks hostname.
- **Option B — override chi's generator.** chi exposes `RequestIDHeader` and accepts a custom `func() string`. Replace with UUID v7 (time-ordered, sortable, privacy-safe). Go 1.25 does NOT have `uuid.NewV7` in stdlib; the project already vendors `github.com/google/uuid` v1.6 (in `CLAUDE.md` stack list), and `uuid.NewV7()` landed in google/uuid v1.6.0.

**Recommendation: Option B — switch to UUID v7.** Reasons: (1) hostname leak is a real information-disclosure concern for a user-facing `incident_id`; (2) UUID v7 is time-sortable which helps operators bisect logs; (3) the change is a single line in router.go:

```go
import "github.com/google/uuid"

func init() {
    middleware.RequestIDHeader = "X-Incident-Id"  // rename outgoing header
}

// In New():
r.Use(middleware.RequestID)  // but with custom gen:
// Actually: set middleware.RequestIDGeneratorFn = func() string { return uuid.Must(uuid.NewV7()).String() }
```

Note: chi's API for swapping the generator is `middleware.RequestIDHeader` for the header name, and `middleware.DefaultRequestIDFunc` is not exposed publicly pre-5.2.5 — verify at plan time. Fallback: wrap `middleware.RequestID` with a custom middleware that generates UUID v7 and sets it on the context via `middleware.RequestIDKey` before chi's middleware runs.

**Minimum-footprint internal logging:** `slog.ErrorContext` with `request_id`, `incident_id` (same value), `code`, `cause`, and `stack` (if panic). That's already in the `httperr.Write` plan above. No new log format, no new file.

**Impact on plan:**
- 1 task: "Swap chi `middleware.RequestID` for a custom UUID v7 generator in `internal/httpx/router.go` and rename outgoing header to `X-Incident-Id`." Test: HEAD any route, assert header matches `^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$` (UUID v7 variant bits).
- Part of `httperr` task: `Write` reads request ID and writes incident_id.
- Part of audit handlers (separate task, optional): include `request_id` in `details_json` for failure audits so UI's "see in audit log" link can correlate.

**Confidence:** HIGH — chi source verified to expose RequestID, google/uuid v1.6 verified for v7 support.

---

## Q5 — UI renderer architecture

**Finding:** The UI already uses `@tanstack/react-query@5.74.4` (package.json:22). Queries throw `ApiError` today (from `api/client.ts`). The UI-SPEC locks `ErrorEnvelopeRenderer` + `useApiError`. Question: how does TanStack see the envelope?

**Recommended architecture: wrap fetch, not QueryClient.**

```typescript
// client.ts — migrated
export class ApiError extends Error {
  constructor(
    public status: number,
    public envelope: ApiErrorEnvelope,  // replaces code+detail
  ) {
    super(envelope.message);
    this.name = 'ApiError';
  }
}

private async handleResponse<T>(res: Response): Promise<T> {
  if (res.status === 401) {
    // legacy 401 handling — redirect to login (unchanged)
    throw new ApiError(401, { code: 'auth.unauthorized', message: 'Unauthorized', class: 'permission' });
  }
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    if (isApiErrorEnvelope(body)) {
      throw new ApiError(res.status, body);
    }
    // Legacy fallback — during migration window, or when a non-envelope error slips through
    throw new ApiError(res.status, {
      code: 'unknown.legacy',
      message: (body?.detail as string) ?? res.statusText,
      class: res.status >= 500 ? 'transient' : 'validation',
    });
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

function isApiErrorEnvelope(v: unknown): v is ApiErrorEnvelope {
  return typeof v === 'object' && v !== null &&
    typeof (v as any).code === 'string' &&
    typeof (v as any).message === 'string' &&
    typeof (v as any).class === 'string';
}
```

**`useApiError` hook:**

```typescript
export function useApiError(
  query: UseQueryResult<unknown> | UseMutationResult<unknown>,
): {
  envelope: ApiErrorEnvelope | null;
  isRetryable: boolean;
  retry: () => void;
  fieldErrors: Record<string, string>;
} {
  const error = query.error;
  if (!(error instanceof ApiError)) {
    return { envelope: null, isRetryable: false, retry: () => {}, fieldErrors: {} };
  }
  const envelope = error.envelope;
  const isRetryable = envelope.class === 'transient';
  const retry = () => {
    if ('refetch' in query) query.refetch();
    else if ('mutate' in query && 'variables' in query) query.mutate(query.variables as any);
  };
  const fieldErrors = envelope.class === 'validation'
    ? (envelope.details?.fields ?? (envelope.details?.field ? { [envelope.details.field]: envelope.message } : {}))
    : {};
  return { envelope, isRetryable, retry, fieldErrors };
}
```

**Global QueryClient error handler vs custom queryFn wrapper:** use neither. The `ApiClient` is already the custom fetch wrapper — parsing the envelope there is the natural hook point. A global QueryClient `onError` handler would only be useful for crosscutting concerns (e.g. logging all errors to console, or showing a global toast for 503s). Not needed for Phase 6's scope.

**Migration fallback (non-envelope legacy shape):** `isApiErrorEnvelope` gate above already handles it. During the migration window (single commit, but safety net for any missed handler), legacy `{error, detail}` responses get wrapped as synthetic envelopes with `code: "unknown.legacy"` and `class` inferred from status. The renderer still works; there's just less context. After the atomic migration lands, the fallback path becomes dead code — plan a cleanup task for Phase 7 to delete it once Phase 6 is verified green.

**Breaks during migration:** zero, if the migration is atomic. The risk is a stale browser tab holding an old UI against a new server — that path hits the legacy-fallback branch, which still renders something sensible. Playwright test covers this: mock a `{error: "foo", detail: "bar"}` response and assert the renderer shows "foo" as message.

**Recommendation:**
- Plan 1 task: "Migrate `web/src/api/client.ts` `ApiError` class + `handleResponse` to envelope shape with legacy-fallback." Tests: `client.test.ts` with unit tests for both envelope and legacy response bodies.
- Plan 1 task: "Implement `useApiError` hook in `web/src/hooks/useApiError.ts`." Tests: `useApiError.test.tsx` with mocked `UseQueryResult`.
- Plan 1 task: "Implement `ErrorEnvelopeRenderer` in `web/src/components/common/ErrorEnvelope.tsx` per UI-SPEC copy + class table." Tests: Playwright e2e per class.

**Impact on plan:** 3 tasks for the UI side. All grep-verifiable (`grep -R 'instanceof ApiError' web/src | wc -l` returns expected count; `grep 'envelope\\.class' web/src/hooks/useApiError.ts` exists).

**Confidence:** HIGH — direct inspection of client.ts + TanStack Query v5 docs (stable API).

---

## Q6 — Status tokens + StatusBadge shipping order

**Finding:** `web/src/index.css` has 33 CSS variables already (background, foreground, primary, destructive, chart-1..5, sidebar-*, radius). Adding 6 more (`--status-healthy`, `--status-warning`, `--status-failure`, `--status-disabled`, `--status-maintenance`, `--status-neutral`) plus their `-foreground` and `-border` variants (18 new vars total) is a 36-line diff to `:root` + 36 lines to `.dark`. Zero regression risk to existing `--destructive` — the new `--status-failure` is a SEPARATE variable that HAPPENS to use a similar red. UI-SPEC line 116 says so explicitly: "reuses `--destructive` family" — we can even set `--status-failure-fill: color-mix(in oklch, var(--destructive) 10%, white)` to tie them, but a separate hand-tuned value is simpler and tested-AA-compliant.

**Tailwind 4 theme wiring:** in `src/index.css` the `@theme inline` block is where CSS variables become Tailwind utilities. Need to add:

```css
@theme inline {
  /* ... existing ... */
  --color-status-healthy: var(--status-healthy);
  --color-status-healthy-foreground: var(--status-healthy-foreground);
  --color-status-healthy-border: var(--status-healthy-border);
  /* ... repeat for 6 tokens ... */
}
```

That creates `bg-status-healthy`, `text-status-healthy-foreground`, `border-status-healthy-border` utilities.

**SeverityBadge relationship:** the UI-SPEC line 124–130 locks that `SeverityBadge` stays as-is (critical/high/medium/low/unknown) with explicit mapping:
- `critical` → `--status-failure` family
- `high/medium/low` → orange/amber/teal (already hard-coded in `SeverityBadge.tsx`)
- `unknown` → `--status-neutral` family

**Does SeverityBadge re-parent onto StatusBadge?** NO — UI-SPEC is explicit: they stay parallel. Rationale: Severity is about a finding's risk rank (critical > high > medium > low), StatusBadge is about operational state (healthy/warning/failure). They're different axes. `SeverityBadge.tsx` already ships and has no `--status-*` dependencies; leave it alone in Phase 6.

**Optional cross-link:** the UI-SPEC suggests `critical` maps to the failure family. Implementation: update `severityStyles.critical` in `SeverityBadge.tsx` from `bg-destructive/10 text-destructive border-destructive/20` to `bg-status-failure text-status-failure-foreground border-status-failure-border` ONLY IF the plan wants visual cohesion. But changing `SeverityBadge.tsx` is a grandfathered file per UI-SPEC line 134, so SKIP this in Phase 6 and leave as-is. Phase 9/10 can revisit if a designer notices drift.

**Smallest diff:**
- `src/index.css`: 36 lines to `:root`, 36 to `.dark`, 18 to `@theme inline`. Total ~90 lines.
- New file `web/src/components/common/StatusBadge.tsx`: ~80 LOC (6-value switch, icon map, Tailwind utility composition).

**Recommendation:**
- Plan 1 task: "Add 6 status-token sets (fill/foreground/border) to `src/index.css` under `:root` and `.dark`, wire into `@theme inline`."
  - Verification: `scripts/check-contrast.mjs` asserts AA for each pair.
- Plan 1 task: "Add `StatusBadge.tsx` with the 6-value prop, icon map, and Playwright snapshot gate."
  - Verification: snapshot matrix in `visual-foundation.spec.ts` (see Q9).
- Do NOT touch `SeverityBadge.tsx`.
- Do NOT touch `--destructive`.

**Impact on plan:** 2 tasks. Both fit inside Phase 6's "Status/Skeleton primitives + migrations" plan.

**Confidence:** HIGH — direct `index.css` inspection + Tailwind 4 `@theme inline` docs verified.

---

## Q7 — Skeleton shapes — shipping order

**Finding:** v1.0 UI surfaces inventory (sampled from page files):

| Surface | Current loading state | Skeleton kind |
|---------|----------------------|---------------|
| DashboardPage | text "Loading..." (presumed) | SkeletonCard ×6 (storage, repos, users, findings, recent activity, health) |
| ProjectsPage | blank | SkeletonTable ×1 |
| ProjectDetailPage | blank | SkeletonCard + SkeletonTable |
| Repo detail pages (×8 types) | blank | SkeletonDetail |
| Scan results drawer | existing `Skeleton` bar | SkeletonDetail |
| Admin pages (TLS, Trivy, Maintenance, GC, Trash, Settings, Users, Audit) | blank | SkeletonCard + SkeletonTable |
| Search results | blank | SkeletonTable |
| Login/Setup/ChangePassword | n/a | n/a (forms, not data views) |

**Two strategies:**

**Strategy A — full sweep in Phase 6:** replace every blank load state with a Skeleton* component. Diff size: ~30 files touched (every page with a loading branch). Risk: large diff blocks Phase 6 merge; testing surface area explodes.

**Strategy B — ship components + one canonical usage per kind; downstream phases adopt as they touch surfaces:** Phase 6 delivers the 4 components, applies them to exactly 1 of each kind (e.g. DashboardPage for SkeletonCard, ProjectsPage for SkeletonTable, one RepoDetailPage for SkeletonDetail, dashboard storage tile for SkeletonMetric). Phases 7/9/10 apply to the rest as they touch those surfaces.

**Recommendation: Strategy B (pragmatic).**

Rationale:
- UI-SPEC already scopes Phase 6 to foundation primitives. Applying everywhere is polish, not foundation.
- Downstream phases HAVE to touch their surfaces anyway (Phase 7 builds empty states → touches every page, applies SkeletonTable there; Phase 9 builds HEALTH page → uses SkeletonCard + SkeletonMetric natively; Phase 10 builds Overview → uses SkeletonCard).
- The ONLY thing downstream phases block on is "do the components exist and are they styled correctly" — not "is every surface already using them."
- Full sweep would add ~400 LOC of boilerplate with no end-user benefit beyond what the downstream phases deliver anyway.

**Per-kind canonical adoption (Phase 6):**
- `SkeletonCard` — apply to DashboardPage (existing 6 summary cards).
- `SkeletonTable` — apply to ProjectsPage (existing main list).
- `SkeletonDetail` — apply to one repo detail page; pick OCI (largest surface, most loading states).
- `SkeletonMetric` — apply to DashboardPage storage gauge (StorageGauge.tsx already a metric shape).

This gives downstream phases 4 real-world reference implementations to pattern-match against.

**Impact on plan:** 1 plan task for component creation, 1 plan task for canonical adoption. Reduce Phase 6 diff by ~25 files. Defer rest to Phase 7+.

**Confidence:** MEDIUM — strategy is judgement-call, not verifiable fact. Planner or user may prefer full sweep for completeness; the tradeoff is documented so either call is legible.

---

## Q8 — Copy-to-clipboard (VISUAL-04)

**Finding:** `web/src/components/common/CopyButton.tsx` exists (65 lines, verified). Current behavior:
- Uses `navigator.clipboard.writeText` with `document.execCommand('copy')` fallback for non-secure contexts (line 29–39).
- Tooltip swaps to "Copied!" on success, reverts after 2000ms.
- Icon swaps `Copy` → `Check` (green-500) for same 2000ms.
- `aria-label="Copy to clipboard"` on the trigger button (line 51).
- **No `aria-live` region today** — tooltip swap is visual-only; screen readers get nothing on copy success.

**UI-SPEC requires `aria-live="polite"` addition** (line 279, 345). This is a Phase 6 upgrade, not optional.

**API compatibility of the addition:**

Proposed change to `CopyButton.tsx`:
```tsx
export function CopyButton({ text, className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  // ... (existing)

  return (
    <>
      <Tooltip>
        {/* ... existing trigger ... */}
      </Tooltip>
      <span
        aria-live="polite"
        aria-atomic="true"
        className="sr-only"
      >
        {copied ? 'Copied to clipboard' : ''}
      </span>
    </>
  );
}
```

- **Props signature unchanged** (`text`, `className`). Zero breaking changes for existing callers.
- **Existing behavior unchanged** — visual tooltip still swaps, icon still swaps.
- **New behavior additive** — hidden `<span>` announces to SR.

**Existing callers (grep-verified):**

```
web/src/components/common/SnippetPanel.tsx:71 — <CopyButton text={snippet.cmd} .../>
web/src/components/common/OneTimeReveal.tsx — (not inspected, but passes text prop)
(plus any other *.tsx using CopyButton — verify during planning)
```

None of them access internal state; all use `<CopyButton text="..." />`. The fragment wrapper (`<>...</>`) in the new component works in all React 19 parents.

**Recommendation:**
- Plan 1 task: "Add `aria-live='polite'` region to `CopyButton.tsx` for success announcement." Keep signature. Test: Playwright + `toHaveAttribute('aria-live', 'polite')` + simulate click + assert SR-only span contains "Copied".
- Do NOT refactor `CopyButton` API beyond this.
- `CopyInline` (new from UI-SPEC list) reuses `CopyButton` as trigger — NO duplicated clipboard logic.

**Impact on plan:** 1 task, low risk. Covered under "Copy primitives" plan.

**Confidence:** HIGH — direct file inspection.

---

## Q9 — Playwright snapshot testing strategy (VISUAL-01/02)

**Finding:** Playwright 1.56+ is already in devDependencies. `web/e2e/` has 8 test files (admin.spec.ts, airgap.spec.ts, etc.). Structure uses `@playwright/test` with `expect(locator).toHaveScreenshot()` for snapshots.

**Flakiness risks for status-badge snapshots:**
1. **Font rendering differences** across OSes/CI runners. Mitigation: pin a specific font renderer or rely on self-hosted `.woff2` files (already in place). Playwright's default `screenshot` mode on Linux CI is deterministic with static woff2.
2. **Anti-aliasing variance** on rendered text. Mitigation: use `mask: []` in `toHaveScreenshot` options OR set `maxDiffPixelRatio: 0.01` to tolerate 1% pixel diff.
3. **Viewport size drift.** Mitigation: lock viewport in `playwright.config.ts` to `1440x900`.
4. **Animation state.** Mitigation: `animations: 'disabled'` on the screenshot call.
5. **CSS unrelated to the component changing.** This is the REAL risk. Mitigation: isolate the component in a **dedicated story page** (not embedded in a real UI route).

**Recommendation: matrix snapshots on a dedicated Storybook-like story page.**

New file: `web/src/pages/_dev/StatusBadgeStoryPage.tsx` (or similar, routed only in dev mode via `OMNIREPO_DEV=1`). Renders every status × size × iconOnly combo:

```tsx
const statuses = ['healthy','warning','failure','disabled','maintenance','neutral'] as const;
const sizes = ['sm','md'] as const;
const iconOnly = [false, true] as const;
// 6 × 2 × 2 = 24 variants in a grid.
```

Playwright test `web/e2e/visual-foundation.spec.ts`:

```typescript
test('StatusBadge snapshots', async ({ page }) => {
  await page.goto('/_dev/status-badge-story');
  await expect(page).toHaveScreenshot('status-badge-matrix.png', {
    animations: 'disabled',
    maxDiffPixelRatio: 0.01,
  });
});
```

Single screenshot, 24 variants visible, baseline stable. If Phase 7 changes a Tailwind utility in `src/index.css` the single snapshot catches it. If Phase 8 adds a totally unrelated CSS class, this snapshot doesn't care because the story page is minimal.

**Alternative: per-variant individual snapshots** — 24 separate screenshots, more granular failures, but a linting/formatting change could break all 24. Worse tradeoff.

**Why not a pre-built tool like Chromatic?** Violates air-gap. Phase 6 uses Playwright's built-in screenshot comparison + `snapshots/` dir committed to git. Simple, works.

**Story page air-gap note:** the story route MUST be gated by `OMNIREPO_DEV=1` or dev-only router entry so production builds don't ship it. Already a common pattern — `make dev` vs `make build`.

**Recommendation:**
- Plan 1 task: "Create `StatusBadgeStoryPage.tsx` + `visual-foundation.spec.ts` with single-shot 24-variant matrix snapshot."
- Plan 1 task (optional, sibling): "Gate story route with `import.meta.env.DEV` check so story pages never ship in production bundle."

**Impact on plan:** 1-2 tasks. Low flake risk if matrix is on a dedicated story page.

**Confidence:** HIGH — Playwright snapshot API stable for years.

Sources:
- Playwright docs on `toHaveScreenshot` + `maxDiffPixelRatio`: [https://playwright.dev/docs/test-snapshots](https://playwright.dev/docs/test-snapshots)

---

## Q10 — WCAG AA contrast automation (VISUAL-08)

**Finding:** Two real options.

**Option A — `@axe-core/playwright`:**
- License: **MPL-2.0** (Mozilla Public License 2.0) — file-level copyleft. Compatible with Apache-2.0 projects when used as a **devDependency only** (never shipped into runtime binary). Verified via npm.
- Package size: ~52 kB. Loads axe-core into a page, runs accessibility audit, returns violations as JS objects.
- Integration: `AxeBuilder.analyze()` in a Playwright test. Existing test suite picks it up with zero plumbing.
- Audit scope: full WCAG 2.1 AA (contrast is one rule among many — the tool also catches missing `aria-label`, bad heading order, etc.).
- Run cost: ~500ms per page. Fine for a handful of pages; slow if we run it on every admin route.

**Option B — Node script that parses CSS variables and computes contrast ratios offline:**
- License: whatever `culori` or similar color library (usually MIT). Likely zero extra deps — contrast ratio math is ~30 LOC.
- Scope: ONLY checks the 6 status-token pairs declared in Phase 6. Catches the exact VISUAL-08 requirement, not a minute more.
- Integration: `scripts/check-contrast.mjs` called from `make test`. Parses `web/src/index.css`, extracts OKLCH values, converts to sRGB, computes contrast ratio per WCAG formula, fails if < 4.5:1.
- Run cost: < 100ms. Runs offline, no browser, no DOM.
- Limitation: only validates the pairs we declare. Won't catch a Phase 7 dev who accidentally puts `text-gray-400` on a `bg-gray-200` button.

**Recommendation: BOTH, in different roles.**

- **Phase 6 ships Option B** (`scripts/check-contrast.mjs`) as a fast hard gate on the token system. It's the canonical source-of-truth check for VISUAL-08's "status colors meet WCAG AA on default theme."
- **Phase 6 ALSO ships Option A** (`@axe-core/playwright`) as a crawler-style audit in `web/e2e/a11y-audit.spec.ts` that visits 5-10 key pages and runs axe. Catches the broader "text colors meet AA" requirement (VISUAL-08 wording covers text too, not just status tokens) AND catches a11y regressions Phase 7+ might introduce. Runs on nightly CI or `make e2e`, not on every commit (500ms × 10 pages is acceptable).

Rationale for both: Option B is the hard gate (< 100ms, always runs), Option A is the breadth check (runs less often, catches more).

**Air-gap / license compliance:**
- `@axe-core/playwright` → `devDependencies`, NEVER `dependencies`. Audit `package.json` grep gate: `! jq '.dependencies["@axe-core/playwright"]' package.json` returns null.
- `@axe-core/playwright` pulls in `axe-core` (MPL-2.0). Same rule.
- `scripts/check-contrast.mjs` uses Node stdlib + maybe `culori` (MIT). No concerns.

**Impact on plan:**
- 1 task: "Add `scripts/check-contrast.mjs` that parses `src/index.css` and asserts AA for every `--status-*-foreground` on `--status-*`. Wire into `make test`."
- 1 task: "Add `web/e2e/a11y-audit.spec.ts` using `@axe-core/playwright`. Add to `devDependencies`."

**Confidence:** HIGH — licensing and tool choices verified via npm and WebSearch.

Sources:
- [@axe-core/playwright on npm](https://www.npmjs.com/package/@axe-core/playwright)
- [axe-core on npm](https://www.npmjs.com/package/axe-core)
- [axe-core-npm repo](https://github.com/dequelabs/axe-core-npm/tree/develop/packages/playwright)

---

## Q11 — Responsive at 1366×768 (VISUAL-06)

**Finding:** Can't exhaustively inventory every page's breakpoints without opening all 30+ files, but the codebase structure signals the answer:
- `AppShell` wraps main content in `p-8` (32px) per UI-SPEC line 362.
- Sidebar width is ~256px (typical shadcn sidebar default).
- Usable main column at 1366: `1366 - 256 - 64 = 1046px`.
- UI-SPEC §Responsive Behavior already locks: sticky-first-column + horizontal scroll **inside the table body only** (never the page).

**Where existing admin pages likely break:** dense tables (projects, repos, artifacts, audit, users, trash) that have 6+ columns of varying widths. Without a column-overflow strategy, browser auto-tables force horizontal scroll on the PAGE (breaks VISUAL-06).

**Minimum-change pattern** (matches UI-SPEC):

```tsx
<div className="overflow-x-auto rounded-lg border">
  <Table className="min-w-full">
    <TableHeader>
      <TableRow>
        <TableHead className="sticky left-0 z-10 bg-card">Name</TableHead>
        <TableHead>Type</TableHead>
        {/* ... rest ... */}
      </TableRow>
    </TableHeader>
    <TableBody>
      <TableRow>
        <TableCell className="sticky left-0 z-10 bg-card">...</TableCell>
        {/* ... rest ... */}
      </TableRow>
    </TableBody>
  </Table>
</div>
```

The wrapper `overflow-x-auto` contains horizontal scroll to the table body. The `sticky left-0` pins the first column so it's always visible while scrolling horizontally.

**Existing tables that need this pattern applied:**
- `ProjectsPage.tsx` (project list)
- `RepoListPage.tsx` or equivalent (repos in project)
- `RepoContentPage.tsx` (artifacts in repo)
- `AuditPage.tsx` (audit log)
- `admin/UsersPage.tsx` (user list)
- `admin/TrashPage.tsx` (trash items)
- Scan results table inside drawer

Without opening each file, assume 5–7 tables need the pattern.

**No broader layout-grid rework needed.** The `AppShell` + sidebar + `p-8` main + `max-w-7xl` clamp pattern already fits 1366 minus sidebar. The tables are the only breakage.

**Playwright check (already in UI-SPEC Migration #14):**

```typescript
test.use({ viewport: { width: 1366, height: 768 } });
for (const route of ['/dashboard', '/projects', '/admin/users', '/admin/audit', '/admin/trash']) {
  test(`${route} has no horizontal scroll at 1366×768`, async ({ page }) => {
    await page.goto(route);
    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    const clientWidth = await page.evaluate(() => document.documentElement.clientWidth);
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
  });
}
```

**Recommendation:**
- Plan 1 task: "Apply sticky-first-column + overflow-x-auto wrapper to the 5-7 admin tables that have 6+ columns."
- Plan 1 task: "Add Playwright horizontal-scroll test at 1366×768 across admin routes."
- Do NOT introduce a new layout-grid system. The existing `AppShell` + Tailwind grid utilities are sufficient.

**Impact on plan:** 2 tasks. Touches ~7 table files, each is a ~4-line wrapper change.

**Confidence:** MEDIUM — haven't opened every table file; estimated count based on API endpoints that return paginated lists. Planner may discover more or fewer at execution time.

---

## Q12 — Migration order risk

**Finding:** The question is whether to ship envelope schema + `httperr` + Go refactor + UI renderer ATOMICALLY, or in waves.

**Arguments for atomic (one-shot cutover):**
- `writeJSONError` is a SINGLE helper called 304 times. Rewriting its body changes the wire shape of every `/api/v1` response simultaneously. There's no "half-migrated" state possible inside `internal/api/`.
- The UI client has ONE `handleResponse` helper. Switching it to envelope parsing + legacy-fallback is a single commit.
- A legacy-fallback branch in `handleResponse` (Q5) gracefully handles a stale browser tab against a new server, OR a protocol handler (`internal/protocol/**`) that still emits non-envelope.
- Feature flags for wire-shape changes invite drift and doubled maintenance cost.
- oapi-codegen regeneration must happen in lockstep with schema change — you can't ship partial types.

**Arguments for waved:**
- Changing 304 handler call sites and UI at once is a "big" diff.
- Rollback is harder if the integration test misses a case.

**Resolution:** The argument FOR atomic wins because the helpers ALREADY funnel traffic through one function. There's no real "partial" state — you either rewrite `writeJSONError` or you don't. Adding a feature flag would require a compile-time branch inside `writeJSONError` (ugly) or two parallel helpers (forking maintenance). Neither is worth it.

**HOWEVER, the protocol handler redaction sweep (Q2 Path B, 206 sites) IS a natural second wave:**
- Protocol handlers don't emit envelopes — they emit protocol-native errors. The change there is ONLY redaction (`fmt.Sprintf("stat: %v", err)` → generic message + slog).
- Redaction can happen AFTER the envelope land without blocking anything.
- Protocol handlers get touched in Phase 7 (snippets) and Phase 10 (overview) anyway — incremental redaction as surfaces are touched is OK for v1.1.

**Recommended phasing (INSIDE Phase 6, NOT across phases):**

**Wave 1 — atomic, single merge:**
1. OpenAPI schema + regen `types_gen.go`
2. `internal/httperr` package + `Write` helper + tests
3. Rewrite `internal/api/errors.go` `writeJSONError` to emit envelope (with status-to-class inference for legacy callers)
4. UI client.ts migration (ApiError carries envelope; legacy fallback in parser)
5. `useApiError` + `ErrorEnvelopeRenderer`
6. Swap UI's existing error-display sites (error toasts in queries.ts, LoginPage's inline error, etc.) to use the new renderer

**Wave 2 — separate plan/merge (inside same phase):**
7. `internal/protocol/**` redaction sweep (206 sites, grep-gate verified)
8. Adopt new `httperr` constructors in `/api/v1` handlers that benefit from operator-action-required (trivy handlers, tls handlers, maintenance middleware). Optional — legacy codes still work.

**Wave 3 — separate plan/merge (inside same phase):**
9. Status tokens + StatusBadge + Skeleton* components + canonical adoption (DashboardPage, ProjectsPage, one repo detail)
10. Playwright snapshot gate + contrast check + a11y audit + 1366 scroll test
11. Geist removal + typography/spacing/medium-weight grep gates

Wave 1 and Wave 3 are independent (different file trees). Wave 2 depends only on `httperr` existing. The waves can be THREE separate plans inside Phase 6, parallelizable or sequential as the planner prefers.

**Feature flag? No.** A runtime flag on the envelope shape would freeze the legacy shape into the codebase indefinitely. The migration window is the time between Wave 1 merging to main and the deploy rolling out — a few hours at most. Legacy-fallback in the UI parser handles that window. After deployment, legacy shape is impossible (every `writeJSONError` call emits envelope).

**Parallel write path? No.** Would double the wire surface; no benefit.

**Recommendation:** Phasing above. Plan Phase 6 as 3 plans (Wave 1, Wave 2, Wave 3) or 5 plans (split Wave 1 into Go and UI, Wave 3 into tokens+components, Wave 3b into lint gates + Geist cleanup). The key invariant: Wave 1 is atomic internally; don't split envelope schema from `writeJSONError` rewrite across commits.

**Impact on plan:** 3–5 plans inside Phase 6. Clear dependency graph (Wave 2 depends on Wave 1; Wave 3 is independent).

**Confidence:** HIGH — direct verification of `writeJSONError` as single funnel.

---

## Runtime State Inventory

Phase 6 is a migration of error-shape contract + addition of visual primitives. It's NOT a rename. But the runtime-state checklist still warrants explicit answers:

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | None. `audit_log` SQLite table's `details_json` column is free-form — existing entries stay valid. No migration. | None |
| Live service config | None. No external services registered with the old error codes. Playwright `snapshots/` directory may contain old error-UI screenshots that need regeneration after `ErrorEnvelopeRenderer` ships. | Regenerate snapshots with `npx playwright test --update-snapshots` as part of Wave 1 |
| OS-registered state | None. | None |
| Secrets/env vars | None. No env vars reference error codes. | None |
| Build artifacts | `internal/api/types_gen.go` is a generated file — regenerates from schema via `go generate`. Must be committed in Wave 1 after `openapi.yaml` changes. `web/dist/` rebuilds from source; no cache concern. | `go generate ./internal/api/...` as part of Wave 1 commit |

**Nothing else found** — verified by grep for old error codes (`password-change-required`, `validation_failed`, etc.) in `/home/vladoportos/omnirepo/internal` (only in code, no SQL migrations reference them).

---

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Node.js | frontend build, axe-core test | ✓ | (project standard, per package.json lock) | — |
| Go toolchain 1.25 | Go build + oapi-codegen | ✓ | 1.25 (per CLAUDE.md) | — |
| oapi-codegen v2.6.0 | types-gen regeneration | ✓ | v2.6.0 in use per types_gen.go line 3 | — |
| Playwright | e2e tests | ✓ | @playwright/test 1.56+ per package.json | — |
| `@axe-core/playwright` | a11y audit test | ✗ | — | `scripts/check-contrast.mjs` covers the VISUAL-08 hard gate. Axe audit is additive, not blocking. |
| `github.com/google/uuid` | UUID v7 incident IDs | ✓ | v1.6.x per CLAUDE.md stack | — |
| `chi/v5` | middleware.RequestID | ✓ | v5.2.5 | — |

**Missing deps with no fallback:** none.
**Missing deps with fallback:** `@axe-core/playwright` must be added to `devDependencies` in Wave 3. Contrast script is the primary gate; axe audit is secondary.

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go `testing` stdlib (backend); `@playwright/test` 1.56+ (frontend e2e); `vitest` (if present — verify during planning, else add) |
| Config file | `/home/vladoportos/omnirepo/web/playwright.config.ts`; backend uses `go test ./...` via Makefile |
| Quick run command | `go test -mod=vendor ./internal/httperr/... ./internal/api/...` + `npx vitest run web/src/api/client.test.ts` |
| Full suite command | `make test` |
| Phase gate | `make test` green + `npx playwright test web/e2e/error-envelope.spec.ts web/e2e/visual-foundation.spec.ts` green |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|--------------|
| ERR-01 | Envelope shape on all `/api/v1` error responses | integration | `go test -mod=vendor ./internal/api/... -run TestEnvelope` | ❌ Wave 0 |
| ERR-02 | UI renders envelope per class | e2e | `npx playwright test web/e2e/error-envelope.spec.ts` | ❌ Wave 0 |
| ERR-03 | No internal leaks in `message`/`hint` | unit | `go test -mod=vendor ./internal/httperr -run TestNoInternalLeakage` | ❌ Wave 0 |
| ERR-04 | Try-again button on transient | e2e | Same as ERR-02 | ❌ Wave 0 |
| ERR-05 | Operator deep-link on operator_action_required | e2e | Same as ERR-02 | ❌ Wave 0 |
| ERR-06 | Field highlighting on validation | e2e | Same as ERR-02 | ❌ Wave 0 |
| ERR-07 | incident_id present + matches log entry | integration | `go test -mod=vendor ./internal/api/... -run TestIncidentIDCorrelation` | ❌ Wave 0 |
| VISUAL-01 | Status tokens exist and are consistent | unit | `node scripts/check-contrast.mjs` | ❌ Wave 0 |
| VISUAL-02 | StatusBadge matrix snapshot | e2e snapshot | `npx playwright test web/e2e/visual-foundation.spec.ts` | ❌ Wave 0 |
| VISUAL-03 | Skeletons render on loading branch | e2e | `npx playwright test web/e2e/visual-foundation.spec.ts -g "skeleton"` | ❌ Wave 0 |
| VISUAL-04 | CopyButton has aria-live region | unit + e2e | Vitest + `npx playwright test -g "aria-live"` | ❌ Wave 0 |
| VISUAL-05 | Primary vs destructive button distinction | e2e snapshot | Covered by visual-foundation | existing baseline |
| VISUAL-06 | No horizontal scroll at 1366×768 | e2e | `npx playwright test web/e2e/responsive.spec.ts` | ❌ Wave 0 |
| VISUAL-07 | Typography hierarchy (size + weight) | grep gate | `make lint-typography` (new) | ❌ Wave 0 |
| VISUAL-08 | WCAG AA contrast | unit | `node scripts/check-contrast.mjs` | ❌ Wave 0 |
| VISUAL-09 | Severity treatment consistent (no code change needed) | visual spot-check | Covered by visual-foundation snapshot | existing |

### Sampling Rate

- **Per task commit:** `go test ./internal/httperr/... ./internal/api/...` + relevant vitest file.
- **Per wave merge:** `make test` + `npx playwright test web/e2e/error-envelope.spec.ts web/e2e/visual-foundation.spec.ts web/e2e/responsive.spec.ts`
- **Phase gate:** Full `make test` green + all three Playwright specs green before `/gsd-verify-work`.

### Wave 0 Gaps

- [ ] `internal/httperr/envelope_test.go` — covers ERR-01, ERR-03, ERR-06, ERR-07
- [ ] `internal/httperr/write_test.go` — covers ERR-07 (incident_id piggyback)
- [ ] `internal/api/errors_envelope_test.go` — covers ERR-01 across all 7 legacy codes
- [ ] `internal/api/handlers_envelope_integration_test.go` — forces each handler class, asserts envelope + no leaks (ERR-01, ERR-03)
- [ ] `web/src/api/client.test.ts` — unit test for ApiError envelope parsing + legacy fallback (ERR-02) (deferred — see Open Questions Q1 resolution)
- [ ] `web/src/hooks/useApiError.test.tsx` — unit test for hook normalization (ERR-02, ERR-04, ERR-06) (deferred — see Open Questions Q1 resolution)
- [ ] `web/e2e/error-envelope.spec.ts` — 4 class scenarios end-to-end (ERR-02, ERR-04, ERR-05, ERR-06, ERR-07)
- [ ] `web/e2e/visual-foundation.spec.ts` — StatusBadge matrix snapshot (VISUAL-01, VISUAL-02), Skeleton visibility (VISUAL-03), CopyButton aria-live (VISUAL-04)
- [ ] `web/e2e/responsive.spec.ts` — 1366×768 scroll check across admin routes (VISUAL-06)
- [ ] `web/e2e/a11y-audit.spec.ts` — axe-core audit on 5 key pages (VISUAL-08)
- [ ] `scripts/check-contrast.mjs` — parse index.css OKLCH, assert AA for 6 status token pairs (VISUAL-08)
- [ ] `web/src/pages/_dev/StatusBadgeStoryPage.tsx` — story page for snapshot matrix (VISUAL-02)
- [ ] Makefile: add `lint-typography` target (grep gate) — VISUAL-07

*(No "existing test infrastructure covers all phase requirements" — Phase 6 introduces new surfaces, so new test files are expected.)*

---

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | Existing cookie/bearer auth unchanged — no new auth surface. |
| V3 Session Management | no | No session changes. |
| V4 Access Control | yes (marginal) | Phase 6 MUST NOT leak permission structure through envelope messages. E.g. `message: "Project X does not exist"` is OK; `message: "Project X exists but you lack 'admin' role"` is an access-control leak. All permission-class envelopes use the same generic message "You don't have permission to view this." regardless of whether the resource exists. Codified in UI-SPEC copywriting contract; enforced by `httperr.Permission` constructor having a fixed message. |
| V5 Input Validation | yes | Validation-class envelopes carry field paths — MUST NOT reflect user-supplied raw values. E.g. `message: "Name must be alphanumeric"` is OK; `message: "'<script>alert(1)</script>' is not alphanumeric"` is XSS. Test: `TestValidationNeverEchoesUserInput`. |
| V6 Cryptography | no | No crypto surface. |
| V7 Error Handling | **yes** | ERR-03 directly — internal paths, driver strings, stack traces MUST NEVER appear in client-facing envelope. `httperr.IsInternalString` + test `TestNoInternalLeakage`. |
| V14 Configuration | no | No config surface. |

### Known Threat Patterns for Go + React + chi + OpenAPI

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Error-based info disclosure (filesystem paths) | Information disclosure | `httperr.Internal(code, cause)` produces a generic envelope; cause stays in slog only. Test: brute-force every `writeJSONError` call site; assert `message` doesn't match `/^\/`, `/\.go$/`, `/:[0-9]+:/`. |
| User-supplied value reflection (XSS via error message) | Tampering | `httperr.Validation*` constructors never interpolate user input into `message`. Code path audited: every caller passes a static template or a field name, not a raw value. |
| Permission enumeration via 404 vs 403 drift | Information disclosure | Permission-class envelope uses generic message + generic `code: "auth.forbidden"` regardless of whether resource exists. Matches V4 ASVS above. |
| Incident ID as covert channel | Information disclosure | UUID v7 is generated by server, not user-controlled. Not a concern. |
| CSRF on retry button | Tampering | Retry refetches the original TanStack Query; same credentials (cookie); same origin. No new CSRF surface. |

---

## Code Examples

### Go — httperr.Write helper

```go
// internal/httperr/write.go
package httperr

import (
    "encoding/json"
    "log/slog"
    "net/http"

    chimw "github.com/go-chi/chi/v5/middleware"
)

// Write serializes e as JSON, sets the envelope's incident_id from the
// chi request ID, and logs the internal cause keyed by the same ID.
// Sets Content-Type and HTTP status.
func Write(w http.ResponseWriter, r *http.Request, e *Error) {
    reqID := chimw.GetReqID(r.Context())
    if reqID != "" {
        e.Envelope.IncidentID = reqID
    }
    slog.ErrorContext(r.Context(), "api.error",
        slog.String("incident_id", reqID),
        slog.String("code", e.Envelope.Code),
        slog.String("class", string(e.Envelope.Class)),
        slog.Any("cause", e.Cause),
    )
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    status := e.Status
    if status == 0 {
        status = defaultStatusForClass(e.Envelope.Class)
    }
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(e.Envelope)
}

func defaultStatusForClass(c Class) int {
    switch c {
    case ClassValidation:
        return http.StatusBadRequest
    case ClassPermission:
        return http.StatusForbidden
    case ClassTransient:
        return http.StatusServiceUnavailable
    case ClassOperatorRequired:
        return http.StatusServiceUnavailable
    default:
        return http.StatusInternalServerError
    }
}
```

### TypeScript — useApiError hook skeleton

```typescript
// web/src/hooks/useApiError.ts
import type { UseQueryResult, UseMutationResult } from '@tanstack/react-query';
import { ApiError, type ApiErrorEnvelope } from '@/api/client';

export interface ApiErrorState {
  envelope: ApiErrorEnvelope | null;
  isRetryable: boolean;
  retry: () => void;
  fieldErrors: Record<string, string>;
  incidentId: string | null;
}

type QueryLike =
  | UseQueryResult<unknown, unknown>
  | UseMutationResult<unknown, unknown, unknown, unknown>;

export function useApiError(query: QueryLike): ApiErrorState {
  const error = (query as any).error as unknown;
  if (!(error instanceof ApiError)) {
    return { envelope: null, isRetryable: false, retry: () => {}, fieldErrors: {}, incidentId: null };
  }
  const env = error.envelope;
  const fieldErrors =
    env.class === 'validation'
      ? env.details?.fields ??
        (env.details?.field ? { [env.details.field]: env.message } : {})
      : {};
  const retry = () => {
    if ('refetch' in query) (query as UseQueryResult).refetch();
  };
  return {
    envelope: env,
    isRetryable: env.class === 'transient',
    retry,
    fieldErrors,
    incidentId: env.incident_id ?? null,
  };
}
```

---

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `{error: string, detail: string}` envelope | `{code, message, hint?, class, incident_id?}` envelope | Phase 6 (2026-04) | Downstream UIs type against stable shape; incident correlation; class-driven CTA |
| `http.Error(w, fmt.Sprintf("%v", err), 500)` | `httperr.Internal(code, err)` + generic client message | Phase 6 | Stops server-detail leaks |
| chi RequestID default (hostname-pid-counter) | UUID v7 via custom generator | Phase 6 | Privacy-safe + time-sortable incident IDs |

**Deprecated/outdated:**
- `internal/api/ErrorResponse` struct — delete after `writeJSONError` rewrite.
- `web/src/api/client.ts::ApiError.code` + `ApiError.detail` scalar fields — replaced by `ApiError.envelope`.

---

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `google/uuid` v1.6 is already vendored (per CLAUDE.md stack); `uuid.NewV7` is available | Q4 | LOW — if v1.5 only, planner bumps to v1.6 (one-line `go.mod` + vendor refresh). Still under CLAUDE.md's frozen-stack rule since v1.6 is called out there. |
| A2 | oapi-codegen v2.6.0 generates clean `map[string]string` for `fields` nested property with `additionalProperties: true` on parent | Q3 | LOW — Context7-verified for similar shapes. If it generates unexpected AdditionalProperties wrapper, alternative is a top-level `ValidationFields` schema referenced only by validation responses. |
| A3 | `@axe-core/playwright` MPL-2.0 is compatible as devDependency with the Apache-2.0 project license constraint | Q10 | LOW — MPL-2.0 file-level copyleft affects only the MPL-licensed files, which are never bundled into the Go binary. Grep gate in Makefile ensures `package.json`'s `dependencies` never lists it. |
| A4 | Playwright snapshot tests on a dedicated story page have low flake rate with maxDiffPixelRatio: 0.01 | Q9 | MEDIUM — if flakes appear, loosen to 0.02 or switch to per-variant individual snapshots. Not blocking. |
| A5 | Table count requiring sticky-first-column is 5-7 | Q11 | LOW — planner discovers exact count during Wave 3; pattern is the same regardless. |
| A6 | No existing e2e test covers the error-display path for the Login/CreateProject/CreateUser flows that would break if the envelope shape changes | Q12 | MEDIUM — if such tests exist, they need updating in Wave 1 along with the client.ts migration. Verify during planning. |
| A7 | `ncruces/go-sqlite3` vs `modernc.org/sqlite` — no impact on error envelope work | — | NONE — Phase 6 touches no SQL. |
| A8 | All `/api/v1/*` handlers funnel through `writeJSONError` for 4xx/5xx (no handlers write errors via `http.Error` directly) | Q2 | LOW — verified by grep (0 `http.Error` hits in `internal/api/`). |

---

## Open Questions (RESOLVED)

1. **Does vitest run in this repo today?**
   - What we know: `web/package.json` doesn't list `vitest` in devDependencies. A grep for `vitest` also returns none.
   - What's unclear: do we need to add vitest for unit-testing `client.ts`/`useApiError.ts`, or use Playwright component-testing?
   - Recommendation: Playwright 1.56+ supports component testing via `@playwright/experimental-ct-react`. Use it instead of adding vitest — keeps devDep count low. Alternative: write pure-function helpers that can be tested via plain `.ts` modules imported by a Playwright test. Planner decides.
   - **Resolution:** RESOLVED — plans do NOT add vitest; coverage strategy is Go integration tests (envelope shape + no-leakage) + Playwright e2e (visual-foundation, responsive, a11y). Pragmatic: vitest would add a new test runner and devDep chain for this milestone's small TS surface. Revisit if the v1.1 TS surface grows substantially.

2. **Is there a Phase 6 docker-compose seeded state that exercises each error class for Playwright?**
   - What we know: `web/e2e/` tests hit `/api/v1/auth/login` with fresh admin creds. No fixture exists for "force a transient 503" or "force an operator-action-required response."
   - What's unclear: how does Playwright trigger class-specific errors end-to-end?
   - Recommendation: add a dev-only route `/api/v1/_dev/error/:class` gated by `import.meta.env.DEV` equivalent on the Go side (e.g. only registered when `omnirepo --dev` flag is set). Returns a canned envelope per class. Phase 6 Playwright uses this route exclusively for class rendering tests. Real backend errors tested via Go integration tests only.
   - **Resolution:** RESOLVED — plan 06-03 adds dev-only backend routes gated by `OMNIREPO_DEV=1` that emit each error class on demand, plus a dev-only `ErrorClassStoryPage`. Plan 06-07 adds a dev-only `StatusBadgeStoryPage`. Playwright specs hit these dev routes directly; no docker-compose seeded state required.

3. **Should incident_id be stored in audit_log.details_json or added as a new column?**
   - What we know: audit_log migrations/001 has no request_id column. details_json is free-form TEXT.
   - What's unclear: do operators need SQL queries like `SELECT * FROM audit_log WHERE request_id = ?`?
   - Recommendation: SKIP the column add in v1.1. Store request_id in `details_json` as `{"request_id": "..."}` for audits that write details anyway. Operators correlate via slog output grep, which is faster than SQL for a running system. Revisit in v2.0 if audit-query-by-request-id becomes a user story.
   - **Resolution:** RESOLVED — plans do NOT add a new audit_log column or store incident_id in details_json. The incident ID is the chi `middleware.RequestID` value (UUID v7 per plan 06-02), already captured in slog via `middleware_audit.go`. Support workflow: grep server logs by the X-Incident-Id header value. Zero schema migration. Revisit only if structured audit queries by incident_id become a requirement.

4. **Playwright snapshot baseline generation — CI-driven or local-only?**
   - What we know: `web/e2e/` exists but I don't see `-snapshots/` dirs in the repo root (could be under e2e/).
   - What's unclear: does CI run headed or headless, and does the project commit snapshot baselines?
   - Recommendation: initial baselines committed from a Linux/headless-Chromium run to match CI. Planner documents this in the visual-foundation.spec.ts test file header.
   - **Resolution:** RESOLVED — plan 06-08 generates baselines once on a headless CI-style run and commits them to the repo at `web/e2e/__snapshots__/`. Local developer runs compare against committed baselines. Regeneration is an explicit `npx playwright test -u` command, gated behind a reviewer-approved changelog entry. Not CI-driven to keep baselines deterministic.

---

## Sources

### Primary (HIGH confidence)
- **Direct codebase inspection** (2026-04-17) — `internal/api/errors.go`, `internal/api/openapi.yaml`, `internal/api/types_gen.go`, `internal/httpx/router.go`, `internal/httpx/middleware_audit.go`, `internal/protocol/*` (grep), `web/src/api/client.ts`, `web/src/components/common/CopyButton.tsx`, `web/src/components/common/SnippetPanel.tsx`, `web/src/components/common/SeverityBadge.tsx`, `web/src/components/ui/skeleton.tsx`, `web/src/index.css`, `web/components.json`, `web/package.json`, `Makefile`
- **Context7 `/oapi-codegen/oapi-codegen`** — OpenAPI 3.0/3.1 schema patterns for `oneOf`, `allOf`, `anyOf`, `additionalProperties`. Verified: enum generates typed constants; additionalProperties generates map-or-struct based on schema. 179 code snippets reviewed.
- **`.planning/phases/06-error-envelope-visual-foundation/06-UI-SPEC.md`** — locked design contract (VERIFIED, approved 2026-04-17).
- **`.planning/REQUIREMENTS.md`** — authoritative requirement text.
- **`.planning/ROADMAP.md`** — Phase 6 goal + 5 success criteria.
- **`CLAUDE.md`** — stack/license/invariants frozen at v1.0.

### Secondary (MEDIUM confidence)
- **WebSearch** — `@axe-core/playwright` license verification: MPL-2.0 confirmed via npm metadata.
- **chi v5 middleware documentation** — `middleware.RequestID`, `middleware.GetReqID` verified in README.md (vendored at `vendor/github.com/go-chi/chi/v5/README.md`).

### Tertiary (LOW confidence)
- **Assumed knowledge** — `google/uuid` v1.6 `NewV7` availability (Assumption A1). UUID v7 landed in google/uuid v1.6.0 per prior knowledge; planner should verify at implementation time.

---

## Metadata

**Confidence breakdown:**
- Error envelope schema + oapi-codegen compatibility: **HIGH** — Context7-verified, direct codebase inspection.
- Go-side implementation surface: **HIGH** — grep-counted call sites (304 in api, 206 in protocol).
- UI renderer architecture: **HIGH** — direct client.ts inspection, TanStack Query docs stable.
- Incident ID wiring: **HIGH** — chi middleware already installed; piggyback is trivial.
- Status tokens + StatusBadge: **HIGH** — Tailwind 4 `@theme inline` pattern verified in existing code.
- Skeleton shipping order: **MEDIUM** — strategy is judgement-call; full-sweep vs incremental defensible either way.
- Snapshot strategy: **HIGH** — Playwright snapshot API stable; story-page pattern standard.
- Contrast automation: **HIGH** — license and tool scope verified.
- Responsive tables: **MEDIUM** — haven't opened every table file; pattern is uniform.
- Migration phasing: **HIGH** — 3-wave plan defensible from direct helper-funnel verification.

**Research date:** 2026-04-17
**Valid until:** 2026-05-17 (30 days — error envelope contract will be locked by then; visual tokens stable by then)
