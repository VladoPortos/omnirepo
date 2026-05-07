package migrations_test

import (
	"context"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
)

// TestMigration033_DropsMislabeledPrerelease — F-07.5 backfill.
//
// Seeds pypi_files + pypi_fts with mixed rows: a broken sdist
// (version="rc1" from the LastIndex parser), a canonical sdist, and a
// wheel (never affected — wheel inline parser used SplitN and read
// parts[1]). After re-applying migration 033:
//   - Broken sdist is deleted from both pypi_files and pypi_fts.
//   - Canonical sdist remains.
//   - Wheel remains even if its version is letter-led (the migration
//     only targets kind='sdist' to avoid clobbering PEP 427 pre-releases
//     the old wheel parser correctly stored).
//   - The affected repo has metadata_state='dirty' so the regen
//     coalescer refreshes the PEP 503 simple-index HTML on next request.
func TestMigration033_DropsMislabeledPrerelease(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Seed a project + repo for the FK parent chain.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(id, name) VALUES(1, 'pp')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(id, project_id, type, name, metadata_state)
		 VALUES(1, 1, 'pypi', 'r1', 'clean')`); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	// The three test rows.
	seed := func(kind, version, filename, digest string) {
		t.Helper()
		if _, err := db.Writer.ExecContext(ctx, `
			INSERT INTO pypi_files(repo_id, project_normalized, version, filename, kind, digest)
			VALUES (1, 'widget', ?, ?, ?, ?)`,
			version, filename, kind, digest); err != nil {
			t.Fatalf("seed pypi_files %s: %v", filename, err)
		}
		if _, err := db.Writer.ExecContext(ctx, `
			INSERT INTO pypi_fts(repo_id, name, version, arch_or_runtime, summary)
			VALUES (1, 'widget', ?, '', '')`, version); err != nil {
			t.Fatalf("seed pypi_fts %s: %v", version, err)
		}
	}
	seed("sdist", "rc1", "widget-1.0.0-rc1.tar.gz", "sha256:rc1")         // broken (F-07.5)
	seed("sdist", "1.2.3", "widget-1.2.3.tar.gz", "sha256:canonical")     // canonical
	seed("wheel", "1.0.0", "widget-1.0.0-py3-none-any.whl", "sha256:wh1") // wheel
	// Defensive: a wheel with a non-digit version (hypothetical, legacy).
	// Post-Codex scope tightening: the EXISTS correlation in the FTS
	// delete must NOT touch pypi_fts rows tied to wheels even when they
	// share a letter-led version shape with a deleted sdist.
	seed("wheel", "dev", "widget-dev-py3-none-any.whl", "sha256:wdev")

	// Re-apply 033: wipe the schema_migrations marker so Apply picks it
	// up as pending on the next pass.
	if _, err := db.Writer.ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE name='033_pypi_drop_mislabeled_prerelease'`); err != nil {
		t.Fatalf("wipe marker: %v", err)
	}
	if _, err := migrations.Apply(ctx, db.Writer); err != nil {
		t.Fatalf("reapply 033: %v", err)
	}

	// Assertions on pypi_files.
	var brokenN, canonN, wheelN int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pypi_files WHERE filename='widget-1.0.0-rc1.tar.gz'`).Scan(&brokenN); err != nil {
		t.Fatalf("count broken: %v", err)
	}
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pypi_files WHERE filename='widget-1.2.3.tar.gz'`).Scan(&canonN); err != nil {
		t.Fatalf("count canonical: %v", err)
	}
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pypi_files WHERE filename='widget-1.0.0-py3-none-any.whl'`).Scan(&wheelN); err != nil {
		t.Fatalf("count wheel: %v", err)
	}
	if brokenN != 0 {
		t.Errorf("broken sdist row survived migration 033: count=%d", brokenN)
	}
	if canonN != 1 {
		t.Errorf("canonical sdist row missing: count=%d", canonN)
	}
	if wheelN != 1 {
		t.Errorf("wheel row dropped (kind='wheel' must be preserved): count=%d", wheelN)
	}
	var wheelDevN, ftsWheelDevN int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pypi_files WHERE filename='widget-dev-py3-none-any.whl'`).Scan(&wheelDevN); err != nil {
		t.Fatalf("count wheel-dev: %v", err)
	}
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pypi_fts WHERE version='dev'`).Scan(&ftsWheelDevN); err != nil {
		t.Fatalf("count fts wheel-dev: %v", err)
	}
	if wheelDevN != 1 || ftsWheelDevN != 1 {
		t.Errorf("migration over-deleted wheel with non-digit version: files=%d fts=%d want 1,1",
			wheelDevN, ftsWheelDevN)
	}

	// FTS5 mirror: the broken version's row should be gone, canonical
	// should remain. (The wheel row shares version='1.0.0' with neither.)
	var ftsBroken, ftsCanon int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pypi_fts WHERE version='rc1'`).Scan(&ftsBroken); err != nil {
		t.Fatalf("count fts broken: %v", err)
	}
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pypi_fts WHERE version='1.2.3'`).Scan(&ftsCanon); err != nil {
		t.Fatalf("count fts canon: %v", err)
	}
	if ftsBroken != 0 {
		t.Errorf("broken pypi_fts row survived: count=%d", ftsBroken)
	}
	if ftsCanon != 1 {
		t.Errorf("canonical pypi_fts row missing: count=%d", ftsCanon)
	}

	// Repo dirty flag — regen coalescer refreshes simple-index HTML.
	var state string
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT metadata_state FROM repos WHERE id=1`).Scan(&state); err != nil {
		t.Fatalf("read repo state: %v", err)
	}
	if state != "dirty" {
		t.Errorf("repo metadata_state=%q; want 'dirty' so simple-index refreshes", state)
	}
}
