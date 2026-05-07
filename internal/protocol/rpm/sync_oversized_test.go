package rpm

// Plan 05-04 STREAMIO-08 (closes audit findings #4 + #5 with end-to-end
// integration coverage): when an upstream serves cap+1 bytes for either an
// artifact body OR a metadata index, the full mirror sync flow must fail
// explicitly via streamio.Err{Artifact|Metadata}TooLarge AND commit zero
// new rows to rpm_packages. This is the on-merge forcing function for any
// future regression that re-introduces the silent-truncation idiom (a
// bare io.LimitReader without a sentinel) anywhere in the rpm sync path.
//
// Plan 05-03 already wires sync_oversize_test.go at the helper layer
// (downloadAndHashWithProgress + fetchAll); this file is the higher-level
// integration test exercising the full SyncHandler.Handle pipeline so the
// row-count + sentinel-propagation invariants land together.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
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

// oversizedZeroReader streams zero bytes (does not allocate). The synthetic
// upstream uses this to emit cap+1 bytes for the oversized-artifact /
// oversized-metadata responses without holding the cap+1 buffer in test
// process memory.
type oversizedZeroReader struct{ remaining int64 }

func (z *oversizedZeroReader) Read(p []byte) (int, error) {
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

// rpmOversizedFixture stands up a fresh DB + repo + SyncHandler wired to a
// caller-supplied upstream URL. Returns the handler, the repo ID, and a
// helper to count rpm_packages rows for the repo.
type rpmOversizedFixture struct {
	t      *testing.T
	h      *SyncHandler
	db     *metadata.DB
	repoID int64
}

func (f *rpmOversizedFixture) countRPMPackages() int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM rpm_packages WHERE repo_id=?`, f.repoID).Scan(&n); err != nil {
		f.t.Fatalf("count rpm_packages: %v", err)
	}
	return n
}

func (f *rpmOversizedFixture) seedSyncJob() int64 {
	f.t.Helper()
	res, err := f.db.Writer.ExecContext(context.Background(),
		`INSERT INTO sync_jobs(kind, repo_id, status, payload_json, log) VALUES ('rpm_sync', ?, 'running', '{}', '{}')`,
		f.repoID,
	)
	if err != nil {
		f.t.Fatalf("seed sync_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newRPMOversizedFixture(t *testing.T, upstreamClient *http.Client) *rpmOversizedFixture {
	t.Helper()
	db := sqlitetest.New(t)
	ctx := context.Background()

	reposRepo := metadata.NewReposRepo(db)
	projectsRepo := metadata.NewProjectsRepo(db)
	rpmPackages := metadata.NewRPMPackagesRepo(db)
	scans := metadata.NewScansRepo(db)

	pid, err := projectsRepo.Create(ctx, "pp", "STREAMIO-08 oversized rpm")
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

	h := NewSyncHandler(SyncDeps{
		DB:          db,
		Path:        pathStore,
		RPMPackages: rpmPackages,
		Repos:       reposRepo,
		Projects:    projectsRepo,
		Scans:       scans,
		HTTPClient:  upstreamClient,
		RepoRoot:    repoRoot,
		Cfg:         config.SyncConfig{MaxParallelDownloadsPerJob: 1},
		SyncJobs:    metadata.NewSyncJobsRepo(db),
	})
	return &rpmOversizedFixture{t: t, h: h, db: db, repoID: rid}
}

// oversizedMinimalRepomd builds a tiny valid repomd.xml referencing primary.xml.gz
// at the supplied size.
func oversizedMinimalRepomd(t *testing.T, primarySize int64) []byte {
	t.Helper()
	root := RepomdRoot{
		Xmlns:    "http://linux.duke.edu/metadata/repo",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Revision: 1,
		Data: []RepomdData{{
			Type:     "primary",
			Checksum: RepomdCksum{Type: "sha256", Value: "deadbeef"},
			Location: RepomdLoc{Href: "repodata/primary.xml.gz"},
			Size:     primarySize,
		}},
	}
	body, err := xml.Marshal(&root)
	if err != nil {
		t.Fatalf("marshal repomd: %v", err)
	}
	return body
}

// oversizedMinimalPrimaryGZ builds a tiny valid primary.xml.gz listing one package
// at the supplied URL with sha256 + size matching artifactBody.
func oversizedMinimalPrimaryGZ(t *testing.T, artifactBody []byte, href string) []byte {
	t.Helper()
	sum := sha256.Sum256(artifactBody)
	primary := PrimaryRoot{
		Xmlns:    "http://linux.duke.edu/metadata/common",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Packages: 1,
		Pkgs: []PrimaryPkg{{
			Type: "rpm", Name: "sample", Arch: "x86_64",
			Version:  PrimaryVer{Epoch: "0", Ver: "1.0", Rel: "1"},
			Checksum: PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: hex.EncodeToString(sum[:])},
			Summary:  "sample",
			Time:     PrimaryTime{File: 1700000000, Build: 1700000000},
			Size:     PrimarySize{Package: int64(len(artifactBody))},
			Location: PrimaryLoc{Href: href},
		}},
	}
	primaryXML, err := xml.Marshal(&primary)
	if err != nil {
		t.Fatalf("marshal primary: %v", err)
	}
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	_, _ = gz.Write(primaryXML)
	_ = gz.Close()
	return gzBuf.Bytes()
}

// oversizedGzExpandsTo returns a valid gzip stream that decompresses to wantBytes.
// Compressed body is tiny (~50 bytes for many KB of repeated 'a').
func oversizedGzExpandsTo(t *testing.T, wantBytes int64) []byte {
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

// TestRPMSync_OversizedArtifactRejected proves STREAMIO-08 (audit #4): when
// upstream returns cap+1 bytes for an .rpm body, sync fails via
// streamio.ErrArtifactTooLarge propagated through %w wrapping AND zero
// new rpm_packages rows are committed.
func TestRPMSync_OversizedArtifactRejected(t *testing.T) {
	const testCap = int64(4096)
	prevCap := maxArtifactBytes
	maxArtifactBytes = testCap
	t.Cleanup(func() { maxArtifactBytes = prevCap })

	// The artifact body the upstream "advertises" via primary.xml.
	// We claim cap bytes; we actually serve cap+1. The cap+1 byte is
	// what trips ReadAllLimited's max+1 check.
	advertisedBody := bytes.Repeat([]byte("x"), int(testCap))
	primaryGZ := oversizedMinimalPrimaryGZ(t, advertisedBody, "Packages/sample.rpm")
	repomdXML := oversizedMinimalRepomd(t, int64(len(primaryGZ)))

	mux := http.NewServeMux()
	mux.HandleFunc("/repodata/repomd.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(repomdXML)
	})
	mux.HandleFunc("/repodata/primary.xml.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(primaryGZ)
	})
	mux.HandleFunc("/Packages/sample.rpm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		// Stream cap+1 zero bytes. Test process never holds the buffer.
		_, _ = io.CopyN(w, &oversizedZeroReader{remaining: testCap + 1}, testCap+1)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fix := newRPMOversizedFixture(t, srv.Client())

	rowsBefore := fix.countRPMPackages()

	jobID := fix.seedSyncJob()
	pb, _ := json.Marshal(map[string]any{"upstream_url": srv.URL})
	err := fix.h.Handle(context.Background(), string(pb), 0, fix.repoID, jobID)

	rowsAfter := fix.countRPMPackages()
	if err == nil {
		t.Fatalf("expected sync error for cap+1 artifact, got nil")
	}

	// Sentinel-propagation note: SyncHandler.Handle wraps the failure path
	// through internal/httpx.SanitizeUpstreamErr (T-03-06-01) which
	// deliberately drops the wrap chain via errors.New(scrubbed) to prevent
	// credential leakage from upstream-error byte strings. errors.Is therefore
	// CANNOT walk back to streamio.ErrArtifactTooLarge through Handle's
	// return value. The sentinel-propagation contract for the rpm sync helper
	// layer is covered by sync_oversize_test.go (Plan 05-03 internal test:
	// downloadAndHashWithProgress → errors.Is(err, streamio.ErrArtifactTooLarge)
	// at the unsanitized boundary). Here we assert the integration invariants:
	//   1. the sanitized message preserves the streamio sentinel TEXT, so
	//      operators reading sync_jobs.last_error see the bug class.
	//   2. zero rows are committed (the row-count delta-zero contract).
	wantToken := streamio.ErrArtifactTooLarge.Error()
	if !strings.Contains(err.Error(), wantToken) {
		t.Fatalf("expected sanitized error to contain %q (streamio.ErrArtifactTooLarge text); got: %v", wantToken, err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("expected zero new rpm_packages rows on cap-exceed failure, got %d new rows (before=%d after=%d)",
			rowsAfter-rowsBefore, rowsBefore, rowsAfter)
	}

	// errors.Is reference kept here so the package import stays used and
	// future refactors that move sanitisation OFF the integration boundary
	// can flip this from a substring check to errors.Is(err, sentinel) by
	// changing one line. The substring check above is the binding contract
	// today (see SanitizeUpstreamErr rationale comment above).
	_ = errors.Is
}

// TestRPMSync_OversizedMetadataRejected proves STREAMIO-08 (audit #5): when
// upstream returns a primary.xml.gz that decompresses to cap+1 bytes, sync
// fails via streamio.ErrMetadataTooLarge AND zero rpm_packages rows commit.
func TestRPMSync_OversizedMetadataRejected(t *testing.T) {
	const testCap = int64(4096)
	prevCap := maxPrimaryDecompressedBytes
	maxPrimaryDecompressedBytes = testCap
	t.Cleanup(func() { maxPrimaryDecompressedBytes = prevCap })

	// gz that decompresses to cap+1 bytes — trips the gz-layer cap.
	bigGZ := oversizedGzExpandsTo(t, testCap+1)
	repomdXML := oversizedMinimalRepomd(t, int64(len(bigGZ)))

	mux := http.NewServeMux()
	mux.HandleFunc("/repodata/repomd.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(repomdXML)
	})
	mux.HandleFunc("/repodata/primary.xml.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bigGZ)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fix := newRPMOversizedFixture(t, srv.Client())

	rowsBefore := fix.countRPMPackages()
	jobID := fix.seedSyncJob()
	pb, _ := json.Marshal(map[string]any{"upstream_url": srv.URL})
	err := fix.h.Handle(context.Background(), string(pb), 0, fix.repoID, jobID)

	rowsAfter := fix.countRPMPackages()
	if err == nil {
		t.Fatalf("expected sync error for cap+1 metadata, got nil")
	}
	// Sentinel propagation through Handle is sanitized — see the artifact
	// test above for the rationale. Match on streamio.ErrMetadataTooLarge
	// text (preserved through SanitizeUpstreamErr's regex scrub).
	wantToken := streamio.ErrMetadataTooLarge.Error()
	if !strings.Contains(err.Error(), wantToken) {
		t.Fatalf("expected sanitized error to contain %q (streamio.ErrMetadataTooLarge text); got: %v", wantToken, err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("expected zero new rpm_packages rows on metadata cap-exceed, got %d new rows (before=%d after=%d)",
			rowsAfter-rowsBefore, rowsBefore, rowsAfter)
	}

	_ = fmt.Sprint // keep "fmt" import used unconditionally
	_ = errors.Is  // keep "errors" import live for the package-pattern parity with the artifact test
}
