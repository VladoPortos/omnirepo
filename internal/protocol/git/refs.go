// Package git — refs walker for the post-ReceivePack hook (D-37) and ref
// classification (§P13). Opens the bare repo via go-git PlainOpen, iterates
// all refs + HEAD explicitly, classifies each, then atomically replaces the
// git_refs rows via GitRefsRepo.ReplaceAll inside a single writer tx.
//
// HEAD is fetched via an explicit repo.Storer.Reference(plumbing.HEAD) call
// because IterReferences() behavior w/r/t HEAD varies across go-git
// backends — the filesystem storer may or may not include it. Dedup
// ensures no double-count.
package git

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	gogitpkg "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// ClassifyRef returns the git_refs.type for a reference name.
// HEAD → "symbolic" (covers both symbolic and detached HEAD per §P13
// documented compromise); refs/heads/* → "branch"; refs/tags/* → "tag";
// everything else → "other".
func ClassifyRef(name string) string {
	switch {
	case name == "HEAD":
		return "symbolic"
	case strings.HasPrefix(name, "refs/heads/"):
		return "branch"
	case strings.HasPrefix(name, "refs/tags/"):
		return "tag"
	default:
		return "other"
	}
}

// WalkAndReplace opens the bare repo at repoPath, iterates every ref
// (including HEAD explicitly), classifies each via ClassifyRef, then
// atomically replaces all git_refs rows for repoID in a single writer tx.
//
// Called by the post-ReceivePack hook while the per-repo mutex is still
// held (D-37). Returns an error if the repo cannot be opened or the DB
// write fails — callers (the handler) are expected to log and swallow
// the error so the push response is unaffected.
func WalkAndReplace(ctx context.Context, db *metadata.DB, refsRepo *metadata.GitRefsRepo, repoID int64, repoPath string) error {
	repo, err := gogitpkg.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("open bare repo: %w", err)
	}

	seen := make(map[string]bool)
	var refs []metadata.GitRef

	// 1. Iterate all refs via Storer.IterReferences().
	iter, err := repo.Storer.IterReferences()
	if err != nil {
		return fmt.Errorf("iter refs: %w", err)
	}
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := string(ref.Name())
		seen[name] = true

		target := ""
		if ref.Type() == plumbing.SymbolicReference {
			target = string(ref.Target())
		} else {
			target = ref.Hash().String()
		}

		refs = append(refs, metadata.GitRef{
			Name:   name,
			Target: target,
			Type:   metadata.GitRefType(ClassifyRef(name)),
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk refs: %w", err)
	}

	// 2. Explicitly fetch HEAD — it may not be in IterReferences for some
	//    backends (filesystem storer). Dedup if already present.
	if !seen["HEAD"] {
		headRef, herr := repo.Storer.Reference(plumbing.HEAD)
		if herr == nil && headRef != nil {
			target := ""
			if headRef.Type() == plumbing.SymbolicReference {
				target = string(headRef.Target())
			} else {
				target = headRef.Hash().String()
			}
			refs = append(refs, metadata.GitRef{
				Name:   "HEAD",
				Target: target,
				Type:   metadata.GitRefSymbolic,
			})
		}
	}

	// 3. Atomic replace inside a writer tx.
	return db.WriteTx(ctx, func(tx *sql.Tx) error {
		return refsRepo.ReplaceAll(ctx, tx, repoID, refs)
	})
}
