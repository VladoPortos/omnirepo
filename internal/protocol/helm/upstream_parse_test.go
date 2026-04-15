package helm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
)

const upstreamIndexYAML = `apiVersion: v1
entries:
  nginx:
    - apiVersion: v2
      name: nginx
      version: 1.0.0
      appVersion: "1.25"
      digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
      urls:
        - https://example.com/charts/nginx-1.0.0.tgz
    - apiVersion: v2
      name: nginx
      version: 1.1.0
      digest: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      urls:
        - charts/nginx-1.1.0.tgz
  redis:
    - apiVersion: v2
      name: redis
      version: 7.0.0
      digest: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
      urls:
        - charts/redis-7.0.0.tgz
generated: "2026-04-15T00:00:00Z"
`

func newHelmUpstream(t *testing.T, requireBasicAuth bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireBasicAuth {
			u, p, ok := r.BasicAuth()
			if !ok || u != "alice" || p != "s3cret" {
				http.Error(w, "auth required", http.StatusUnauthorized)
				return
			}
		}
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(upstreamIndexYAML))
	}))
}

func TestHelmParseUpstreamViaSDK(t *testing.T) {
	srv := newHelmUpstream(t, false)
	defer srv.Close()

	var entries []helm.UpstreamEntry
	count, err := helm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		helm.AuthCreds{}, helm.SyncFilter{},
		func(e helm.UpstreamEntry) error {
			entries = append(entries, e)
			return nil
		})
	if err != nil {
		t.Fatalf("ParseUpstream: %v", err)
	}
	if count != 3 || len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", count)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Digest, "sha256:") {
			t.Fatalf("digest missing sha256 prefix: %+v", e)
		}
		if e.Path == "" || e.Filename == "" {
			t.Fatalf("missing path/filename: %+v", e)
		}
	}
}

func TestHelmParseUpstreamWithCreds(t *testing.T) {
	srv := newHelmUpstream(t, true)
	defer srv.Close()

	if _, err := helm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		helm.AuthCreds{}, helm.SyncFilter{}, func(helm.UpstreamEntry) error { return nil }); err == nil {
		t.Fatal("expected 401 without creds")
	}
	if _, err := helm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		helm.AuthCreds{User: "alice", Password: "s3cret"}, helm.SyncFilter{},
		func(helm.UpstreamEntry) error { return nil }); err != nil {
		t.Fatalf("with creds: %v", err)
	}
}

func TestHelmParseUpstreamFilter(t *testing.T) {
	srv := newHelmUpstream(t, false)
	defer srv.Close()

	var got []string
	_, err := helm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		helm.AuthCreds{}, helm.SyncFilter{Names: []string{"nginx"}},
		func(e helm.UpstreamEntry) error {
			got = append(got, e.Filename)
			return nil
		})
	if err != nil {
		t.Fatalf("ParseUpstream: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 nginx entries, got %v", got)
	}
}

func TestHelmParseUpstreamContextCancel(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hold }))
	defer srv.Close()
	defer close(hold)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := helm.ParseUpstream(ctx, srv.Client(), srv.URL,
		helm.AuthCreds{}, helm.SyncFilter{}, func(helm.UpstreamEntry) error { return nil }); err == nil {
		t.Fatal("expected timeout")
	}
}
