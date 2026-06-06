// Package metadata — GitRefsRepo owns the git_refs mirror table. Rows are
// rebuilt synchronously by the post-ReceivePack walker still holding the
// per-repo mutex: classify each ref, DELETE the old rows for the repo,
// batch-INSERT the new set.
//
// Batch INSERTs chunk at 200 rows to stay well under
// SQLITE_MAX_VARIABLE_NUMBER (default 999 on modernc.org/sqlite; 4 columns
// × 200 rows = 800 placeholders).
package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GitRefType is the compile-time enum mirroring the DDL CHECK on
// git_refs.type. Keep this set in sync with migration 017.
type GitRefType string

const (
	GitRefBranch   GitRefType = "branch"
	GitRefTag      GitRefType = "tag"
	GitRefSymbolic GitRefType = "symbolic" // HEAD and similar
	GitRefOther    GitRefType = "other"
)

// GitRef mirrors one git_refs row.
type GitRef struct {
	ID        int64
	RepoID    int64
	Name      string
	Target    string
	Type      GitRefType
	UpdatedAt time.Time
}

// GitRefsRepo is the typed repo for git_refs.
type GitRefsRepo struct{ db *DB }

// NewGitRefsRepo constructs the repo bound to db.
func NewGitRefsRepo(db *DB) *GitRefsRepo { return &GitRefsRepo{db: db} }

// gitRefsChunkSize caps the rows-per-INSERT to stay under
// SQLITE_MAX_VARIABLE_NUMBER (4 placeholders per row × 200 = 800).
const gitRefsChunkSize = 200

// ReplaceAll rebuilds the full ref set for repoID inside the caller's tx.
// Deletes every existing row for the repo, then batch-INSERTs `refs` in
// chunks of 200. Empty `refs` leaves the repo with no ref rows — caller
// is expected to always pass the full live set.
func (r *GitRefsRepo) ReplaceAll(ctx context.Context, tx *sql.Tx, repoID int64, refs []GitRef) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM git_refs WHERE repo_id = ?`, repoID,
	); err != nil {
		return fmt.Errorf("git_refs: delete by repo %d: %w", repoID, err)
	}
	if len(refs) == 0 {
		return nil
	}
	const chunk = gitRefsChunkSize
	for i := 0; i < len(refs); i += chunk {
		end := i + chunk
		if end > len(refs) {
			end = len(refs)
		}
		batch := refs[i:end]
		if err := r.insertBatch(ctx, tx, repoID, batch); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceAllTx is the explicitly-tx-scoped variant of ReplaceAll. It is
// functionally identical — prune all git_refs for repoID, batch-INSERT the
// new set in chunks of gitRefsChunkSize — but the `Tx` suffix makes the
// caller-owned-transaction contract load-bearing in call sites where the
// surrounding writer tx is the unit of atomicity: the post-fetch refs
// rewrite must be atomic so concurrent readers never observe a partial
// ref set.
//
// This exists so the git mirror sync handler
// (internal/protocol/git/sync_handler.go) reads:
//
//	h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
//	    return refsRepo.ReplaceAllTx(ctx, tx, repoID, newRefs)
//	})
//
// rather than the older ReplaceAll name which predates the tx-requirement
// contract. Both methods delegate to the same implementation — ReplaceAllTx
// exists for intent-reading clarity at the sync handler's writer-tx
// boundary. The existing receive-pack walker (internal/protocol/git/refs.go
// WalkAndReplace) continues to use ReplaceAll unchanged to minimize churn.
func (r *GitRefsRepo) ReplaceAllTx(ctx context.Context, tx *sql.Tx, repoID int64, refs []GitRef) error {
	return r.ReplaceAll(ctx, tx, repoID, refs)
}

func (r *GitRefsRepo) insertBatch(ctx context.Context, tx *sql.Tx, repoID int64, batch []GitRef) error {
	if len(batch) == 0 {
		return nil
	}
	placeholders := make([]string, len(batch))
	args := make([]any, 0, len(batch)*4)
	for i, ref := range batch {
		if ref.Name == "" || ref.Target == "" || ref.Type == "" {
			return fmt.Errorf("git_refs: ref %d name/target/type required", i)
		}
		placeholders[i] = "(?, ?, ?, ?)"
		args = append(args, repoID, ref.Name, ref.Target, string(ref.Type))
	}
	q := "INSERT INTO git_refs(repo_id, name, target, type) VALUES " + strings.Join(placeholders, ", ")
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("git_refs: batch insert (%d rows): %w", len(batch), err)
	}
	return nil
}

// List returns every ref for repoID ordered by name ASC. Used by the UI.
func (r *GitRefsRepo) List(ctx context.Context, repoID int64) ([]GitRef, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT id, repo_id, name, target, type, updated_at
		FROM git_refs WHERE repo_id = ?
		ORDER BY name ASC
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("git_refs: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []GitRef
	for rows.Next() {
		var g GitRef
		var typ, updated string
		if err := rows.Scan(&g.ID, &g.RepoID, &g.Name, &g.Target, &typ, &updated); err != nil {
			return nil, fmt.Errorf("git_refs: scan: %w", err)
		}
		g.Type = GitRefType(typ)
		g.UpdatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", updated)
		out = append(out, g)
	}
	return out, rows.Err()
}
