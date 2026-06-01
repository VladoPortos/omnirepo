package helm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/protocol/helm"
	"github.com/vladoportos/omnirepo/internal/protocol/helm/ociclient"
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

// TestUpstreamEntrySourceClassification verifies that ParseUpstream tags
// each UpstreamEntry with the correct EntrySourceKind based on its Path
// prefix, which drives fetchAndCommit dispatch. The fixture
// index.yaml above yields only https:// URLs, so every entry must be
// EntrySourceHTTP. The second sub-test synthesizes an oci:// entry by
// serving a tweaked index.yaml and asserts EntrySourceOCI.
func TestUpstreamEntrySourceClassification(t *testing.T) {
	t.Run("https_entries_tagged_http", func(t *testing.T) {
		srv := newHelmUpstream(t, false)
		defer srv.Close()

		var entries []helm.UpstreamEntry
		_, err := helm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
			helm.AuthCreds{}, helm.SyncFilter{},
			func(e helm.UpstreamEntry) error {
				entries = append(entries, e)
				return nil
			})
		if err != nil {
			t.Fatalf("ParseUpstream: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("expected ≥1 entry")
		}
		for _, e := range entries {
			if e.Source != helm.EntrySourceHTTP {
				t.Errorf("entry %q: got Source=%v, want EntrySourceHTTP", e.Path, e.Source)
			}
		}
	})

	t.Run("oci_entries_tagged_oci", func(t *testing.T) {
		// Synthesize an index.yaml whose URLs point at oci://... — ParseUpstream
		// must still tag these even though the sync handler will
		// later route them through ociclient, not HTTP.
		ociIndex := `apiVersion: v1
entries:
  redis:
    - apiVersion: v2
      name: redis
      version: 7.0.0
      digest: cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
      urls:
        - oci://registry-1.docker.io/bitnamicharts/redis:7.0.0
generated: "2026-04-22T00:00:00Z"
`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/index.yaml" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/x-yaml")
			_, _ = w.Write([]byte(ociIndex))
		}))
		defer srv.Close()

		var entries []helm.UpstreamEntry
		_, err := helm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
			helm.AuthCreds{}, helm.SyncFilter{},
			func(e helm.UpstreamEntry) error {
				entries = append(entries, e)
				return nil
			})
		if err != nil {
			t.Fatalf("ParseUpstream: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if got := entries[0].Source; got != helm.EntrySourceOCI {
			t.Errorf("oci entry: got Source=%v, want EntrySourceOCI", got)
		}
	})
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

