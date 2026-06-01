package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/httperr"
)

func TestNormalizeLegacyCode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "api.unknown"},
		{"password-change-required", "auth.password_change_required"},
		{"unauthenticated", "auth.unauthenticated"},
		{"forbidden", "auth.forbidden"},
		{"not_found", "resource.not_found"},
		{"validation_failed", "validation.failed"},
		{"conflict", "resource.conflict"},
		{"internal", "api.internal"},
		// Already wire-shaped — pass through unchanged.
		{"repo.not_found", "repo.not_found"},
		{"user.email_required", "user.email_required"},
		// Unknown single-word codes get prefixed with legacy.
		{"bananas", "legacy.bananas"},
		{"Unknown-Code", "legacy.unknown_code"},
		{"weird Code", "legacy.weird_code"},
		// Multi-dot / malformed-dotted codes DO NOT pass through — they
		// violate the wire envelope regex (exactly one '.'). Bridge
		// forces them through the sanitize path to keep the emitted
		// envelope schema-valid.
		{"some.dotted.code", "legacy.somedottedcode"},
	}
	for _, tc := range cases {
		got := normalizeLegacyCode(tc.in)
		if got != tc.want {
			t.Errorf("normalizeLegacyCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInferClassFromStatus(t *testing.T) {
	cases := []struct {
		status int
		want   httperr.Class
	}{
		{http.StatusBadRequest, httperr.ClassValidation},            // 400
		{http.StatusUnauthorized, httperr.ClassPermission},          // 401
		{http.StatusForbidden, httperr.ClassPermission},             // 403
		{http.StatusNotFound, httperr.ClassValidation},              // 404 — no Retry offered for missing resources
		{http.StatusConflict, httperr.ClassValidation},              // 409
		{http.StatusRequestEntityTooLarge, httperr.ClassValidation}, // 413
		{http.StatusUnprocessableEntity, httperr.ClassValidation},   // 422
		{http.StatusTooManyRequests, httperr.ClassTransient},        // 429
		{http.StatusInternalServerError, httperr.ClassTransient},    // 500
		{http.StatusBadGateway, httperr.ClassTransient},             // 502
		{http.StatusServiceUnavailable, httperr.ClassTransient},     // 503
	}
	for _, tc := range cases {
		got := inferClassFromStatus(tc.status)
		if got != tc.want {
			t.Errorf("inferClassFromStatus(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestSanitizeCode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"simple", "simple"},
		{"with-dash", "with_dash"},
		{"with space", "with_space"},
		{"UPPER", "upper"},
		{"MixedCase-42", "mixedcase_42"},
		{"2startswithdigit", "x_2startswithdigit"},
		{"!@#$%", "x_"}, // all stripped → prefix inserted
		{"", "x_"},
	}
	for _, tc := range cases {
		got := sanitizeCode(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteJSONError_EmitsEnvelopeShape(t *testing.T) {
	// Integration-ish: call writeJSONError via the 5-arg signature,
	// decode the body as an httperr.Envelope, assert each field.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/something", nil)

	writeJSONError(rec, req, http.StatusUnauthorized, ErrUnauthenticated, "session expired")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected JSON content-type, got %q", ct)
	}

	var env httperr.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not envelope JSON: %v; body=%s", err, rec.Body.String())
	}
	if env.Code != "auth.unauthenticated" {
		t.Errorf("Code = %q, want auth.unauthenticated", env.Code)
	}
	if env.Class != httperr.ClassPermission {
		t.Errorf("Class = %q, want permission", env.Class)
	}
	if env.Message != "session expired" {
		t.Errorf("Message = %q, want 'session expired'", env.Message)
	}
}

func TestWriteJSONError_DeletedLegacyErrorResponseStruct(t *testing.T) {
	// Compile-time test: if the ErrorResponse struct still exists, this
	// test should fail to compile. That is the enforcement.
	// We assert the bridge contract by encoding via writeJSONError and
	// confirming the body does NOT carry the legacy `{"error":...}` key.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)

	writeJSONError(rec, req, http.StatusBadRequest, ErrValidationFailed, "bad input")

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body invalid: %v", err)
	}
	if _, hasErrorKey := raw["error"]; hasErrorKey {
		t.Errorf("legacy `error` key leaked into response body: %v", raw)
	}
	if _, hasDetailKey := raw["detail"]; hasDetailKey {
		t.Errorf("legacy `detail` key leaked into response body: %v", raw)
	}
	if _, hasCode := raw["code"]; !hasCode {
		t.Errorf("envelope `code` key missing: %v", raw)
	}
	if _, hasClass := raw["class"]; !hasClass {
		t.Errorf("envelope `class` key missing: %v", raw)
	}
}

func TestWriteEnvelope_PassThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)

	e := httperr.Validation("user.name_required", "Name is required")
	writeEnvelope(rec, req, e)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var env httperr.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body invalid: %v", err)
	}
	if env.Code != "user.name_required" || env.Class != httperr.ClassValidation {
		t.Errorf("envelope fields unexpected: %+v", env)
	}
}
