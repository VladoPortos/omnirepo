// Package git — bare-repo lifecycle hooks for type="git" repos (D-38).
// OnRepoCreate seeds a bare repo on disk + HEAD ref row.
// OnRepoDelete soft-moves the bare-repo dir to trash.
package git

import (
	"context"
	"database/sql"
	"path/filepath"

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
func (h *Handler) OnRepoCreate(ctx context.Context, tx *sql.Tx, repoID int64, repoType, projectName, repoName string) (map[string]any, error) {
	if repoType != "git" {
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

// OnRepoDelete soft-moves the bare-repo dir to trash for type="git" repos.
// git_refs rows cascade via FK ON DELETE CASCADE in migration 017.
// For non-git repo types, returns nil immediately.
func (h *Handler) OnRepoDelete(ctx context.Context, repo *metadata.Repo, projectName string, trash storage.Trash) error {
	if repo.Type != "git" {
		return nil
	}

	repoPath := filepath.Join(h.dataRoot, "repos", projectName, "git", repo.Name+".git")
	_, err := trash.Move(ctx, repoPath, "git-repo", repo.ID)
	return err
}
