package pypi

// Plan 05-03 STREAMIO-06 (audit #5): pypi upstream_parse must fail-explicit
// with streamio.ErrMetadataTooLarge whenever an upstream serves cap+1
// bytes for the Simple-index page or for a project page.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/streamio"
)

func TestParseUpstreamSimpleIndex_OversizedBody_ReturnsMetadataTooLarge(t *testing.T) {
	t.Parallel()

	const cap int64 = 1024
	prev := maxSimpleIndexBytes
	maxSimpleIndexBytes = cap
	t.Cleanup(func() { maxSimpleIndexBytes = prev })

	body := bytes.Repeat([]byte("a"), int(cap+1))
	mux := http.NewServeMux()
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got, err := ParseUpstreamSimpleIndex(context.Background(), srv.Client(), srv.URL, AuthCreds{})
	if err == nil {
		t.Fatalf("expected error from cap+1 simple-index, got nil (got %d projects)", len(got))
	}
	if !errors.Is(err, streamio.ErrMetadataTooLarge) {
		t.Fatalf("expected errors.Is(err, streamio.ErrMetadataTooLarge); got %v", err)
	}
}

func TestParseUpstreamProject_OversizedBody_ReturnsMetadataTooLarge(t *testing.T) {
	t.Parallel()

	const cap int64 = 1024
	prev := maxProjectPageBytes
	maxProjectPageBytes = cap
	t.Cleanup(func() { maxProjectPageBytes = prev })

	body := bytes.Repeat([]byte("a"), int(cap+1))
	mux := http.NewServeMux()
	mux.HandleFunc("/simple/requests/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got, err := ParseUpstreamProject(context.Background(), srv.Client(), srv.URL, "requests", AuthCreds{})
	if err == nil {
		t.Fatalf("expected error from cap+1 project page, got nil (got %d files)", len(got))
	}
	if !errors.Is(err, streamio.ErrMetadataTooLarge) {
		t.Fatalf("expected errors.Is(err, streamio.ErrMetadataTooLarge); got %v", err)
	}
}
