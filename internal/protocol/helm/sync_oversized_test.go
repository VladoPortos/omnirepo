package helm

// Plan 05-04 STREAMIO-08 (closes audit findings #4 + #5 with end-to-end
// integration coverage for Helm): when an upstream serves cap+1 bytes for
// either a chart .tgz body OR the index.yaml metadata, the full mirror
// sync flow must fail with an error whose Error() text contains the
// streamio.Err{Artifact|Metadata}TooLarge sentinel string AND commit zero
// new rows to helm_charts.
//
// Plan 05-03 wired downloadAndHash through streamio.ReadAllLimited at the
// artifact layer. Plan 05-04 closes the helm-specific metadata gap that
// 05-03 missed: helm/upstream_parse.go::ParseUpstream previously fetched
// index.yaml via io.Copy(..., io.LimitReader(64 MiB)) which silently
// truncated. Plan 05-04 replaced that with streamio.ReadAllLimited via
// the new maxIndexYAMLBytes cap var (test-overridable for cap+1).
//
// Sentinel-propagation note: SyncHandler.Handle wraps the failure path
// through internal/httpx.SanitizeUpstreamErr (T-03-06-01) which deliberately
// drops the wrap chain to prevent credential leakage. errors.Is therefore
// CANNOT walk back to streamio.ErrArtifactTooLarge through Handle's return
// value — but the sanitised text preserves the streamio sentinel string.
// The errors.Is contract at the helper layer is covered by Plan 05-03's
// sync_oversize_test.go.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/storage"
	"github.com/dxc-internal/omnirepo/internal/streamio"
)

type helmOversizedZeroReader struct{ remaining int64 }

func (z *helmOversizedZeroReader) Read(p []byte) (int, error) {
	if z.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > z.remaining {
		n = z.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 0
	}
	z.remaining -= n
	return int(n), nil
}

type helmOversizedFixture struct {
	t      *testing.T
	h      *SyncHandler
	db     *metadata.DB
	repoID int64
}

