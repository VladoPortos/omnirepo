package git_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	gitpkg "github.com/dxc-internal/omnirepo/internal/protocol/git"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// newOnRepoCreateHandler builds a gitpkg.Handler wired to a fresh sqlitetest
// DB under dataRoot. The returned handler mirrors the shape used in
// refs_test.go so future tests against OnRepoCreate can reuse this helper.
func newOnRepoCreateHandler(t *testing.T, db *metadata.DB, dataRoot string) *gitpkg.Handler {
	t.Helper()
	return gitpkg.New(gitpkg.Deps{
		Backend:  gitpkg.SelectBackend(defaultCfg()),
		Config:   defaultCfg(),
		Locks:    storage.NewLocks(),
		Repos:    metadata.NewReposRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Audit:    &fakeAuditLogger{},
		DataRoot: dataRoot,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		DB:       db,
		Refs:     metadata.NewGitRefsRepo(db),
	})
}

// TestOnRepoCreate_MirrorSkipsInitBare is the load-bearing Pitfall D guard:
// when the repos row carries is_mirror=1, OnRepoCreate must NOT create the
// bare-repo directory on disk — gogit.PlainCloneContext refuses a non-empty
// target. The hook also skips the HEAD ref seed (the upstream clone brings
// the full ref set).
func TestOnRepoCreate_MirrorSkipsInitBare(t *testing.T) {
	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	refsRepo := metadata.NewGitRefsRepo(db)
	dataRoot := t.TempDir()
	reposRepo := metadata.NewReposRepo(db)

	h := newOnRepoCreateHandler(t, db, dataRoot)

	ctx := context.Background()
	var repoID int64
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var insErr error
		repoID, insErr = reposRepo.CreateInTx(ctx, tx, projID, "git", "mirror-repo", "", nil, nil, nil)
		if insErr != nil {
			return insErr
		}
		if err := reposRepo.SetMirrorConfigInTx(ctx, tx, repoID, metadata.MirrorConfig{
			IsMirror:    true,
			UpstreamURL: "https://upstream.example.com/repo.git",
		}); err != nil {
			return err
		}
		_, err := h.OnRepoCreate(ctx, tx, repoID, "git", "testproj", "mirror-repo")
		return err
	})
	if err != nil {
		t.Fatalf("OnRepoCreate (mirror): %v", err)
	}

	// Pitfall D assertion: bare-repo directory must NOT exist yet.
	bareDir := filepath.Join(dataRoot, "repos", "testproj", "git", "mirror-repo.git")
	if _, err := os.Stat(bareDir); !os.IsNotExist(err) {
		t.Fatalf("bare repo dir %q should NOT exist for mirror repo (Pitfall D), got stat err: %v", bareDir, err)
	}

	// HEAD ref row must NOT exist either — the sync handler populates refs
	// from the upstream clone on first sync.
	if _, err := refsRepo.FindByName(ctx, repoID, "HEAD"); err == nil {
		t.Fatalf("HEAD ref should NOT be seeded for mirror repo; found one")
	}
}

// TestOnRepoCreate_NonMirrorInitsBare pins the non-mirror path: a plain
// type=git repo (is_mirror=0) still triggers InitBare + HEAD seed exactly
// as before plan 11-06. Regression guard on the Task 1 refactor.
func TestOnRepoCreate_NonMirrorInitsBare(t *testing.T) {
	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	refsRepo := metadata.NewGitRefsRepo(db)
	dataRoot := t.TempDir()
	reposRepo := metadata.NewReposRepo(db)

	h := newOnRepoCreateHandler(t, db, dataRoot)

	ctx := context.Background()
	var repoID int64
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var insErr error
		repoID, insErr = reposRepo.CreateInTx(ctx, tx, projID, "git", "plain-repo", "", nil, nil, nil)
		if insErr != nil {
			return insErr
		}
		// No SetMirrorConfigInTx → is_mirror stays 0 (default).
		_, err := h.OnRepoCreate(ctx, tx, repoID, "git", "testproj", "plain-repo")
		return err
	})
	if err != nil {
		t.Fatalf("OnRepoCreate (non-mirror): %v", err)
	}

	// Bare-repo dir SHOULD exist.
	bareDir := filepath.Join(dataRoot, "repos", "testproj", "git", "plain-repo.git")
	fi, err := os.Stat(bareDir)
	if err != nil {
		t.Fatalf("bare repo dir %q should exist for non-mirror repo: %v", bareDir, err)
	}
	if !fi.IsDir() {
		t.Fatalf("bare repo path %q should be a directory", bareDir)
	}

	// HEAD ref SHOULD be seeded to refs/heads/main.
	headRef, err := refsRepo.FindByName(ctx, repoID, "HEAD")
	if err != nil {
		t.Fatalf("find HEAD: %v", err)
	}
	if headRef.Target != "refs/heads/main" {
		t.Fatalf("HEAD target = %q, want refs/heads/main", headRef.Target)
	}
	if headRef.Type != metadata.GitRefSymbolic {
		t.Fatalf("HEAD type = %q, want symbolic", headRef.Type)
	}
}
