# Phase 6: Error Envelope & Visual Foundation — Pattern Map

**Mapped:** 2026-04-17
**Files analyzed:** 38 new/modified
**Analogs found:** 34 / 38 (4 greenfield)

> Phase 6 is a MIGRATION (error-shape contract) + ADDITION (visual primitives). Most new files have a close v1.0 analog in the same package/dir. Protocol redaction is a sweep across 29 files with one canonical excerpt. Frontend primitives nearly all have a v1.0 sibling under `web/src/components/common/` that already commits to the same Tailwind-utility + Base-UI conventions.

---

## File Classification

### Backend (Go) — new primitive + migration

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `internal/httperr/envelope.go` | utility (new pkg) | error-construction | `internal/api/errors.go` | exact (role + flow) |
| `internal/httperr/write.go` | utility (new pkg) | request-response error writer | `internal/api/errors.go::writeJSONError` | exact |
| `internal/httperr/envelope_test.go` | test | table-driven unit | `internal/api/admin_trash_test.go` | role-match |
| `internal/httperr/write_test.go` | test | HTTP recorder | `internal/api/api_test.go` helpers | role-match |
| `internal/api/errors.go` (rewrite) | utility (modify) | request-response error writer | self (prior commit) | exact |
| `internal/api/openapi.yaml` (extend) | config (modify) | schema source | self (existing `/auth/login` 401 inline `{error,detail}`) | exact |
| `internal/api/types_gen.go` (regen) | generated code | build artifact | self (produced by `go generate`) | exact (regen only) |
| `internal/api/handlers_envelope_integration_test.go` | test (new) | HTTP integration | `internal/api/admin_trash_test.go` | exact |
| `internal/api/errors_envelope_test.go` | test (new) | unit | `internal/api/admin_trash_test.go` helpers | role-match |
| `internal/httpx/router.go` (modify) | config (modify) | middleware install | self | exact |
| `internal/httpx/middleware_audit.go` (read, no change) | reference | — | self | N/A |
| `internal/httpx/middleware_envelope.go` (new) | middleware | request-response panic→envelope | `internal/httpx/middleware_audit.go` | role-match |
| `internal/protocol/raw/get.go` (redact) | handler (modify) | protocol-native response | self — grandfathered redaction sweep | exact (mechanical) |
| `internal/protocol/raw/delete.go`, `raw/listing.go` (redact) | handler (modify) | protocol-native response | `raw/get.go` | exact |
| `internal/protocol/rpm/{get,put}.go` (redact) | handler (modify) | protocol-native response | `raw/get.go` | exact |
| `internal/protocol/{deb,pypi,helm,oci,s3,git}/*.go` (redact, ~25 files) | handler (modify) | protocol-native response | `raw/get.go` | exact |

### Frontend (TypeScript/React) — new primitives + migration

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `web/src/api/client.ts` (modify) | service (modify) | fetch → envelope parse | self | exact |
| `web/src/api/client.test.ts` (new) | test | unit | *(no existing vitest — greenfield)* | **none** |
| `web/src/hooks/useApiError.ts` (new) | hook | query → normalized state | `web/src/hooks/useAuth.ts` | role-match |
| `web/src/hooks/useApiError.test.tsx` (new) | test | unit | *(no existing vitest — greenfield)* | **none** |
| `web/src/components/common/ErrorEnvelope.tsx` (new) | component | renderer (props → UI) | `web/src/components/common/SeverityBadge.tsx` + `OneTimeReveal.tsx` | role-match (Badge icon+color map) + partial (alert panel) |
| `web/src/components/common/StatusBadge.tsx` (new) | component | props → Badge primitive | `web/src/components/common/SeverityBadge.tsx` | exact |
| `web/src/components/common/SkeletonCard.tsx` (new) | component | composition over `@/components/ui/skeleton` | `web/src/components/ui/skeleton.tsx` | role-match |
| `web/src/components/common/SkeletonTable.tsx` (new) | component | composition | `web/src/components/ui/skeleton.tsx` | role-match |
| `web/src/components/common/SkeletonDetail.tsx` (new) | component | composition | `web/src/components/ui/skeleton.tsx` | role-match |
| `web/src/components/common/SkeletonMetric.tsx` (new) | component | composition | `web/src/components/common/StorageGauge.tsx` (layout) + skeleton | partial |
| `web/src/components/common/CopyInline.tsx` (new) | component | wrapper over `CopyButton` | `web/src/components/common/OneTimeReveal.tsx` (inline pre+button pattern) | exact |
| `web/src/components/common/CopyButton.tsx` (modify) | component (modify) | adds `aria-live` region | self | exact |
| `web/src/index.css` (extend) | config (modify) | token declarations | self (existing `:root` + `.dark` blocks) | exact |
| `web/src/pages/_dev/StatusBadgeStoryPage.tsx` (new) | page (dev-only) | matrix render | `web/src/pages/DashboardPage.tsx` (grid layout) | partial |
| `web/src/App.tsx` (modify) | config (modify) | route registration (dev-gated) | self | exact |

### Test + migration tooling

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `scripts/check-contrast.mjs` (new) | utility (offline script) | CSS parse → WCAG math | *(no existing node scripts under `scripts/` — greenfield)* | **none** |
| `web/e2e/error-envelope.spec.ts` (new) | e2e test | Playwright scenarios | `web/e2e/admin.spec.ts` | exact |
| `web/e2e/visual-foundation.spec.ts` (new) | e2e snapshot test | Playwright screenshot | `web/e2e/dashboard.spec.ts` + Playwright `toHaveScreenshot` docs | role-match |
| `web/e2e/responsive.spec.ts` (new) | e2e test | viewport + scroll check | `web/e2e/admin.spec.ts` | role-match |
| `web/e2e/a11y-audit.spec.ts` (new) | e2e test | axe audit | `web/e2e/admin.spec.ts` + `@axe-core/playwright` README | partial |
| `web/package.json` (modify — devDep add) | config (modify) | — | self | exact |
| `Makefile` (modify — add `lint-typography`, wire `check-contrast.mjs`) | config (modify) | — | self (existing `grep-cdn` target) | exact |

