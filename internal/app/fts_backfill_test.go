package app

import (
	"context"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

// TestEnsureFTSIndexed_Backfill inserts repos directly (skipping IndexRepo)
// to simulate a DB seeded before the bootstrap FTS fix. The helper must
// detect the gap and populate repos_fts so search works on subsequent boots.
func TestEnsureFTSIndexed_Backfill(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()

	tx, err := db.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	var projectID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='p1'`).Scan(&projectID); err != nil {
		t.Fatalf("select pid: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repos(project_id, type, name, description_md, auto_scan, block_on_severity, public_read)
		VALUES (?, 'docker', 'r1', 'hello', 0, 'none', 0),
		       (?, 'raw',    'r2', 'world', 0, 'none', 0)
	`, projectID, projectID); err != nil {
		t.Fatalf("insert repos: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Pre-condition: FTS empty, repos has rows → backfill must fire.
	var ftsCount int64
	_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos_fts`).Scan(&ftsCount)
	if ftsCount != 0 {
		t.Fatalf("pre-condition: repos_fts should be empty, got %d", ftsCount)
	}

	if err := ensureFTSIndexed(ctx, db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if err := db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos_fts`).Scan(&ftsCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if ftsCount != 2 {
		t.Fatalf("repos_fts after backfill: got %d want 2", ftsCount)
	}

	// Search should now return the backfilled rows.
	res, err := db.SearchAll(ctx, metadata.SearchParams{Query: "r1", Kind: "repo", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("search returned 0 results after backfill")
	}

	// Idempotent: running again is a no-op.
	if err := ensureFTSIndexed(ctx, db); err != nil {
		t.Fatalf("backfill second run: %v", err)
	}
	_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos_fts`).Scan(&ftsCount)
	if ftsCount != 2 {
		t.Fatalf("repos_fts after idempotent run: got %d want 2", ftsCount)
	}
}

// TestEnsureFTSIndexed_OrphanRow guards against a naive COUNT-based check:
// when repos_fts has an orphan row that does not match any repo, a
// count-only comparison against repos could be fooled into skipping a
// genuinely missing repo. The LEFT-JOIN backfill must still add the
// missing repo even in this skewed state.
func TestEnsureFTSIndexed_OrphanRow(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()

	tx, err := db.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	var projectID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE name='p1'`).Scan(&projectID); err != nil {
		t.Fatalf("select pid: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repos(project_id, type, name, description_md, auto_scan, block_on_severity, public_read)
		VALUES (?, 'docker', 'r1', '', 0, 'none', 0)
	`, projectID); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	// Insert an orphan fts row pointing at a non-existent rowid so the
	// counts look "balanced" (repos=1, repos_fts=1) even though the real
	// repo is not indexed.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repos_fts(rowid, repo_name, project_name, description, type)
		VALUES (999, 'ghost', 'p1', '', 'docker')
	`); err != nil {
		t.Fatalf("insert orphan fts: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := ensureFTSIndexed(ctx, db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// The real repo (rowid=1) must now be indexed.
	var n int
	if err := db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos_fts WHERE rowid = 1`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("real repo not indexed after backfill with orphan row")
	}
}
