package deb_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDEBPutStreamingFidelity is the streaming-rewrite regression guard for
// STREAMIO-02 (audit finding #3). Asserts that after a PUT, the on-disk
// .deb's sha256 matches the digest stored in deb_packages.Digest — i.e.
// the bytes that hit the request body are the same bytes that landed at
// the canonical pool path AND the same bytes whose digest was recorded.
//
// Existing TestDEBPutRoundTrip checks size + filename equality but never
// equates sha256(on-disk) with the DB digest column, so a mid-pipeline
// copy-corruption bug between Tee and PathStore.Put could slip past it.
func TestDEBPutStreamingFidelity(t *testing.T) {
	f := newDEBFixture(t)
	_, repoID := f.seedDEBRepo("proj", "myrepo", false)

	body := buildTestDeb(t, "mypkg", "1.0-1", "amd64")
	wantSum := sha256.Sum256(body)
	wantHex := hex.EncodeToString(wantSum[:])

	resp := f.do(t, http.MethodPut,
		"/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=stable&component=main",
		body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, out)
	}

	// On-disk pool path matches the URL pool path.
	diskPath := filepath.Join(f.repoRoot, "proj", "deb", "myrepo",
		"pool", "m", "mypkg", "mypkg_1.0-1_amd64.deb")
	diskBytes, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read on-disk: %v", err)
	}
	if int64(len(diskBytes)) != int64(len(body)) {
		t.Fatalf("disk size=%d want %d", len(diskBytes), len(body))
	}
	gotSum := sha256.Sum256(diskBytes)
	gotHex := hex.EncodeToString(gotSum[:])
	if gotHex != wantHex {
		t.Fatalf("on-disk sha256=%s want %s", gotHex, wantHex)
	}

	pkgs, err := f.debPackages.ListByRepo(context.Background(), repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("got %d rows want 1", len(pkgs))
	}
	gotDB := strings.TrimPrefix(pkgs[0].Digest, "sha256:")
	if gotDB != wantHex {
		t.Fatalf("db digest=%q want sha256:%s", pkgs[0].Digest, wantHex)
	}
}

// TestDEBPutOversizedReturns413 asserts the MaxBytesReader cap still fires
// after the streaming rewrite.
func TestDEBPutOversizedReturns413(t *testing.T) {
	f := newDEBFixture(t)
	_, _ = f.seedDEBRepo("proj", "myrepo", false)

	body := make([]byte, 2<<20) // 2 MiB > 1 MiB fixture cap.
	resp := f.do(t, http.MethodPut,
		"/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=stable&component=main",
		body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s want 413", resp.StatusCode, out)
	}
}