### Cleanup tasks (no analog needed — mechanical)

| Modified File | Role | Action | Analog |
|---------------|------|--------|--------|
| `web/src/index.css` line 4 | config | delete `@import "@fontsource-variable/geist";` | none (one-line deletion) |
| `web/package.json` (`dependencies`) | config | delete `@fontsource-variable/geist` entry | none |

---

## Pattern Assignments

### `internal/httperr/envelope.go` (new utility package)

**Analog:** `internal/api/errors.go` (lines 1–28) — the existing `ErrorResponse` struct + stable-code constants. Same *shape* (tagged JSON struct, exported constants). Phase 6's package is the *public* version of what `internal/api/errors.go` had as *package-private* helpers.

**Package header + constants pattern** (analog: `internal/api/errors.go:1-28`):
```go
// Package api hosts the hand-written HTTP handlers for OmniRepo's REST surface
// (D-36). Types live in types_phase1.go; handlers live in admin_phase1.go;
// this file defines the shared JSON error envelope + response helpers used by
// every endpoint.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ErrorResponse is the canonical JSON error envelope (D-36).
type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

// Stable error codes consumed by UI and tests.
const (
	ErrPasswordChangeRequired = "password-change-required"
	ErrUnauthenticated        = "unauthenticated"
	ErrForbidden              = "forbidden"
	// ...
)
```

**New file follows the same structure:** package doc, one exported struct (`Envelope`), one exported interface (`Error` with `Unwrap`), exported `Class` type + constants, exported option type + helpers. Keep exported surface small — constructors only.

**Core pattern to copy — tagged JSON struct with `omitempty` on optional fields:**
```go
type Envelope struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Hint       string         `json:"hint,omitempty"`        // matches ErrorResponse.Detail's omitempty
	Class      Class          `json:"class"`
	IncidentID string         `json:"incident_id,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}
