package rpm_test

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

// TestRPMPutStreamingFidelity is the streaming-rewrite regression guard.
// It asserts that after a PUT, the on-disk
// artifact's sha256 matches the digest stored in rpm_packages — i.e. the
// bytes that hit the request body are the same bytes that landed at the
// canonical storage path AND the same bytes whose digest was recorded in
// the metadata row. Future regressions that re-introduce double-buffering
// or mid-pipeline copies that diverge from the hash-tee path will break
// this assertion.
//
// The existing TestRPMPutRoundTrip checks size + filename equality but
// never explicitly equates sha256(on-disk bytes) with the DB digest column,
// so a copy-corruption bug between Tee and PathStore.Put could slip past it.
func TestRPMPutStreamingFidelity(t *testing.T) {
	f := newRPMFixture(t)
	_, repoID := f.seedRepo("proj", "myrepo", false)

	body := readFixtureRPM(t)
	// Independently compute what the digest MUST be if streaming is correct.
	wantSum := sha256.Sum256(body)
	wantHex := hex.EncodeToString(wantSum[:])

	resp := f.put(t, "/proj/rpm/myrepo/packages/"+sampleRPMCanonical, body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, out)
	}

	// Read on-disk file and compute sha256. It MUST equal wantHex.
	diskPath := filepath.Join(f.repoRoot, "proj", "rpm", "myrepo", "packages", sampleRPMCanonical)
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

	// DB row's Digest column ("sha256:<hex>") must match the same hash.
	pkgs, err := f.rpmPackages.ListByRepo(context.Background(), repoID)
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

// TestRPMPutOversizedReturns413 is the streaming-rewrite negative-path guard.
// The MaxBytesReader cap is unchanged by the rewrite; only the post-cap
// buffer pattern changes. This test asserts the cap still fires (the
// fixture sets maxPutBytes = 1 MiB; sample.rpm is ~23 KiB, so we send a
// 2 MiB padded body to exceed the cap deterministically).
func TestRPMPutOversizedReturns413(t *testing.T) {
	f := newRPMFixture(t)
	_, _ = f.seedRepo("proj", "myrepo", false)

	body := make([]byte, 2<<20) // 2 MiB > 1 MiB fixture cap.
	resp := f.put(t, "/proj/rpm/myrepo/packages/"+sampleRPMCanonical, body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s want 413", resp.StatusCode, out)
	}
}
