// Package git — bare-repo creation hook for type="git" repos.
// CreateRepoHook seeds a bare repo on disk + a HEAD ref row inside the
// caller's repo-create transaction.
package git

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// CreateRepoHook is the git arm of the composed repo-create hook. Both the
// API path (composedRepoCreateHook in app.Run) and the bootstrap hook call
// it inside the same writer tx as the repos row INSERT so a failure rolls
// back the repo row. It:
//  1. Creates the bare repo on disk via InitBare.
//  2. Seeds a git_refs row with HEAD → refs/heads/main.
//
// For non-git repo types, it returns nil immediately.
//
// Pitfall: mirror repos MUST NOT pre-init the bare repo —
// the git mirror sync handler calls gogit.PlainCloneContext on first sync,
// which requires an EMPTY target directory (it returns ErrTargetDirNotEmpty
// otherwise). If InitBare ran here first, the .git/ skeleton would be
// present and the clone would fail. For mirror repos we therefore skip
// BOTH the InitBare call and the HEAD ref seed — PlainCloneContext will
// create the bare layout and populate refs on first sync.
//
// Audit finding #7: InitBare creates the dir on disk inside the hook, but
// the outer tx can still roll back if a later step fails. The freshly
// initialised dir is removed on a seed failure so the filesystem doesn't
// hold an orphan that has no repos row.
func CreateRepoHook(ctx context.Context, tx *sql.Tx, repoID int64, repoType, projectName, repoName, dataRoot string, refs *metadata.GitRefsRepo) error {
	if repoType != "git" {
		return nil
	}

	// Pitfall: branch on is_mirror inside the same writer tx that
	// just INSERTed the repos row. Mirrors → no bare init, no HEAD seed.
	isMirror, err := repoIsMirror(ctx, tx, repoID)
	if err != nil {
		return err
	}
	if isMirror {
		return nil
	}

	repoPath := filepath.Join(dataRoot, "repos", projectName, "git", repoName+".git")
	if err := InitBare(repoPath, "main"); err != nil {
		return err
	}

	seed := []metadata.GitRef{
		{Name: "HEAD", Target: "refs/heads/main", Type: metadata.GitRefSymbolic},
	}
	if err := refs.ReplaceAll(ctx, tx, repoID, seed); err != nil {
		_ = os.RemoveAll(repoPath)
		return err
	}

	return nil
}

// repoIsMirror reads is_mirror from the just-INSERTed repos row under the
// same writer tx. Using the caller-owned tx (rather than a Reader-pool
// read) guarantees we observe the INSERT that happens earlier in the same
// transaction — SQLite WAL readers can miss a writer's uncommitted work.
// Returns false when is_mirror is 0, NULL, or the row has somehow
// disappeared.
func repoIsMirror(ctx context.Context, tx *sql.Tx, repoID int64) (bool, error) {
	var m sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT is_mirror FROM repos WHERE id = ?`, repoID).Scan(&m)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("git.CreateRepoHook: read is_mirror for repo %d: %w", repoID, err)
	}
	return m.Valid && m.Int64 != 0, nil
}
