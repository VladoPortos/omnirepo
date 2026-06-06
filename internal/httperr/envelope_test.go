package httperr_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/httperr"
)

// ----------------------------------------------------------------------------
// Code regex / validity
// ----------------------------------------------------------------------------

func TestCodeRegex_ValidCodes(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"repo.not_found", true},
		{"user.name_required", true},
		{"trivy.db_missing", true},
		{"a.b", true},
		{"a1.b2_c3", true},
		{"auth.forbidden", true},

		// Invalid — leading digit
		{"1repo.not_found", false},
		// Invalid — no dot
		{"reponotfound", false},
		// Invalid — uppercase
		{"Repo.NotFound", false},
		// Invalid — empty
		{"", false},
		// Invalid — multiple dots
		{"a.b.c", false},
		// Invalid — leading underscore
		{"_repo.not_found", false},
		// Invalid — trailing dot
		{"repo.", false},
		// Invalid — leading dot
		{".not_found", false},
		// Invalid — whitespace
		{"BAD CODE", false},
		// Invalid — hyphen
		{"repo-name.not_found", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.code, func(t *testing.T) {
			got := httperr.CodeIsValid(c.code)
			if got != c.want {
				t.Fatalf("CodeIsValid(%q) = %v, want %v", c.code, got, c.want)
			}
		})
	}
}

func TestCodeRegex_EveryConstructorPanicsOnBadCode(t *testing.T) {
	constructors := []struct {
		name string
		run  func()
	}{
		{"Validation", func() { httperr.Validation("BAD CODE", "msg") }},
		{"ValidationFields", func() { httperr.ValidationFields("BAD CODE", "msg", map[string]string{"a": "b"}) }},
		{"Permission", func() { httperr.Permission("BAD CODE", "msg") }},
		{"Transient", func() { httperr.Transient("BAD CODE", "msg", 0) }},
		{"OperatorRequired", func() { httperr.OperatorRequired("BAD CODE", "msg", "/x", "y") }},
		{"Internal", func() { httperr.Internal("BAD CODE", errors.New("boom")) }},
	}
	for _, c := range constructors {
		c := c
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s(bad code) did not panic", c.name)
				}
			}()
			c.run()
		})
	}
}

// ----------------------------------------------------------------------------
// Validation constructors
// ----------------------------------------------------------------------------

func TestValidation_ReturnsCorrectClassAndStatus(t *testing.T) {
	e := httperr.Validation("user.name_required", "Name is required")
	if e == nil {
		t.Fatal("nil error")
	}
	if e.Envelope.Class != httperr.ClassValidation {
		t.Errorf("class = %q, want %q", e.Envelope.Class, httperr.ClassValidation)
	}
	if e.Status != 400 {
		t.Errorf("status = %d, want 400", e.Status)
	}
	if e.Envelope.Code != "user.name_required" {
		t.Errorf("code = %q", e.Envelope.Code)
	}
	if e.Envelope.Message != "Name is required" {
		t.Errorf("message = %q", e.Envelope.Message)
	}
}


func TestValidationFields_SetsFieldsMap(t *testing.T) {
	in := map[string]string{"user.name": "required", "user.email": "invalid"}
	e := httperr.ValidationFields("user.form_invalid", "Please fix the errors below.", in)
	if e.Envelope.Details == nil {
		t.Fatal("Details nil")
	}
	got, ok := e.Envelope.Details["fields"].(map[string]string)
	if !ok {
		t.Fatalf("details[fields] wrong type, got %T", e.Envelope.Details["fields"])
	}
	if got["user.name"] != "required" {
		t.Errorf("user.name = %q, want required", got["user.name"])
	}
	if got["user.email"] != "invalid" {
		t.Errorf("user.email = %q, want invalid", got["user.email"])
	}
}

// ----------------------------------------------------------------------------
// Permission constructor
// ----------------------------------------------------------------------------

func TestPermission_ReturnsCorrectClassAndStatus(t *testing.T) {
	e := httperr.Permission("auth.forbidden", "You do not have permission to view this.")
	if e.Envelope.Class != httperr.ClassPermission {
		t.Errorf("class = %q, want %q", e.Envelope.Class, httperr.ClassPermission)
	}
	if e.Status != 403 {
		t.Errorf("status = %d, want 403", e.Status)
	}
	if string(e.Envelope.Class) != "permission" {
		t.Errorf("class string = %q, want %q", string(e.Envelope.Class), "permission")
	}
}

// ----------------------------------------------------------------------------
// Transient constructor
// ----------------------------------------------------------------------------

