package api_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogitpkg "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// createBareRepoWithCommit initialises a bare repo at repoPath, creates an
// initial commit with one file, and returns the commit hash.
func createBareRepoWithCommit(t *testing.T, repoPath string) plumbing.Hash {
	t.Helper()
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := gogitpkg.PlainInit(repoPath, true, gogitpkg.WithDefaultBranch("refs/heads/main"))
	if err != nil {
		t.Fatal(err)
	}

	// Create an in-memory worktree is not possible for bare repos, so we
	// build the tree manually via the object storer.
	storage := repo.Storer

	// Store a blob.
	blobObj := &plumbing.MemoryObject{}
	blobObj.SetType(plumbing.BlobObject)
	content := []byte("hello world\n")
	blobObj.SetSize(int64(len(content)))
	w, err := blobObj.Writer()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(content)
	_ = w.Close()
	blobHash, err := storage.SetEncodedObject(blobObj)
	if err != nil {
		t.Fatal(err)
	}

	// Store a tree with one entry.
	treeObj := &plumbing.MemoryObject{}
	treeObj.SetType(plumbing.TreeObject)
	tree := object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "README.md",
				Mode: 0o100644,
				Hash: blobHash,
			},
		},
	}
	enc := &plumbing.MemoryObject{}
	enc.SetType(plumbing.TreeObject)
	if err := tree.Encode(enc); err != nil {
		t.Fatal(err)
	}
	treeHash, err := storage.SetEncodedObject(enc)
	if err != nil {
		t.Fatal(err)
	}

	// Store a commit.
	commitObj := &plumbing.MemoryObject{}
	commitObj.SetType(plumbing.CommitObject)
	commit := object.Commit{
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message:  "initial commit",
		TreeHash: treeHash,
	}
	encCommit := &plumbing.MemoryObject{}
	encCommit.SetType(plumbing.CommitObject)
	if err := commit.Encode(encCommit); err != nil {
		t.Fatal(err)
	}
	commitHash, err := storage.SetEncodedObject(encCommit)
	if err != nil {
		t.Fatal(err)
	}

	// Update refs/heads/main to point to the commit.
	ref := plumbing.NewHashReference("refs/heads/main", commitHash)
	if err := storage.SetReference(ref); err != nil {
		t.Fatal(err)
	}

	return commitHash
}

func TestGitBrowse_TreeEndpoint(t *testing.T) {
	s := newTestServer(t)
	rootID, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "gitproj", "")
	_ = s.deps.Members.Add(ctx, pid, rootID)
	_, _ = s.deps.Repos.Create(ctx, pid, "git", "myrepo", "", nil, nil, nil)

	// Create the bare repo on disk.
	repoPath := filepath.Join(s.dataRoot, "repos", "gitproj", "git", "myrepo.git")
	createBareRepoWithCommit(t, repoPath)

	resp, body := s.do(t, "GET", "/api/v1/projects/gitproj/repos/git/myrepo/tree/main", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	entries, ok := body["items"].([]any)
	if !ok || len(entries) == 0 {
		t.Fatalf("expected tree entries, got %v", body)
	}
	first := entries[0].(map[string]any)
	if first["name"] != "README.md" {
		t.Fatalf("expected README.md, got %v", first["name"])
	}
}

func TestGitBrowse_BlobEndpoint(t *testing.T) {
	s := newTestServer(t)
	rootID, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "gitproj2", "")
	_ = s.deps.Members.Add(ctx, pid, rootID)
	_, _ = s.deps.Repos.Create(ctx, pid, "git", "myrepo", "", nil, nil, nil)

	repoPath := filepath.Join(s.dataRoot, "repos", "gitproj2", "git", "myrepo.git")
	createBareRepoWithCommit(t, repoPath)

	resp, body := s.do(t, "GET", "/api/v1/projects/gitproj2/repos/git/myrepo/blob/main/README.md", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	if body["name"] != "README.md" {
		t.Fatalf("expected name=README.md, got %v", body["name"])
	}
	if body["is_binary"] != false {
		t.Fatalf("expected is_binary=false, got %v", body["is_binary"])
	}
	content, ok := body["content"].(string)
	if !ok || content == "" {
		t.Fatalf("expected non-empty content, got %v", body["content"])
	}
}

