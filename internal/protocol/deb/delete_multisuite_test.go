package deb_test

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// TestDEBDelete_MultiSuite_RemovesAllRows pins the M-4 fix. A .deb published to
// two suites shares ONE pool file on disk and has one deb_packages row per
// suite (same storage_pool_path). Deleting the pool file must remove BOTH rows
// before trashing the file. The previous delete keyed on filename with LIMIT 1:
// it removed one arbitrary suite's row and trashed the shared file, leaving the
// other suite's row pointing at a file that had been moved to trash — its
// Packages index still advertised the package, but downloads 404'd.
func TestDEBDelete_MultiSuite_RemovesAllRows(t *testing.T) {
	f := newDEBFixture(t)
	_, repoID := f.seedDEBRepo("proj", "myrepo", false)

	// Add a second suite alongside the seeded "stable".
	if err := f.db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		return f.aptSuites.InsertBatch(context.Background(), tx, repoID, []metadata.AptSuite{
			{RepoID: repoID, Suite: "testing", Component: "main", Architecture: "amd64"},
		})
	}); err != nil {
		t.Fatalf("add testing suite: %v", err)
	}

	body := buildTestDeb(t, "mypkg", "1.0-1", "amd64")
	// Publish the SAME package into both suites → one pool file, two rows.
	for _, suite := range []string{"stable", "testing"} {
		url := "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb?suite=" + suite + "&component=main"
		resp := f.do(t, http.MethodPut, url, body, true)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT to suite %s: %d", suite, resp.StatusCode)
		}
	}

	diskPath := filepath.Join(f.repoRoot, "proj", "deb", "myrepo", "pool", "m", "mypkg", "mypkg_1.0-1_amd64.deb")
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("baseline file missing: %v", err)
	}
	pkgs, _ := f.debPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 2 {
		t.Fatalf("baseline rows: got %d, want 2 (one per suite, shared pool file)", len(pkgs))
	}
	// Both rows must share the same storage_pool_path (the file being deleted).
	if pkgs[0].StoragePoolPath != pkgs[1].StoragePoolPath || pkgs[0].StoragePoolPath == "" {
		t.Fatalf("rows do not share storage_pool_path: %q vs %q", pkgs[0].StoragePoolPath, pkgs[1].StoragePoolPath)
	}

	// Delete the pool file (the route is suite-agnostic).
	delURL := "/proj/deb/myrepo/pool/m/mypkg/mypkg_1.0-1_amd64.deb"
	resp := f.do(t, http.MethodDelete, delURL, nil, true)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: status=%d (want 204)", resp.StatusCode)
	}

	// BOTH suite rows gone — no orphan left pointing at the trashed file.
	pkgs, _ = f.debPackages.ListByRepo(context.Background(), repoID)
	if len(pkgs) != 0 {
		t.Fatalf("rows after DELETE: got %d, want 0 (both suites removed; no orphan row)", len(pkgs))
	}
	// File trashed.
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("pool file still on disk after DELETE: %v", err)
	}
}