func TestTransient_SetsRetryAfterWhenPositive(t *testing.T) {
	e := httperr.Transient("storage.temporarily_unavailable", "We couldn't reach the server.", 3000)
	if e.Envelope.Class != httperr.ClassTransient {
		t.Errorf("class = %q", e.Envelope.Class)
	}
	if e.Status != 503 {
		t.Errorf("status = %d, want 503", e.Status)
	}
	if e.Envelope.Details == nil {
		t.Fatal("Details nil")
	}
	got, ok := e.Envelope.Details["retry_after_ms"].(int)
	if !ok {
		t.Fatalf("retry_after_ms not int, got %T", e.Envelope.Details["retry_after_ms"])
	}
	if got != 3000 {
		t.Errorf("retry_after_ms = %d, want 3000", got)
	}
}

func TestTransient_OmitsRetryAfterWhenZero(t *testing.T) {
	e := httperr.Transient("storage.temporarily_unavailable", "We couldn't reach the server.", 0)
	if e.Envelope.Details != nil {
		if _, ok := e.Envelope.Details["retry_after_ms"]; ok {
			t.Errorf("retry_after_ms unexpectedly present: %v", e.Envelope.Details)
		}
	}
}

// ----------------------------------------------------------------------------
// OperatorRequired constructor
// ----------------------------------------------------------------------------

func TestOperatorRequired_SetsRouteAndLabel(t *testing.T) {
	e := httperr.OperatorRequired(
		"trivy.db_missing",
		"Trivy database not installed.",
		"/admin/trivy",
		"Go to Admin → Trivy",
	)
	if e.Envelope.Class != httperr.ClassOperatorRequired {
		t.Errorf("class = %q, want %q", e.Envelope.Class, httperr.ClassOperatorRequired)
	}
	if e.Status != 503 {
		t.Errorf("status = %d, want 503", e.Status)
	}
	if e.Envelope.Details == nil {
		t.Fatal("Details nil")
	}
	if route, _ := e.Envelope.Details["operator_route"].(string); route != "/admin/trivy" {
		t.Errorf("operator_route = %q", route)
	}
	if label, _ := e.Envelope.Details["operator_label"].(string); label != "Go to Admin → Trivy" {
		t.Errorf("operator_label = %q", label)
	}
}

// ----------------------------------------------------------------------------
// Internal constructor
// ----------------------------------------------------------------------------

func TestInternal_HasGenericMessageAndCause(t *testing.T) {
	cause := errors.New("db query failed")
	e := httperr.Internal("api.unexpected", cause)
	if e.Envelope.Message != "An internal error occurred." {
		t.Errorf("message = %q", e.Envelope.Message)
	}
	if e.Envelope.Class != httperr.ClassTransient {
		t.Errorf("class = %q, want %q", e.Envelope.Class, httperr.ClassTransient)
	}
	if e.Status != 500 {
		t.Errorf("status = %d, want 500", e.Status)
	}
	if e.Cause == nil {
		t.Fatal("cause nil")
	}
	if !errors.Is(e.Cause, cause) {
		t.Errorf("cause chain broken")
	}
}

func TestInternal_EnvelopeMessageNeverLeaksCause(t *testing.T) {
	leaky := errors.New("/var/lib/omnirepo/db.sqlite: open: permission denied")
	e := httperr.Internal("api.unexpected", leaky)
	if strings.Contains(e.Envelope.Message, "/var/lib/omnirepo") {
		t.Errorf("message leaks path: %q", e.Envelope.Message)
	}
	if strings.Contains(e.Envelope.Message, "sqlite") {
		t.Errorf("message leaks sqlite: %q", e.Envelope.Message)
	}
	if strings.Contains(e.Envelope.Message, "permission denied") {
		t.Errorf("message leaks syscall text: %q", e.Envelope.Message)
	}
	if e.Envelope.Hint != "" {
		t.Errorf("hint should be empty, got %q", e.Envelope.Hint)
	}
}


// ----------------------------------------------------------------------------
// IsInternalString
// ----------------------------------------------------------------------------