func (f *helmOversizedFixture) countHelmCharts() int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM helm_charts WHERE repo_id=?`, f.repoID).Scan(&n); err != nil {
		f.t.Fatalf("count helm_charts: %v", err)
	}
	return n
}

func (f *helmOversizedFixture) seedSyncJob() int64 {
	f.t.Helper()
	res, err := f.db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('helm_sync', ?, 'running', '{}', '{}')`,
		f.repoID,
	)
	if err != nil {
		f.t.Fatalf("seed sync_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newHelmOversizedFixture(t *testing.T, upstreamClient *http.Client) *helmOversizedFixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	helmCharts := metadata.NewHelmChartsRepo(db)
	scans := metadata.NewScansRepo(db)

	pid, err := projectsRepo.Create(ctx, "pp", "STREAMIO-08 oversized helm")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "helm", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	pathStore := storage.NewPathStore(repoRoot)

	h := NewSyncHandler(SyncDeps{
		DB:         db,
		Path:       pathStore,
		HelmCharts: helmCharts,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		HTTPClient: upstreamClient,
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
	})
	return &helmOversizedFixture{t: t, h: h, db: db, repoID: rid}
}

// helmMakeChartTGZ builds a minimal valid Helm chart tgz with the supplied
// Chart.yaml fields. Local copy of the testutil helper (which lives in
// package helm_test, inaccessible from this internal test).
func helmMakeChartTGZ(t *testing.T, name, version string) []byte {
	t.Helper()
	chartYAML := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\ntype: application\n", name, version)
	notes := "Test chart NOTES\n"

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	writeTarFile := func(path, body string) {
		h := &tar.Header{
			Name:     path,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar header %s: %v", path, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", path, err)
		}
	}
	writeTarFile(name+"/Chart.yaml", chartYAML)
	writeTarFile(name+"/templates/NOTES.txt", notes)
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

// TestHelmSync_OversizedArtifactRejected proves STREAMIO-08 (audit #4) for
// Helm: when upstream returns cap+1 bytes for a chart .tgz body, sync
// fails with an error whose text contains streamio.ErrArtifactTooLarge AND
// zero new helm_charts rows commit.
func TestHelmSync_OversizedArtifactRejected(t *testing.T) {
	const testCap = int64(4096)
	prevCap := maxArtifactBytes
	maxArtifactBytes = testCap
	t.Cleanup(func() { maxArtifactBytes = prevCap })

	// Advertise a chart with sha256 + size for cap bytes; serve cap+1.
	advertisedBody := bytes.Repeat([]byte("x"), int(testCap))
	sum := sha256.Sum256(advertisedBody)
	digestHex := hex.EncodeToString(sum[:])

	index := `apiVersion: v1
entries:
  nginx:
    - apiVersion: v2
      name: nginx
      version: 1.0.0
      digest: ` + digestHex + `
      urls:
        - charts/nginx-1.0.0.tgz
generated: "2026-04-25T00:00:00Z"
`
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(index))
	})
	mux.HandleFunc("/charts/nginx-1.0.0.tgz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = io.CopyN(w, &helmOversizedZeroReader{remaining: testCap + 1}, testCap+1)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fix := newHelmOversizedFixture(t, srv.Client())

	rowsBefore := fix.countHelmCharts()
	jobID := fix.seedSyncJob()
	pb, _ := json.Marshal(map[string]string{"upstream_url": srv.URL})
	err := fix.h.Handle(context.Background(), string(pb), 0, fix.repoID, jobID)

	rowsAfter := fix.countHelmCharts()
	if err == nil {
		t.Fatalf("expected sync error for cap+1 artifact, got nil")
	}
	wantToken := streamio.ErrArtifactTooLarge.Error()
	if !strings.Contains(err.Error(), wantToken) {
		t.Fatalf("expected sanitized error to contain %q (streamio.ErrArtifactTooLarge text); got: %v", wantToken, err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("expected zero new helm_charts rows on cap-exceed failure, got %d new rows (before=%d after=%d)",
			rowsAfter-rowsBefore, rowsBefore, rowsAfter)
	}
	_ = errors.Is
	_ = helmMakeChartTGZ // keep helper referenced for future overflow-of-real-chart variant
}

// TestHelmSync_OversizedMetadataRejected proves STREAMIO-08 (audit #5) for
// Helm — closes the gap Plan 05-03 missed: when upstream returns cap+1
// bytes for index.yaml, sync now fails via streamio.ErrMetadataTooLarge AND
// zero helm_charts rows commit. Pre-Plan-05-04 the io.LimitReader at 64
// MiB silently truncated, leaving sync to parse a partial index.yaml and
// drop charts past the truncation point.
func TestHelmSync_OversizedMetadataRejected(t *testing.T) {
	const testCap = int64(4096)
	prevCap := maxIndexYAMLBytes
	maxIndexYAMLBytes = testCap
	t.Cleanup(func() { maxIndexYAMLBytes = prevCap })

	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		// Stream cap+1 zero bytes — trips ParseUpstream's new sentinel.
		_, _ = io.CopyN(w, &helmOversizedZeroReader{remaining: testCap + 1}, testCap+1)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fix := newHelmOversizedFixture(t, srv.Client())

	rowsBefore := fix.countHelmCharts()
	jobID := fix.seedSyncJob()
	pb, _ := json.Marshal(map[string]string{"upstream_url": srv.URL})
	err := fix.h.Handle(context.Background(), string(pb), 0, fix.repoID, jobID)

	rowsAfter := fix.countHelmCharts()
	if err == nil {
		t.Fatalf("expected sync error for cap+1 metadata, got nil")
	}
	wantToken := streamio.ErrMetadataTooLarge.Error()
	if !strings.Contains(err.Error(), wantToken) {
		t.Fatalf("expected sanitized error to contain %q (streamio.ErrMetadataTooLarge text); got: %v", wantToken, err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("expected zero new helm_charts rows on metadata cap-exceed, got %d new rows (before=%d after=%d)",
			rowsAfter-rowsBefore, rowsBefore, rowsAfter)
	}
	_ = errors.Is
}
