// Package api envelope-bridge unit tests.
//
// Phase 6 / plan 04 task 1 — asserts the legacy-to-envelope normalization
// surface (writeJSONError → httperr.Write bridge) is code-regex-valid,
// class-inference-table-correct, and panic-safe at the bridge boundary.
//
// These are unit tests at the package level — they exercise the pure
// helpers in errors.go without spinning up the chi router or test server.
// Integration tests live in handlers_envelope_integration_test.go.
package api

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/httperr"
)

// envelopeCodeRegex mirrors the ApiErrorEnvelope code pattern from
// openapi.yaml. Every normalized code MUST match this or the wire body
// violates the generated schema.
var envelopeCodeRegex = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// TestNormalizedCodesPassRegex is the regression gate for ERR-01. Each
// legacy ErrCode constant feeds through normalizeLegacyCode; the result
// MUST match the dotted-snake-case regex required by the generated
// ApiErrorEnvelope.code type.
func TestNormalizedCodesPassRegex(t *testing.T) {
	legacyCodes := []string{
		ErrPasswordChangeRequired,
		ErrUnauthenticated,
		ErrForbidden,
		ErrNotFound,
		ErrValidationFailed,
		ErrConflict,
		ErrInternal,
	}
	for _, code := range legacyCodes {
		t.Run(code, func(t *testing.T) {
			normalized := normalizeLegacyCode(code)
			if !envelopeCodeRegex.MatchString(normalized) {
				t.Errorf("normalizeLegacyCode(%q) = %q, does not match envelope code regex", code, normalized)
			}
		})
	}
}

// TestNormalizeLegacyCode_Table exhaustively pins the legacy→dotted
// mapping. Every assertion here is part of the wire contract: UI code
// branches on these exact strings when deciding what to render.
func TestNormalizeLegacyCode_Table(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Core legacy constants — every one of these must map to the
		// dotted form the UI expects.
		{"password-change-required", "auth.password_change_required"},
		{"unauthenticated", "auth.unauthenticated"},
		{"forbidden", "auth.forbidden"},
		{"not_found", "resource.not_found"},
		{"validation_failed", "validation.failed"},
		{"conflict", "resource.conflict"},
		{"internal", "api.internal"},

		// Pass-through: wire-shaped dotted codes stay unchanged.
		{"auth.password_change_required", "auth.password_change_required"},
		{"user.name_required", "user.name_required"},
		// Multi-dot codes DO NOT pass through — they violate the wire
		// regex (exactly one '.'). They fall to the sanitize path so the
		// emitted envelope stays schema-valid.
		{"some.dotted.code", "legacy.somedottedcode"},

		// Sanitization: uppercase + dashes → lowercase + underscores, prefixed "legacy.".
		{"UPPER-case", "legacy.upper_case"},
		{"has spaces", "legacy.has_spaces"},

		// Edge cases.
		{"", "api.unknown"},           // empty → sentinel
		{"123bad", "legacy.x_123bad"}, // leading digit guarded by x_
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalizeLegacyCode(tc.in)
			if got != tc.want {
				t.Errorf("normalizeLegacyCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !envelopeCodeRegex.MatchString(got) {
				t.Errorf("result %q does not match regex", got)
			}
		})
	}
}

// TestInferClassFromStatus_Table pins the HTTP status → ApiErrorClass
// mapping. The UI renders class-specific CTAs (retry button for
// transient, deep-link button for operator_action_required, etc.) so
// any drift here surfaces as the wrong widget.
func TestInferClassFromStatus_Table(t *testing.T) {
	cases := []struct {
		status int
		want   httperr.Class
	}{
		// Validation class — user-correctable input issues.
		{400, httperr.ClassValidation},
		{404, httperr.ClassValidation}, // 404 → validation, NOT transient (no retry widget for missing resources)
		{409, httperr.ClassValidation},
		{413, httperr.ClassValidation},
		{422, httperr.ClassValidation},

		// Permission class — auth issues.
		{401, httperr.ClassPermission},
		{403, httperr.ClassPermission},

		// Transient class — retryable server-side blips.
		{429, httperr.ClassTransient},
		{500, httperr.ClassTransient},
		{502, httperr.ClassTransient},
		{503, httperr.ClassTransient},
		{504, httperr.ClassTransient},
	}
	for _, tc := range cases {
		t.Run("status_"+strconv.Itoa(tc.status), func(t *testing.T) {
			got := inferClassFromStatus(tc.status)
			if got != tc.want {
				t.Errorf("inferClassFromStatus(%d) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestSanitizeCode_LocalSegmentRegex pins every sanitizeCode output as a
// valid local-segment string (the second half of a dotted envelope code).
// The regex here is the local-segment portion of envelopeCodeRegex.
func TestSanitizeCode_LocalSegmentRegex(t *testing.T) {
	localRegex := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	cases := []string{
		"foo",
		"FOO",
		"foo-bar",
		"foo_bar",
		"foo bar",
		"foo!@#bar",
		"123bad",
		"",
		"_leading",
		"UPPER-CASE with junk!!!",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got := sanitizeCode(in)
			if !localRegex.MatchString(got) {
				t.Errorf("sanitizeCode(%q) = %q does not match local regex %q", in, got, localRegex)
			}
		})
	}
}

// TestNormalizeLegacyCode_NeverLeaksShapeInternals is the ERR-03 gate
// for the bridge: whatever you feed normalizeLegacyCode, the output
// MUST match the wire-shape regex. This protects against a future call
// site that accidentally threads err.Error() (which may contain file
// paths or source-location noise) into the `code` arg of
// writeJSONError. The regex itself forbids slashes, dots beyond one,
// colons, whitespace, and digits at position 0 — i.e. any shape that
// could encode a path like "/home/…" or a source location like
// "errors.go:123".
func TestNormalizeLegacyCode_NeverLeaksShapeInternals(t *testing.T) {
	malicious := []string{
		"/home/omnirepo/data/project.db",
		"open: no such file",
		"runtime.gopanic",
		"errors.go:123",
		"goroutine 12 [running]",
		"sqlite: constraint failed", // legacy. prefix result drops ':' and ' '
	}
	pathMarker := regexp.MustCompile(`/`)
	srcLocMarker := regexp.MustCompile(`\.go:\d+`)
	for _, in := range malicious {
		t.Run(in, func(t *testing.T) {
			got := normalizeLegacyCode(in)
			if !envelopeCodeRegex.MatchString(got) {
				t.Errorf("normalizeLegacyCode(%q) = %q does not match envelope regex", in, got)
			}
			if pathMarker.MatchString(got) {
				t.Errorf("normalizeLegacyCode(%q) = %q contains path char", in, got)
			}
			if srcLocMarker.MatchString(got) {
				t.Errorf("normalizeLegacyCode(%q) = %q contains source location", in, got)
			}
		})
	}
}

// TestContainsDot is a trivial gate for the pass-through detection used
// by normalizeLegacyCode. A code with a "." is considered already-dotted
// and passes through unchanged.
func TestContainsDot(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", false},
		{"foo", false},
		{"foo.bar", true},
		{".", true},
		{"foo.bar.baz", true},
	} {
		if got := containsDot(tc.in); got != tc.want {
			t.Errorf("containsDot(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
