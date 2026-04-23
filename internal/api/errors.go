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
	"regexp"

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
//
// If detail is empty the bridge fills in a generic class-appropriate
// sentence via defaultMessageForStatus so the wire body always carries
// a non-empty ApiErrorEnvelope.message (schema-required).
func writeJSONError(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	if detail == "" {
		detail = defaultMessageForStatus(status)
	}
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

// handleMethodNotAllowed is the /api/v1 router's MethodNotAllowed hook
// (F-15.2). Without it, chi's default handler emits a zero-byte body,
// which breaks the envelope contract every other /api/v1 error path
// honours. Scoped to /api/v1 so protocol routers (OCI /v2, Git smart
// HTTP, raw PUT, etc.) keep their native error shapes.
func handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, r, http.StatusMethodNotAllowed, ErrValidationFailed, "Method not allowed for this route.")
}

// defaultMessageForStatus returns a developer-authored, user-facing
// sentence for a given HTTP status. Used by writeJSONError when the
// handler passed "" as detail — typically in 500 paths where the
// internal cause is logged via slog and not safe to serialize. The
// sentence is static (no interpolation) so it can never leak
// internals (ERR-03).
func defaultMessageForStatus(status int) string {
	switch {
	case status == 401:
		return "You must be signed in to do that."
	case status == 403:
		return "You do not have permission to do that."
	case status == 404:
		return "That resource does not exist."
	case status == 409:
		return "That conflicts with existing data."
	case status == 413:
		return "Request body too large."
	case status == 422:
		return "One or more fields are invalid."
	case status == 429:
		return "Too many requests — please try again shortly."
	case status >= 500:
		return "An internal error occurred."
	case status >= 400:
		return "The request was not valid."
	default:
		return "An error occurred."
	}
}

// writeEnvelope is the first-class path for Phase 6+ handlers that
// want explicit class control (operator_action_required,
// validation-with-fields, transient with retry_after_ms, etc.). Wraps
// httperr.Write for discoverability from within the api package.
func writeEnvelope(w http.ResponseWriter, r *http.Request, e *httperr.Error) {
	httperr.Write(w, r, e)
}

// writeFieldValidationError is the 422+details.field variant of
// writeJSONError. Lets handlers that validate a single slug field
// (create project / create repo / create bucket) tell the UI which
// <Input> to highlight. The envelope's class is always "validation"
// and the status is 422 — mirrors the existing ErrValidationFailed
// conventions so wire compatibility is preserved. Field is the
// form input id or dotted DTO path (e.g. "name", "user.email") —
// whichever the UI uses to index its fieldErrors map.
func writeFieldValidationError(w http.ResponseWriter, r *http.Request, code, field, detail string) {
	if detail == "" {
		detail = defaultMessageForStatus(http.StatusUnprocessableEntity)
	}
	e := &httperr.Error{
		Envelope: httperr.Envelope{
			Code:    normalizeLegacyCode(code),
			Message: detail,
			Class:   httperr.ClassValidation,
			Details: map[string]any{"field": field},
		},
		Status: http.StatusUnprocessableEntity,
	}
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

// codeShapeRegex is the exact wire-envelope code pattern. Used by
// normalizeLegacyCode to decide whether an already-dotted code is safe
// to pass through verbatim. Any code that fails this regex (including
// codes that contain a "." but also carry internal markers like
// "errors.go:123" or "runtime.gopanic") is forced through the sanitize
// path so the output never leaks internals and always matches the
// envelope schema (ERR-03).
var codeShapeRegex = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// normalizeLegacyCode converts legacy dashed or single-word codes to
// the dotted form required by the ApiErrorEnvelope schema. Codes that
// already match the wire envelope code regex are passed through
// unchanged (post-Phase 6 callers supply dotted codes directly).
// Unknown codes are prefixed "legacy." to preserve client-visible
// stability during the migration window. The output is ALWAYS a valid
// envelope code and MUST NOT contain internal-leakage substrings
// (file paths, source locations, stack markers, driver strings) —
// see httperr.IsInternalString.
func normalizeLegacyCode(code string) string {
	if code == "" {
		return "api.unknown"
	}
	if mapped, ok := legacyCodeMap[code]; ok {
		return mapped
	}
	// Pass through only when the input already matches the wire shape.
	// A permissive "contains '.'" check lets malformed internals like
	// "errors.go:123" or "/home/…/foo.db" reach the wire body (ERR-03
	// regression gate in errors_envelope_test.go).
	if codeShapeRegex.MatchString(code) {
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
// dropped. Codes that would start with anything other than a lowercase
// letter (empty, digit, or underscore) get a stable `x_` prefix so the
// result is always a valid local segment matching ^[a-z][a-z0-9_]*$.
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
	if len(out) == 0 || !(out[0] >= 'a' && out[0] <= 'z') {
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