```

**Note:** field names must match `internal/api/types_gen.go` generated types (once OpenAPI regen runs). Verify with `grep 'ApiErrorEnvelope' internal/api/types_gen.go` after regen.

---

### `internal/httperr/write.go` (new)

**Analog:** `internal/api/errors.go::writeJSONError` (lines 30–35) — the one-shot "set header, write status, encode JSON" helper. New `Write` extends that with request-ID piggyback + slog of the internal cause.

**Imports pattern** (analog: `internal/httpx/middleware_audit.go:1-9`):
```go
package httpx

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)
```

**Request-ID read + slog pattern** (analog: `internal/httpx/middleware_audit.go:15-31`):
```go
// AuditEnter emits an slog "audit.enter" record at the start of the request
// and an "audit.exit" record with the duration at the end.
func AuditEnter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := middleware.GetReqID(r.Context())
		slog.InfoContext(r.Context(), "audit.enter",
			slog.String("request_id", reqID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		)
		next.ServeHTTP(w, r)
		slog.InfoContext(r.Context(), "audit.exit",
			slog.String("request_id", reqID),
			// ...
		)
	})
}
```

**JSON write pattern** (analog: `internal/api/errors.go:30-44`):
```go
// writeJSONError emits a JSON error envelope with status code.
func writeJSONError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: code, Detail: detail})
}
```

**`httperr.Write` composes both:** `reqID := middleware.GetReqID(r.Context())` + `slog.ErrorContext(...)` + existing 3-line JSON write. See RESEARCH.md §"Go — httperr.Write helper" (lines 1038–1087) for the canonical 30-line implementation — copy verbatim into `internal/httperr/write.go`.

---

### `internal/api/errors.go` (rewrite)

**Self-analog.** Only change: replace the body of `writeJSONError` to construct a `httperr.Envelope` via a status→class switch (keeping the existing 4-arg signature for all 304 call sites) + pass through `httperr.Write`. Keep `writeJSON` unchanged. Delete the `ErrorResponse` struct (line 14) after regen.

**Diff sketch:**
```go
// writeJSONError emits a JSON error envelope with status code.
// Phase 6: bridges legacy callers to the ApiErrorEnvelope shape by
// inferring `class` from `status`. Handlers that need explicit class
// control (operator_action_required, validation-with-fields) should
// call httperr.Write directly with a constructed *httperr.Error.
func writeJSONError(w http.ResponseWriter, status int, code, detail string) {
	class := inferClassFromStatus(status)
	httperr.Write(w, r, &httperr.Error{  // NOTE: r is not in scope here — signature must grow
		Status: status,
		Envelope: httperr.Envelope{
			Code:    code,
			Message: detail,
			Class:   class,
		},
	})
}
```

**Signature caveat — must add `*http.Request`:** all 304 call sites pass `w, status, code, detail`. `httperr.Write` needs `r` for the request ID. Two options:
1. Widen `writeJSONError(w, r, status, code, detail)` — mechanical sed across 304 sites.
2. Add `writeEnvelope(w, r, *httperr.Error)` as the new first-class path; keep `writeJSONError` emitting a generic envelope with `IncidentID=""` (no correlation), and have new Phase 6+ handlers use `writeEnvelope` when correlation is needed.

Prefer (1) for ERR-07 fidelity on every error. Plan a single mechanical sweep in Wave 1.

---

### `internal/api/openapi.yaml` (extend)

**Analog:** existing inline 401 response block at lines 973–983 — the "schema is embedded in the operation" pattern that every error response uses today. Phase 6 REPLACES these inline schemas with `$ref` to new shared `components/responses/*` entries.

**Existing inline pattern** (lines 973–983):
```yaml
        "401":
          description: Invalid credentials
          content:
            application/json:
              schema:
                type: object
                properties:
                  error:
                    type: string
                  detail:
                    type: string
```

**New pattern — add once, `$ref` everywhere:**
```yaml
components:
  schemas:
    ApiErrorClass:
      type: string
      enum: [validation, permission, transient, operator_action_required]
    ApiErrorEnvelope:
      # (see RESEARCH.md Q1, lines 95-143, for the full fragment to paste)
  responses:
    ValidationError:
      description: Validation failed
      content:
        application/json:
          schema: { $ref: '#/components/schemas/ApiErrorEnvelope' }
    # ... PermissionError, NotFoundError, TransientError, OperatorActionRequired ...
```

**Then each operation's error block becomes:**
```yaml
        "401":
          $ref: '#/components/responses/PermissionError'
```

Grep gate to verify migration complete: `grep -c 'error:' internal/api/openapi.yaml | awk '$1 == 0 { exit 1 }'` (after cleanup, should only appear inside `ApiErrorEnvelope` schema comment).

---

### `internal/api/handlers_envelope_integration_test.go` (new)

**Analog:** `internal/api/admin_trash_test.go:1-60` — package-external test (`package api_test`), uses `newTestServer(t)`, `seedTestUser`, `s.login`, `s.do` helpers.

**Test setup pattern** (analog: `internal/api/admin_trash_test.go:1-26`):
```go
package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAdminTrash_ListEmpty(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	// ... body assertions ...
}
```

**New tests force each error class via existing endpoints:**
- `TestEnvelope_Validation_Login`: POST `/api/v1/auth/login` with invalid body → assert envelope shape + `class="validation"` + no internal path in `message`.
- `TestEnvelope_Permission_ProjectCreate`: POST `/api/v1/projects` with non-admin cookie → `class="permission"`.
- `TestEnvelope_NotFound_Project`: GET `/api/v1/projects/__nope__` → envelope with `code="not_found"` (mapping to `validation` class per spec-lite).
- `TestEnvelope_NoInternalLeakage`: iterate all error responses, assert `message` doesn't match `/^\//`, `/\.go$/`, `/sqlite/`, `/goroutine/`, `/runtime\./`, `/:[0-9]+:/`.
- `TestEnvelope_IncidentID_MatchesHeader`: make a failing request, assert `body.incident_id == resp.Header.Get("X-Incident-Id")` (or `X-Request-Id`, pending Q4 decision).

Each test uses the exact `s.do(t, method, path, cookie, body)` pattern shown above — zero new test harness.

---

### `internal/httpx/router.go` (modify)

**Self-analog, tiny diff.** Add UUID v7 generator (RESEARCH.md Q4). Current state (lines 28–38):
```go
func New(d Deps) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)     // <— Phase 6 replaces generator here
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(StructuredLogger(d.Config))
	r.Use(AuditEnter)
	r.Use(MaintenanceMode(d.Settings))
	r.Use(AuditExit)
	return r
}
```

Phase 6 swap: before `r.Use(middleware.RequestID)`, set `middleware.RequestIDHeader = "X-Incident-Id"` and wrap with a custom middleware that generates `uuid.Must(uuid.NewV7()).String()` and stashes via `context.WithValue(ctx, middleware.RequestIDKey, id)`. Place new `middleware_envelope.go` in `internal/httpx/` (same package) and register via `r.Use(EnvelopeRecoverer)` *replacing* the generic `middleware.Recoverer`.

---

### `internal/httpx/middleware_envelope.go` (new)

**Analog:** `internal/httpx/middleware_audit.go` — same package convention (`package httpx`), handler-wrapping middleware, slog usage.

**Middleware skeleton to copy** (analog: `middleware_audit.go:15-32`):
```go
func AuditEnter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := middleware.GetReqID(r.Context())
		// ... log, call next, log ...
		next.ServeHTTP(w, r)
	})
}
```

`EnvelopeRecoverer` wraps handlers with `defer func() { if rec := recover(); rec != nil { httperr.Write(w, r, httperr.Internal("api.panic", fmt.Errorf("%v", rec))) } }()`. Import `internal/httperr` — clean boundary since `httpx` doesn't currently import anything from `api`.

---

### `internal/protocol/raw/get.go` (redact — canonical excerpt for 29-file sweep)

**Self-analog.** 206 `http.Error(w, fmt.Sprintf("%v", err), 500)` sites across 29 files. All get the same mechanical redaction. Canonical before/after:

**Before (line 52, and ~206 more identical sites):**
```go
info, err := os.Stat(absPath)
if err != nil {
	if errors.Is(err, os.ErrNotExist) && res.relPath == "" {
		h.listDir(w, r, res, absPath)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, fmt.Sprintf("stat: %v", err), http.StatusInternalServerError)  // LEAKS err chain
	return
}
```

**After — redact `%v`, log real cause:**
```go
info, err := os.Stat(absPath)
if err != nil {
	if errors.Is(err, os.ErrNotExist) && res.relPath == "" {
		h.listDir(w, r, res, absPath)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.ErrorContext(r.Context(), "raw.get.stat_failed",
		slog.String("incident_id", chimw.GetReqID(r.Context())),
		slog.String("path", res.relPath),  // internal path in LOG, not response
		slog.Any("err", err),
	)
	http.Error(w, "storage error", http.StatusInternalServerError)  // generic client-facing
	return
}
```

**Protocol handlers keep emitting protocol-native errors** (not JSON envelope) because `docker pull` / `apt-get` / `pip` don't parse JSON errors. Only the `%v`-interpolation is removed.

**Grep gate after sweep:** `! grep -RE 'http\.Error\(.*%v' internal/protocol/` — must return zero.

---

### `web/src/api/client.ts` (modify)

**Self-analog.** The existing file has every shape Phase 6 needs — `ApiError` class, `handleResponse` helper, `upload` XHR fallback. Migration is in place.

**Current pattern (lines 10–19, 101–114):**
```typescript
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    public detail: string,
  ) {
    super(detail);
    this.name = 'ApiError';
  }
}

// ...

private async handleResponse<T>(res: Response): Promise<T> {
  if (res.status === 401) {
    throw new ApiError(401, 'unauthorized', 'Unauthorized');
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({
      error: 'unknown',
      detail: res.statusText,
    }));
    throw new ApiError(res.status, err.error ?? 'unknown', err.detail ?? res.statusText);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}
```

**Phase 6 migration:** replace `code + detail` scalar props with `envelope: ApiErrorEnvelope`; add `isApiErrorEnvelope(v: unknown)` type guard; legacy `{error, detail}` responses get wrapped into a synthetic envelope (see RESEARCH.md Q5 lines 456–495 for the verbatim migration code).

**XHR upload path (lines 79–94):** identical swap — `ApiError` constructor call at line 89 `new ApiError(xhr.status, err.error ?? 'unknown', err.detail ?? xhr.statusText)` becomes `new ApiError(xhr.status, parsedEnvelope)`.

**Callers to update (grep):** `grep -Rn 'err\.code\|err\.detail' web/src/` finds every site that reads the old scalar fields — switch to `err.envelope.code / err.envelope.message`. Known sites:
- `web/src/pages/LoginPage.tsx:41-47` — reads `err.status` only (already compatible — no field change needed, just re-verify).
- `web/src/api/queries.ts:136` — reads `err.status` only (no change).

---

### `web/src/hooks/useApiError.ts` (new)

**Analog:** `web/src/hooks/useAuth.ts` — same "hook composes existing hooks + returns a convenience object" convention.

**Hook shape pattern to copy** (analog: `useAuth.ts:1-27`):
```typescript
/**
 * Auth hook composing useMe + login/logout/changePassword mutations.
 */

