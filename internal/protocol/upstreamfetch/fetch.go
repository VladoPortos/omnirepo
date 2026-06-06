// Package upstreamfetch holds the HTTP fetch helpers shared by the
// mirror-sync upstream clients (deb, rpm, pypi, helm). Exactly one copy of
// credential application, capped metadata GETs, and the hashing artifact
// download — these were previously duplicated per protocol package.
package upstreamfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/vladoportos/omnirepo/internal/jobs"
	"github.com/vladoportos/omnirepo/internal/streamio"
)

// Creds carries optional upstream auth. Token wins over basic auth.
// Protocol packages alias this as their AuthCreds.
type Creds struct {
	User, Password, Token string
}

// ApplyCreds sets the Authorization header on req: Bearer when Token is
// set, otherwise basic auth when either User or Password is non-empty.
// No-op when all fields are empty.
func ApplyCreds(req *http.Request, creds Creds) {
	switch {
	case creds.Token != "":
		req.Header.Set("Authorization", "Bearer "+creds.Token)
	case creds.User != "" || creds.Password != "":
		req.SetBasicAuth(creds.User, creds.Password)
	}
}

// FetchAll GETs urlStr and returns at most maxBytes of body, failing
// explicit (streamio.ErrMetadataTooLarge) on cap+1 metadata bodies
// instead of the prior silent-truncation idiom. errPrefix namespaces the
// error text per protocol (e.g. "deb upstream", "rpm upstream").
func FetchAll(ctx context.Context, client *http.Client, urlStr string, creds Creds, maxBytes int64, errPrefix string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build req: %w", errPrefix, err)
	}
	ApplyCreds(req, creds)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: get %s: %w", errPrefix, urlStr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s -> %d", errPrefix, urlStr, resp.StatusCode)
	}
	body, err := streamio.ReadAllLimited(resp.Body, maxBytes, streamio.ErrMetadataTooLarge)
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", errPrefix, err)
	}
	return body, nil
}

// DownloadAndHash GETs urlStr and returns (body, size, sha256-hex). When
// progress is non-nil, the response body is wrapped with
// jobs.CountingReader so every non-zero read advances *accumulatedDone
// (atomic under parallel downloads) and triggers a throttled progress.Set
// with the supplied step; total is passed through verbatim. The body is
// capped at maxBytes, failing explicit with streamio.ErrArtifactTooLarge
// on cap+1 instead of silently truncating.
func DownloadAndHash(ctx context.Context, client *http.Client, urlStr string, creds Creds, progress *jobs.ProgressWriter, step string, accumulatedDone *int64, total, maxBytes int64) ([]byte, int64, string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("build req: %w", err)
	}
	ApplyCreds(req, creds)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("get %s: %w", urlStr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, "", fmt.Errorf("%s -> %d", urlStr, resp.StatusCode)
	}
	hasher := sha256.New()
	var reader = io.TeeReader(resp.Body, hasher)
	if progress != nil && accumulatedDone != nil {
		reader = &jobs.CountingReader{R: reader, OnRead: func(n int) {
			done := atomic.AddInt64(accumulatedDone, int64(n))
			_ = progress.Set(ctx, step, done, total)
		}}
	}
	body, err := streamio.ReadAllLimited(reader, maxBytes, streamio.ErrArtifactTooLarge)
	if err != nil {
		return nil, 0, "", fmt.Errorf("read %s: %w", urlStr, err)
	}
	return body, int64(len(body)), hex.EncodeToString(hasher.Sum(nil)), nil
}
