// Package api hosts the hand-written HTTP handlers for OmniRepo's REST
// surface. Types live in types_gen.go (generated from openapi.yaml);
// handlers live in individual files; this file bridges handler call
// sites to the canonical internal/httperr envelope (Phase 6 / ERR-01).
//
// writeJSONError keeps its (w, status, code, detail) call convention
// from v1.0 — widened to (w, r, status, code, detail) so the bridge
// can populate the envelope's incident_id from the chi request
// context — and emits an ApiErrorEnvelope JSON body via httperr.Write
// instead of the legacy `{error, detail}` shape.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dxc-internal/omnirepo/internal/httperr"
)

// Stable error codes consumed by UI and legacy tests. Phase 6
// normalizes each to a dotted code in writeJSONError before the
// envelope ships, so these constants remain source-compatible with
// every existing call site.
const (
	ErrPasswordChangeRequired = "password-change-required"
	ErrUnauthenticated        = "unauthenticated"
	ErrForbidden              = "forbidden"
	ErrNotFound               = "not_found"
	ErrValidationFailed       = "validation_failed"
	ErrConflict               = "conflict"
	ErrInternal               = "internal"
)

// writeJSON emits an arbitrary JSON body with a status code. Used for
// successful responses; error responses funnel through writeJSONError
// or writeEnvelope instead.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

// writeJSONError is the legacy-signature bridge. Every handler call
// site uses this; the body constructs a httperr.Error with class
// inferred from status and a code normalized from the legacy set, and
// delegates to httperr.Write (which stamps the envelope's incident_id
// from the chi request context and logs the internal cause, if any,
// via slog).
//
// The 5-arg signature (r added versus the v1.0 helper) is the single
// mechanical widening plan 06-02 applies across every /api/v1 handler.
func writeJSONError(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	e := &httperr.Error{
		Envelope: httperr.Envelope{
			Code:    normalizeLegacyCode(code),
			Message: detail,
			Class:   inferClassFromStatus(status),
		},
		Status: status,
	}
	httperr.Write(w, r, e)
}

// writeEnvelope is the first-class path for Phase 6+ handlers that
// want explicit class control (operator_action_required,
// validation-with-fields, transient with retry_after_ms, etc.). Wraps
// httperr.Write for discoverability from within the api package.
func writeEnvelope(w http.ResponseWriter, r *http.Request, e *httperr.Error) {
	httperr.Write(w, r, e)
}

// legacyCodeMap translates the v1.0 hand-written ErrCode constants to
// the dotted form required by the ApiErrorEnvelope schema
// (^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$).
var legacyCodeMap = map[string]string{
	ErrPasswordChangeRequired: "auth.password_change_required",
	ErrUnauthenticated:        "auth.unauthenticated",
	ErrForbidden:              "auth.forbidden",
	ErrNotFound:               "resource.not_found",
	ErrValidationFailed:       "validation.failed",
	ErrConflict:               "resource.conflict",
	ErrInternal:               "api.internal",
}

// normalizeLegacyCode converts legacy dashed or single-word codes to
// the dotted form required by the ApiErrorEnvelope schema. Codes that
// already contain a "." are passed through unchanged (post-Phase 6
// callers supply dotted codes directly). Unknown codes are prefixed
// "legacy." to preserve client-visible stability during the migration
// window.
func normalizeLegacyCode(code string) string {
	if code == "" {
		return "api.unknown"
	}
	if mapped, ok := legacyCodeMap[code]; ok {
		return mapped
	}
	if containsDot(code) {
		return code
	}
	return "legacy." + sanitizeCode(code)
}

func containsDot(s string) bool {
	for _, c := range s {
		if c == '.' {
			return true
		}
	}
	return false
}

// sanitizeCode lowercases and replaces dashes/spaces with underscores
// to satisfy the ApiErrorEnvelope code regex. Characters that are
// neither alphanumeric, underscore, nor the transformable set are
// dropped. Codes that would start with a digit (or come out empty) get
// a stable `x_` prefix so the result is always a valid local segment.
func sanitizeCode(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		case c == '-' || c == ' ':
			out = append(out, '_')
		}
	}
	if len(out) == 0 || (out[0] >= '0' && out[0] <= '9') {
		out = append([]byte("x_"), out...)
	}
	return string(out)
}

// inferClassFromStatus maps an HTTP status code to the ApiErrorClass
// the UI should render against. 404 is treated as validation (not
// transient) so the client UI does not offer a Retry button for
// missing resources.
func inferClassFromStatus(status int) httperr.Class {
	switch {
	case status == 401, status == 403:
		return httperr.ClassPermission
	case status == 429:
		return httperr.ClassTransient
	case status >= 500:
		return httperr.ClassTransient
	default: // 400/404/409/413/422 and any other 4xx
		return httperr.ClassValidation
	}
}

// maxAdminJSONBodyBytes is the default cap for small admin JSON
// POST/PATCH bodies (create project, create repo, create user, etc.).
// Larger payloads (e.g. description_md in repo PATCH) already use
// repos.maxRepoPatchBodyBytes. Audit finding #10.
const maxAdminJSONBodyBytes int64 = 64 << 10 // 64 KiB

// decodeJSONBody wraps r.Body with http.MaxBytesReader(limit) and
// decodes it into out. On overflow it writes 413 + validation_failed;
// on malformed JSON it writes 400. Returns true on success. Callers
// just `return` on false.
//
// Audit finding #10: standardizes body-size defense so new handlers
// don't forget MaxBytesReader and existing unbounded JSON decodes
// (handleCreateUser, handleCreateProject, handleCreateRepo) cannot
// force unbounded buffering.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONError(w, r, http.StatusRequestEntityTooLarge, ErrValidationFailed, "request body too large")
			return false
		}
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return false
	}
	return true
}