import { useQueryClient } from '@tanstack/react-query';
import { useMe, useLogin, useLogout, useChangePassword } from '@/api/queries';

export function useAuth() {
  const qc = useQueryClient();
  const { data: user, isLoading } = useMe();
  const login = useLogin();
  const logout = useLogout();
  const changePassword = useChangePassword();

  return {
    user: user ?? null,
    isLoading,
    isAuthenticated: !!user,
    mustChangePassword: user?.must_change_password ?? false,
    isSuperAdmin: user?.is_super_admin ?? false,
    login,
    logout,
    changePassword,
    clearCache: () => qc.clear(),
  };
}
```

**`useApiError` follows the same recipe:** top-level JSDoc, typed params, returns a plain object. See RESEARCH.md §"TypeScript — useApiError hook skeleton" (lines 1091–1130) for the verbatim implementation — takes `UseQueryResult | UseMutationResult`, returns `{envelope, isRetryable, retry, fieldErrors, incidentId}`.

---

### `web/src/components/common/ErrorEnvelope.tsx` (new)

**Analogs (two):**
1. **Icon+color lookup pattern** → `web/src/components/common/SeverityBadge.tsx` (the literal `Record<level, styleString>` map).
2. **Alert-panel rendering with icon + text + action** → `web/src/components/common/OneTimeReveal.tsx` (amber-banner pattern inside a container).

**Style-map pattern to copy** (analog: `SeverityBadge.tsx:8-16`):
```typescript
type SeverityLevel = 'critical' | 'high' | 'medium' | 'low' | 'unknown';

const severityStyles: Record<SeverityLevel, string> = {
  critical: 'bg-destructive/10 text-destructive border-destructive/20',
  high: 'bg-orange-500/10 text-orange-600 border-orange-500/20 dark:text-orange-400',
  medium: 'bg-amber-500/10 text-amber-600 border-amber-500/20 dark:text-amber-400',
  low: 'bg-teal-500/10 text-teal-600 border-teal-500/20 dark:text-teal-400',
  unknown: 'bg-muted text-muted-foreground border-border',
};
```

**Phase 6 port — `class → {icon, color token, retryable}` map:**
```typescript
type ErrorClass = 'validation' | 'permission' | 'transient' | 'operator_action_required';

const classStyles: Record<ErrorClass, {
  icon: LucideIcon;
  colorClass: string;     // bg-status-* + text-status-*-foreground + border-status-*-border
  retryable: boolean;
}> = {
  validation:              { icon: AlertCircle,   colorClass: 'bg-status-warning ...',    retryable: false },
  permission:              { icon: Lock,          colorClass: 'bg-status-failure ...',    retryable: false },
  transient:               { icon: RefreshCw,     colorClass: 'bg-status-warning ...',    retryable: true  },
  operator_action_required:{ icon: Wrench,        colorClass: 'bg-status-maintenance ...',retryable: false },
};
```

**Alert-banner composition pattern** (analog: `OneTimeReveal.tsx:60-75`):
```tsx
<DialogDescription>
  <span className="inline-flex items-start gap-2 text-amber-600 dark:text-amber-400">
    <AlertTriangle className="mt-0.5 size-4 shrink-0" />
    {warningText}
  </span>
</DialogDescription>
{/* ... */}
<div className="relative rounded-md bg-muted p-3 pr-10">
  <code className="block break-all font-mono text-sm leading-relaxed select-all">
    {internalSecret}
  </code>
  <CopyButton
    text={internalSecret}
    className="absolute right-1.5 top-1.5"
  />
</div>
```

Use the same inline-flex + icon + text row for the primary message; stack `hint` below; render retry button / operator CTA / incident-id chip with `<CopyButton>` all as flat children in the same outer container. Accessibility: `role="alert"` for failure/permission/operator, `role="status" aria-live="polite"` for validation/transient (per UI-SPEC §Accessibility).

**Imports pattern** (analog: `OneTimeReveal.tsx:1-17`):
```typescript
import { useState, useCallback, useEffect } from 'react';
import { Dialog, /* ... */ } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { CopyButton } from './CopyButton';
import { AlertTriangle } from 'lucide-react';
```

---

### `web/src/components/common/StatusBadge.tsx` (new)

**Analog:** `web/src/components/common/SeverityBadge.tsx` — EXACT match. Same role (status-pill component), same data flow (props → Badge primitive), same file neighborhood.

**Exact pattern to copy and adapt** (analog: `SeverityBadge.tsx:1-35`):
```typescript
/**
 * Badge with severity color per 05-UI-SPEC semantic colors.
 */