func TestIsInternalString_DetectsLeakage(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/foo/bar", true},
		{"failed: .go:123", true},
		{"sqlite3 error", true},
		{"sqlite", true},
		{"goroutine 1 [running]", true},
		{"runtime.something", true},
		{"read: connection reset", true},
		{"open: no such file", true},
		{"stat: not found", true},
		{"Name is required", false},
		{"You do not have permission", false},
		{"Trivy database not installed.", false},
		{"", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			got := httperr.IsInternalString(c.in)
			if got != c.want {
				t.Errorf("IsInternalString(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// JSON marshalling
// ----------------------------------------------------------------------------

func TestEnvelope_JSONMarshal(t *testing.T) {
	// Minimum envelope — only required fields.
	min := httperr.Envelope{
		Code:    "repo.not_found",
		Message: "Repository not found.",
		Class:   httperr.ClassValidation,
	}
	b, err := json.Marshal(min)
	if err != nil {
		t.Fatalf("marshal err: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"code":"repo.not_found"`) {
		t.Errorf("missing code key: %s", s)
	}
	if !strings.Contains(s, `"message":"Repository not found."`) {
		t.Errorf("missing message key: %s", s)
	}
	if !strings.Contains(s, `"class":"validation"`) {
		t.Errorf("missing class key: %s", s)
	}
	if strings.Contains(s, "hint") {
		t.Errorf("hint should be omitted when empty: %s", s)
	}
	if strings.Contains(s, "incident_id") {
		t.Errorf("incident_id should be omitted when empty: %s", s)
	}
	if strings.Contains(s, "details") {
		t.Errorf("details should be omitted when empty: %s", s)
	}

	// Fully-populated envelope — all fields present.
	full := httperr.Envelope{
		Code:       "user.name_required",
		Message:    "Name is required",
		Hint:       "Enter a name.",
		Class:      httperr.ClassValidation,
		IncidentID: "01930000-0000-7000-8000-000000000000",
		Details:    map[string]any{"field": "user.name"},
	}
	b2, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal err: %v", err)
	}
	s2 := string(b2)
	for _, key := range []string{`"code"`, `"message"`, `"hint"`, `"class"`, `"incident_id"`, `"details"`} {
		if !strings.Contains(s2, key) {
			t.Errorf("missing %s in full envelope: %s", key, s2)
		}
	}

	// Round-trip — unmarshal should produce equal struct.
	var back httperr.Envelope
	if err := json.Unmarshal(b2, &back); err != nil {
		t.Fatalf("unmarshal err: %v", err)
	}
	if back.Code != full.Code || back.Class != full.Class || back.IncidentID != full.IncidentID {
		t.Errorf("roundtrip mismatch: %+v vs %+v", back, full)
	}
}

// ----------------------------------------------------------------------------
// errors.As chain
// ----------------------------------------------------------------------------

func TestAs_UnwrapsErrorChain(t *testing.T) {
	base := httperr.Validation("user.name_required", "Name is required")
	wrapped := fmt.Errorf("handle user create: %w", base)

	var got *httperr.Error
	if !errors.As(wrapped, &got) {
		t.Fatal("errors.As returned false on wrapped *Error")
	}
	if got != base {
		t.Errorf("errors.As did not return the original *Error pointer")
	}

	// Error() returns Envelope.Message
	if base.Error() != "Name is required" {
		t.Errorf("Error() = %q, want %q", base.Error(), "Name is required")
	}

	// Unwrap returns Cause (nil in this case since Validation has no cause)
	if base.Unwrap() != nil {
		t.Errorf("Unwrap() should be nil")
	}

	// Unwrap with cause
	causeErr := errors.New("inner")
	withCause := httperr.Internal("api.unexpected", causeErr)
	if withCause.Unwrap() != causeErr {
		t.Errorf("Unwrap() mismatch")
	}
	if !errors.Is(withCause, causeErr) {
		t.Errorf("errors.Is should find the cause")
	}
}

// ----------------------------------------------------------------------------
// Options composition
// ----------------------------------------------------------------------------

func TestOptions_WithHintStatusDetail(t *testing.T) {
	e := httperr.Validation(
		"user.name_required",
		"Name is required",
		httperr.WithHint("Enter a name between 3 and 30 characters."),
		httperr.WithStatus(422),
		httperr.WithDetail("extra", "info"),
	)
	if e.Envelope.Hint != "Enter a name between 3 and 30 characters." {
		t.Errorf("hint mismatch: %q", e.Envelope.Hint)
	}
	if e.Status != 422 {
		t.Errorf("status = %d, want 422", e.Status)
	}
	if e.Envelope.Details["extra"] != "info" {
		t.Errorf("detail extra = %v", e.Envelope.Details["extra"])
	}
}

func TestOptions_WithCause(t *testing.T) {
	cause := errors.New("disk full")
	e := httperr.Transient("storage.temporarily_unavailable", "We couldn't reach the server.", 0, httperr.WithCause(cause))
	if e.Cause != cause {
		t.Errorf("WithCause did not set cause")
	}
}

// ----------------------------------------------------------------------------
// No-leak guarantee: constructor inputs never interpolated via %v
// ----------------------------------------------------------------------------

func TestConstructors_DoNotFormatUserInput(t *testing.T) {
	// Safety net: the constructor code is asserted in acceptance criteria
	// to contain no fmt.Sprintf/%v formatting. This test confirms that
	// messages passed in are stored verbatim, never wrapped.
	msg := "verbatim message with %v and %s formatters"
	e := httperr.Validation("user.name_required", msg)
	if e.Envelope.Message != msg {
		t.Errorf("message altered: %q", e.Envelope.Message)
	}
}
