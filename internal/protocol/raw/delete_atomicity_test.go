package raw_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestRawDelete_TxFailure_FileAndRowIntact is the fault-injection
// proof for the raw protocol. The handler is ordered so the
// DB tx commits BEFORE trash.Move; this test arms a one-shot failpoint on
// (*metadata.DB).WriteTx, issues a DELETE, and asserts the dual invariant:
//
//   - The original file is STILL on disk at its storage path (not moved
//     to trash, because tx never committed).
//   - The raw_files row is STILL present (rolled back).
//
// Then issues a second DELETE without re-arming the failpoint and asserts
// it succeeds (204) — proves the failpoint is one-shot and the system is
// recoverable after a transient DB failure.
//
// A future regression that re-inverts the ordering (move-before-tx) will
// fail the post-failure assertion: file would be in trash even though the
// row is intact, breaking filesystem-DB consistency.
func TestRawDelete_TxFailure_FileAndRowIntact(t *testing.T) {
	f := newRawFixture(t)
	_, repoID := f.seedRepo("p", "r", false, false)

	body := []byte("atomicity-keep")
	resp := f.put(t, "/p/raw/r/keep.txt", body, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("baseline PUT: %d", resp.StatusCode)
	}
	resp.Body.Close()

	diskPath := filepath.Join(f.repoRoot, "p", "raw", "r", "keep.txt")

	// Sanity: baseline state before arming the failpoint.
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("baseline file missing: %v", err)
	}
	if _, found, err := f.files.Get(context.Background(), repoID, "keep.txt"); err != nil || !found {
		t.Fatalf("baseline row missing: found=%v err=%v", found, err)
	}

	// Arm the one-shot failpoint. The next WriteTx fn runs to completion
	// then the deferred rollback fires, returning the sentinel.
	sentinel := errors.New("synthetic-tx-fail")
	f.db.SetWriteTxFailpointForTest(sentinel)

	resp = f.del(t, "/p/raw/r/keep.txt", true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DELETE under tx failure: status=%d (want 500)", resp.StatusCode)
	}
	resp.Body.Close()

	// THE ATOMICITY INVARIANT (load-bearing).
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("file gone after rolled-back DELETE (should be intact): %v", err)
	}
	if _, found, err := f.files.Get(context.Background(), repoID, "keep.txt"); err != nil || !found {
		t.Fatalf("row gone after rolled-back DELETE (should be intact): found=%v err=%v", found, err)
	}

	// Recovery DELETE — failpoint cleared, normal flow committing AND
	// moving the file to trash. This proves the system recovers from a
	// transient DB failure without operator intervention beyond a retry.
	resp = f.del(t, "/p/raw/r/keep.txt", true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("recovery DELETE: status=%d (want 204)", resp.StatusCode)
	}
	resp.Body.Close()

	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after recovery DELETE: err=%v", err)
	}
	if _, found, _ := f.files.Get(context.Background(), repoID, "keep.txt"); found {
		t.Fatal("row still present after recovery DELETE")
	}
}
