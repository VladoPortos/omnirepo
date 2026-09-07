package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/storage"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDEBDriftRestoreRetainedMembership(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	pid, err := metadata.NewProjectsRepo(s.db).Create(ctx, "shared", "shared")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := metadata.NewReposRepo(s.db).Create(ctx, pid, "deb", "apt", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkgs := metadata.NewDEBPackagesRepo(s.db)
	var oldSuite int64
	if err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		suites := metadata.NewAptSuitesRepo(s.db)
		liveSuite, err := suites.Insert(ctx, tx, rid, "stable", "main", "amd64")
		if err != nil {
			return err
		}
		oldSuite, err = suites.Insert(ctx, tx, rid, "oldstable", "main", "amd64")
		if err != nil {
			return err
		}
		_, err = pkgs.Insert(ctx, tx, &metadata.DEBPackage{RepoID: rid, SuiteID: liveSuite, Package: "acme", Version: "1.0", Architecture: "amd64", Digest: "sha256:abc", Filename: "acme.deb", StoragePoolPath: "pool/acme.deb"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "acme.deb")
	if err := os.WriteFile(path, []byte("shared"), 0600); err != nil {
		t.Fatal(err)
	}
	trash := storage.NewTrash(t.TempDir())
	snapshot, _ := json.Marshal(map[string]any{"repo_id": rid, "suite_id": oldSuite, "package": "acme", "version": "1.0", "architecture": "amd64", "digest": "sha256:abc", "filename": "acme.deb", "storage_pool_path": "pool/acme.deb"})
	snapshotter := trash.(interface {
		SnapshotRetained(context.Context, string, string, int64, string, json.RawMessage) (string, error)
	})
	if _, err := snapshotter.SnapshotRetained(ctx, path, "deb_package_drift", 99, "", snapshot); err != nil {
		t.Fatal(err)
	}
	entries, err := trash.List(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries %v %v", entries, err)
	}
	deps := s.deps
	deps.DEBPackages = pkgs
	deps.Trash = trash
	w := httptest.NewRecorder()
	deps.HandleDriftRestoreForTest(w, httptest.NewRequest("POST", "/restore", nil), entries[0], "99")
	if w.Code != 200 {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	rows, err := pkgs.ListByRepo(ctx, rid)
	if err != nil || len(rows) != 2 {
		t.Fatalf("restored memberships=%d: %v", len(rows), err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "shared" {
		t.Fatalf("shared bytes changed: %q %v", b, err)
	}
	entries, err = trash.List(ctx)
	if err != nil || len(entries) != 0 {
		t.Fatalf("snapshot not consumed: %v %v", entries, err)
	}
}
