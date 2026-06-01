package deb

// End-to-end integration coverage for DEB: when an upstream serves cap+1
// bytes for either an artifact body OR a metadata index, the full mirror
// sync flow must fail explicitly with an error whose Error() text contains
// the streamio.Err{Artifact|Metadata}TooLarge sentinel string AND commit
// zero new rows to deb_packages.
//
// Sentinel-propagation note: SyncHandler.Handle wraps the failure path
// through internal/httpx.SanitizeUpstreamErr which deliberately
// drops the wrap chain to prevent credential leakage. errors.Is therefore
// CANNOT walk back to streamio.ErrArtifactTooLarge through Handle's return
// value — but the sanitised text preserves the streamio sentinel string.
// The errors.Is contract at the helper layer is covered by
// sync_oversize_test.go.

import (
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

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/storage"
	"github.com/vladoportos/omnirepo/internal/streamio"
)

type debOversizedZeroReader struct{ remaining int64 }

func (z *debOversizedZeroReader) Read(p []byte) (int, error) {
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

type debOversizedFixture struct {
	t      *testing.T
	h      *SyncHandler
	db     *metadata.DB
	repoID int64
}

func (f *debOversizedFixture) countDEBPackages() int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM deb_packages WHERE repo_id=?`, f.repoID).Scan(&n); err != nil {
		f.t.Fatalf("count deb_packages: %v", err)
	}
	return n
}

func (f *debOversizedFixture) seedSyncJob() int64 {
	f.t.Helper()
	res, err := f.db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('apt_sync', ?, 'running', '{}', '{}')`,
		f.repoID,
	)
	if err != nil {
		f.t.Fatalf("seed sync_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newDEBOversizedFixture(t *testing.T, upstreamClient *http.Client) *debOversizedFixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	debPkgs := metadata.NewDEBPackagesRepo(db)
	aptSuites := metadata.NewAptSuitesRepo(db)
	scans := metadata.NewScansRepo(db)

	pid, err := projectsRepo.Create(ctx, "pp", "oversized deb")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "deb", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	pathStore := storage.NewPathStore(repoRoot)

	h := NewSyncHandler(SyncDeps{
		DB:          db,
		Path:        pathStore,
		DEBPackages: debPkgs,
		AptSuites:   aptSuites,
		Repos:       reposRepo,
		Projects:    projectsRepo,
		Scans:       scans,
		HTTPClient:  upstreamClient,
		RepoRoot:    repoRoot,
		Cfg:         config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:    metadata.NewSyncJobsRepo(db),
	})
	return &debOversizedFixture{t: t, h: h, db: db, repoID: rid}
}

// debDecompressedPackagesGZ returns a gzip stream that decompresses to a
// Packages-style payload of approximately wantBytes (decompressed). Used
// to trip maxPackagesDecompressedBytes when set to a tiny test cap.
func debDecompressedPackagesGZ(t *testing.T, wantBytes int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	chunk := bytes.Repeat([]byte("a"), 4096)
	written := int64(0)
	for written < wantBytes {
		n := int64(len(chunk))
		if written+n > wantBytes {
			n = wantBytes - written
		}
		if _, err := gz.Write(chunk[:n]); err != nil {
			t.Fatalf("gz write: %v", err)
		}
		written += n
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

// debMakePackagesGZ builds a real Packages.gz body referencing one .deb at
// poolPath with the given size + sha256 — used by the oversized-artifact
// test where the metadata is real and only the artifact body is cap+1.
func debMakePackagesGZ(t *testing.T, pkg, version, poolPath string, size int64, sha256Hex string) []byte {
	t.Helper()
	body := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: amd64\nFilename: %s\nSize: %d\nSHA256: %s\nDescription: oversized test\n",
		pkg, version, poolPath, size, sha256Hex)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(body))
	_ = gz.Close()
	return buf.Bytes()
}

// TestDEBSync_OversizedArtifactRejected proves that for
// DEB, when upstream returns cap+1 bytes for a .deb body, sync fails with
// an error whose text contains streamio.ErrArtifactTooLarge AND zero
// deb_packages rows commit.
func TestDEBSync_OversizedArtifactRejected(t *testing.T) {
	const testCap = int64(4096)
	prevCap := maxArtifactBytes
	maxArtifactBytes = testCap
	t.Cleanup(func() { maxArtifactBytes = prevCap })

	// Advertised body in Packages.gz: claim cap bytes; serve cap+1.
	advertisedBody := bytes.Repeat([]byte("x"), int(testCap))
	sum := sha256.Sum256(advertisedBody)
	pkgsGZ := debMakePackagesGZ(t, "mypkg", "1.0-1",
		"pool/main/m/mypkg/mypkg_1.0-1_amd64.deb",
		int64(len(advertisedBody)), hex.EncodeToString(sum[:]))

	releaseBody := "Suite: stable\nCodename: stable\nComponents: main\nArchitectures: amd64\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/dists/stable/InRelease", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // force Release fallback
	})
	mux.HandleFunc("/dists/stable/Release", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(releaseBody))
	})
	mux.HandleFunc("/dists/stable/main/binary-amd64/Packages.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pkgsGZ)
	})
	mux.HandleFunc("/pool/main/m/mypkg/mypkg_1.0-1_amd64.deb", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.CopyN(w, &debOversizedZeroReader{remaining: testCap + 1}, testCap+1)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fix := newDEBOversizedFixture(t, srv.Client())

	rowsBefore := fix.countDEBPackages()

	jobID := fix.seedSyncJob()
	pb, _ := json.Marshal(map[string]any{
		"upstream_url": srv.URL,
		"suite":        "stable",
		"filter": map[string]any{
			"components": []string{"main"},
			"arches":     []string{"amd64"},
		},
	})
	err := fix.h.Handle(context.Background(), string(pb), 0, fix.repoID, jobID)

	rowsAfter := fix.countDEBPackages()
	if err == nil {
		t.Fatalf("expected sync error for cap+1 artifact, got nil")
	}
	wantToken := streamio.ErrArtifactTooLarge.Error()
	if !strings.Contains(err.Error(), wantToken) {
		t.Fatalf("expected sanitized error to contain %q (streamio.ErrArtifactTooLarge text); got: %v", wantToken, err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("expected zero new deb_packages rows on cap-exceed failure, got %d new rows (before=%d after=%d)",
			rowsAfter-rowsBefore, rowsBefore, rowsAfter)
	}
	_ = errors.Is // keeps "errors" import live for parity with sibling tests
}

// TestDEBSync_OversizedMetadataRejected proves that for
// DEB, when upstream returns a Packages.gz that decompresses to cap+1
// bytes, sync fails via streamio.ErrMetadataTooLarge AND zero deb_packages
// rows commit.
func TestDEBSync_OversizedMetadataRejected(t *testing.T) {
	const testCap = int64(4096)
	prevCap := maxPackagesDecompressedBytes
	maxPackagesDecompressedBytes = testCap
	t.Cleanup(func() { maxPackagesDecompressedBytes = prevCap })

	// Compressed body that decompresses to cap+1 bytes — trips the gz cap.
	bigGZ := debDecompressedPackagesGZ(t, testCap+1)

	releaseBody := "Suite: stable\nCodename: stable\nComponents: main\nArchitectures: amd64\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/dists/stable/InRelease", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/dists/stable/Release", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(releaseBody))
	})
	mux.HandleFunc("/dists/stable/main/binary-amd64/Packages.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bigGZ)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fix := newDEBOversizedFixture(t, srv.Client())

	rowsBefore := fix.countDEBPackages()
	jobID := fix.seedSyncJob()
	pb, _ := json.Marshal(map[string]any{
		"upstream_url": srv.URL,
		"suite":        "stable",
		"filter": map[string]any{
			"components": []string{"main"},
			"arches":     []string{"amd64"},
		},
	})
	err := fix.h.Handle(context.Background(), string(pb), 0, fix.repoID, jobID)

	rowsAfter := fix.countDEBPackages()
	if err == nil {
		t.Fatalf("expected sync error for cap+1 metadata, got nil")
	}
	wantToken := streamio.ErrMetadataTooLarge.Error()
	if !strings.Contains(err.Error(), wantToken) {
		t.Fatalf("expected sanitized error to contain %q (streamio.ErrMetadataTooLarge text); got: %v", wantToken, err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("expected zero new deb_packages rows on metadata cap-exceed, got %d new rows (before=%d after=%d)",
			rowsAfter-rowsBefore, rowsBefore, rowsAfter)
	}
	_ = errors.Is
}
