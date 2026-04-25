package pypi_test

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

// TestLegacyUploadStreamingFidelity is the regression guard for STREAMIO-04
// (legacy path / audit finding #3). Asserts that after a legacy multipart
// upload, the on-disk wheel's sha256 matches the digest stored in
// pypi_files.Digest — i.e. the body bytes flowed through the temp-file
// staging path to the canonical packages/ directory without copy-corruption.
//
// Existing TestLegacyUploadRoundTripWheel checks bytes.Equal(disk, body)
// but does not equate the disk hash with the DB digest column. A regression
// that re-introduces a buffer-the-whole-tmp pattern between staging and
// PathStore.Put could still pass that test if the buffer is correct;
// this test catches the structural property loss directly.
func TestLegacyUploadStreamingFidelity(t *testing.T) {
	f := newHandlerFixture(t)
	_, rid := f.seedRepo("proj1", "internal", false, false)

	wheel := makeWheelBytes(t, "Flask", "2.3.0")
	wantSum := sha256.Sum256(wheel)
	wantHex := hex.EncodeToString(wantSum[:])

	resp := twineUpload(t, f.srv.URL, "proj1", "internal",
		"Flask-2.3.0-py3-none-any.whl", wheel, "bdist_wheel", f.basicAuth())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	abs := filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "packages", "Flask-2.3.0-py3-none-any.whl")
	diskBytes, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read on-disk: %v", err)
	}
	if int64(len(diskBytes)) != int64(len(wheel)) {
		t.Fatalf("disk size=%d want %d", len(diskBytes), len(wheel))
	}
	gotSum := sha256.Sum256(diskBytes)
	gotHex := hex.EncodeToString(gotSum[:])
	if gotHex != wantHex {
		t.Fatalf("on-disk sha256=%s want %s", gotHex, wantHex)
	}

	row, err := f.pypiRepo.FindByFilename(context.Background(), rid, "Flask-2.3.0-py3-none-any.whl")
	if err != nil || row == nil {
		t.Fatalf("row missing: %v", err)
	}
	gotDB := strings.TrimPrefix(row.Digest, "sha256:")
	if gotDB != wantHex {
		t.Fatalf("db digest=%q want sha256:%s", row.Digest, wantHex)
	}
}

// TestPEP694CommitStreamingFidelity is the regression guard for STREAMIO-04
// (PEP 694 commit path). Same property as the legacy test: after the
// staged file is promoted into PathStore via the commit endpoint, the
// on-disk file's sha256 must equal the DB pypi_files.Digest. Catches any
// re-introduction of os.ReadFile + bytes.NewReader between session-staging
// and pathStore.Put.
func TestPEP694CommitStreamingFidelity(t *testing.T) {
	f := newHandlerFixture(t)
	_, rid := f.seedRepo("proj1", "internal", false, false)

	wheel := makeWheelBytes(t, "Flask", "2.3.0")
	wantSum := sha256.Sum256(wheel)
	wantHex := hex.EncodeToString(wantSum[:])

	sid, status := pep694CreateSession(t, f.srv.URL, "proj1", "internal", "Flask", "2.3.0", f.basicAuth())
	if status != http.StatusCreated || sid == "" {
		t.Fatalf("create session status=%d sid=%q", status, sid)
	}
	if s := pep694Upload(t, f.srv.URL, "proj1", "internal", sid,
		"Flask-2.3.0-py3-none-any.whl", wheel, f.basicAuth()); s != http.StatusAccepted {
		t.Fatalf("upload status=%d", s)
	}
	if s := pep694Commit(t, f.srv.URL, "proj1", "internal", sid, f.basicAuth()); s != http.StatusOK {
		t.Fatalf("commit status=%d", s)
	}

	abs := filepath.Join(f.repoRoot, "proj1", "pypi", "internal", "packages", "Flask-2.3.0-py3-none-any.whl")
	diskBytes, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read on-disk: %v", err)
	}
	if int64(len(diskBytes)) != int64(len(wheel)) {
		t.Fatalf("disk size=%d want %d", len(diskBytes), len(wheel))
	}
	gotSum := sha256.Sum256(diskBytes)
	gotHex := hex.EncodeToString(gotSum[:])
	if gotHex != wantHex {
		t.Fatalf("on-disk sha256=%s want %s", gotHex, wantHex)
	}

	row, err := f.pypiRepo.FindByFilename(context.Background(), rid, "Flask-2.3.0-py3-none-any.whl")
	if err != nil || row == nil {
		t.Fatalf("row missing: %v", err)
	}
	gotDB := strings.TrimPrefix(row.Digest, "sha256:")
	if gotDB != wantHex {
		t.Fatalf("db digest=%q want sha256:%s", row.Digest, wantHex)
	}
}
