// Package git — bare-repo lifecycle hooks for type="git" repos (D-38).
// OnRepoCreate seeds a bare repo on disk + HEAD ref row.
// OnRepoDelete soft-moves the bare-repo dir to trash.
package git

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// OnRepoCreate is the RepoCreateHookFn for type="git" repos. It:
//  1. Creates the bare repo on disk via InitBare.
//  2. Seeds a git_refs row with HEAD → refs/heads/main (D-38).
//
// For non-git repo types, it returns (nil, nil) immediately.
// Runs inside the same writer tx as the repos row INSERT (composed hook
// pattern from Phase 3).
//
// Pitfall D (plan 11-06): mirror repos MUST NOT pre-init the bare repo —
// the git mirror sync handler calls gogit.PlainCloneContext on first sync,
// which requires an EMPTY target directory (it returns ErrTargetDirNotEmpty
// otherwise). If InitBare ran here first, the .git/ skeleton would be
// present and the clone would fail. For mirror repos we therefore skip
// BOTH the InitBare call and the HEAD ref seed — PlainCloneContext will
// create the bare layout and populate refs on first sync. See plan
// 11-06-PLAN.md <action> for the full rationale.
func (h *Handler) OnRepoCreate(ctx context.Context, tx *sql.Tx, repoID int64, repoType, projectName, repoName string) (map[string]any, error) {
	if repoType != "git" {
		return nil, nil
	}

	// Pitfall D: branch on is_mirror inside the same writer tx that
	// just INSERTed the repos row. Mirrors → no bare init, no HEAD seed.
	isMirror, err := h.repoIsMirror(ctx, tx, repoID)
	if err != nil {
		return nil, err
	}
	if isMirror {
		return nil, nil
	}

	repoPath := filepath.Join(h.dataRoot, "repos", projectName, "git", repoName+".git")
	if err := InitBare(repoPath, "main"); err != nil {
		return nil, err
	}

	seed := []metadata.GitRef{
		{Name: "HEAD", Target: "refs/heads/main", Type: metadata.GitRefSymbolic},
	}
	if err := h.refs.ReplaceAll(ctx, tx, repoID, seed); err != nil {
		return nil, err
	}

	return nil, nil
}

// repoIsMirror reads is_mirror from the just-INSERTed repos row under the
// same writer tx. Using the caller-owned tx (rather than h.repos.FindByID
// which reads from the reader pool) guarantees we observe the INSERT that
// happens earlier in the same transaction — SQLite WAL readers can miss a
// writer's uncommitted work. Returns false when is_mirror is 0, NULL, or
// the row has somehow disappeared.
func (h *Handler) repoIsMirror(ctx context.Context, tx *sql.Tx, repoID int64) (bool, error) {
	var m sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT is_mirror FROM repos WHERE id = ?`, repoID).Scan(&m)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("git.OnRepoCreate: read is_mirror for repo %d: %w", repoID, err)
	}
	return m.Valid && m.Int64 != 0, nil
}

// OnRepoDelete soft-moves the bare-repo dir to trash for type="git" repos.
// git_refs rows cascade via FK ON DELETE CASCADE in migration 017.
// For non-git repo types, returns nil immediately.
func (h *Handler) OnRepoDelete(ctx context.Context, repo *metadata.Repo, projectName string, trash storage.Trash) error {
	if repo.Type != "git" {
		return nil
	}

	repoPath := filepath.Join(h.dataRoot, "repos", projectName, "git", repo.Name+".git")
	// F-15: OnRepoDelete is called by the api layer inside its request
	// context, so the actor login rides along via ctx. GC-initiated repo
	// deletes (future) will omit this and the sidecar will store an empty
	// "deleted_by" which the UI renders as "—".
	_, err := trash.Move(ctx, repoPath, "git-repo", repo.ID, auth.ActorLoginFromContext(ctx))
	return err
}
