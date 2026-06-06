package git_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	gitpkg "github.com/vladoportos/omnirepo/internal/protocol/git"
)

// TestCreateRepoHook_MirrorSkipsInitBare is the load-bearing Pitfall D guard:
// when the repos row carries is_mirror=1, CreateRepoHook must NOT create the
// bare-repo directory on disk — gogit.PlainCloneContext refuses a non-empty
// target. The hook also skips the HEAD ref seed (the upstream clone brings
// the full ref set).
func TestCreateRepoHook_MirrorSkipsInitBare(t *testing.T) {
	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	refsRepo := metadata.NewGitRefsRepo(db)
	dataRoot := t.TempDir()
	reposRepo := metadata.NewReposRepo(db)

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
		return gitpkg.CreateRepoHook(ctx, tx, repoID, "git", "testproj", "mirror-repo", dataRoot, refsRepo)
	})
	if err != nil {
		t.Fatalf("CreateRepoHook (mirror): %v", err)
	}

	// Pitfall D assertion: bare-repo directory must NOT exist yet.
	bareDir := filepath.Join(dataRoot, "repos", "testproj", "git", "mirror-repo.git")
	if _, err := os.Stat(bareDir); !os.IsNotExist(err) {
		t.Fatalf("bare repo dir %q should NOT exist for mirror repo (Pitfall D), got stat err: %v", bareDir, err)
	}

	// HEAD ref row must NOT exist either — the sync handler populates refs
	// from the upstream clone on first sync.
	refs, err := refsRepo.List(ctx, repoID)
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("no refs should be seeded for mirror repo; got %d", len(refs))
	}
}

// TestCreateRepoHook_NonMirrorInitsBare pins the non-mirror path: a plain
// type=git repo (is_mirror=0) still triggers InitBare + HEAD seed exactly
// as before. Regression guard on the refactor.
func TestCreateRepoHook_NonMirrorInitsBare(t *testing.T) {
	db := sqlitetest.New(t)
	projID := seedProject(t, db)
	refsRepo := metadata.NewGitRefsRepo(db)
	dataRoot := t.TempDir()
	reposRepo := metadata.NewReposRepo(db)

	ctx := context.Background()
	var repoID int64
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var insErr error
		repoID, insErr = reposRepo.CreateInTx(ctx, tx, projID, "git", "plain-repo", "", nil, nil, nil)
		if insErr != nil {
			return insErr
		}
		// No SetMirrorConfigInTx → is_mirror stays 0 (default).
		return gitpkg.CreateRepoHook(ctx, tx, repoID, "git", "testproj", "plain-repo", dataRoot, refsRepo)
	})
	if err != nil {
		t.Fatalf("CreateRepoHook (non-mirror): %v", err)
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
	headRef := findRef(t, refsRepo, repoID, "HEAD")
	if headRef.Target != "refs/heads/main" {
		t.Fatalf("HEAD target = %q, want refs/heads/main", headRef.Target)
	}
	if headRef.Type != metadata.GitRefSymbolic {
		t.Fatalf("HEAD type = %q, want symbolic", headRef.Type)
	}
}