func TestGitBrowse_CommitsEndpoint(t *testing.T) {
	s := newTestServer(t)
	rootID, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "gitproj3", "")
	_ = s.deps.Members.Add(ctx, pid, rootID)
	_, _ = s.deps.Repos.Create(ctx, pid, "git", "myrepo", "", nil, nil, nil)

	repoPath := filepath.Join(s.dataRoot, "repos", "gitproj3", "git", "myrepo.git")
	createBareRepoWithCommit(t, repoPath)

	resp, body := s.do(t, "GET", "/api/v1/projects/gitproj3/repos/git/myrepo/commits/main", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	commits, ok := body["items"].([]any)
	if !ok || len(commits) == 0 {
		t.Fatalf("expected commits, got %v", body)
	}
	first := commits[0].(map[string]any)
	if first["author"] != "Test User" {
		t.Fatalf("expected author=Test User, got %v", first["author"])
	}
}

// TestGitBrowse_RefsMatchOpenAPIContract verifies the /refs endpoint
// emits {name, type, sha} per the OpenAPI GitRef schema and excludes
// symbolic refs (HEAD), which the schema enum does not cover. Before
// this fix the handler emitted `target` instead of `sha` and included
// HEAD as type=symbolic, which crashed the React git detail page
// with TypeError: Cannot read properties of undefined (reading 'slice').
func TestGitBrowse_RefsMatchOpenAPIContract(t *testing.T) {
	s := newTestServer(t)
	rootID, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, code := s.login(t, "root", pw)
	if code != 200 {
		t.Fatalf("login code=%d", code)
	}

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "refproj", "")
	_ = s.deps.Members.Add(ctx, pid, rootID)
	repoID, _ := s.deps.Repos.Create(ctx, pid, "git", "refrepo", "", nil, nil, nil)

	// Post-ReceivePack normally populates git_refs; seed it directly so we
	// isolate the SQL → JSON contract and don't depend on the push path.
	writeRef := func(name, target, kind string) {
		t.Helper()
		if _, err := s.db.Writer.ExecContext(ctx,
			`INSERT INTO git_refs(repo_id,name,target,type) VALUES(?,?,?,?)`,
			repoID, name, target, kind); err != nil {
			t.Fatalf("seed git_refs: %v", err)
		}
	}
	writeRef("HEAD", "refs/heads/main", "symbolic")
	writeRef("refs/heads/main", "708d53ca02af99f509db472ff519fc69bbd8bf3d", "branch")
	writeRef("refs/tags/v1", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "tag")

	resp, body := s.do(t, "GET", "/api/v1/projects/refproj/repos/git/refrepo/refs", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("expected items array, got %v", body)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 refs (HEAD filtered), got %d: %v", len(items), items)
	}
	for _, it := range items {
		m := it.(map[string]any)
		if _, ok := m["sha"].(string); !ok {
			t.Fatalf("ref missing `sha` string field (OpenAPI contract): %v", m)
		}
		if _, ok := m["target"]; ok {
			t.Fatalf("ref still emits legacy `target` key (should be renamed to `sha`): %v", m)
		}
		kind, _ := m["type"].(string)
		if kind != "branch" && kind != "tag" {
			t.Fatalf("ref type=%q outside OpenAPI enum [branch, tag]", kind)
		}
	}
}

func TestGitBrowse_NonMemberDenied(t *testing.T) {
	s := newTestServer(t)
	_, _ = seedTestUser(t, s.db, "root", "r@x", true, false)
	_, alicePW := seedTestUser(t, s.db, "alice", "a@x", false, false)

	ctx := context.Background()
	pid, _ := s.deps.Projects.Create(ctx, "secret-git", "")
	_, _ = s.deps.Repos.Create(ctx, pid, "git", "myrepo", "", nil, nil, nil)

	aliceCookie, _, _ := s.login(t, "alice", alicePW)
	resp, _ := s.do(t, "GET", "/api/v1/projects/secret-git/repos/git/myrepo/tree/main", aliceCookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-member, got %d", resp.StatusCode)
	}
}