import { cn } from '@/lib/utils';
import { Badge } from '@/components/ui/badge';

type SeverityLevel = 'critical' | 'high' | 'medium' | 'low' | 'unknown';

const severityStyles: Record<SeverityLevel, string> = {
  critical: 'bg-destructive/10 text-destructive border-destructive/20',
  high: 'bg-orange-500/10 text-orange-600 border-orange-500/20 dark:text-orange-400',
  medium: 'bg-amber-500/10 text-amber-600 border-amber-500/20 dark:text-amber-400',
  low: 'bg-teal-500/10 text-teal-600 border-teal-500/20 dark:text-teal-400',
  unknown: 'bg-muted text-muted-foreground border-border',
};

interface SeverityBadgeProps {
  severity: string;
  className?: string;
}

export function SeverityBadge({ severity, className }: SeverityBadgeProps) {
  const level = severity.toLowerCase() as SeverityLevel;
  const style = severityStyles[level] ?? severityStyles.unknown;

  return (
    <Badge
      variant="outline"
      className={cn(style, className)}
    >
      {severity.charAt(0).toUpperCase() + severity.slice(1).toLowerCase()}
    </Badge>
  );
}
```

**Deltas for `StatusBadge`:**
1. Type union is the 6 status tokens: `'healthy' | 'warning' | 'failure' | 'disabled' | 'maintenance' | 'neutral'`.
2. Style strings use the NEW `bg-status-*` utility tokens (declared in Phase 6 `index.css` under `@theme inline`), NOT the raw Tailwind palette. **UI-SPEC line 133 FORBIDS raw palette in new code.**
3. Add an icon prop (lucide-react): `CheckCircle2 / AlertTriangle / XCircle / MinusCircle / Wrench / Info`. Include icon inside Badge using `<span className="inline-flex items-center gap-1">`.
4. Props: `status` (required), `label` (required string), `size?: 'sm' | 'md'`, `iconOnly?: boolean` (must set `aria-label` when true).

**Do NOT** consolidate `SeverityBadge` into `StatusBadge` — they are different semantic axes (UI-SPEC §Color, line 123–130 + RESEARCH.md Q6).

---

### `web/src/components/common/SkeletonCard.tsx`, `SkeletonTable.tsx`, `SkeletonDetail.tsx`, `SkeletonMetric.tsx` (new)

**Analog:** `web/src/components/ui/skeleton.tsx` — the 13-line primitive that Phase 6 composes into shape-specific variants.

**Primitive pattern to copy** (analog: `web/src/components/ui/skeleton.tsx` full):
```typescript
import { cn } from "@/lib/utils"

function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("animate-pulse rounded-md bg-muted", className)}
      {...props}
    />
  )
}

export { Skeleton }
```

**Composition pattern — `SkeletonCard`:**
```typescript
import { Skeleton } from '@/components/ui/skeleton';
import { Card, CardContent, CardHeader } from '@/components/ui/card';

interface SkeletonCardProps {
  rows?: number;
  showAction?: boolean;
}

export function SkeletonCard({ rows = 3, showAction = false }: SkeletonCardProps) {
  return (
    <Card role="status" aria-label="Loading">
      <CardHeader>
        <Skeleton className="h-4 w-32" />
      </CardHeader>
      <CardContent className="space-y-2">
        {Array.from({ length: rows }).map((_, i) => (
          <Skeleton key={i} className="h-3 w-full" />
        ))}
        {showAction && <Skeleton className="h-8 w-24 mt-4" />}
      </CardContent>
    </Card>
  );
}
```

**Required for all four:** outer container carries `role="status" aria-label="Loading"` (UI-SPEC §Accessibility — only the outer container announces, inner bars are decorative).

**`SkeletonMetric` layout analog** → `web/src/components/common/StorageGauge.tsx:16-35` (metric = label-line + big-number + delta-line). Copy the flex-between header + tabular-nums pattern; replace text with `<Skeleton>` bars.

---

### `web/src/components/common/CopyInline.tsx` (new)

**Analog:** `web/src/components/common/OneTimeReveal.tsx:67-75` — the EXACT "pre block + right-aligned CopyButton with absolute positioning" snippet that Phase 6 extracts into its own component.

**Pattern to copy** (analog: `OneTimeReveal.tsx:67-75`):
```tsx
<div className="relative rounded-md bg-muted p-3 pr-10">
  <code className="block break-all font-mono text-sm leading-relaxed select-all">
    {internalSecret}
  </code>
  <CopyButton
    text={internalSecret}
    className="absolute right-1.5 top-1.5"
  />
</div>
```

**Scope-limit for Phase 6+ NEW placements (UI-SPEC line 63):**
The 6px (`right-1.5 top-1.5`) inset is grandfathered ONLY to the two v1.0 files where it already appears (`SnippetPanel.tsx`, `OneTimeReveal.tsx`). `CopyInline.tsx` is a NEW placement, so it MUST use `right-2 top-2` (8px). Spacing-carve-out lint (Migration task #19) enforces this.

**`CopyInline` composition:**
```tsx
interface CopyInlineProps {
  value: string;
  label?: string;
  masked?: boolean;  // show ••••• for masked keys until hover/click
}

