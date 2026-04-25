package pypi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// TestPyPIDelete_TxFailure_FileAndRowIntact is the ATOMICDEL-06 fault-injection
// proof for the PyPI protocol. Same shape as raw/rpm/deb/helm: arm a one-shot
// WriteTx failpoint, DELETE, assert .whl + DB row both intact, recovery DELETE
// cleans up.
//
// Storage path: <repoRoot>/<project>/pypi/<repo>/packages/<filename> (see
// internal/protocol/pypi/handler.go:packageStorageKey).
func TestPyPIDelete_TxFailure_FileAndRowIntact(t *testing.T) {
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "atomic", true, false)

	wheel := makeWheelBytes(t, "mypkg", "1.0")
	const filename = "mypkg-1.0-py3-none-any.whl"

	resp := twineUpload(t, f.srv.URL, "proj1", "atomic", filename, wheel, "bdist_wheel", f.basicAuth())
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("baseline upload: %d", resp.StatusCode)
	}

	diskPath := filepath.Join(f.repoRoot, "proj1", "pypi", "atomic", "packages", filename)

	// Sanity baseline.
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("baseline file missing: %v", err)
	}
	row, err := f.pypiRepo.FindByFilename(context.Background(), repoID, filename)
	if err != nil || row == nil {
		t.Fatalf("baseline row missing: row=%v err=%v", row, err)
	}

	// Arm the one-shot failpoint.
	sentinel := errors.New("synthetic-tx-fail")
	f.db.SetWriteTxFailpointForTest(sentinel)

	delURL := f.srv.URL + "/proj1/pypi/atomic/packages/" + filename
	req, _ := http.NewRequest(http.MethodDelete, delURL, nil)
	req.Header.Set("Authorization", f.basicAuth())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DELETE under tx failure: status=%d (want 500)", resp.StatusCode)
	}

	// THE ATOMICITY INVARIANT.
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("file gone after rolled-back DELETE: %v", err)
	}
	row, _ = f.pypiRepo.FindByFilename(context.Background(), repoID, filename)
	if row == nil {
		t.Fatal("row gone after rolled-back DELETE (should be intact)")
	}

	// Recovery DELETE.
	req, _ = http.NewRequest(http.MethodDelete, delURL, nil)
	req.Header.Set("Authorization", f.basicAuth())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("recovery DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("recovery DELETE: status=%d (want 204)", resp.StatusCode)
	}

	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after recovery DELETE: %v", err)
	}
	row, _ = f.pypiRepo.FindByFilename(context.Background(), repoID, filename)
	if row != nil {
		t.Fatal("row still present after recovery DELETE")
	}
}

// failingTrash is a storage.Trash impl whose Move always returns moveErr.
// MoveWithSnapshot/Restore/List delegate to an inner real trash so the rest
// of the system behaves normally. Used by the trash-Move-failure-propagation
// test below to prove that a failing post-tx-commit Move surfaces as HTTP
// 500 (the v1.5 silent-discard regression guard from CONTEXT D-02).
type failingTrash struct {
	inner   storage.Trash
	moveErr error
}

func (f *failingTrash) Move(ctx context.Context, srcPath, kind string, id int64, actor string) (string, error) {
	return "", f.moveErr
}

func (f *failingTrash) MoveWithSnapshot(ctx context.Context, srcPath, kind string, id int64, actor string, snapshot json.RawMessage) (string, error) {
	return f.inner.MoveWithSnapshot(ctx, srcPath, kind, id, actor, snapshot)
}

func (f *failingTrash) Restore(ctx context.Context, trashPath, dstPath string) error {
	return f.inner.Restore(ctx, trashPath, dstPath)
}

func (f *failingTrash) List(ctx context.Context) ([]storage.TrashEntry, error) {
	return f.inner.List(ctx)
}

