package deb_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestDEBDelete_TxFailure_FileAndRowIntact is the ATOMICDEL-06 fault-injection
// proof for the DEB protocol. Same shape as raw/rpm: arm a one-shot WriteTx
// failpoint, DELETE, assert file + row both intact, recovery DELETE cleans
// up.
//
// Storage path layout: <repoRoot>/<project>/deb/<repo>/pool/<component>/
// <pkg>/<filename>.deb (storageKeyForPool puts the literal "pool/m/mypkg/..."
// suffix straight on the URL — see internal/protocol/deb/handler.go:300).
func TestDEBDelete_TxFailure_FileAndRowIntact(t *testing.T) {
	f := newDEBFixture(t)
	_, repoID := f.seedDEBRepo("proj", "myrepo", false)

	body := buildTestDeb(t, "mypkg", "1.0-1", "amd64")
	urlPath := "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=stable&component=main"
	resp := f.do(t, http.MethodPut, urlPath, body, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("baseline PUT: %d", resp.StatusCode)
	}

	diskPath := filepath.Join(f.repoRoot, "proj", "deb", "myrepo", "pool", "m", "mypkg", "mypkg_1.0-1_amd64.deb")

	// Sanity baseline.
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("baseline file missing: %v", err)
	}
	pkgs, _ := f.debPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 1 {
		t.Fatalf("baseline rows: got %d, want 1", len(pkgs))
	}

	// Arm the one-shot failpoint.
	sentinel := errors.New("synthetic-tx-fail")
	f.db.SetWriteTxFailpointForTest(sentinel)

	delURL := "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb"
	resp = f.do(t, http.MethodDelete, delURL, nil, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DELETE under tx failure: status=%d (want 500)", resp.StatusCode)
	}

	// THE ATOMICITY INVARIANT.
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("file gone after rolled-back DELETE: %v", err)
	}
	pkgs, _ = f.debPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 1 {
		t.Fatalf("rows after rolled-back DELETE: got %d, want 1 (intact)", len(pkgs))
	}

	// Recovery DELETE.
	resp = f.do(t, http.MethodDelete, delURL, nil, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("recovery DELETE: status=%d (want 204)", resp.StatusCode)
	}

	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after recovery DELETE: %v", err)
	}
	pkgs, _ = f.debPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 0 {
		t.Fatalf("rows after recovery DELETE: got %d, want 0", len(pkgs))
	}
}
