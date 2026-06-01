package api_test

// Tests for dev-only canned-error routes that the UI story page hits to
// verify ErrorEnvelopeRenderer rendering for each ApiErrorClass. Routes
// are gated behind OMNIREPO_DEV=1 at mount time.
//
// Phase 6 / plan 03 task 1 — RED gate before implementation.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/api"
)

// mountDevOnly spins up a bare chi router with just MountDevErrorRoutes
// registered, so the test exercises registration + handler output without
// touching the full /api/v1 middleware stack.
func mountDevOnly(t *testing.T) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	api.MountDevErrorRoutes(r)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

func withDevEnv(t *testing.T, on bool) {
	t.Helper()
	prev := os.Getenv("OMNIREPO_DEV")
	if on {
		if err := os.Setenv("OMNIREPO_DEV", "1"); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.Unsetenv("OMNIREPO_DEV"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if prev == "" {
			_ = os.Unsetenv("OMNIREPO_DEV")
		} else {
			_ = os.Setenv("OMNIREPO_DEV", prev)
		}
	})
}

func TestMountDevErrorRoutes_DisabledByDefault(t *testing.T) {
	withDevEnv(t, false)
	ts := mountDevOnly(t)

	resp, err := http.Get(ts.URL + "/api/v1/_dev/error/validation")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when OMNIREPO_DEV is unset, got %d", resp.StatusCode)
	}
}

func TestMountDevErrorRoutes_ValidationEnvelope(t *testing.T) {
	withDevEnv(t, true)
	ts := mountDevOnly(t)

	env := fetchEnvelope(t, ts.URL+"/api/v1/_dev/error/validation", http.StatusBadRequest)

	if got := env["code"]; got != "dev.validation" {
		t.Fatalf("code=%v, want dev.validation", got)
	}
	if got := env["class"]; got != "validation" {
		t.Fatalf("class=%v, want validation", got)
	}
	if _, ok := env["message"].(string); !ok {
		t.Fatalf("expected string message, got %v", env["message"])
	}
	details, ok := env["details"].(map[string]any)
	if !ok {
		t.Fatalf("expected details map, got %v", env["details"])
	}
	fields, ok := details["fields"].(map[string]any)
	if !ok {
		t.Fatalf("expected details.fields map, got %v", details["fields"])
	}
	if got := fields["user.email"]; got != "invalid" {
		t.Fatalf("details.fields[user.email]=%v, want invalid", got)
	}
}

func TestMountDevErrorRoutes_PermissionEnvelope(t *testing.T) {
	withDevEnv(t, true)
	ts := mountDevOnly(t)

	env := fetchEnvelope(t, ts.URL+"/api/v1/_dev/error/permission", http.StatusForbidden)

	if got := env["code"]; got != "dev.permission" {
		t.Fatalf("code=%v, want dev.permission", got)
	}
	if got := env["class"]; got != "permission" {
		t.Fatalf("class=%v, want permission", got)
	}
	if _, ok := env["hint"].(string); !ok {
		t.Fatalf("expected hint string, got %v", env["hint"])
	}
}

func TestMountDevErrorRoutes_TransientEnvelope(t *testing.T) {
	withDevEnv(t, true)
	ts := mountDevOnly(t)

	env := fetchEnvelope(t, ts.URL+"/api/v1/_dev/error/transient", http.StatusServiceUnavailable)

	if got := env["class"]; got != "transient" {
		t.Fatalf("class=%v, want transient", got)
	}
	details, ok := env["details"].(map[string]any)
	if !ok {
		t.Fatalf("expected details map, got %v", env["details"])
	}
	retryAfter, ok := details["retry_after_ms"].(float64)
	if !ok || retryAfter != 3000 {
		t.Fatalf("retry_after_ms=%v, want 3000", details["retry_after_ms"])
	}
}

func TestMountDevErrorRoutes_OperatorRequiredEnvelope(t *testing.T) {
	withDevEnv(t, true)
	ts := mountDevOnly(t)

	env := fetchEnvelope(t, ts.URL+"/api/v1/_dev/error/operator_action_required", http.StatusServiceUnavailable)

	if got := env["class"]; got != "operator_action_required" {
		t.Fatalf("class=%v, want operator_action_required", got)
	}
	details, ok := env["details"].(map[string]any)
	if !ok {
		t.Fatalf("expected details map, got %v", env["details"])
	}
	if got := details["operator_route"]; got != "/admin/trivy" {
		t.Fatalf("details.operator_route=%v, want /admin/trivy", got)
	}
	if got := details["operator_label"]; got == nil || got == "" {
		t.Fatalf("details.operator_label=%v, want non-empty string", got)
	}
}

func TestMountDevErrorRoutes_UnknownClassIs400(t *testing.T) {
	withDevEnv(t, true)
	ts := mountDevOnly(t)

	resp, err := http.Get(ts.URL + "/api/v1/_dev/error/not_a_class")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown class, got %d", resp.StatusCode)
	}
}

// fetchEnvelope GETs the URL, asserts the given status code, and decodes the
// body as an ApiErrorEnvelope JSON object. Any failure fails the test.
func fetchEnvelope(t *testing.T, url string, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("status=%d, want %d", resp.StatusCode, wantStatus)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}
