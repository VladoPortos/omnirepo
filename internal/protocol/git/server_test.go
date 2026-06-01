package git_test

import (
	"path/filepath"
	"testing"

	gogitpkg "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"

	git "github.com/vladoportos/omnirepo/internal/protocol/git"
)

// TestInitBare verifies InitBare creates a bare repo with HEAD → refs/heads/<initialBranch>.
func TestInitBare(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "repo.git")
	if err := git.InitBare(dir, "main"); err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	repo, err := gogitpkg.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	head, err := repo.Storer.Reference(plumbing.HEAD)
	if err != nil {
		t.Fatalf("reference HEAD: %v", err)
	}
	if head.Type() != plumbing.SymbolicReference {
		t.Fatalf("HEAD type = %v, want symbolic", head.Type())
	}
	want := plumbing.ReferenceName("refs/heads/main")
	if head.Target() != want {
		t.Fatalf("HEAD target = %q, want %q", head.Target(), want)
	}
}

// TestInitBareDefaultBranch checks an arbitrary initial branch.
func TestInitBareDefaultBranch(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "r.git")
	if err := git.InitBare(dir, "trunk"); err != nil {
		t.Fatal(err)
	}
	repo, err := gogitpkg.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Storer.Reference(plumbing.HEAD)
	if err != nil {
		t.Fatal(err)
	}
	if string(head.Target()) != "refs/heads/trunk" {
		t.Fatalf("HEAD target = %q, want refs/heads/trunk", head.Target())
	}
}
