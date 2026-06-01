package migrations_test

import (
	"context"
	"strings"
	"testing"
)

// TestMigration035_AddsReposDriftPurge verifies that after migration 035:
//   - repos has a drift_purge column (INTEGER NOT NULL DEFAULT 0)
//   - inserting a repos row without an explicit drift_purge yields 0
func TestMigration035_AddsReposDriftPurge(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	rows, err := db.Reader.QueryContext(ctx, `PRAGMA table_info(repos)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue, pk any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		if name == "drift_purge" {
			found = true
			if colType != "INTEGER" {
				t.Errorf("repos.drift_purge type = %q, want INTEGER", colType)
			}
			if notNull != 1 {
				t.Errorf("repos.drift_purge notnull = %d, want 1", notNull)
			}
			// dflt_value: SQLite returns "0" (string) for an INTEGER DEFAULT 0
			if s, ok := dfltValue.(string); !ok || s != "0" {
				t.Errorf("repos.drift_purge dflt_value = %v (%T), want \"0\"", dfltValue, dfltValue)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if !found {
		t.Fatal("repos.drift_purge column not found after migration 035")
	}

	// Seed project parent.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(id, name) VALUES (1, 'testproj')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Insert a repos row WITHOUT explicit drift_purge — DEFAULT 0 must apply.
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO repos(project_id, type, name) VALUES (1, 'pypi', 'test')
	`); err != nil {
		t.Fatalf("insert repos: %v", err)
	}

	var dp int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT drift_purge FROM repos WHERE project_id=1 AND name='test'`).Scan(&dp); err != nil {
		t.Fatalf("select drift_purge: %v", err)
	}
	if dp != 0 {
		t.Errorf("repos.drift_purge default = %d, want 0", dp)
	}
}

// TestMigration035_AddsSyncJobsSummary verifies that after migration 035:
//   - sync_jobs has a summary column (TEXT NOT NULL DEFAULT '{}')
//   - inserting a sync_jobs row without an explicit summary yields '{}'
func TestMigration035_AddsSyncJobsSummary(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	rows, err := db.Reader.QueryContext(ctx, `PRAGMA table_info(sync_jobs)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue, pk any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		if name == "summary" {
			found = true
			if colType != "TEXT" {
				t.Errorf("sync_jobs.summary type = %q, want TEXT", colType)
			}
			if notNull != 1 {
				t.Errorf("sync_jobs.summary notnull = %d, want 1", notNull)
			}
			// dflt_value is reported with literal quotes by SQLite for a string
			// DEFAULT: observed as `'{}'` (four chars including the single
			// quotes) or `{}` depending on driver. Accept either by trimming
			// surrounding single quotes.
			if s, ok := dfltValue.(string); !ok || strings.Trim(s, "'") != "{}" {
				t.Errorf("sync_jobs.summary dflt_value = %v (%T), want literal '{}' (trimmed)", dfltValue, dfltValue)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if !found {
		t.Fatal("sync_jobs.summary column not found after migration 035")
	}

	// Seed parents then insert a sync_jobs row without explicit summary.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(id, name) VALUES (1, 'p1')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(id, project_id, type, name) VALUES (1, 1, 'pypi', 'r1')`); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO sync_jobs(kind, project_id, repo_id) VALUES ('pypi_sync', 1, 1)`); err != nil {
		t.Fatalf("insert sync_job: %v", err)
	}

	var summary string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT summary FROM sync_jobs WHERE repo_id=1`).Scan(&summary); err != nil {
		t.Fatalf("select summary: %v", err)
	}
	if summary != "{}" {
		t.Errorf("sync_jobs.summary default = %q, want \"{}\"", summary)
	}
}