export function CopyInline({ value, label, masked = false }: CopyInlineProps) {
  return (
    <div className="relative rounded-md bg-muted p-3 pr-10">
      {label && <span className="sr-only">{label}</span>}
      <code className="block break-all font-mono text-xs leading-relaxed select-all">
        {masked ? '•'.repeat(Math.min(value.length, 32)) : value}
      </code>
      <CopyButton
        text={value}
        className="absolute right-2 top-2"  /* 8px — NOT 6px */
      />
    </div>
  );
}
```

---

### `web/src/components/common/CopyButton.tsx` (modify — add `aria-live`)

**Self-analog.** Existing file (65 lines) already implements clipboard + tooltip swap + icon swap. Phase 6 adds ONE hidden `aria-live="polite"` span for screen-reader announcement.

**Current return** (lines 42–63):
```tsx
return (
  <Tooltip>
    <TooltipTrigger
      render={
        <Button
          variant="ghost"
          size="icon-sm"
          className={className}
          onClick={handleCopy}
          aria-label="Copy to clipboard"
        />
      }
    >
      {copied ? (
        <Check className="size-3.5 text-green-500" />
      ) : (
        <Copy className="size-3.5" />
      )}
    </TooltipTrigger>
    <TooltipContent>{copied ? 'Copied!' : 'Copy'}</TooltipContent>
  </Tooltip>
);
```

**Phase 6 diff — wrap in fragment, add sr-only announcement:**
```tsx
return (
  <>
    <Tooltip>
      {/* ... existing trigger + content unchanged ... */}
    </Tooltip>
    <span aria-live="polite" aria-atomic="true" className="sr-only">
      {copied ? 'Copied to clipboard' : ''}
    </span>
  </>
);
```

**Props signature unchanged.** Zero breaking changes to 2+ existing callers (`SnippetPanel.tsx:71`, `OneTimeReveal.tsx:72`).

---

### `web/src/index.css` (extend)

**Self-analog.** Existing `:root` (lines 80–113) declares 33 CSS variables; `.dark` (lines 115–147) mirrors them; `@theme inline` (lines 37–78) maps the variables to Tailwind utility names.

**Pattern to copy — `:root` variable declaration:**
```css
:root {
  --background: oklch(1 0 0);
  --foreground: oklch(0.145 0 0);
  /* ... existing ... */
  --destructive: oklch(0.577 0.245 27.325);
  --border: oklch(0.922 0 0);
}
```

**`.dark` mirror:**
```css
.dark {
  --background: oklch(0.145 0 0);
  --foreground: oklch(0.985 0 0);
  /* ... mirrored inverse ... */
}
```

**`@theme inline` mapping** (lines 37–78):
```css
@theme inline {
  --color-destructive: var(--destructive);
  --color-border: var(--border);
  /* ... all --color-X: var(--X) ... */
}
```

**Phase 6 additions — 18 new variables + 18 `@theme inline` mappings:**

1. Under `:root` — add 6 triples (fill / foreground / border) for the 6 status tokens per UI-SPEC Color table (lines 113–119):
```css
  --status-healthy: oklch(0.96 0.04 165);
  --status-healthy-foreground: oklch(0.45 0.14 165);
  --status-healthy-border: oklch(0.88 0.07 165);
  --status-warning: oklch(0.97 0.05 85);
  --status-warning-foreground: oklch(0.5 0.16 70);
  --status-warning-border: oklch(0.89 0.11 85);
  /* ... failure, disabled, maintenance, neutral ... */
```

2. Under `.dark` — mirror with hand-tuned dark values (UI-SPEC doesn't spec dark, but tokens MUST exist — copy root values for now; dark UI not activated in v1.1).

3. Under `@theme inline` — wire each so `bg-status-healthy` / `text-status-healthy-foreground` / `border-status-healthy-border` become valid utilities:
```css
  --color-status-healthy: var(--status-healthy);
  --color-status-healthy-foreground: var(--status-healthy-foreground);
  --color-status-healthy-border: var(--status-healthy-border);
  /* ... 18 total ... */
```

4. **Add `prefers-reduced-motion` block** (UI-SPEC Accessibility line 349) after the existing `@layer base` block:
```css
@media (prefers-reduced-motion: reduce) {
  .animate-pulse { animation: none; }
}
```

5. **Delete line 4**: `@import "@fontsource-variable/geist";` — Migration task #16.

---

### `web/src/pages/_dev/StatusBadgeStoryPage.tsx` (new dev-only page)

**Analog:** `web/src/pages/DashboardPage.tsx:73-82` — the existing grid-layout pattern used by dashboard cards.

**Grid layout pattern to copy** (analog: `DashboardPage.tsx:73-82`):
```tsx
<div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
  {[
    { title: 'Projects', icon: FolderKanban, value: data?.project_count ?? 0 },
    // ... rest of tiles ...
  ].map((tile, i) => (
    /* render each tile */
  ))}
</div>
```

**Story page — matrix render for Playwright snapshot:**
```tsx
// web/src/pages/_dev/StatusBadgeStoryPage.tsx
import { StatusBadge } from '@/components/common/StatusBadge';

const statuses = ['healthy','warning','failure','disabled','maintenance','neutral'] as const;
const sizes = ['sm','md'] as const;

export function StatusBadgeStoryPage() {
  return (
    <div className="p-8 space-y-6">
      <h1 className="text-2xl font-semibold">StatusBadge matrix</h1>
      <div className="grid gap-4" style={{ gridTemplateColumns: 'repeat(6, auto)' }}>
        {statuses.flatMap((status) =>
          sizes.map((size) => (
            <StatusBadge key={`${status}-${size}`} status={status} label={status} size={size} />
          ))
        )}
      </div>
    </div>
  );
}
```

**Route registration** (analog: `web/src/App.tsx` — existing lazy route pattern at lines 32–37):
```tsx
const AdminUsersPage = lazy(() =>
  import('@/pages/admin/UsersPage').catch(() => ({
    default: () => <PlaceholderPage name="Users" />,
  })),
);
```

Phase 6 adds: dev-only route gated by `import.meta.env.DEV`:
```tsx
{import.meta.env.DEV && {
  path: '/_dev/status-badge-story',
  element: <StatusBadgeStoryPage />,
}}
```

---

### `web/e2e/error-envelope.spec.ts` + `visual-foundation.spec.ts` + `responsive.spec.ts` (new)

**Analog:** `web/e2e/admin.spec.ts:1-46` — the existing admin spec has the auth-bootstrap + `page.goto()` + locator pattern all three new specs copy.

**beforeEach auth pattern** (analog: `admin.spec.ts:10-23`):
```typescript
import { test, expect } from '@playwright/test';

