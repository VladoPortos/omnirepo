// Package httperr defines the canonical JSON error envelope for OmniRepo's
// /api/v1 and /admin REST endpoints.
//
// Protocol handlers in internal/protocol/{oci,rpm,deb,pypi,helm,raw,s3,git}
// do NOT use this package — they emit protocol-native error shapes.
//
// Usage:
//
//	err := httperr.Validation("user.name_required", "Name is required")
//	httperr.Write(w, r, err)
//
// Design invariants:
//
//   - Envelope.Message is the only human-facing sentence and MUST NOT contain
//     filesystem paths, Go driver strings, stack markers, or any substring
//     supplied by an internal error. Constructors accept static developer
//     strings only; none of them interpolate user or cause data.
//   - Envelope.Code matches ^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$ (compile-time
//     constants in call sites). Constructors panic on violation; the panic
//     is a developer-time surface, not a runtime user-input surface.
//   - httperr.Internal wraps a server cause, logs it via Write, and emits a
//     generic "An internal error occurred." message to clients.
package httperr

import (
	"regexp"
)

// Class is the stable client-facing error classification. UI renders
// class-appropriate icons, copy, and CTAs off this value.
type Class string

// Class constants mirror ApiErrorClass in internal/api/types_gen.go.
// These values MUST stay in lockstep with the OpenAPI enum.
const (
	ClassValidation       Class = "validation"
	ClassPermission       Class = "permission"
	ClassTransient        Class = "transient"
	ClassOperatorRequired Class = "operator_action_required"
)

