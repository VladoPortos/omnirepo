package helm_test

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

// TestHelmPutStreamingFidelity is the streaming-rewrite regression guard.
// Asserts that after a PUT, the on-disk
// .tgz's sha256 matches the digest stored in helm_charts.Digest — i.e.
// the bytes that hit the request body are the same bytes that landed at
// the canonical charts/ path AND the same bytes whose digest was recorded.
//
// Existing TestHelmPutChartStoresRow checks bytes.Equal(disk, body) but
// only asserts the DB digest has the "sha256:" prefix; it never equates
// the digest to the actual sha256 of the bytes. A mid-pipeline corruption
// between Tee and PathStore.Put could slip past it.
func TestHelmPutStreamingFidelity(t *testing.T) {
	f := newFixture(t)
	_, rid := f.seedRepo("proj1", "charts1", false, false)

	tgz := makeChartTGZ(t, "mychart", "1.2.3", "v1", "a test chart", []string{"a", "b"})
	wantSum := sha256.Sum256(tgz)
	wantHex := hex.EncodeToString(wantSum[:])

	resp := f.put(t, "/proj1/helm/charts1/charts/mychart-1.2.3.tgz", tgz, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}

	diskPath := filepath.Join(f.repoRoot, "proj1", "helm", "charts1", "charts", "mychart-1.2.3.tgz")
	diskBytes, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read on-disk: %v", err)
	}
	if int64(len(diskBytes)) != int64(len(tgz)) {
		t.Fatalf("disk size=%d want %d", len(diskBytes), len(tgz))
	}
	gotSum := sha256.Sum256(diskBytes)
	gotHex := hex.EncodeToString(gotSum[:])
	if gotHex != wantHex {
		t.Fatalf("on-disk sha256=%s want %s", gotHex, wantHex)
	}

	row, err := f.charts.FindByNameVersion(context.Background(), rid, "mychart", "1.2.3")
	if err != nil || row == nil {
		t.Fatalf("helm_charts row missing: %v", err)
	}
	gotDB := strings.TrimPrefix(row.Digest, "sha256:")
	if gotDB != wantHex {
		t.Fatalf("db digest=%q want sha256:%s", row.Digest, wantHex)
	}
}

// TestHelmPutOversizedReturns413 asserts the MaxBytesReader cap still fires
// after the streaming rewrite (chart upload path).
func TestHelmPutOversizedReturns413(t *testing.T) {
	f := newFixture(t)
	_, _ = f.seedRepo("proj1", "charts1", false, false)

	body := make([]byte, 2<<20) // 2 MiB > 1 MiB fixture cap.
	resp := f.put(t, "/proj1/helm/charts1/charts/mychart-1.2.3.tgz", body, true)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s want 413", resp.StatusCode, out)
	}
}
