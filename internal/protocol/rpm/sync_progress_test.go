package rpm_test

// Byte-level progress emission for RPM sync.
//
// Pre-computes totalBytes from summed primary.xml <size package="..."/>
// values, wraps per-package downloads with jobs.CountingReader.
// Test serves the real testdata/sample.rpm via a fake upstream and
// verifies the sync_jobs row carries advanced progress_bytes + a "done"
// or "pulling <stem>.rpm" step after sync completes.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/rpm"
	"github.com/vladoportos/omnirepo/internal/storage"
)

func rpmShaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newRPMProgressFixture(t *testing.T) (*rpm.SyncHandler, *metadata.DB, int64, string) {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	rpmPackages := metadata.NewRPMPackagesRepo(db)
	scans := metadata.NewScansRepo(db)

	// Read the real sample.rpm used by parse_test.go as the sole package.
	sampleRPM, err := os.ReadFile("testdata/sample.rpm")
	if err != nil {
		t.Fatalf("read sample.rpm: %v", err)
	}
	sampleDigest := rpmShaHex(sampleRPM)

	primary := rpm.PrimaryRoot{
		Xmlns:    "http://linux.duke.edu/metadata/common",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Packages: 1,
		Pkgs: []rpm.PrimaryPkg{{
			Type: "rpm",
			Name: "sample", Arch: "x86_64",
			Version:  rpm.PrimaryVer{Epoch: "0", Ver: "1.0", Rel: "1"},
			Checksum: rpm.PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: sampleDigest},
			Summary:  "sample test rpm",
			Time:     rpm.PrimaryTime{File: 1700000000, Build: 1700000000},
			Size:     rpm.PrimarySize{Package: int64(len(sampleRPM))},
			Location: rpm.PrimaryLoc{Href: "Packages/sample.rpm"},
		}},
	}
	primaryXML, err := xml.Marshal(&primary)
	if err != nil {
		t.Fatalf("marshal primary: %v", err)
	}
	var gzBuf bytes.Buffer
	gzw := gzip.NewWriter(&gzBuf)
	_, _ = gzw.Write(primaryXML)
	_ = gzw.Close()
	primaryGZ := gzBuf.Bytes()

	repomd := rpm.RepomdRoot{
		Xmlns:    "http://linux.duke.edu/metadata/repo",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Revision: 1,
		Data: []rpm.RepomdData{{
			Type:     "primary",
			Checksum: rpm.RepomdCksum{Type: "sha256", Value: "abc"},
			Location: rpm.RepomdLoc{Href: "repodata/primary.xml.gz"},
			Size:     int64(len(primaryGZ)),
		}},
	}
	repomdXML, err := xml.Marshal(&repomd)
	if err != nil {
		t.Fatalf("marshal repomd: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/repodata/repomd.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(repomdXML)
	})
	mux.HandleFunc("/repodata/primary.xml.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(primaryGZ)
	})
	mux.HandleFunc("/Packages/sample.rpm", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sampleRPM)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pid, err := projectsRepo.Create(ctx, "pp", "phase8 plan 02 rpm")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	rid, err := reposRepo.Create(ctx, pid, "rpm", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	dataRoot := t.TempDir()
	repoRoot := filepath.Join(dataRoot, "repos")
	pathStore := storage.NewPathStore(repoRoot)

	h := rpm.NewSyncHandler(rpm.SyncDeps{
		DB:          db,
		Path:        pathStore,
		RPMPackages: rpmPackages,
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

func seedRPMSyncJob(t *testing.T, db *metadata.DB, repoID int64) int64 {
	t.Helper()
	res, err := db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('rpm_sync', ?, 'running', '{}', '{}')`,
		repoID,
	)
	if err != nil {
		t.Fatalf("seed sync_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestRPMSync_EmitsByteProgress(t *testing.T) {
	h, db, repoID, upURL := newRPMProgressFixture(t)
	jobID := seedRPMSyncJob(t, db, repoID)

	payload := map[string]any{"upstream_url": upURL}
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
		t.Errorf("total_bytes=%d; want >0 (sum of primary.xml size)", totalBytes)
	}
	if progressBytes <= 0 {
		t.Errorf("progress_bytes=%d; want >0", progressBytes)
	}
	if currentStep != "done" && !strings.HasPrefix(currentStep, "pulling ") {
		t.Errorf("current_step=%q; want 'done' or 'pulling <stem>.rpm'", currentStep)
	}

	// Sanity: unused import guard.
	_ = fmt.Sprint
}
