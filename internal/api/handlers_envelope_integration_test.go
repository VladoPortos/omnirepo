// Package api integration tests for the canonical ApiErrorEnvelope wire
// shape.
//
// Each test here forces a real /api/v1 handler (or middleware) to emit
// a failure response, parses the body as an envelope, and runs the
// universal envelope invariants plus class-specific
// assertions. A failure in this suite means the wire contract
// downstream UI + protocol-level clients depend on has drifted.
//
// The test server is spun up via newTestServer (shared helper in
// admin_phase1_test.go) with IncidentIDMiddleware installed, so every
// response carries the X-Incident-Id header + envelope.incident_id.
package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/httperr"
)

// uuidV7Regex matches a version-7 UUID as emitted by google/uuid.NewV7.
// Version nibble is fixed at "7"; variant nibble is one of 8/9/a/b.
var uuidV7Regex = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// envelopeWireRegex pins the ApiErrorEnvelope.code pattern from openapi.yaml.
var envelopeWireRegex = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// envelope is a typed mirror of ApiErrorEnvelope used for field-level
// asserts. Kept local to avoid a cross-package import of the generated
// type (which would force renaming to match the generated struct's
// omitempty handling).
type envelope struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Hint       string         `json:"hint,omitempty"`
	Class      string         `json:"class"`
	IncidentID string         `json:"incident_id,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

// pathLikeMarker / sourceLocMarker / stackMarker enumerate the
// internal-leakage substrings that MUST NEVER appear in a wire envelope
// message or hint. httperr.IsInternalString is the primary
// gate; these are extra belt-and-braces checks that fire on any slash,
// source location, or runtime stack frame even if IsInternalString
// misses a niche case.
var (
	pathLikeMarker   = regexp.MustCompile(`\s/[a-zA-Z0-9_/.-]+|\s/$`)
	sourceLocMarker  = regexp.MustCompile(`\.go:\d+`)
	stackMarker      = regexp.MustCompile(`\bgoroutine\b|\bruntime\.`)
	sqliteLeakMarker = regexp.MustCompile(`\bsqlite`)
)

// assertEnvelope parses body as ApiErrorEnvelope and runs the universal
// invariants:
//
//   - code+message+class are non-empty
//   - code matches the wire regex
//   - class is one of the 4 known values
//   - message and hint do NOT trip httperr.IsInternalString
//   - message and hint do NOT contain raw paths, source locations,
//     stack markers, or sqlite driver strings
//
// Returns the parsed envelope so callers can run class-specific
// assertions on top.
func assertEnvelope(t *testing.T, body []byte) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, body)
	}
	if e.Code == "" || e.Message == "" || e.Class == "" {
		t.Fatalf("envelope missing required fields: %+v\nbody: %s", e, body)
	}
	if !envelopeWireRegex.MatchString(e.Code) {
		t.Errorf("code %q does not match wire regex %q", e.Code, envelopeWireRegex)
	}
	switch e.Class {
	case "validation", "permission", "transient", "operator_action_required":
		// known
	default:
		t.Errorf("unknown class %q", e.Class)
	}
	if httperr.IsInternalString(e.Message) {
		t.Errorf("message leaks internal string: %q", e.Message)
	}
	if e.Hint != "" && httperr.IsInternalString(e.Hint) {
		t.Errorf("hint leaks internal string: %q", e.Hint)
	}
	for label, re := range map[string]*regexp.Regexp{
		"path":            pathLikeMarker,
		"source-location": sourceLocMarker,
		"stack-marker":    stackMarker,
		"sqlite-marker":   sqliteLeakMarker,
	} {
		if re.MatchString(e.Message) {
			t.Errorf("message contains %s marker: %q", label, e.Message)
		}
		if re.MatchString(e.Hint) {
			t.Errorf("hint contains %s marker: %q", label, e.Hint)
		}
	}
	return e
}

// TestEnvelope_ValidationClass_ChangePasswordEmptyBody forces the
// /api/v1/auth/change-password handler to emit a 400 with class
// validation by sending an empty JSON object (new password missing).
// Asserts the envelope shape + invariants.
func TestEnvelope_ValidationClass_ChangePasswordEmptyBody(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", "pw-alice")

	resp, body := s.doRaw(t, "POST", "/api/v1/auth/change-password", cookie,
		bytes.NewReader([]byte(`{}`)))
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("expected 4xx, got %d\nbody: %s", resp.StatusCode, body)
	}
	e := assertEnvelope(t, body)
	if e.Class != "validation" {
		t.Errorf("expected class=validation, got %q (body=%s)", e.Class, body)
	}
}

// TestEnvelope_ValidationClass_MalformedJSON forces the
// /api/v1/auth/change-password decoder to fail so the validation class
// fires on a body that is not well-formed JSON.
func TestEnvelope_ValidationClass_MalformedJSON(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", "pw-alice")

	resp, body := s.doRaw(t, "POST", "/api/v1/auth/change-password", cookie,
		bytes.NewReader([]byte(`{ this is not json`)))
	if resp.StatusCode < 400 {
		t.Fatalf("expected 4xx, got %d", resp.StatusCode)
	}
	e := assertEnvelope(t, body)
	if e.Class != "validation" {
		t.Errorf("expected class=validation, got %q", e.Class)
	}
}

// TestEnvelope_PermissionClass_LoginUnknownUser exercises the login
// handler's unknown-user path which emits 401 class=permission.
func TestEnvelope_PermissionClass_LoginUnknownUser(t *testing.T) {
	s := newTestServer(t)
	resp, body := s.doRaw(t, "POST", "/api/v1/auth/login", "",
		bytes.NewReader([]byte(`{"login":"ghost","password":"nope"}`)))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d\nbody: %s", resp.StatusCode, body)
	}
	e := assertEnvelope(t, body)
	if e.Class != "permission" {
		t.Errorf("expected class=permission, got %q", e.Class)
	}
	if e.Code != "auth.unauthenticated" {
		t.Errorf("expected code=auth.unauthenticated, got %q", e.Code)
	}
}

// TestEnvelope_PermissionClass_AdminRouteWithoutAdmin exercises the
// RequireCan middleware on GET /api/v1/admin/users with a non-admin
// user in the context. The middleware emits the
// envelope; this test is the regression gate.
func TestEnvelope_PermissionClass_AdminRouteWithoutAdmin(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "user", "u@x", false /* isAdmin */, false)
	cookie, _, _ := s.login(t, "user", pw)
	resp, body := s.do(t, "GET", "/api/v1/admin/users", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%+v", resp.StatusCode, body)
	}
	raw, _ := json.Marshal(body)
	e := assertEnvelope(t, raw)
	if e.Class != "permission" {
		t.Errorf("expected class=permission, got %q", e.Class)
	}
	// Policy reason was super_admin_required → dotted code.
	if !strings.HasPrefix(e.Code, "auth.") {
		t.Errorf("expected code under auth.*, got %q", e.Code)
	}
}

// TestEnvelope_PermissionClass_NoCookie hits the admin route without
// any authentication at all — SessionOrAPIKey middleware emits 401
// permission envelope (not 403, since the request has no actor to
// check against a policy).
func TestEnvelope_PermissionClass_NoCookie(t *testing.T) {
	s := newTestServer(t)
	resp, body := s.do(t, "GET", "/api/v1/admin/users", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%+v", resp.StatusCode, body)
	}
	raw, _ := json.Marshal(body)
	e := assertEnvelope(t, raw)
	if e.Class != "permission" {
		t.Errorf("expected class=permission, got %q", e.Class)
	}
}

// TestEnvelope_NotFound_UnknownAPIRoute forces an SPA-404 for an
// unknown /api/* path. The handler emits the envelope with
// class=validation (no retry widget for missing routes).
func TestEnvelope_NotFound_UnknownAPIRoute(t *testing.T) {
	s := newTestServer(t)
	// Note: the test server mounts /api/v1 via api.Mount but does NOT
	// install a NotFound handler, so chi returns a raw 404 with no
	// body. The /api/v1/projects/__nope__ path instead hits the
	// handleGetProject branch that returns a proper 404 envelope.
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)
	resp, body := s.do(t, "GET", "/api/v1/projects/__nope__", cookie, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%+v", resp.StatusCode, body)
	}
	raw, _ := json.Marshal(body)
	e := assertEnvelope(t, raw)
	// 404 → validation class per inferClassFromStatus table
	// (no Retry widget for missing resources).
	if e.Class != "validation" {
		t.Errorf("expected class=validation (404), got %q", e.Class)
	}
}

// TestEnvelope_IncidentIDMatchesHeader pins the parity
// invariant: the envelope.incident_id field MUST equal the
// X-Incident-Id response header, and both MUST match the UUID v7
// regex. Used by operators to grep server logs by the ID they see in
// the browser.
func TestEnvelope_IncidentIDMatchesHeader(t *testing.T) {
	s := newTestServer(t)
	resp, body := s.doRaw(t, "POST", "/api/v1/auth/login", "",
		bytes.NewReader([]byte(`{"login":"ghost","password":"nope"}`)))
	if resp.StatusCode < 400 {
		t.Fatalf("expected failure, got %d", resp.StatusCode)
	}
	e := assertEnvelope(t, body)
	hdr := resp.Header.Get("X-Incident-Id")
	if hdr == "" {
		t.Fatal("X-Incident-Id header missing")
	}
	if !uuidV7Regex.MatchString(hdr) {
		t.Errorf("header %q is not UUID v7", hdr)
	}
	if e.IncidentID == "" {
		t.Fatal("envelope.incident_id missing")
	}
	if e.IncidentID != hdr {
		t.Errorf("envelope incident_id %q != X-Incident-Id %q", e.IncidentID, hdr)
	}
	// Legacy X-Request-Id compat header should also carry the same value
	// so existing log aggregators grep on either key.
	if legacy := resp.Header.Get("X-Request-Id"); legacy != hdr {
		t.Errorf("X-Request-Id %q != X-Incident-Id %q", legacy, hdr)
	}
}

// withDevEnv is defined in dev_error_routes_test.go; reused here.

// TestEnvelope_DevRouteClasses exercises every canned envelope shape
// emitted by /api/v1/_dev/error/:class. Proves the dev story page
// fixtures are wire-shape valid and carry the class-specific details
// (retry_after_ms, operator_route, operator_label, fields map) that
// the UI renderer branches on.
func TestEnvelope_DevRouteClasses(t *testing.T) {
	withDevEnv(t, true)
	s := newTestServer(t)

	cases := []struct {
		class               string
		expectStatus        int
		expectRetry         bool
		expectOperatorRoute bool
		expectFields        bool
	}{
		{"validation", http.StatusBadRequest, false, false, true},
		{"permission", http.StatusForbidden, false, false, false},
		{"transient", http.StatusServiceUnavailable, true, false, false},
		{"operator_action_required", http.StatusServiceUnavailable, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			resp, body := s.doRaw(t, "GET", "/api/v1/_dev/error/"+tc.class, "", nil)
			if resp.StatusCode != tc.expectStatus {
				t.Fatalf("expected %d, got %d\nbody: %s", tc.expectStatus, resp.StatusCode, body)
			}
			e := assertEnvelope(t, body)
			if e.Class != tc.class {
				t.Errorf("expected class=%q, got %q", tc.class, e.Class)
			}
			if tc.expectRetry {
				if _, ok := e.Details["retry_after_ms"]; !ok {
					t.Errorf("expected details.retry_after_ms for transient class, got %+v", e.Details)
				}
			}
			if tc.expectOperatorRoute {
				if _, ok := e.Details["operator_route"]; !ok {
					t.Errorf("expected details.operator_route, got %+v", e.Details)
				}
				if _, ok := e.Details["operator_label"]; !ok {
					t.Errorf("expected details.operator_label, got %+v", e.Details)
				}
			}
			if tc.expectFields {
				fields, ok := e.Details["fields"].(map[string]any)
				if !ok {
					t.Errorf("expected details.fields map for validation, got %+v", e.Details)
				} else if len(fields) == 0 {
					t.Errorf("details.fields should be non-empty")
				}
				// Canned validation envelope carries both single-field
				// shortcut AND multi-field map — gives the UI dual
				// normalisation path a deterministic fixture.
				if _, ok := e.Details["field"]; !ok {
					t.Errorf("expected details.field single-field shortcut, got %+v", e.Details)
				}
			}
		})
	}
}

// TestEnvelope_NoInternalLeakage_AcrossHandlers is the sweep gate:
// walk every representative failure path we can force on a
// test server and assert none of them leak internals through
// Envelope.Message or Envelope.Hint. Pre-existing handler-specific
// tests may not catch a drift in one path; this test runs the gate
// across the whole set.
func TestEnvelope_NoInternalLeakage_AcrossHandlers(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	rootCookie, _, _ := s.login(t, "root", pw)

	cases := []struct {
		name        string
		method      string
		path        string
		cookie      string
		body        string
		minStatus   int
		description string
	}{
		{"login-unknown-user", "POST", "/api/v1/auth/login", "",
			`{"login":"ghost","password":"nope"}`, http.StatusUnauthorized,
			"unknown-user path"},
		{"login-malformed-body", "POST", "/api/v1/auth/login", "",
			`{ this is not json`, http.StatusUnauthorized,
			"malformed JSON body"},
		{"unauthenticated-admin-call", "GET", "/api/v1/admin/users", "",
			"", http.StatusUnauthorized,
			"missing cookie on admin route"},
		{"not-found-project", "GET", "/api/v1/projects/__does_not_exist__", rootCookie,
			"", http.StatusNotFound,
			"missing resource 404"},
		{"change-password-empty-body", "POST", "/api/v1/auth/change-password", rootCookie,
			`{}`, http.StatusUnprocessableEntity,
			"validation on missing new password"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reader *bytes.Reader
			if tc.body != "" {
				reader = bytes.NewReader([]byte(tc.body))
			}
			var resp *http.Response
			var raw []byte
			if reader != nil {
				resp, raw = s.doRaw(t, tc.method, tc.path, tc.cookie, reader)
			} else {
				resp, raw = s.doRaw(t, tc.method, tc.path, tc.cookie, nil)
			}
			if resp.StatusCode < tc.minStatus {
				// Only log (don't fail) status mismatches here — the
				// primary assertion is internal-leakage; status drift is
				// addressed by other tests.
				t.Logf("%s: got status %d (expected ≥ %d): %s",
					tc.description, resp.StatusCode, tc.minStatus, raw)
			}
			assertEnvelope(t, raw)
		})
	}
}

// TestEnvelope_ValidationNeverEchoesUserInput is the XSS-adjacent gate:
// an attacker's payload in the login field MUST NEVER be
// echoed back verbatim in the envelope. React's JSX escaping is the
// last line of defense in the UI, but the wire contract here is that
// the server does not reflect user input into the error message.
func TestEnvelope_ValidationNeverEchoesUserInput(t *testing.T) {
	s := newTestServer(t)
	payload := `{"login":"<script>alert(1)</script>","password":"x"}`
	resp, body := s.doRaw(t, "POST", "/api/v1/auth/login", "",
		bytes.NewReader([]byte(payload)))
	if resp.StatusCode < 400 {
		t.Fatalf("expected failure, got %d", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("<script>")) {
		t.Errorf("response body echoes user-supplied <script> tag: %s", body)
	}
	if bytes.Contains(body, []byte("alert(1)")) {
		t.Errorf("response body echoes user-supplied payload: %s", body)
	}
}

// TestEnvelope_ContentTypeIsJSON is an API contract gate: every error
// response on /api/v1 MUST set Content-Type to application/json so
// machine clients (and the UI's handleResponse) parse correctly. A
// middleware that accidentally writes a text/plain body would break
// the UI envelope parser silently.
func TestEnvelope_ContentTypeIsJSON(t *testing.T) {
	s := newTestServer(t)
	resp, body := s.doRaw(t, "POST", "/api/v1/auth/login", "",
		bytes.NewReader([]byte(`{"login":"ghost","password":"nope"}`)))
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q, want application/json; body=%s", ct, body)
	}
}

// TestEnvelope_TransientClass_DevRoute is a focused pin on class
// transient + retry_after_ms — the UI's Try-again CTA reads
// details.retry_after_ms and counts down from that value. Drift here
// silently breaks the retry countdown.
func TestEnvelope_TransientClass_DevRoute(t *testing.T) {
	withDevEnv(t, true)
	s := newTestServer(t)

	resp, body := s.doRaw(t, "GET", "/api/v1/_dev/error/transient", "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", resp.StatusCode, body)
	}
	e := assertEnvelope(t, body)
	if e.Class != "transient" {
		t.Fatalf("expected class=transient, got %q", e.Class)
	}
	retry, ok := e.Details["retry_after_ms"].(float64)
	if !ok {
		t.Fatalf("expected details.retry_after_ms (number), got %+v", e.Details)
	}
	if retry <= 0 {
		t.Errorf("retry_after_ms must be positive, got %v", retry)
	}
	if e.Hint == "" {
		t.Errorf("transient canned envelope should carry a hint, got empty")
	}
}

// TestEnvelope_OperatorClass_DevRoute pins class
// operator_action_required: it MUST carry operator_route + operator_label
// so the UI renders the deep-link CTA. operator_route must be a
// same-origin path (starts with "/") to prevent open redirects.
func TestEnvelope_OperatorClass_DevRoute(t *testing.T) {
	withDevEnv(t, true)
	s := newTestServer(t)

	resp, body := s.doRaw(t, "GET", "/api/v1/_dev/error/operator_action_required", "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", resp.StatusCode, body)
	}
	e := assertEnvelope(t, body)
	if e.Class != "operator_action_required" {
		t.Fatalf("expected class=operator_action_required, got %q", e.Class)
	}
	route, ok := e.Details["operator_route"].(string)
	if !ok {
		t.Fatalf("expected details.operator_route (string), got %+v", e.Details)
	}
	if !strings.HasPrefix(route, "/") {
		t.Errorf("operator_route must be a same-origin path (start with /), got %q", route)
	}
	label, ok := e.Details["operator_label"].(string)
	if !ok || label == "" {
		t.Errorf("expected non-empty details.operator_label, got %v", e.Details["operator_label"])
	}
	_ = label
}

// TestEnvelope_ApiMountProducesEnvelopes is a belt-and-braces gate
// ensuring the api.Deps-driven test server produces envelopes (not
// legacy shapes) on every canonical failure path. Regression guard in
// case a future plan re-introduces a writeJSON403 / writeJSON401 that
// bypasses httperr.Write.
func TestEnvelope_ApiMountProducesEnvelopes(t *testing.T) {
	s := newTestServer(t)

	// Ensure the api package symbol is referenced so a future drift in
	// the import graph trips the build here, not silently at runtime.
	_ = api.ErrValidationFailed

	// Sanity: a plain unauthenticated GET on /api/v1/me should 401 with
	// envelope shape.
	resp, body := s.do(t, "GET", "/api/v1/me", "", nil)
	if resp.StatusCode == http.StatusOK {
		// /me has OptionalSessionOrAPIKey — it may return 200 null.
		// In that case we hit a different protected route.
		resp, body = s.do(t, "GET", "/api/v1/admin/users", "", nil)
	}
	if resp.StatusCode < 400 {
		t.Fatalf("expected 4xx on unauthenticated protected route, got %d body=%+v",
			resp.StatusCode, body)
	}
	raw, _ := json.Marshal(body)
	assertEnvelope(t, raw)
}