// TestParseOCIUpstream covers pure-OCI top-level helm mirror enumeration
// (the prior implementation left SyncHandler unable to process oci://
// top-level refs). The FakeClient stands in for the Helm SDK's OCI registry
// so the test stays hermetic.
func TestParseOCIUpstream(t *testing.T) {
	t.Run("yields semver tags with synthetic filenames and source=oci", func(t *testing.T) {
		fake := ociclient.NewFake()
		fake.Tags["registry-1.docker.io/bitnamicharts/nginx"] = []string{
			"15.14.0",
			"15.14.1",
			"latest",  // non-semver → skipped
			"invalid", // non-semver → skipped
		}
		var got []helm.UpstreamEntry
		n, err := helm.ParseOCIUpstream(context.Background(), fake,
			"oci://registry-1.docker.io/bitnamicharts/nginx",
			helm.AuthCreds{User: "u", Password: "p"},
			helm.SyncFilter{},
			func(e helm.UpstreamEntry) error { got = append(got, e); return nil })
		if err != nil {
			t.Fatalf("ParseOCIUpstream: %v", err)
		}
		if n != 2 || len(got) != 2 {
			t.Fatalf("want 2 yielded, got n=%d entries=%d", n, len(got))
		}
		// Order mirrors ListTags order from the fake; sort before asserting.
		sort.Slice(got, func(i, j int) bool { return got[i].Path < got[j].Path })
		if got[0].Path != "oci://registry-1.docker.io/bitnamicharts/nginx:15.14.0" {
			t.Errorf("entry[0].Path = %q", got[0].Path)
		}
		if got[0].Filename != "nginx-15.14.0.tgz" {
			t.Errorf("entry[0].Filename = %q, want nginx-15.14.0.tgz", got[0].Filename)
		}
		if got[0].Source != helm.EntrySourceOCI {
			t.Errorf("entry[0].Source = %v, want EntrySourceOCI", got[0].Source)
		}
	})

	t.Run("credentials thread through to ListTags", func(t *testing.T) {
		fake := ociclient.NewFake()
		fake.Tags["registry-1.docker.io/bitnamicharts/redis"] = []string{"7.0.0"}
		_, err := helm.ParseOCIUpstream(context.Background(), fake,
			"oci://registry-1.docker.io/bitnamicharts/redis",
			helm.AuthCreds{User: "alice", Password: "pat"},
			helm.SyncFilter{},
			func(helm.UpstreamEntry) error { return nil })
		if err != nil {
			t.Fatalf("ParseOCIUpstream: %v", err)
		}
		if fake.LastCreds.User != "alice" || fake.LastCreds.Password != "pat" {
			t.Errorf("creds not threaded: got %+v", fake.LastCreds)
		}
	})

	t.Run("name filter excludes the whole upstream when chart does not match", func(t *testing.T) {
		fake := ociclient.NewFake()
		fake.Tags["registry-1.docker.io/bitnamicharts/postgresql"] = []string{"15.0.0"}
		yielded := 0
		n, err := helm.ParseOCIUpstream(context.Background(), fake,
			"oci://registry-1.docker.io/bitnamicharts/postgresql",
			helm.AuthCreds{},
			helm.SyncFilter{Names: []string{"nginx"}}, // mismatch
			func(helm.UpstreamEntry) error { yielded++; return nil })
		if err != nil {
			t.Fatalf("ParseOCIUpstream: %v", err)
		}
		if n != 0 || yielded != 0 {
			t.Errorf("expected 0 yield, got n=%d yielded=%d", n, yielded)
		}
		// Efficiency: filter short-circuits before ListTags is called.
		for _, c := range fake.Calls {
			if strings.HasPrefix(c, "ListTags:") {
				t.Errorf("ListTags should be skipped when name filter rejects the chart; got %q", c)
			}
		}
	})

	t.Run("glob filter on filename", func(t *testing.T) {
		fake := ociclient.NewFake()
		fake.Tags["registry-1.docker.io/bitnamicharts/nginx"] = []string{"15.14.0", "17.0.0"}
		var got []string
		_, err := helm.ParseOCIUpstream(context.Background(), fake,
			"oci://registry-1.docker.io/bitnamicharts/nginx",
			helm.AuthCreds{},
			helm.SyncFilter{Globs: []string{"*-17.*.tgz"}},
			func(e helm.UpstreamEntry) error { got = append(got, e.Filename); return nil })
		if err != nil {
			t.Fatalf("ParseOCIUpstream: %v", err)
		}
		if len(got) != 1 || got[0] != "nginx-17.0.0.tgz" {
			t.Errorf("glob filter result = %v, want [nginx-17.0.0.tgz]", got)
		}
	})

	t.Run("nil client returns descriptive error", func(t *testing.T) {
		_, err := helm.ParseOCIUpstream(context.Background(), nil,
			"oci://registry-1.docker.io/bitnamicharts/nginx",
			helm.AuthCreds{}, helm.SyncFilter{},
			func(helm.UpstreamEntry) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "OCIClient not wired") {
			t.Errorf("want OCIClient-not-wired error, got %v", err)
		}
	})

	t.Run("ListTags error propagates wrapped", func(t *testing.T) {
		fake := ociclient.NewFake()
		fake.Errors["registry-1.docker.io/bitnamicharts/nginx"] = errors.New("registry unreachable")
		_, err := helm.ParseOCIUpstream(context.Background(), fake,
			"oci://registry-1.docker.io/bitnamicharts/nginx",
			helm.AuthCreds{}, helm.SyncFilter{},
			func(helm.UpstreamEntry) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "registry unreachable") {
			t.Errorf("want wrapped ListTags error, got %v", err)
		}
	})

	t.Run("bad oci ref (no path segment) returns descriptive error", func(t *testing.T) {
		fake := ociclient.NewFake()
		_, err := helm.ParseOCIUpstream(context.Background(), fake,
			"oci://registry-1.docker.io", // missing chart path
			helm.AuthCreds{}, helm.SyncFilter{},
			func(helm.UpstreamEntry) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "cannot derive chart name") {
			t.Errorf("want chart-name-derive error, got %v", err)
		}
	})

	// Regression — Bitnami publishes "<ver>-metadata" sidecar tags
	// alongside every chart tag; these are single-layer OCI artifacts
	// carrying scan + SBOM data, not Helm charts. Semver parses them
	// (pre-release label), so the naive filter yielded them — Helm SDK
	// Pull aborted mid-batch with "minimum number of descriptors".
	t.Run("bitnami -metadata sidecar tags are filtered at enumeration", func(t *testing.T) {
		fake := ociclient.NewFake()
		fake.Tags["registry-1.docker.io/bitnamicharts/nginx"] = []string{
			"22.6.5",
			"22.6.5-metadata",
			"22.6.4",
			"22.6.4-metadata",
			// Capitalized variant — should also be skipped (lower-case compare).
			"22.6.3-METADATA",
			"22.6.3",
		}
		var got []string
		_, err := helm.ParseOCIUpstream(context.Background(), fake,
			"oci://registry-1.docker.io/bitnamicharts/nginx",
			helm.AuthCreds{User: "u", Password: "p"},
			helm.SyncFilter{},
			func(e helm.UpstreamEntry) error { got = append(got, e.Filename); return nil })
		if err != nil {
			t.Fatalf("ParseOCIUpstream: %v", err)
		}
		sort.Strings(got)
		want := []string{"nginx-22.6.3.tgz", "nginx-22.6.4.tgz", "nginx-22.6.5.tgz"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i, w := range want {
			if got[i] != w {
				t.Errorf("entry[%d] = %q, want %q", i, got[i], w)
			}
		}
	})
}
