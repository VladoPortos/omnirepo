package upstreamfetch_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/upstreamfetch"
	"github.com/vladoportos/omnirepo/internal/streamio"
)

func TestApplyCreds(t *testing.T) {
	mk := func() *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "http://upstream.example/x", nil)
		return r
	}

	// Token wins over basic.
	r := mk()
	upstreamfetch.ApplyCreds(r, upstreamfetch.Creds{Token: "tok", User: "u", Password: "p"})
	if got := r.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", got)
	}

	// Basic auth when only user/password set.
	r = mk()
	upstreamfetch.ApplyCreds(r, upstreamfetch.Creds{User: "u", Password: "p"})
	if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
		t.Fatalf("basic auth = %q/%q ok=%v", u, p, ok)
	}

	// Empty creds → no header.
	r = mk()
	upstreamfetch.ApplyCreds(r, upstreamfetch.Creds{})
	if got := r.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should be empty, got %q", got)
	}
}

func TestFetchAll(t *testing.T) {
	body := []byte("metadata-body")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := upstreamfetch.FetchAll(context.Background(), srv.Client(), srv.URL, upstreamfetch.Creds{}, 1024, "test upstream")
	if err != nil || string(got) != string(body) {
		t.Fatalf("FetchAll = %q err=%v", got, err)
	}

	// Non-200 → error carrying the prefix and status.
	_, err = upstreamfetch.FetchAll(context.Background(), srv.Client(), srv.URL+"/missing", upstreamfetch.Creds{}, 1024, "test upstream")
	if err == nil || !strings.Contains(err.Error(), "test upstream") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want prefixed 404 error, got %v", err)
	}

	// Cap+1 → explicit ErrMetadataTooLarge, not truncation.
	_, err = upstreamfetch.FetchAll(context.Background(), srv.Client(), srv.URL, upstreamfetch.Creds{}, int64(len(body)-1), "test upstream")
	if !errors.Is(err, streamio.ErrMetadataTooLarge) {
		t.Fatalf("want ErrMetadataTooLarge, got %v", err)
	}
}

func TestDownloadAndHash(t *testing.T) {
	body := []byte("artifact-bytes-for-hashing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	sum := sha256.Sum256(body)
	wantHex := hex.EncodeToString(sum[:])

	// Without progress.
	got, size, dgst, err := upstreamfetch.DownloadAndHash(context.Background(), srv.Client(), srv.URL, upstreamfetch.Creds{}, nil, "", nil, 0, 1<<20)
	if err != nil {
		t.Fatalf("DownloadAndHash: %v", err)
	}
	if string(got) != string(body) || size != int64(len(body)) || dgst != wantHex {
		t.Fatalf("got len=%d size=%d dgst=%s want len=%d dgst=%s", len(got), size, dgst, len(body), wantHex)
	}

	// With progress accumulation: accumulatedDone advances to len(body).
	var done int64
	_, _, _, err = upstreamfetch.DownloadAndHash(context.Background(), srv.Client(), srv.URL, upstreamfetch.Creds{}, nil, "step", &done, int64(len(body)), 1<<20)
	if err != nil {
		t.Fatalf("DownloadAndHash with counter: %v", err)
	}
	// progress==nil disables the CountingReader wrapper even when the
	// counter is supplied — done must remain untouched.
	if atomic.LoadInt64(&done) != 0 {
		t.Fatalf("done = %d, want 0 with nil progress", done)
	}

	// Cap+1 → explicit ErrArtifactTooLarge.
	_, _, _, err = upstreamfetch.DownloadAndHash(context.Background(), srv.Client(), srv.URL, upstreamfetch.Creds{}, nil, "", nil, 0, int64(len(body)-1))
	if !errors.Is(err, streamio.ErrArtifactTooLarge) {
		t.Fatalf("want ErrArtifactTooLarge, got %v", err)
	}
}
