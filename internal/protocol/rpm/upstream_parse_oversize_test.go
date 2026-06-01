package rpm

// rpm upstream_parse must fail-explicit with streamio.ErrMetadataTooLarge
// whenever an upstream serves cap+1 bytes for the raw HTTP body (repomd /
// primary.xml.gz) OR for the gunzipped primary.xml stream. Previously the
// helper used io.ReadAll(io.LimitReader(...)) which silently truncated
// metadata to cap bytes — committing a partial mirror with packages dropped.

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
		w.Header().Set("Content-Type", "application/xml")
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

// Builds a tiny gzip stream that decompresses to want bytes of "a".
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

// minimalRepomd returns the smallest possible repomd.xml referencing primary
// at the supplied href.
func minimalRepomd(primaryHref string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<repomd>
  <data type="primary">
    <location href="` + primaryHref + `"/>
    <checksum type="sha256">deadbeef</checksum>
  </data>
</repomd>`
}

func TestParseUpstream_OversizedDecompressedPrimary_ReturnsMetadataTooLarge(t *testing.T) {
	t.Parallel()

	// Shrink the decompressed-primary cap so a tiny compressed body
	// expands to cap+1.
	const decompCap int64 = 1024
	prev := maxPrimaryDecompressedBytes
	maxPrimaryDecompressedBytes = decompCap
	t.Cleanup(func() { maxPrimaryDecompressedBytes = prev })

	primaryGZ := gzBytes(t, int(decompCap+1))

	mux := http.NewServeMux()
	mux.HandleFunc("/repodata/repomd.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(minimalRepomd("repodata/primary.xml.gz")))
	})
	mux.HandleFunc("/repodata/primary.xml.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(primaryGZ)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	count, err := ParseUpstream(context.Background(), srv.Client(), srv.URL, AuthCreds{}, SyncFilter{}, func(UpstreamEntry) error { return nil })
	if err == nil {
		t.Fatalf("expected error from cap+1 decompressed primary, got nil (count=%d)", count)
	}
	if !errors.Is(err, streamio.ErrMetadataTooLarge) {
		t.Fatalf("expected errors.Is(err, streamio.ErrMetadataTooLarge); got %v", err)
	}
	// Sanity: the err string still mentions "primary" so operators can
	// localize the failure layer.
	if !strings.Contains(err.Error(), "primary") {
		t.Fatalf("expected error to reference primary; got %q", err.Error())
	}
}
