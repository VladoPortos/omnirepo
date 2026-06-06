package rpm

// RPM mirror sync must fail explicitly with streamio.ErrArtifactTooLarge
// when an upstream serves cap+1 bytes for an artifact body. The helper
// previously used io.ReadAll(io.LimitReader(...)) which silently truncated
// to cap bytes — committing a hash-correct-but-truncated artifact. This
// test enforces the sentinel behavior at the downloadAndHashWithProgress
// layer (package-private — internal test).

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/upstreamfetch"
	"github.com/vladoportos/omnirepo/internal/streamio"
)

func TestDownloadAndHashWithProgress_OversizedUpstream_ReturnsArtifactTooLarge(t *testing.T) {
	t.Parallel()

	// Override the artifact cap to a tiny value for the duration of the
	// test so we can serve cap+1 bytes synthetically without allocating
	// gigabytes.
	const testCap int64 = 1024
	prev := maxArtifactBytes
	maxArtifactBytes = testCap
	t.Cleanup(func() { maxArtifactBytes = prev })

	// Serve cap+1 bytes deterministically. Single byte over should be
	// enough to trip the sentinel.
	body := bytes.Repeat([]byte("a"), int(testCap+1))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	got, size, dgst, err := upstreamfetch.DownloadAndHash(context.Background(), srv.Client(), srv.URL, AuthCreds{}, nil, "", nil, 0, maxArtifactBytes)
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
