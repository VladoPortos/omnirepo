package rpm_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestRPMDelete_TxFailure_FileAndRowIntact is the ATOMICDEL-06 fault-injection
// proof for the RPM protocol. Mirrors the raw atomicity test shape: arm a
// one-shot WriteTx failpoint, issue DELETE, assert file-on-disk and DB row
// are both intact (tx rolled back → no filesystem mutation), then issue a
// recovery DELETE and assert the system fully cleans up.
//
// Storage path layout: <repoRoot>/<project>/rpm/<repo>/packages/<filename>.
// A future regression that re-inverts the ordering (move-before-tx) will
// fail the post-failure assertion: the .rpm would be moved to trash even
// though the rpm_packages row is rolled back.
func TestRPMDelete_TxFailure_FileAndRowIntact(t *testing.T) {
	f := newRPMFixture(t)
	_, repoID := f.seedRepo("proj", "myrepo", false)

	body := readFixtureRPM(t)
	resp := f.put(t, "/proj/rpm/myrepo/packages/"+sampleRPMCanonical, body, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("baseline PUT: %d", resp.StatusCode)
	}

	diskPath := filepath.Join(f.repoRoot, "proj", "rpm", "myrepo", "packages", sampleRPMCanonical)

	// Sanity baseline.
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("baseline file missing: %v", err)
	}
	pkgs, _ := f.rpmPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 1 {
		t.Fatalf("baseline rows: got %d, want 1", len(pkgs))
	}

	// Arm the one-shot failpoint.
	sentinel := errors.New("synthetic-tx-fail")
	f.db.SetWriteTxFailpointForTest(sentinel)

	resp = f.doMethod(t, http.MethodDelete, "/proj/rpm/myrepo/packages/"+sampleRPMCanonical, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DELETE under tx failure: status=%d (want 500)", resp.StatusCode)
	}

	// THE ATOMICITY INVARIANT.
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("file gone after rolled-back DELETE: %v", err)
	}
	pkgs, _ = f.rpmPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 1 {
		t.Fatalf("rows after rolled-back DELETE: got %d, want 1 (intact)", len(pkgs))
	}

	// Recovery DELETE.
	resp = f.doMethod(t, http.MethodDelete, "/proj/rpm/myrepo/packages/"+sampleRPMCanonical, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("recovery DELETE: status=%d (want 204)", resp.StatusCode)
	}

	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after recovery DELETE: %v", err)
	}
	pkgs, _ = f.rpmPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 0 {
		t.Fatalf("rows after recovery DELETE: got %d, want 0", len(pkgs))
	}
}