// TestPyPIDelete_TrashMoveFailure_PropagatesError is the CONTEXT D-02
// regression-guard. Plan 04-01 replaced the silent `_, _ = h.trash.Move(...)`
// discard in pypi/upload_legacy.go's deletePackage with explicit error
// propagation; this test pins that behavior so a future revert silently
// re-introducing the swallow trips the build.
//
// Setup: build a parallel pypi.Handler whose Trash field is a failingTrash
// wrapper that always errors from Move. Issue DELETE; assert the response
// is HTTP 500 (NOT the 204 that the silent-discard pattern would produce).
//
// The DB row is gone — the tx committed fine. That is the post-04-01 atomic
// ordering (tx commits before trash.Move). The file remains at its original
// storage path, which is the documented CONTEXT D-05 trade-off (orphaned
// file is operator-recoverable; orphaned row was the worse worst-case the
// reorder eliminated).
func TestPyPIDelete_TrashMoveFailure_PropagatesError(t *testing.T) {
	// Reuse the standard fixture for DB/storage seeding, then mount a
	// parallel pypi.Handler on a fresh chi router with a swapped Trash.
	// Building the handler inline (rather than mutating fixture state)
	// keeps the failingTrash scoped to this test and avoids cross-test
	// leakage.
	f := newHandlerFixture(t)
	_, repoID := f.seedRepo("proj1", "trashfail", true, false)

	wheel := makeWheelBytes(t, "mypkg", "1.0")
	const filename = "mypkg-1.0-py3-none-any.whl"

	// Use the regular fixture handler to seed the file + row (no DB error).
	resp := twineUpload(t, f.srv.URL, "proj1", "trashfail", filename, wheel, "bdist_wheel", f.basicAuth())
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("baseline upload: %d", resp.StatusCode)
	}

	diskPath := filepath.Join(f.repoRoot, "proj1", "pypi", "trashfail", "packages", filename)
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("baseline file missing: %v", err)
	}

	// Build a parallel pypi.Handler with the failingTrash. Reuse the
	// fixture's repos/users/etc. so the auth + project membership lookups
	// resolve identically. The fresh server is mounted on a separate chi
	// router so we control which Trash the DELETE handler hits.
	moveErr := errors.New("synthetic-trash-move-fail")
	swapped := &failingTrash{
		inner:   storage.NewTrash(filepath.Join(f.dataRoot, "trash")),
		moveErr: moveErr,
	}
	h := pypi.New(pypi.Deps{
		DB:          f.db,
		Users:       f.users,
		APIKeys:     f.apiKeys,
		Sessions:    metadata.NewSessionsRepo(f.db),
		Repos:       f.repos,
		Projects:    f.projects,
		Members:     metadata.NewMembersRepo(f.db),
		PyPIFiles:   f.pypiRepo,
		Scans:       f.scans,
		Coalescer:   f.registry,
		PEP694:      f.pep694,
		Path:        storage.NewPathStore(f.repoRoot),
		Trash:       swapped,
		Audit:       f.auditLog,
		MaxPutBytes: 4 << 20,
		RepoRoot:    f.repoRoot,
	})
	r := chi.NewRouter()
	h.Mount(r)
	parallelSrv := httptest.NewServer(r)
	t.Cleanup(parallelSrv.Close)

	delURL := parallelSrv.URL + "/proj1/pypi/trashfail/packages/" + filename
	req, _ := http.NewRequest(http.MethodDelete, delURL, nil)
	req.Header.Set("Authorization", f.basicAuth())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// THE REGRESSION-GUARD INVARIANT: post-04-01, a failing trash.Move
	// surfaces as HTTP 500. The pre-04-01 silent discard would have
	// returned 204 here.
	if resp.StatusCode != http.StatusInternalServerError {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("DELETE under failing trash.Move: status=%d (want 500); body=%s", resp.StatusCode, bodyBytes)
	}

	// Atomic ordering side-effect (CONTEXT D-05): the DB row is gone (tx
	// committed before trash.Move was attempted) and the file remains at
	// its original path (operator-recoverable).
	row, _ := f.pypiRepo.FindByFilename(context.Background(), repoID, filename)
	if row != nil {
		t.Fatalf("post-trash-failure: row should be gone (tx committed first), got %+v", row)
	}
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("post-trash-failure: file should still be on disk (orphan), got %v", err)
	}

}