test.describe('Admin pages', () => {
  test.beforeEach(async ({ request }) => {
    const resp = await request.post('/api/v1/auth/login', {
      data: { login: 'admin', password: 'changeme' },
    });
    const body = await resp.json();
    if (body.must_change_password) {
      await request.post('/api/v1/auth/change-password', {
        data: { current: 'changeme', new: 'AdminTest1!' },
      });
      await request.post('/api/v1/auth/login', {
        data: { login: 'admin', password: 'AdminTest1!' },
      });
    }
  });
```

**Locator + visible-by-timeout pattern** (analog: `admin.spec.ts:42-46`):
```typescript
const toggle = page.locator(
  '[role="switch"], button:has-text("maintenance"), input[type="checkbox"]',
);
await expect(toggle.first()).toBeVisible({ timeout: 10000 });
```

**`error-envelope.spec.ts` additions:**
- 4 scenarios (one per class). Each hits a dev-only `/api/v1/_dev/error/:class` route that returns a canned envelope (RESEARCH.md Open Question #2 recommends this). Assertions:
  - `await expect(page.locator('[role="alert"]')).toContainText('...')` for failure/permission/operator.
  - `await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible()` for transient.
  - Field-highlight: `await expect(page.locator('input[aria-invalid="true"]')).toHaveCSS('border-color', ...)` for validation.

**`visual-foundation.spec.ts`:**
```typescript
test('StatusBadge matrix snapshot', async ({ page }) => {
  await page.goto('/_dev/status-badge-story');
  await expect(page).toHaveScreenshot('status-badge-matrix.png', {
    animations: 'disabled',
    maxDiffPixelRatio: 0.01,
  });
});
```

**`responsive.spec.ts`:**
```typescript
test.use({ viewport: { width: 1366, height: 768 } });

const adminRoutes = ['/dashboard', '/projects', '/admin/users', '/admin/audit', '/admin/trash'];

for (const route of adminRoutes) {
  test(`${route} no horizontal scroll at 1366×768`, async ({ page }) => {
    await page.goto(route);
    const { scrollWidth, clientWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
  });
}
```

**Playwright config** (analog: `web/playwright.config.ts` full) — no changes needed for new specs; existing `baseURL`, `webServer`, `ignoreHTTPSErrors` all apply.

---

### `scripts/check-contrast.mjs` (new — greenfield)

**No analog in repo.** No existing node scripts under `scripts/` (the directory doesn't exist — verified). Phase 6 creates the directory + first script.

**Pattern reference (external, not in repo):** WCAG 2.1 relative-luminance formula. Self-contained ~80 LOC script using Node stdlib only:
1. Read `web/src/index.css` as UTF-8.
2. Regex extract `oklch(L C H)` values for each `--status-*` variable in `:root`.
3. Convert OKLCH → sRGB (manual formula or via `culori` (MIT) if devDep is acceptable).
4. Compute contrast ratio `(L_lighter + 0.05) / (L_darker + 0.05)`.
5. Assert ratio ≥ 4.5 for each text/fill pair; exit 1 on failure.

**Wire into Makefile `test` target** (analog: existing `grep-cdn` target for `make` integration pattern):
```makefile
grep-cdn:
	@set -e; \
	echo "grep-cdn: web/dist/"; \
	! grep -rPI 'https?://(?!localhost|127\.0\.0\.1|example\.com)' web/dist/ 2>/dev/null \
		|| (echo "ERROR: external URL in web/dist/" && exit 1); \
	# ...
```

Phase 6 adds:
```makefile
check-contrast:
	@node scripts/check-contrast.mjs

test: ...
	$(MAKE) check-contrast   # new gate
```

---

### `Makefile` (modify — add `lint-typography`, `lint-spacing-carveout`, `check-contrast`)

**Analog:** the existing `grep-cdn` target (Makefile lines ~65–80, shown earlier in context). Same "Perl-grep negative-lookahead, fail on match" pattern.

**Pattern to copy** (analog: `grep-cdn` in Makefile):
```makefile
grep-cdn:
	@set -e; \
	echo "grep-cdn: web/dist/"; \
	! grep -rPI 'https?://(?!localhost|127\.0\.0\.1)' web/dist/ 2>/dev/null \
		|| (echo "ERROR: external URL in web/dist/" && exit 1); \
	echo "grep-cdn: clean"
```

**Phase 6 adds (Migration tasks #17/18/19):**
```makefile
lint-typography:
	@set -e; \
	echo "lint-typography: font-weight gate"; \
	! grep -rPI --include='*.tsx' --include='*.ts' \
		'\b(font-medium|font-bold|font-light)\b' \
		$$(cat scripts/typography-allowlist.txt | sed 's|^|--exclude=|') \
		web/src/ 2>/dev/null \
		|| (echo "ERROR: forbidden font-weight class in new code" && exit 1); \
	echo "lint-typography: size gate"; \
	! grep -rPI --include='*.tsx' --include='*.ts' \
		'\b(text-3xl|text-4xl|text-base|text-xl)\b' \
		web/src/ 2>/dev/null \
		|| (echo "ERROR: forbidden text-size class in new code" && exit 1); \
	echo "lint-typography: clean"

lint-spacing-carveout:
	@set -e; \
	! grep -rPI --include='*.tsx' \
		--exclude='SnippetPanel.tsx' --exclude='OneTimeReveal.tsx' \
		'(right-1\.5|top-1\.5)' web/src/ 2>/dev/null \
		|| (echo "ERROR: 6px inset outside SnippetPanel.tsx/OneTimeReveal.tsx" && exit 1); \
	echo "lint-spacing-carveout: clean"
```

Both wire into the existing `test:` target at the top of the Makefile.

---

## Shared Patterns

### Request-ID Correlation (ERR-07)

**Source:** `internal/httpx/router.go:28-38` + `internal/httpx/middleware_audit.go:15-32`
**Apply to:** `internal/httperr/write.go`, any new Go middleware, every failing handler in `internal/api/` via the rewritten `writeJSONError`.

```go
// In any handler / middleware that needs the incident_id:
reqID := middleware.GetReqID(r.Context())
slog.ErrorContext(r.Context(), "api.error",
    slog.String("incident_id", reqID),
    slog.String("code", code),
    slog.Any("cause", err),
)
```

### Tailwind utility composition with `cn()` (visual primitives)

**Source:** `web/src/components/common/SeverityBadge.tsx:28-32` + `web/src/components/common/StorageGauge.tsx:20`
**Apply to:** `StatusBadge.tsx`, `ErrorEnvelope.tsx`, all `Skeleton*.tsx`, `CopyInline.tsx`.

```typescript
import { cn } from '@/lib/utils';
// ...
return <Badge variant="outline" className={cn(style, className)}>{label}</Badge>;
```

Never concatenate Tailwind classes with string `+` — always `cn()`.

### Go test server + login scaffold

**Source:** `internal/api/admin_phase1_test.go` (`newTestServer`, `seedTestUser`, `s.login`, `s.do`)
**Apply to:** `internal/api/handlers_envelope_integration_test.go`, any Phase 6+ integration test touching HTTP.

```go
s := newTestServer(t)
_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
cookie, _, _ := s.login(t, "root", pw)
resp, body := s.do(t, "GET", "/api/v1/...", cookie, nil)
```

### Playwright auth bootstrap in `beforeEach`

**Source:** `web/e2e/admin.spec.ts:10-23`
**Apply to:** `error-envelope.spec.ts`, `visual-foundation.spec.ts`, `responsive.spec.ts`, `a11y-audit.spec.ts`.

```typescript
test.beforeEach(async ({ request }) => {
  const resp = await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: 'changeme' },
  });
  const body = await resp.json();
  if (body.must_change_password) {
    await request.post('/api/v1/auth/change-password', {
      data: { current: 'changeme', new: 'AdminTest1!' },
    });
    await request.post('/api/v1/auth/login', {
      data: { login: 'admin', password: 'AdminTest1!' },
    });
  }
});
```

### JSDoc top-of-file component header

**Source:** `web/src/components/common/SeverityBadge.tsx:1-3`, `web/src/components/common/CopyButton.tsx:1-3`, `web/src/components/common/OneTimeReveal.tsx:1-4`
**Apply to:** every new component in `web/src/components/common/`.

```typescript
/**
 * [One-line purpose]. [Reference to spec section / UI-SPEC line if relevant].
 */
