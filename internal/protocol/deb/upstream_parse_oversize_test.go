package deb

// deb upstream_parse must fail-explicit with streamio.ErrMetadataTooLarge
// whenever an upstream serves cap+1 bytes for the raw HTTP body
// (Release / Packages(.gz)) OR for the gunzipped Packages stream.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/streamio"
)

func TestFetchAll_OversizedHTTPBody_ReturnsMetadataTooLarge(t *testing.T) {
	t.Parallel()

	const cap int64 = 1024
	body := bytes.Repeat([]byte("a"), int(cap+1))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	got, err := fetchAll(context.Background(), srv.Client(), srv.URL, AuthCreds{}, cap)
	if err == nil {
		t.Fatalf("expected error from cap+1 upstream, got nil (len=%d)", len(got))
	}
	if !errors.Is(err, streamio.ErrMetadataTooLarge) {
		t.Fatalf("expected errors.Is(err, streamio.ErrMetadataTooLarge); got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil body on over-limit; got %d bytes", len(got))
	}
}

func gzBytes(t *testing.T, want int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(bytes.Repeat([]byte("a"), want)); err != nil {
		t.Fatalf("gz write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

// minimalRelease returns a Release file with just enough fields for
// ParseUpstream to enumerate one (component, arch).
func minimalRelease() string {
	return "Suite: stable\nCodename: stable\nComponents: main\nArchitectures: amd64\n"
}

func TestParseUpstream_OversizedDecompressedPackages_ReturnsMetadataTooLarge(t *testing.T) {
	t.Parallel()

	const decompCap int64 = 1024
	prev := maxPackagesDecompressedBytes
	maxPackagesDecompressedBytes = decompCap
	t.Cleanup(func() { maxPackagesDecompressedBytes = prev })

	packagesGZ := gzBytes(t, int(decompCap+1))

	mux := http.NewServeMux()
	mux.HandleFunc("/dists/stable/InRelease", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(minimalRelease()))
	})
	mux.HandleFunc("/dists/stable/main/binary-amd64/Packages.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(packagesGZ)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	count, err := ParseUpstream(context.Background(), srv.Client(), srv.URL, "stable", AuthCreds{}, SyncFilter{}, func(UpstreamEntry) error { return nil })
	if err == nil {
		t.Fatalf("expected error from cap+1 decompressed Packages, got nil (count=%d)", count)
	}
	if !errors.Is(err, streamio.ErrMetadataTooLarge) {
		t.Fatalf("expected errors.Is(err, streamio.ErrMetadataTooLarge); got %v", err)
	}
	if !strings.Contains(err.Error(), "Packages") {
		t.Fatalf("expected error to reference Packages; got %q", err.Error())
	}
}
