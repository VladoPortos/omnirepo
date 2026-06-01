package oci_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestChallenge_IgnoresXForwardedProto is the regression guard. An
// untrusted X-Forwarded-Proto: https from any client must NOT be able to
// flip the challenge realm scheme — that would let a malicious client
// redirect the docker CLI's follow-up token fetch to an attacker's origin.
// Until config.HTTP.ExternalURL + TrustedProxies are added, the scheme is
// driven solely by r.TLS.
func TestChallenge_IgnoresXForwardedProto(t *testing.T) {
	f := newOCIFixture(t)

	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/nope/docker/nope/manifests/latest", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	got := resp.Header.Get("WWW-Authenticate")
	// httptest.NewServer is plaintext, so r.TLS is nil — realm MUST stay
	// http regardless of the injected X-Forwarded-Proto header.
	if !strings.HasPrefix(got, `Bearer realm="http://`) {
		t.Fatalf("XFP must be ignored; got realm %q", got)
	}
}

// TestVerifyBearer_NoAuthHeader_Challenges asserts that the no-creds
// branch still emits a Bearer challenge.
func TestVerifyBearer_NoAuthHeader_Challenges(t *testing.T) {
	f := newOCIFixture(t)

	resp, err := http.Get(f.srv.URL + "/v2/nope/docker/nope/manifests/latest")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
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

	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/nope/docker/nope/manifests/latest", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d; want 401", resp.StatusCode)
	}
}

// TestVerifyBearer_EmptyBearer_Challenges feeds "Bearer " (no token).
func TestVerifyBearer_EmptyBearer_Challenges(t *testing.T) {
	f := newOCIFixture(t)

	req, _ := http.NewRequest("GET", f.srv.URL+"/v2/nope/docker/nope/manifests/latest", nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
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

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root: %d", resp.StatusCode)
	}
}