// Envelope is the canonical wire shape for all /api/v1 error responses.
// Fields mirror internal/api/types_gen.go ApiErrorEnvelope exactly; do
// not diverge without updating the OpenAPI schema first.
type Envelope struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Hint       string         `json:"hint,omitempty"`
	Class      Class          `json:"class"`
	IncidentID string         `json:"incident_id,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

// Error wraps an Envelope with an HTTP status + an internal cause that is
// logged (via httperr.Write) and never serialized to clients.
type Error struct {
	Envelope Envelope
	Status   int
	Cause    error
}

// Error satisfies the error interface; returns Envelope.Message so
// errors.New("wrap: %w") still produces a client-safe sentence.
func (e *Error) Error() string { return e.Envelope.Message }

// Unwrap exposes the internal cause for errors.Is / errors.As traversal.
func (e *Error) Unwrap() error { return e.Cause }

// Option mutates an Error during construction.
type Option func(*Error)

// WithHint sets the optional remediation hint. Callers MUST pass a static
// developer-authored sentence — never user input, file paths, or cause text.
func WithHint(h string) Option { return func(e *Error) { e.Envelope.Hint = h } }

// WithStatus overrides the class-default HTTP status (e.g. 422 for a
// validation that specifically needs Unprocessable Entity semantics).
func WithStatus(s int) Option { return func(e *Error) { e.Status = s } }

// WithCause attaches an internal error that will be logged (not serialized)
// by httperr.Write. The cause never appears in the client envelope.
func WithCause(c error) Option { return func(e *Error) { e.Cause = c } }

// WithDetail adds a single key/value pair to Envelope.Details. Primitive
// values are fine; caller is responsible for ensuring they do not carry
// internal-only information. Use IsInternalString for screening.
func WithDetail(k string, v any) Option {
	return func(e *Error) {
		if e.Envelope.Details == nil {
			e.Envelope.Details = map[string]any{}
		}
		e.Envelope.Details[k] = v
	}
}

// internalMarkers flags strings that look like internal-only leakage
// (filesystem paths, Go driver messages, stack markers). Used by tests
// (e.g. the api handlers envelope integration test) to assert
// Envelope.Message / Envelope.Hint never carry these substrings.
var internalMarkers = []*regexp.Regexp{
	// Filesystem absolute paths (matches both leading and mid-string).
	regexp.MustCompile(`(^|\s)/[a-zA-Z0-9_/.-]+`),
	// Go source locations (e.g. "file.go:123").
	regexp.MustCompile(`\.go:\d+`),
	// Stack markers — the word "goroutine" or a runtime.* frame.
	regexp.MustCompile(`\b(goroutine|runtime\.)`),
	// Go driver / syscall leaks. Note: "sqlite" is matched as a bare
	// substring because the driver appears in messages like "sqlite3 ...".
	regexp.MustCompile(`sqlite`),
	regexp.MustCompile(`\b(sql:|read:|open:|stat:)`),
}

// IsInternalString reports whether s contains substrings that look like
// internal-only information (paths, source locations, stack markers,
// driver leaks). Used by leak-screening tests (api envelope integration
// gate) to prevent internal-string leaks reaching wire envelopes.
func IsInternalString(s string) bool {
	for _, re := range internalMarkers {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// codeRegex enforces the OpenAPI pattern ^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$
var codeRegex = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// CodeIsValid reports whether code matches the OpenAPI regex. Constructors
// call this and panic on false — codes are developer-supplied constants, so
// a failure here indicates a typo at compile time, not runtime user input.
func CodeIsValid(code string) bool { return codeRegex.MatchString(code) }

// Validation returns a validation-class Error with HTTP 400.
func Validation(code, msg string, opts ...Option) *Error {
	return build(code, msg, ClassValidation, 400, opts...)
}

// ValidationFields returns a validation-class Error with details.fields
// set to a map of field-path → error-code. Used for multi-field form
// failures so the UI can mark every invalid input in one render.
func ValidationFields(code, msg string, fields map[string]string) *Error {
	return Validation(code, msg, WithDetail("fields", fields))
}

// Permission returns a permission-class Error with HTTP 403.
func Permission(code, msg string, opts ...Option) *Error {
	return build(code, msg, ClassPermission, 403, opts...)
}

// Transient returns a transient-class Error with HTTP 503 and
// details.retry_after_ms set if retryAfterMs > 0. Use for failures the
// client can retry without operator intervention (network blip, brief
// storage pressure, rate-limit).
func Transient(code, msg string, retryAfterMs int, opts ...Option) *Error {
	base := []Option{}
	if retryAfterMs > 0 {
		base = append(base, WithDetail("retry_after_ms", retryAfterMs))
	}
	base = append(base, opts...)
	return build(code, msg, ClassTransient, 503, base...)
}

// OperatorRequired returns an operator_action_required-class Error (HTTP
// 503) with details.operator_route + details.operator_label set so the
// UI can deep-link to the admin page that will unblock the operation.
func OperatorRequired(code, msg, operatorRoute, operatorLabel string, opts ...Option) *Error {
	base := []Option{WithDetail("operator_route", operatorRoute), WithDetail("operator_label", operatorLabel)}
	base = append(base, opts...)
	return build(code, msg, ClassOperatorRequired, 503, base...)
}

// Internal wraps an opaque server-side failure into a transient-class
// Error (HTTP 500). The cause is logged via httperr.Write but never
// serialized; Envelope.Message is a generic "An internal error occurred."
// that cannot leak internals.
func Internal(code string, cause error) *Error {
	e := build(code, "An internal error occurred.", ClassTransient, 500)
	e.Cause = cause
	return e
}

// build is the single construction helper — every public constructor
// routes through here so the code-regex check and option application
// live in one place.
func build(code, msg string, cls Class, status int, opts ...Option) *Error {
	if !CodeIsValid(code) {
		// Developer error — a constant in this codebase is malformed.
		// Panic at test time; production binary will still ship a valid
		// envelope because these codes are string literals, not user input.
		panic("httperr: invalid code " + code + " (must match ^[a-z][a-z0-9_]*\\.[a-z][a-z0-9_]*$)")
	}
	e := &Error{
		Envelope: Envelope{Code: code, Message: msg, Class: cls},
		Status:   status,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

