package pypi_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// --------------------------------------------------------------------------
// Phase 8 Plan 01 (MIRROR-03) — MirrorGuard rejects PyPI legacy twine uploads
// on mirror-flagged repos, returns 201 on plain repos.
// --------------------------------------------------------------------------

func TestUpload_MirrorRepoReturns403(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "mirrored", true, false)
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.repos.SetMirrorConfigInTx(context.Background(), tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://upstream.example/simple/",
			FilterJSON:  `{}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}

	wheel := makeWheelBytes(t, "mypkg", "1.0")
	resp := twineUpload(t, f.srv.URL, "proj1", "mirrored", "mypkg-1.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "repo_is_mirror") {
		t.Fatalf("body missing repo_is_mirror: %s", body)
	}
}

func TestUpload_NonMirrorRepoStillWorks(t *testing.T) {
	f := newHandlerFixture(t)
	_, _ = f.seedRepo("proj1", "plain", true, false)
	wheel := makeWheelBytes(t, "mypkg", "1.0")
	resp := twineUpload(t, f.srv.URL, "proj1", "plain", "mypkg-1.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201/200 (non-mirror); body=%s", resp.StatusCode, body)
	}
}

// TestUpload_DuplicateFilenameReturns409 — wt3 §7.7 / F-07.1.
// Twine re-uploading an existing filename must 409 with
// code=pypi.file_exists; the stored row digest/size must NOT change
// (released artifacts are immutable per PyPI semantics). Sync-path
// callers enter via PyPIFilesRepo.Insert (upsert) and remain idempotent.
func TestUpload_DuplicateFilenameReturns409(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "plain-dup", true, false)

	wheel := makeWheelBytes(t, "mypkg", "1.0")
	resp := twineUpload(t, f.srv.URL, "proj1", "plain-dup", "mypkg-1.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 200/201", resp.StatusCode)
	}

	before, err := f.pypiRepo.FindByFilename(context.Background(), repoID, "mypkg-1.0-py3-none-any.whl")
	if err != nil {
		t.Fatalf("find before: %v", err)
	}
	if before == nil {
		t.Fatalf("row not inserted on first upload")
	}
	beforeDigest := before.Digest
	beforeSize := before.SizeBytes
	beforeUploadedAt := before.UploadedAt

	// Second upload of the same filename (same bytes, as twine would resend).
	resp2 := twineUpload(t, f.srv.URL, "proj1", "plain-dup", "mypkg-1.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("dup upload status = %d, want 409; body=%s", resp2.StatusCode, body)
	}
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "pypi.file_exists") {
		t.Fatalf("body missing pypi.file_exists: %s", body)
	}

	after, err := f.pypiRepo.FindByFilename(context.Background(), repoID, "mypkg-1.0-py3-none-any.whl")
	if err != nil {
		t.Fatalf("find after: %v", err)
	}
	if after == nil {
		t.Fatalf("row disappeared after rejected dup upload")
	}
	if after.Digest != beforeDigest {
		t.Fatalf("digest mutated on rejected dup: before=%s after=%s", beforeDigest, after.Digest)
	}
	if after.SizeBytes != beforeSize {
		t.Fatalf("size mutated on rejected dup: before=%d after=%d", beforeSize, after.SizeBytes)
	}
	if !after.UploadedAt.Equal(beforeUploadedAt) {
		t.Fatalf("uploaded_at mutated on rejected dup: before=%s after=%s", beforeUploadedAt, after.UploadedAt)
	}

	// F-07.1 Codex follow-up: prior fix Put'd the new blob before the
	// tx check fired + then Delete'd it on rollback — wiping the winner's
	// on-disk bytes. Assert the blob for the original upload is still
	// present and its sha256 matches the DB row's digest.
	blobPath := filepath.Join(f.repoRoot, "proj1", "pypi", "plain-dup", "packages", "mypkg-1.0-py3-none-any.whl")
	got, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("blob unlinked after 409 dup: %v", err)
	}
	sum := sha256.Sum256(got)
	if want := "sha256:" + hex.EncodeToString(sum[:]); want != after.Digest {
		t.Fatalf("blob sha256 %s != row digest %s (blob mutated on dup)", want, after.Digest)
	}
}

// TestDeletePackage_MirrorRepoReturns403 — plan 08-06 Codex rescue Q3a.
// DELETE /pypi/<repo>/packages/<filename> is a mutating operation and
// must be rejected on mirror-flagged repos by MirrorGuardFixed. Before
// this fix, the route sat outside the guard group.
func TestDeletePackage_MirrorRepoReturns403(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "mirrored-del", true, false)
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.repos.SetMirrorConfigInTx(context.Background(), tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://upstream.example/simple/",
			FilterJSON:  `{}`,
			CredID:      nil,
			ScanOnSync:  false,
		})
	}); err != nil {
		t.Fatalf("set mirror cfg: %v", err)
	}
	url := f.srv.URL + "/proj1/pypi/mirrored-del/packages/mypkg-1.0-py3-none-any.whl"
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	req.Header.Set("Authorization", f.basicAuth())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 403; body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "repo_is_mirror") {
		t.Fatalf("body missing repo_is_mirror: %s", body)
	}
}
