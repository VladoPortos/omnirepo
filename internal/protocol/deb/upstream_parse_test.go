package deb_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/protocol/deb"
)

const releaseBody = `Suite: stable
Codename: bookworm
Components: main contrib
Architectures: amd64 arm64
`

func makePackagesGZ(t *testing.T, paragraphs ...string) []byte {
	t.Helper()
	body := strings.Join(paragraphs, "\n") + "\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(body))
	_ = gz.Close()
	return buf.Bytes()
}

func newDEBUpstream(t *testing.T, requireBasicAuth bool) *httptest.Server {
	t.Helper()
	pkgsAmd64 := makePackagesGZ(t,
		`Package: curl
Version: 7.88.1-10
Architecture: amd64
Maintainer: Alessandro Ghedini <ghedo@debian.org>
Filename: pool/main/c/curl/curl_7.88.1-10_amd64.deb
Size: 1234567
SHA256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
Description: command line tool
`,
		`Package: bash
Version: 5.2.15-2
Architecture: amd64
Filename: pool/main/b/bash/bash_5.2.15-2_amd64.deb
Size: 987654
SHA256: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
Description: GNU Bourne Again SHell
`,
	)
	pkgsArm64 := makePackagesGZ(t,
		`Package: curl
Version: 7.88.1-10
Architecture: arm64
Filename: pool/main/c/curl/curl_7.88.1-10_arm64.deb
Size: 1234560
SHA256: cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
Description: command line tool (arm64)
`,
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireBasicAuth {
			u, p, ok := r.BasicAuth()
			if !ok || u != "alice" || p != "s3cret" {
				http.Error(w, "auth required", http.StatusUnauthorized)
				return
			}
		}
		switch r.URL.Path {
		case "/dists/stable/InRelease":
			http.NotFound(w, r) // force fallback to Release for one path
		case "/dists/stable/Release":
			_, _ = w.Write([]byte(releaseBody))
		case "/dists/stable/main/binary-amd64/Packages.gz":
			_, _ = w.Write(pkgsAmd64)
		case "/dists/stable/main/binary-arm64/Packages.gz":
			_, _ = w.Write(pkgsArm64)
		case "/dists/stable/contrib/binary-amd64/Packages.gz",
			"/dists/stable/contrib/binary-arm64/Packages.gz":
			_, _ = w.Write(makePackagesGZ(t)) // empty
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestDEBParseUpstreamYieldsPackages(t *testing.T) {
	srv := newDEBUpstream(t, false)
	defer srv.Close()

	var got []deb.UpstreamEntry
	count, err := deb.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		"stable", deb.AuthCreds{}, deb.SyncFilter{Components: []string{"main"}},
		func(e deb.UpstreamEntry) error { got = append(got, e); return nil })
	if err != nil {
		t.Fatalf("ParseUpstream: %v", err)
	}
	// 2 amd64 + 1 arm64 = 3 entries
	if count != 3 {
		t.Fatalf("expected 3 entries, got %d (%+v)", count, got)
	}
	for _, e := range got {
		if !strings.HasPrefix(e.Digest, "sha256:") {
			t.Fatalf("missing digest: %+v", e)
		}
		if e.Filename == "" || e.Path == "" {
			t.Fatalf("missing path/filename: %+v", e)
		}
	}
}

func TestDEBParseUpstreamWithCreds(t *testing.T) {
	srv := newDEBUpstream(t, true)
	defer srv.Close()
	if _, err := deb.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		"stable", deb.AuthCreds{}, deb.SyncFilter{},
		func(deb.UpstreamEntry) error { return nil }); err == nil {
		t.Fatal("expected 401")
	}
	if _, err := deb.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		"stable", deb.AuthCreds{User: "alice", Password: "s3cret"}, deb.SyncFilter{Components: []string{"main"}},
		func(deb.UpstreamEntry) error { return nil }); err != nil {
		t.Fatalf("with creds: %v", err)
	}
}

func TestDEBParseUpstreamFilter(t *testing.T) {
	srv := newDEBUpstream(t, false)
	defer srv.Close()
	var got []string
	_, err := deb.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		"stable", deb.AuthCreds{},
		deb.SyncFilter{Components: []string{"main"}, Names: []string{"curl"}},
		func(e deb.UpstreamEntry) error {
			got = append(got, fmt.Sprintf("%s/%s", e.Arch, e.Control.Package))
			return nil
		})
	if err != nil {
		t.Fatalf("ParseUpstream: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("name filter wrong: %v", got)
	}
	for _, g := range got {
		if !strings.Contains(g, "curl") {
			t.Fatalf("non-curl leaked: %v", got)
		}
	}
}

func TestDEBParseUpstreamContextCancel(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hold }))
	defer srv.Close()
	defer close(hold)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := deb.ParseUpstream(ctx, srv.Client(), srv.URL,
		"stable", deb.AuthCreds{}, deb.SyncFilter{},
		func(deb.UpstreamEntry) error { return nil }); err == nil {
		t.Fatal("expected timeout")
	}
}
