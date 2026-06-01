package pypi

// End-to-end integration coverage for PyPI: when an upstream serves cap+1
// bytes for either an artifact body OR a metadata index (the
// /simple/<project>/ page), the full mirror sync flow must fail with an
// error whose Error() text contains the streamio.Err{Artifact|Metadata}TooLarge
// sentinel string AND commit zero new rows to pypi_files.
//
// Sentinel-propagation note: SyncHandler.Handle wraps the failure path
// through internal/httpx.SanitizeUpstreamErr which deliberately drops the
// wrap chain to prevent credential leakage. errors.Is therefore CANNOT walk
// back to streamio.ErrArtifactTooLarge through Handle's return value — the
// sanitised text preserves the streamio sentinel string. The errors.Is
// contract at the helper layer is covered by sync_oversize_test.go +
// upstream_parse_oversize_test.go.

import (
	"bytes"
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

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/storage"
	"github.com/vladoportos/omnirepo/internal/streamio"
)

type pypiOversizedZeroReader struct{ remaining int64 }

func (z *pypiOversizedZeroReader) Read(p []byte) (int, error) {
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

type pypiOversizedFixture struct {
	t      *testing.T
	h      *SyncHandler
	db     *metadata.DB
	repoID int64
}

func (f *pypiOversizedFixture) countPyPIFiles() int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pypi_files WHERE repo_id=?`, f.repoID).Scan(&n); err != nil {
		f.t.Fatalf("count pypi_files: %v", err)
	}
	return n
}

func (f *pypiOversizedFixture) seedSyncJob() int64 {
	f.t.Helper()
	res, err := f.db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('pypi_sync', ?, 'running', '{}', '{}')`,
		f.repoID,
	)
	if err != nil {
		f.t.Fatalf("seed sync_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newPyPIOversizedFixture(t *testing.T, upstreamClient *http.Client) *pypiOversizedFixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	pypiFiles := metadata.NewPyPIFilesRepo(db)
	scans := metadata.NewScansRepo(db)

	pid, err := projectsRepo.Create(ctx, "pp", "oversized pypi")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "pypi", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	pathStore := storage.NewPathStore(repoRoot)

	h := NewSyncHandler(SyncDeps{
		DB:         db,
		Path:       pathStore,
		PyPIFiles:  pypiFiles,
		Repos:      reposRepo,
		Projects:   projectsRepo,
		Scans:      scans,
		HTTPClient: upstreamClient,
		RepoRoot:   repoRoot,
		Cfg:        config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:   metadata.NewSyncJobsRepo(db),
	})
	return &pypiOversizedFixture{t: t, h: h, db: db, repoID: rid}
}

// TestPyPISync_OversizedArtifactRejected proves that for PyPI, when upstream
// returns cap+1 bytes for a .whl body, sync fails with an error whose text
// contains streamio.ErrArtifactTooLarge AND zero new pypi_files rows commit.
func TestPyPISync_OversizedArtifactRejected(t *testing.T) {
	const testCap = int64(4096)
	prevCap := maxArtifactBytes
	maxArtifactBytes = testCap
	t.Cleanup(func() { maxArtifactBytes = prevCap })

	// PEP 691 advertises cap bytes; upstream serves cap+1.
	advertisedBody := bytes.Repeat([]byte("x"), int(testCap))
	sum := sha256.Sum256(advertisedBody)
	digestHex := hex.EncodeToString(sum[:])

	indexJSON := `{"meta":{"api-version":"1.0"},"projects":[{"name":"acme"}]}`
	projectJSON := fmt.Sprintf(`{
		"meta": {"api-version":"1.0"},
		"name":"acme",
		"files":[
			{"filename":"acme-1.0.0-py3-none-any.whl","url":"/packages/acme-1.0.0-py3-none-any.whl","hashes":{"sha256":"%s"},"size":%d}
		]
	}`, digestHex, len(advertisedBody))

	mux := http.NewServeMux()
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(indexJSON))
	})
	mux.HandleFunc("/simple/acme/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(projectJSON))
	})
	mux.HandleFunc("/packages/acme-1.0.0-py3-none-any.whl", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.CopyN(w, &pypiOversizedZeroReader{remaining: testCap + 1}, testCap+1)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fix := newPyPIOversizedFixture(t, srv.Client())

	rowsBefore := fix.countPyPIFiles()
	jobID := fix.seedSyncJob()
	pb, _ := json.Marshal(map[string]any{"upstream_url": srv.URL})
	err := fix.h.Handle(context.Background(), string(pb), 0, fix.repoID, jobID)

	rowsAfter := fix.countPyPIFiles()
	if err == nil {
		t.Fatalf("expected sync error for cap+1 artifact, got nil")
	}
	wantToken := streamio.ErrArtifactTooLarge.Error()
	if !strings.Contains(err.Error(), wantToken) {
		t.Fatalf("expected sanitized error to contain %q (streamio.ErrArtifactTooLarge text); got: %v", wantToken, err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("expected zero new pypi_files rows on cap-exceed failure, got %d new rows (before=%d after=%d)",
			rowsAfter-rowsBefore, rowsBefore, rowsAfter)
	}
	_ = errors.Is
}

// TestPyPISync_OversizedMetadataRejected proves that for PyPI, when upstream
// returns cap+1 bytes for the /simple/<project>/ page, sync fails with an
// error whose text contains streamio.ErrMetadataTooLarge AND zero pypi_files
// rows commit.
func TestPyPISync_OversizedMetadataRejected(t *testing.T) {
	const testCap = int64(4096)
	prevCap := maxProjectPageBytes
	maxProjectPageBytes = testCap
	t.Cleanup(func() { maxProjectPageBytes = prevCap })

	indexJSON := `{"meta":{"api-version":"1.0"},"projects":[{"name":"acme"}]}`

	mux := http.NewServeMux()
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(indexJSON))
	})
	mux.HandleFunc("/simple/acme/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		// Stream cap+1 zero bytes — trips ParseUpstreamProject's cap.
		_, _ = io.CopyN(w, &pypiOversizedZeroReader{remaining: testCap + 1}, testCap+1)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fix := newPyPIOversizedFixture(t, srv.Client())

	rowsBefore := fix.countPyPIFiles()
	jobID := fix.seedSyncJob()
	pb, _ := json.Marshal(map[string]any{"upstream_url": srv.URL})
	err := fix.h.Handle(context.Background(), string(pb), 0, fix.repoID, jobID)

	rowsAfter := fix.countPyPIFiles()
	if err == nil {
		t.Fatalf("expected sync error for cap+1 metadata, got nil")
	}
	wantToken := streamio.ErrMetadataTooLarge.Error()
	if !strings.Contains(err.Error(), wantToken) {
		t.Fatalf("expected sanitized error to contain %q (streamio.ErrMetadataTooLarge text); got: %v", wantToken, err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("expected zero new pypi_files rows on metadata cap-exceed, got %d new rows (before=%d after=%d)",
			rowsAfter-rowsBefore, rowsBefore, rowsAfter)
	}
	_ = errors.Is
}
