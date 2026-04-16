// Package app — one-shot FTS backfill for pre-existing databases.
//
// Historically the bootstrap path inserted repos directly and did not
// populate repos_fts. DBs seeded that way (including the dev/fixture DB used
// for manual testing) come up with an empty FTS index so global search
// returns zero results for every term. The bootstrap now indexes repos
// inline; this helper closes the gap for databases that predate the fix.
//
// The backfill is per-repo, not bulk: it finds any repo whose rowid has no
// matching repos_fts entry and indexes just those. This keeps the operation
// idempotent and correct even when repos_fts has partial or stale rows
// (e.g. one orphan FTS row left behind by a failed migration would
// otherwise make a naive COUNT-based check skip real missing rows).
package app

import (
	"context"
	"fmt"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

func ensureFTSIndexed(ctx context.Context, db *metadata.DB) error {
	tx, err := db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Find non-deleted repos that have no matching repos_fts row. repos_fts
	// uses rowid = repo.id (see metadata.IndexRepo), so a LEFT JOIN on
	// rowid isolates the missing set precisely — no race window, and
	// resilient to partial/stale FTS state.
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id, r.name, p.name, COALESCE(r.description_md, ''), r.type
		FROM repos r
		JOIN projects p ON p.id = r.project_id
		LEFT JOIN repos_fts f ON f.rowid = r.id
		WHERE r.deleted_at IS NULL AND f.rowid IS NULL
	`)
	if err != nil {
		return fmt.Errorf("select missing repos: %w", err)
	}

	type repoRow struct {
		id                                       int64
		name, projectName, description, repoType string
	}
	var missing []repoRow
	for rows.Next() {
		var rr repoRow
		if err := rows.Scan(&rr.id, &rr.name, &rr.projectName, &rr.description, &rr.repoType); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan repo: %w", err)
		}
		missing = append(missing, rr)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate repos: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close rows: %w", err)
	}

	if len(missing) == 0 {
		// Commit the empty tx (rollback would also be fine) so the
		// deferred rollback becomes a no-op. Keeps the code path uniform.
		return tx.Commit()
	}

	for _, rr := range missing {
		if err := metadata.IndexRepo(ctx, tx, rr.id, rr.name, rr.projectName, rr.description, rr.repoType); err != nil {
			return fmt.Errorf("index repo %d: %w", rr.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