```

### JSON response writing

**Source:** `internal/api/errors.go:30-44`
**Apply to:** `internal/httperr/write.go` (`Write` helper).

```go
w.Header().Set("Content-Type", "application/json; charset=utf-8")
w.WriteHeader(status)
_ = json.NewEncoder(w).Encode(body)
```

---

## No Analog Found (greenfield — planner uses RESEARCH.md patterns)

| File | Role | Reason |
|------|------|--------|
| `web/src/api/client.test.ts` | unit test | No vitest in repo; RESEARCH.md Open Question #1 recommends Playwright component testing OR adding vitest. Planner decides during Wave 1 planning. |
| `web/src/hooks/useApiError.test.tsx` | unit test | Same — no vitest infrastructure. |
| `scripts/check-contrast.mjs` | offline script | `scripts/` directory does not exist; Phase 6 creates it. Pattern is pure Node + WCAG formula; no codebase precedent. Use RESEARCH.md Q10 as spec. |
| `web/e2e/a11y-audit.spec.ts` | axe breadth audit | Playwright auth pattern analog EXISTS (`admin.spec.ts`) but the `@axe-core/playwright` integration is net-new. Planner imports `AxeBuilder` per `@axe-core/playwright` README and calls `analyze()` on 5 seeded pages. Composes the analog's auth bootstrap + new axe invocation. |

---

## Metadata

**Analog search scope:**
- `internal/api/` (all `.go` + `.yaml`)
- `internal/httpx/`
- `internal/protocol/{raw,rpm,deb,pypi,helm,oci,s3,git}/`
- `web/src/components/{common,ui}/`
- `web/src/hooks/`
- `web/src/api/`
- `web/src/pages/` (sampled: Login, Dashboard, Projects, one repo detail)
- `web/e2e/` (all 8 specs)
- `Makefile`
- `web/package.json`, `web/playwright.config.ts`, `web/src/index.css`, `web/src/App.tsx`

**Files scanned:** ~40 direct reads, ~15 greps for call-site counts.

**Pattern extraction date:** 2026-04-17

**Key finding:** Phase 6 has an unusually high analog-match rate (89%) because it is a migration + primitive-addition on a codebase that already commits to one idiom per concern:
- Error writing: every handler funnels through `writeJSONError` (→ `httperr` is a one-helper swap).
- Tailwind styling: one `cn()`-based style-map per component (`SeverityBadge` is the template for `StatusBadge` + `ErrorEnvelope`).
- Go tests: every integration test uses `newTestServer` + `s.do` (→ envelope tests are drop-in).
- Playwright: every spec uses `beforeEach` auth bootstrap (→ new specs are drop-in).
- CSS tokens: one `:root`/`.dark`/`@theme inline` triple (→ status tokens are additive lines).

The 4 greenfield files are all test/tooling scaffolds (no production code), which means plan-time risk is concentrated in tooling choice, not production patterns.
