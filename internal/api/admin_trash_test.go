package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAdminTrash_ListEmpty(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d", resp.StatusCode)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, got %v", body)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestAdminTrash_SoftDeleteShowsInList(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Create a project and repo, then delete the repo (which moves to trash).
	s.do(t, "POST", "/api/v1/projects", cookie, map[string]any{"name": "proj"})

	// Create repo.
	s.do(t, "POST", "/api/v1/projects/proj/repos", cookie, map[string]any{
		"name": "r1", "type": "docker",
	})

	// Create on-disk tree so trash.Move actually has something to move.
	onDisk := filepath.Join(s.dataRoot, "repos", "proj", "docker", "r1")
	if err := os.MkdirAll(onDisk, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onDisk, "blob"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Delete repo.
	resp, _ := s.do(t, "DELETE", "/api/v1/projects/proj/repos/docker/r1", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete repo code=%d", resp.StatusCode)
	}

	// List trash — should have at least 1 entry.
	resp, body := s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list trash code=%d", resp.StatusCode)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("expected trash entries, got %v", body)
	}
}

func TestAdminTrash_Purge(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Create and delete a repo to get trash entry.
	s.do(t, "POST", "/api/v1/projects", cookie, map[string]any{"name": "proj"})
	s.do(t, "POST", "/api/v1/projects/proj/repos", cookie, map[string]any{
		"name": "r1", "type": "raw",
	})
	onDisk := filepath.Join(s.dataRoot, "repos", "proj", "raw", "r1")
	_ = os.MkdirAll(onDisk, 0o750)
	_ = os.WriteFile(filepath.Join(onDisk, "f"), []byte("x"), 0o644)

	s.do(t, "DELETE", "/api/v1/projects/proj/repos/raw/r1", cookie, nil)

	// List trash, get the ID.
	_, body := s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	items := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no trash entries")
	}
	entry := items[0].(map[string]any)
	trashID := entry["id"].(string)

	// Purge.
	resp, _ := s.do(t, "DELETE", "/api/v1/admin/trash/"+trashID, cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("purge code=%d", resp.StatusCode)
	}

	// List again — should be empty.
	_, body = s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	items = body["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("expected 0 items after purge, got %d", len(items))
	}
}

func TestAdminTrash_NonSuperAdmin403(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	resp, _ := s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("code=%d want 403", resp.StatusCode)
	}
}
