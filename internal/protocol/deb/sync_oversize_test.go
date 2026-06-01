package deb

// Deb mirror sync must fail explicitly
// with streamio.ErrArtifactTooLarge when an upstream serves cap+1 bytes
// for an artifact body.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vladoportos/omnirepo/internal/streamio"
)

func TestDownloadAndHashWithProgress_OversizedUpstream_ReturnsArtifactTooLarge(t *testing.T) {
	t.Parallel()

	const testCap int64 = 1024
	prev := maxArtifactBytes
	maxArtifactBytes = testCap
	t.Cleanup(func() { maxArtifactBytes = prev })

	body := bytes.Repeat([]byte("a"), int(testCap+1))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	got, size, dgst, err := downloadAndHashWithProgress(context.Background(), srv.Client(), srv.URL, AuthCreds{}, nil, "", nil, 0)
	if err == nil {
		t.Fatalf("expected error from cap+1 upstream, got nil (size=%d dgst=%q len(body)=%d)", size, dgst, len(got))
	}
	if !errors.Is(err, streamio.ErrArtifactTooLarge) {
		t.Fatalf("expected errors.Is(err, streamio.ErrArtifactTooLarge); got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil body on over-limit; got %d bytes", len(got))
	}
}
