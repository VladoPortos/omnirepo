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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// --------------------------------------------------------------------------
// MirrorGuard rejects PyPI legacy twine uploads on mirror-flagged repos,
// returns 201 on plain repos.
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

// TestUpload_DuplicateFilenameReturns409.
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

	// Prior fix Put'd the new blob before the tx check fired + then
	// Delete'd it on rollback — wiping the winner's on-disk bytes. Assert
	// the blob for the original upload is still present and its sha256
	// matches the DB row's digest.
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

// TestUpload_ConcurrentFirstUploads_ExactlyOneWins. Ten goroutines race to
// upload the same filename. Exactly one must succeed (2xx + row insert); the
// other nine must 409; the on-disk blob's sha256 must match the DB row's
// digest.
//
// Before the per-(repo, filename) mutex, two concurrent first-uploads
// could both pass FindByFilename + both PathStore.Put (last-rename-wins
// on disk); the first to commit won the DB row but the on-disk bytes
// belonged to whoever renamed last — so GET /packages/<filename> could
// return bytes whose sha256 didn't match the published digest.
func TestUpload_ConcurrentFirstUploads_ExactlyOneWins(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "concurrent", true, false)

	const N = 10
	const filename = "racepkg-1.0-py3-none-any.whl"
	wheel := makeWheelBytes(t, "racepkg", "1.0")
	expectSum := sha256.Sum256(wheel)

	var ok, conflict, other int64
	var wg sync.WaitGroup
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			resp := twineUpload(t, f.srv.URL, "proj1", "concurrent", filename, wheel, "bdist_wheel", f.basicAuth())
			defer func() { _ = resp.Body.Close() }()
			switch resp.StatusCode {
			case http.StatusOK, http.StatusCreated:
				atomic.AddInt64(&ok, 1)
			case http.StatusConflict:
				atomic.AddInt64(&conflict, 1)
			default:
				atomic.AddInt64(&other, 1)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&ok); got != 1 {
		t.Fatalf("wanted exactly 1 winner, got ok=%d conflict=%d other=%d", got, conflict, other)
	}
	if got := atomic.LoadInt64(&conflict); got != N-1 {
		t.Fatalf("wanted %d 409s, got ok=%d conflict=%d other=%d", N-1, ok, got, other)
	}
	if got := atomic.LoadInt64(&other); got != 0 {
		t.Fatalf("unexpected non-409 non-2xx response count=%d", got)
	}

	row, err := f.pypiRepo.FindByFilename(context.Background(), repoID, filename)
	if err != nil {
		t.Fatalf("find row: %v", err)
	}
	if row == nil {
		t.Fatalf("no row after concurrent uploads")
	}
	if want := "sha256:" + hex.EncodeToString(expectSum[:]); row.Digest != want {
		t.Fatalf("row digest %s != expected %s", row.Digest, want)
	}

	blobPath := filepath.Join(f.repoRoot, "proj1", "pypi", "concurrent", "packages", filename)
	blob, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("blob missing: %v", err)
	}
	blobSum := sha256.Sum256(blob)
	if hex.EncodeToString(blobSum[:]) != hex.EncodeToString(expectSum[:]) {
		t.Fatalf("on-disk blob sha256 %x != expected %x (loser's bytes landed)", blobSum, expectSum)
	}
}

// TestUpload_AcceptsNonPEP440Filename.
//
// Locks the upload-path non-change: the legacy twine upload handler
// (upload_legacy.go) does NOT call pep440.Validate on the filename, so
// internal publishers can still ship artifacts whose dashed filename shape
// isn't PEP 440 (common for nightly / snapshot / pre-tag internal releases)
// as long as the in-archive PKG-INFO carries a valid Name + Version. The
// Validate gate inside parseSdistFilename was added separately, but
// ParseSdistAs (parse.go:134-183) intentionally falls back to PKG-INFO
// metadata when the filename parse fails — which is the code path this test
// exercises.
//
// If a future refactor tightens the upload path to require a PEP-440-
// parseable filename, this test will fail — the guard is intentional.
func TestUpload_AcceptsNonPEP440Filename(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "permissive-upload", true, false)

	// makeSdistBytes builds an in-memory tar.gz with PKG-INFO carrying
	// Name + Version. Filename "foo-nightly.tar.gz" fails
	// parseSdistFilename (candidate "nightly" fails pep440.Validate) but
	// ParseSdistAs reads PKG-INFO and uses "0.0.dev0" as the Version.
	sdist := makeSdistBytes(t, "foo", "0.0.dev0")

	resp := twineUpload(
		t, f.srv.URL,
		"proj1", "permissive-upload",
		"foo-nightly.tar.gz",
		sdist,
		"sdist",
		f.basicAuth(),
	)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status = %d, want 200/201; body=%s", resp.StatusCode, body)
	}

	row, err := f.pypiRepo.FindByFilename(context.Background(), repoID, "foo-nightly.tar.gz")
	if err != nil {
		t.Fatalf("find row: %v", err)
	}
	if row == nil {
		t.Fatalf("no pypi_files row inserted for verbatim filename \"foo-nightly.tar.gz\"")
	}
	if row.Filename != "foo-nightly.tar.gz" {
		t.Errorf("row.Filename = %q, want foo-nightly.tar.gz (verbatim)", row.Filename)
	}
	if row.Version != "0.0.dev0" {
		t.Errorf("row.Version = %q, want 0.0.dev0 (from PKG-INFO, not from filename slot)", row.Version)
	}
	if row.ProjectNormalized != "foo" {
		t.Errorf("row.ProjectNormalized = %q, want foo", row.ProjectNormalized)
	}
}

// TestDeletePackage_MirrorRepoReturns403.
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
