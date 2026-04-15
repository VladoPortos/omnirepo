package oci_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestChallengeHeaderUsesXForwardedProto validates that when a TLS-
// terminating reverse proxy injects X-Forwarded-Proto, the realm scheme
// reflects that. Important for deployments behind nginx/traefik where
// r.TLS is nil but the public URL is https.
func TestChallengeHeaderUsesXForwardedProto(t *testing.T) {
	f := newOCIFixture(t)

	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/_catalog", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	got := resp.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(got, `Bearer realm="https://`) {
		t.Fatalf("expected https realm; got %q", got)
	}
}

// TestVerifyBearer_NoAuthHeader_Challenges asserts that the no-creds
// branch still emits a Bearer challenge.
func TestVerifyBearer_NoAuthHeader_Challenges(t *testing.T) {
	f := newOCIFixture(t)

	resp, _ := http.Get(f.srv.URL + "/v2/_catalog")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d; want 401", resp.StatusCode)
	}
	if h := resp.Header.Get("WWW-Authenticate"); h == "" {
		t.Fatal("missing WWW-Authenticate header on challenge")
	}
}

// TestVerifyBearer_MalformedBearer_Challenges feeds garbage as the
// Bearer token. Should hit the parse-error branch and challenge.
func TestVerifyBearer_MalformedBearer_Challenges(t *testing.T) {
	f := newOCIFixture(t)

	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/_catalog", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d; want 401", resp.StatusCode)
	}
}

// TestVerifyBearer_EmptyBearer_Challenges feeds "Bearer " (no token).
func TestVerifyBearer_EmptyBearer_Challenges(t *testing.T) {
	f := newOCIFixture(t)

	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/_catalog", nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d; want 401", resp.StatusCode)
	}
}

// TestRouteIsolation confirms that the /v2 router is mounted exactly at
// /v2 (not at root) — a request to / must not match the OCI handler.
func TestRouteIsolation(t *testing.T) {
	f := newOCIFixture(t)
	r2 := chi.NewRouter()
	r2.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("root"))
	})
	f.handler.Mount(r2)

	srv := httptest.NewServer(r2)
	t.Cleanup(srv.Close)

	resp, _ := http.Get(srv.URL + "/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root: %d", resp.StatusCode)
	}
}
