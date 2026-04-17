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

// TestDeleteRepo_GitMovesDotGitDir pins audit finding #3: deleting a git
// repo must move the on-disk `<repo>.git` directory to trash. Before the
// fix, handleDeleteRepo used `<repo>` with no .git suffix, so the bare repo
// stayed on disk and trash/restore was broken for type=git.
func TestDeleteRepo_GitMovesDotGitDir(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	s.do(t, "POST", "/api/v1/projects", cookie, map[string]any{"name": "proj"})
	s.do(t, "POST", "/api/v1/projects/proj/repos", cookie, map[string]any{
		"name": "r1", "type": "git",
	})

	// The real repo hook InitBare's the `.git` dir; the test server's
	// RepoCreateHook is nil, so simulate the post-create filesystem state.
	bareDir := filepath.Join(s.dataRoot, "repos", "proj", "git", "r1.git")
	if err := os.MkdirAll(filepath.Join(bareDir, "refs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bareDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ALSO place a file at the bugged path (without .git) to prove the
	// delete doesn't accidentally pick it up. The post-fix behavior must
	// leave this untouched.
	bugDir := filepath.Join(s.dataRoot, "repos", "proj", "git", "r1")
	if err := os.MkdirAll(bugDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bugDir, "unrelated"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, _ := s.do(t, "DELETE", "/api/v1/projects/proj/repos/git/r1", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete code=%d", resp.StatusCode)
	}

	// Bare repo dir must be gone from its original location.
	if _, err := os.Stat(bareDir); !os.IsNotExist(err) {
		t.Fatalf(".git dir still on disk after delete: err=%v", err)
	}
	// Unrelated sibling dir must remain (wasn't this delete's target).
	if _, err := os.Stat(bugDir); err != nil {
		t.Fatalf("unrelated sibling dir removed: %v", err)
	}

	// Trash must contain a git-repo entry pointing at the moved .git.
	_, body := s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("trash empty after git delete: %v", body)
	}
	found := false
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["type"] == "git-repo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no git-repo trash entry: %v", items)
	}
}

// TestAdminTrash_RestoreReconstructsOriginalPath pins audit finding #2: the
// restore endpoint must put the tree back at its exact pre-delete location
// (e.g. repos/proj/raw/r1), not at repos/r1 which was the old bug.
func TestAdminTrash_RestoreReconstructsOriginalPath(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	s.do(t, "POST", "/api/v1/projects", cookie, map[string]any{"name": "proj"})
	s.do(t, "POST", "/api/v1/projects/proj/repos", cookie, map[string]any{
		"name": "r1", "type": "raw",
	})

	orig := filepath.Join(s.dataRoot, "repos", "proj", "raw", "r1")
	if err := os.MkdirAll(orig, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orig, "f"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Delete → moves to trash.
	if resp, _ := s.do(t, "DELETE", "/api/v1/projects/proj/repos/raw/r1", cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("delete code=%d", resp.StatusCode)
	}
	if _, err := os.Stat(orig); !os.IsNotExist(err) {
		t.Fatalf("orig not moved to trash: err=%v", err)
	}

	// Put a decoy file at the BUGGY restore target (repos/r1) to prove the
	// fix doesn't collide with or clobber unrelated content.
	decoy := filepath.Join(s.dataRoot, "repos", "r1")
	if err := os.MkdirAll(decoy, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "unrelated"), []byte("KEEP"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Find trash id.
	_, body := s.do(t, "GET", "/api/v1/admin/trash", cookie, nil)
	items := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no trash entries")
	}
	entry := items[0].(map[string]any)
	trashID := entry["id"].(string)

	// Restore.
	resp, _ := s.do(t, "POST", "/api/v1/admin/trash/"+trashID+"/restore", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("restore code=%d", resp.StatusCode)
	}

	// Content must be back at the ORIGINAL location.
	if got, err := os.ReadFile(filepath.Join(orig, "f")); err != nil || string(got) != "data" {
		t.Fatalf("restore missed original path: got=%q err=%v", got, err)
	}
	// Decoy must be untouched — restore must not have clobbered it via the
	// old buggy basename-only path.
	if got, err := os.ReadFile(filepath.Join(decoy, "unrelated")); err != nil || string(got) != "KEEP" {
		t.Fatalf("decoy was clobbered: got=%q err=%v", got, err)
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
