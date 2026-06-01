package deb_test

// Phase 8 Plan 02 / M2.4 — byte-level progress emission.
//
// APT pre-computes totalBytes from summed Packages Size: fields, then
// wraps per-package downloads with jobs.CountingReader so progress_bytes
// advances during the body read. Verifies the sync_jobs row carries
// non-zero progress after a successful sync against a fake upstream.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blakesmith/ar"

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/deb"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// makeMiniDeb builds a minimal valid .deb archive (ar format with
// debian-binary + control.tar + data.tar.gz) containing only the control
// paragraph we care about. The deb sync handler calls Parse() on the
// downloaded bytes which requires a structurally valid .deb.
func makeMiniDeb(t *testing.T, pkg, version string) []byte {
	t.Helper()
	// control.tar with ./control
	var ctlBuf bytes.Buffer
	tw := tar.NewWriter(&ctlBuf)
	ctl := fmt.Sprintf(`Package: %s
Version: %s
Architecture: amd64
Maintainer: Test <t@x>
Description: test package
`, pkg, version)
	if err := tw.WriteHeader(&tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(ctl))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(ctl))
	_ = tw.Close()

	var dataBuf bytes.Buffer
	gz := gzip.NewWriter(&dataBuf)
	dw := tar.NewWriter(gz)
	_ = dw.Close()
	_ = gz.Close()

	var out bytes.Buffer
	aw := ar.NewWriter(&out)
	if err := aw.WriteGlobalHeader(); err != nil {
		t.Fatal(err)
	}
	members := []struct {
		name string
		body []byte
	}{
		{"debian-binary", []byte("2.0\n")},
		{"control.tar", ctlBuf.Bytes()},
		{"data.tar.gz", dataBuf.Bytes()},
	}
	for _, m := range members {
		hdr := &ar.Header{Name: m.name, Size: int64(len(m.body)), Mode: 0o644}
		if err := aw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := aw.Write(m.body); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func debShaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func makePackagesGZWithBody(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(body))
	_ = gz.Close()
	return buf.Bytes()
}

func newDEBProgressFixture(t *testing.T) (*deb.SyncHandler, *metadata.DB, int64, string) {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	debPkgs := metadata.NewDEBPackagesRepo(db)
	aptSuites := metadata.NewAptSuitesRepo(db)
	scans := metadata.NewScansRepo(db)

	// Two real .deb blobs with known SHA256.
	deb1 := makeMiniDeb(t, "curl", "7.88.1-10")
	deb2 := makeMiniDeb(t, "bash", "5.2.15-2")
	d1 := debShaHex(deb1)
	d2 := debShaHex(deb2)
	pkgsBody := fmt.Sprintf(`Package: curl
Version: 7.88.1-10
Architecture: amd64
Filename: pool/main/c/curl/curl_7.88.1-10_amd64.deb
Size: %d
SHA256: %s
Description: command line tool

Package: bash
Version: 5.2.15-2
Architecture: amd64
Filename: pool/main/b/bash/bash_5.2.15-2_amd64.deb
Size: %d
SHA256: %s
Description: GNU Bourne Again SHell
`, len(deb1), d1, len(deb2), d2)
	pkgsGZ := makePackagesGZWithBody(t, pkgsBody)

	releaseBody := `Suite: stable
Codename: stable
Components: main
Architectures: amd64
`
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
	mux.HandleFunc("/pool/main/c/curl/curl_7.88.1-10_amd64.deb", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(deb1)
	})
	mux.HandleFunc("/pool/main/b/bash/bash_5.2.15-2_amd64.deb", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(deb2)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pid, err := projectsRepo.Create(ctx, "pp", "phase8 plan 02 deb")
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

	h := deb.NewSyncHandler(deb.SyncDeps{
		DB:          db,
		Path:        pathStore,
		DEBPackages: debPkgs,
		AptSuites:   aptSuites,
		Repos:       reposRepo,
		Projects:    projectsRepo,
		Scans:       scans,
		HTTPClient:  srv.Client(),
		RepoRoot:    repoRoot,
		Cfg:         config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:    metadata.NewSyncJobsRepo(db),
	})
	return h, db, rid, srv.URL
}

func seedDEBSyncJob(t *testing.T, db *metadata.DB, repoID int64) int64 {
	t.Helper()
	res, err := db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('apt_sync', ?, 'running', '{}', '{}')`,
		repoID,
	)
	if err != nil {
		t.Fatalf("seed sync_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestDEBSync_EmitsByteProgress(t *testing.T) {
	h, db, repoID, upURL := newDEBProgressFixture(t)
	jobID := seedDEBSyncJob(t, db, repoID)

	payload := map[string]any{
		"upstream_url": upURL,
		"suite":        "stable",
		"filter": map[string]any{
			"components": []string{"main"},
			"arches":     []string{"amd64"},
		},
	}
	pb, _ := json.Marshal(payload)
	if err := h.Handle(context.Background(), string(pb), 0, repoID, jobID); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var (
		progressBytes, totalBytes int64
		currentStep               string
	)
	if err := db.Reader.QueryRowContext(context.Background(),
		`SELECT progress_bytes, total_bytes, current_step FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&progressBytes, &totalBytes, &currentStep); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if totalBytes <= 0 {
		t.Errorf("total_bytes=%d; want >0 (sum of Packages Size:)", totalBytes)
	}
	if progressBytes <= 0 {
		t.Errorf("progress_bytes=%d; want >0", progressBytes)
	}
	// current_step is either "done" (flushed) or one of the "pulling X_Y" emits.
	if currentStep != "done" && !strings.HasPrefix(currentStep, "pulling ") {
		t.Errorf("current_step=%q; want 'done' or 'pulling <pkg>_<version>'", currentStep)
	}
}
