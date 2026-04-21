package migrations_test

import (
	"bytes"
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
)

func openFreshDB(t *testing.T) *metadata.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := metadata.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func applyReal(t *testing.T, db *metadata.DB) {
	t.Helper()
	if _, err := migrations.Apply(context.Background(), db.Writer); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func TestApplyFreshDB(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	applied, err := migrations.Apply(context.Background(), db.Writer)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) == 0 {
		t.Fatalf("expected at least one migration applied, got zero")
	}
	var n int
	if err := db.Reader.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE name='001_initial'").Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("schema_migrations count for 001_initial = %d, want 1", n)
	}
}

// TestMigration026_LiveOnlyUnique verifies F-7: after migration 026, the
// users / projects / s3_buckets tables enforce uniqueness only against
// live rows (deleted_at IS NULL). Re-creating a login/name after the
// previous holder is soft-deleted must succeed; a collision between two
// LIVE rows must still fail.
func TestMigration026_LiveOnlyUnique(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db.Writer); err != nil {
		t.Fatalf("apply: %v", err)
	}

	mustWrite := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Writer.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("write %q: %v", q, err)
		}
	}
	mustWrite(`INSERT INTO users(login,email,password_hash) VALUES('alice','a@x','hash')`)
	mustWrite(`UPDATE users SET deleted_at=CURRENT_TIMESTAMP WHERE login='alice'`)
	// Must succeed after 026 — fails pre-026 with "UNIQUE constraint failed: users.login".
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(login,email,password_hash) VALUES('alice','a2@x','hash')`); err != nil {
		t.Fatalf("re-create soft-deleted login: %v (F-7 regression)", err)
	}

	// A second LIVE alice must still fail — partial UNIQUE index enforces.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(login,email,password_hash) VALUES('alice','a3@x','hash')`); err == nil {
		t.Fatalf("two LIVE rows with the same login accepted — partial UNIQUE index missing?")
	}

	// Same drill for projects.
	mustWrite(`INSERT INTO projects(name) VALUES('acme')`)
	mustWrite(`UPDATE projects SET deleted_at=CURRENT_TIMESTAMP WHERE name='acme'`)
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES('acme')`); err != nil {
		t.Fatalf("re-create soft-deleted project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES('acme')`); err == nil {
		t.Fatalf("two LIVE projects with the same name accepted")
	}

	// And s3_buckets. Needs a project row for the FK.
	var pid int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE name='acme' AND deleted_at IS NULL`).Scan(&pid); err != nil {
		t.Fatalf("lookup live acme id: %v", err)
	}
	mustWrite(`INSERT INTO s3_buckets(name,project_id) VALUES('cache',?)`, pid)
	mustWrite(`UPDATE s3_buckets SET deleted_at=CURRENT_TIMESTAMP WHERE name='cache'`)
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name,project_id) VALUES('cache',?)`, pid); err != nil {
		t.Fatalf("re-create soft-deleted bucket: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name,project_id) VALUES('cache',?)`, pid); err == nil {
		t.Fatalf("two LIVE buckets with the same name accepted")
	}

	// And repos — migration 027 makes UNIQUE(project_id,type,name) live-only.
	mustWrite(`INSERT INTO repos(project_id,type,name) VALUES(?,'docker','images')`, pid)
	mustWrite(`UPDATE repos SET deleted_at=CURRENT_TIMESTAMP WHERE project_id=? AND type='docker' AND name='images'`, pid)
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES(?,'docker','images')`, pid); err != nil {
		t.Fatalf("re-create soft-deleted repo: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES(?,'docker','images')`, pid); err == nil {
		t.Fatalf("two LIVE repos with the same (project,type,name) accepted")
	}
}

// TestRunner_DisablesForeignKeysForTableRebuild verifies the runner flips
// PRAGMA foreign_keys=OFF around each migration so a classic DROP → RENAME
// table-rebuild commits without tripping the FK integrity check. Without
// that flip (or with only defer_foreign_keys=ON), the rebuild errors out at
// COMMIT with a generic "FOREIGN KEY constraint failed". And after COMMIT
// the runner must leave foreign_keys=ON so regular writes still enforce FKs.
func TestRunner_DisablesForeignKeysForTableRebuild(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()

	setup := fstest.MapFS{
		"001_seed.up.sql": {Data: []byte(`
			CREATE TABLE parents(id INTEGER PRIMARY KEY, name TEXT NOT NULL);
			CREATE TABLE kids(id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parents(id) ON DELETE CASCADE);
			INSERT INTO parents(id,name) VALUES(1,'a'),(2,'b');
			INSERT INTO kids(id,parent_id) VALUES(10,1),(11,2);
		`)},
		"002_rebuild.up.sql": {Data: []byte(`
			CREATE TABLE parents_new(id INTEGER PRIMARY KEY, name TEXT NOT NULL);
			INSERT INTO parents_new SELECT id,name FROM parents;
			DROP TABLE parents;
			ALTER TABLE parents_new RENAME TO parents;
		`)},
	}
	if _, err := migrations.ApplyFSForTest(ctx, db.Writer, setup); err != nil {
		t.Fatalf("apply rebuild migrations: %v (runner must flip foreign_keys=OFF)", err)
	}

	// FK must be re-enabled after commit — an ON DELETE CASCADE should fire.
	if _, err := db.Writer.ExecContext(ctx, `DELETE FROM parents WHERE id=1`); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	var n int
	if err := db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM kids WHERE parent_id=1`).Scan(&n); err != nil {
		t.Fatalf("count kids: %v", err)
	}
	if n != 0 {
		t.Fatalf("ON DELETE CASCADE didn't fire → foreign_keys wasn't re-enabled after migration (kids remaining: %d)", n)
	}
}

// TestRunner_ForeignKeyCheckCatchesBadInserts verifies the runner runs
// PRAGMA foreign_key_check before Commit and rolls back the migration if
// rows with missing parents slipped through while FKs were disabled.
// Without this check, a migration that INSERTed a child with no parent
// would commit silently and the next write would hit a confusing CASCADE
// error far from the root cause.
func TestRunner_ForeignKeyCheckCatchesBadInserts(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()

	fsys := fstest.MapFS{
		"001_seed.up.sql": {Data: []byte(`
			CREATE TABLE parents(id INTEGER PRIMARY KEY);
			CREATE TABLE kids(id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parents(id));
		`)},
		// Insert a kid whose parent_id has no matching row. FKs are OFF
		// during the body, so INSERT would ordinarily commit — the runner's
		// pre-commit foreign_key_check must catch it and fail the tx.
		"002_bad.up.sql": {Data: []byte(`
			INSERT INTO kids(id,parent_id) VALUES(1, 999);
		`)},
	}
	_, err := migrations.ApplyFSForTest(ctx, db.Writer, fsys)
	if err == nil {
		t.Fatalf("expected foreign_key_check to surface the orphaned child, got nil")
	}
	if !strings.Contains(err.Error(), "foreign_key_check failed") {
		t.Fatalf("error should mention foreign_key_check, got: %v", err)
	}
	// 002_bad must NOT be recorded — schema_migrations row is INSERTed
	// after the FK check passes, so a rolled-back tx leaves no trace and
	// a re-run of Apply would retry the migration.
	var n int
	if err := db.Reader.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name='002_bad'`).Scan(&n); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if n != 0 {
		t.Fatalf("002_bad recorded in schema_migrations despite FK failure — rollback didn't fire")
	}
}

func TestApplyIdempotent(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db.Writer); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second, err := migrations.Apply(ctx, db.Writer)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second apply should be no-op, applied %v", second)
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()

	a, p, err := migrations.Status(ctx, db.Reader)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(a) != 0 {
		t.Fatalf("expected applied empty before apply, got %v", a)
	}
	found := false
	for _, stem := range p {
		if stem == "001_initial" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 001_initial pending, got %v", p)
	}

	if _, err := migrations.Apply(ctx, db.Writer); err != nil {
		t.Fatalf("apply: %v", err)
	}
	a, p, err = migrations.Status(ctx, db.Reader)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	found = false
	for _, stem := range a {
		if stem == "001_initial" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 001_initial applied, got %v", a)
	}
	if len(p) != 0 {
		t.Fatalf("expected pending empty after full apply, got %v", p)
	}
}

func TestBrokenMigrationRollsBack(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()

	fsys := fstest.MapFS{
		"001_ok.up.sql":     {Data: []byte(`CREATE TABLE ok1(id INTEGER);`)},
		"002_broken.up.sql": {Data: []byte(`THIS IS NOT SQL;`)},
	}
	_, err := migrations.ApplyFSForTest(ctx, db.Writer, fsys)
	if err == nil {
		t.Fatalf("expected broken migration to fail")
	}
	if !strings.Contains(err.Error(), "002_broken") {
		t.Fatalf("error should name 002_broken, got: %v", err)
	}

	var count int
	if err := db.Reader.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE name='001_ok'").Scan(&count); err != nil {
		t.Fatalf("query 001_ok: %v", err)
	}
	if count != 1 {
		t.Fatalf("001_ok count=%d want 1", count)
	}
	if err := db.Reader.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE name='002_broken'").Scan(&count); err != nil {
		t.Fatalf("query 002_broken: %v", err)
	}
	if count != 0 {
		t.Fatalf("002_broken should not be recorded, count=%d", count)
	}
}

func TestFTS5_TablesAcceptInserts(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO repos_fts(repo_name, project_name, description, type) VALUES (?,?,?,?)`,
			"infra", "dxc", "Infrastructure repo", "git"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts_fts(repo_id, name, version, digest) VALUES (?,?,?,?)`,
			1, "nginx", "1.25.0", "sha256:abc"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cves_fts(cve_id, package, summary) VALUES (?,?,?)`,
			"CVE-2026-0001", "openssl", "remote code exec"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fts5 inserts: %v", err)
	}
}

func TestReposUniqueWithinProjectType(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)
	if _, err := db.Writer.ExecContext(ctx, "INSERT INTO projects(name) VALUES ('p1')"); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, "INSERT INTO repos(project_id,type,name) VALUES (1,'rpm','r1')"); err != nil {
		t.Fatalf("first repo: %v", err)
	}
	_, err := db.Writer.ExecContext(ctx, "INSERT INTO repos(project_id,type,name) VALUES (1,'rpm','r1')")
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("expected UNIQUE violation, got %v", err)
	}
}

func TestReposTypeCheckConstraint(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)
	if _, err := db.Writer.ExecContext(ctx, "INSERT INTO projects(name) VALUES ('p1')"); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	_, err := db.Writer.ExecContext(ctx, "INSERT INTO repos(project_id,type,name) VALUES (1,'bogus','x')")
	if err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("expected CHECK violation, got %v", err)
	}
}

func TestS3BucketsGloballyUnique(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)
	if _, err := db.Writer.ExecContext(ctx, "INSERT INTO projects(name) VALUES ('p1')"); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, "INSERT INTO s3_buckets(name,project_id) VALUES ('shared',1)"); err != nil {
		t.Fatalf("first bucket: %v", err)
	}
	_, err := db.Writer.ExecContext(ctx, "INSERT INTO s3_buckets(name,project_id) VALUES ('shared',1)")
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("expected UNIQUE violation, got %v", err)
	}
}

func TestPhase2MigrationsApply(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	applyReal(t, db)
	want := []string{
		"sync_jobs", "scans", "vulnerabilities",
		"docker_blobs", "docker_manifests", "docker_tags", "blob_upload_sessions",
		"upstream_creds",
	}
	for _, table := range want {
		var name string
		err := db.Reader.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
	// schema_migrations advanced to 004.
	for _, stem := range []string{"002_jobs", "003_oci", "004_upstream_creds"} {
		var n int
		if err := db.Reader.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE name=?`, stem,
		).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", stem, err)
		}
		if n != 1 {
			t.Fatalf("schema_migrations count for %s = %d, want 1", stem, n)
		}
	}
}

func TestSyncJobsLeaseReturning(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	applyReal(t, db)
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO sync_jobs(kind, status) VALUES('test','pending')`,
	); err != nil {
		t.Fatal(err)
	}
	var id int64
	err := db.Writer.QueryRowContext(ctx, `
		UPDATE sync_jobs
		SET status='running'
		WHERE id = (SELECT id FROM sync_jobs WHERE status='pending' LIMIT 1)
		RETURNING id
	`).Scan(&id)
	if err != nil {
		t.Fatalf("RETURNING: %v", err)
	}
	if id == 0 {
		t.Fatal("no id returned from RETURNING")
	}
}

func TestManifestBodyByteIdentical(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	applyReal(t, db)
	ctx := context.Background()
	// Seed project + repo so docker_manifests FK is satisfied.
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'docker','r1')`,
	); err != nil {
		t.Fatalf("repo: %v", err)
	}
	// Body includes a trailing newline and CR byte that JSON re-encoding would drop.
	body := []byte("{\"schemaVersion\":2,\"mediaType\":\"x\",\"trailing\":\"\\n\"}\r\n")
	if _, err := db.Writer.ExecContext(ctx, `
		INSERT INTO docker_manifests(repo_id, digest, media_type, body, size_bytes)
		VALUES (?, ?, ?, ?, ?)
	`, 1, "sha256:abc", "application/vnd.oci.image.manifest.v1+json", body, len(body)); err != nil {
		t.Fatalf("insert manifest: %v", err)
	}
	var got []byte
	if err := db.Reader.QueryRow(
		`SELECT body FROM docker_manifests WHERE repo_id=1 AND digest=?`, "sha256:abc",
	).Scan(&got); err != nil {
		t.Fatalf("select body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body not byte-identical:\n got=%q\nwant=%q", got, body)
	}
}

// Phase 3 Plan 01 — Migrations 008..015.

func TestPhase3MigrationsForwardAndDown(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Forward: every table exists and each migration is recorded.
	forward := []struct {
		stem  string
		table string
	}{
		{"008_signing_keys", "signing_keys"},
		{"009_apt_suites", "apt_suites"},
		{"010_rpm_packages", "rpm_packages"},
		{"011_deb_packages", "deb_packages"},
		{"012_pypi_files", "pypi_files"},
		{"013_helm_charts", "helm_charts"},
		{"014_protocol_fts", "rpm_fts"}, // one of four virtual tables
	}
	for _, tc := range forward {
		var name string
		if err := db.Reader.QueryRow(
			`SELECT name FROM sqlite_master WHERE name=?`, tc.table,
		).Scan(&name); err != nil {
			t.Fatalf("table/vtab %s missing after forward apply: %v", tc.table, err)
		}
		var n int
		if err := db.Reader.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE name=?`, tc.stem,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("schema_migrations %s: n=%d err=%v", tc.stem, n, err)
		}
	}
	// 015 adds two columns to repos.
	for _, col := range []string{"metadata_state", "last_regen_error"} {
		var n int
		if err := db.Reader.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('repos') WHERE name=?`, col,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("column repos.%s missing after 015: n=%d err=%v", col, n, err)
		}
	}
	var stem015 int
	if err := db.Reader.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE name='015_repo_metadata_state'`,
	).Scan(&stem015); err != nil || stem015 != 1 {
		t.Fatalf("schema_migrations 015_repo_metadata_state: n=%d err=%v", stem015, err)
	}

	// Reverse: apply each .down.sql in reverse order (015 first) and verify
	// the schema is scrubbed. The runner itself has no revert-path (D-11
	// says down scripts exist for audit only), so we exec them directly.
	downOrder := []string{
		"015_repo_metadata_state.down.sql",
		"014_protocol_fts.down.sql",
		"013_helm_charts.down.sql",
		"012_pypi_files.down.sql",
		"011_deb_packages.down.sql",
		"010_rpm_packages.down.sql",
		"009_apt_suites.down.sql",
		"008_signing_keys.down.sql",
	}
	for _, f := range downOrder {
		b, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Writer.ExecContext(ctx, string(b)); err != nil {
			t.Fatalf("exec %s: %v", f, err)
		}
	}
	// Every phase-3 table must now be gone.
	for _, tab := range []string{
		"signing_keys", "apt_suites", "rpm_packages", "deb_packages",
		"pypi_files", "helm_charts", "rpm_fts", "deb_fts", "pypi_fts", "helm_fts",
	} {
		var n int
		_ = db.Reader.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, tab,
		).Scan(&n)
		if n != 0 {
			t.Fatalf("%s still present after down: n=%d", tab, n)
		}
	}
	// metadata_state column must be gone.
	var cnt int
	if err := db.Reader.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('repos') WHERE name='metadata_state'`,
	).Scan(&cnt); err != nil || cnt != 0 {
		t.Fatalf("repos.metadata_state still present after 015 down: cnt=%d err=%v", cnt, err)
	}
}

// Phase 4 Plan 02 — Migrations 016..019 (S3 access keys, git extensions,
// S3 objects, S3 multipart).

func TestPhase4MigrationsForwardAndDown(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)

	// Forward: every new table exists and each migration is recorded.
	forward := []struct {
		stem  string
		table string
	}{
		{"016_s3_access_keys", "s3_access_keys"},
		{"017_git_extensions", "git_refs"},
		{"018_s3_objects", "s3_objects"},
		{"019_s3_multipart", "s3_multipart_uploads"},
		{"019_s3_multipart", "s3_multipart_parts"},
	}
	for _, tc := range forward {
		var name string
		if err := db.Reader.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tc.table,
		).Scan(&name); err != nil {
			t.Fatalf("table %s missing after forward apply: %v", tc.table, err)
		}
		var n int
		if err := db.Reader.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE name=?`, tc.stem,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("schema_migrations %s: n=%d err=%v", tc.stem, n, err)
		}
	}

	// 017 adds git_max_push_bytes column to repos.
	var colCount int
	if err := db.Reader.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('repos') WHERE name='git_max_push_bytes'`,
	).Scan(&colCount); err != nil || colCount != 1 {
		t.Fatalf("column repos.git_max_push_bytes missing after 017: n=%d err=%v", colCount, err)
	}
	// Verify it is nullable (no NOT NULL). pragma_table_info.notnull is 0 for NULLable columns.
	var notnull int
	if err := db.Reader.QueryRow(
		`SELECT "notnull" FROM pragma_table_info('repos') WHERE name='git_max_push_bytes'`,
	).Scan(&notnull); err != nil {
		t.Fatalf("query git_max_push_bytes notnull: %v", err)
	}
	if notnull != 0 {
		t.Fatalf("git_max_push_bytes should be NULLable (notnull=0), got notnull=%d", notnull)
	}
	// Verify the column type is INTEGER.
	var colType string
	if err := db.Reader.QueryRow(
		`SELECT type FROM pragma_table_info('repos') WHERE name='git_max_push_bytes'`,
	).Scan(&colType); err != nil {
		t.Fatalf("query git_max_push_bytes type: %v", err)
	}
	if !strings.EqualFold(colType, "INTEGER") {
		t.Fatalf("git_max_push_bytes type = %q, want INTEGER", colType)
	}

	// Inserting NULL into git_max_push_bytes must succeed.
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p4a')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name,git_max_push_bytes) VALUES (?,?,?,NULL)`,
		1, "git", "r-push-null",
	); err != nil {
		t.Fatalf("insert repo with NULL git_max_push_bytes: %v", err)
	}

	// Partial index on s3_access_keys.project_id WHERE revoked_at IS NULL.
	var idxSQL string
	if err := db.Reader.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_s3_access_keys_project'`,
	).Scan(&idxSQL); err != nil {
		t.Fatalf("query idx_s3_access_keys_project: %v", err)
	}
	if !strings.Contains(idxSQL, "WHERE revoked_at IS NULL") {
		t.Fatalf("idx_s3_access_keys_project missing partial predicate: %q", idxSQL)
	}

	// git_refs CHECK constraint must reject bogus `type`.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'git','gr1')`,
	); err != nil {
		t.Fatalf("seed git repo: %v", err)
	}
	var gitRepoID int64
	if err := db.Reader.QueryRow(
		`SELECT id FROM repos WHERE name='gr1'`,
	).Scan(&gitRepoID); err != nil {
		t.Fatalf("find git repo: %v", err)
	}
	_, err := db.Writer.ExecContext(ctx,
		`INSERT INTO git_refs(repo_id,name,target,type) VALUES (?,?,?,?)`,
		gitRepoID, "refs/heads/main", "abc123", "bogus",
	)
	if err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("expected CHECK violation on git_refs.type='bogus', got %v", err)
	}
	// Valid types must insert cleanly.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO git_refs(repo_id,name,target,type) VALUES (?,?,?,?)`,
		gitRepoID, "refs/heads/main", "abc123", "branch",
	); err != nil {
		t.Fatalf("insert valid git_ref: %v", err)
	}
	// UNIQUE(repo_id,name) on git_refs.
	_, err = db.Writer.ExecContext(ctx,
		`INSERT INTO git_refs(repo_id,name,target,type) VALUES (?,?,?,?)`,
		gitRepoID, "refs/heads/main", "def456", "branch",
	)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("expected UNIQUE violation on git_refs(repo_id,name), got %v", err)
	}

	// s3_objects UNIQUE(bucket_id,key) — seed bucket, then duplicate.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name,project_id) VALUES ('b1',1)`,
	); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	var bucketID int64
	if err := db.Reader.QueryRow(`SELECT id FROM s3_buckets WHERE name='b1'`).Scan(&bucketID); err != nil {
		t.Fatalf("find bucket: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_objects(bucket_id,key,size_bytes,etag,sha256) VALUES (?,?,?,?,?)`,
		bucketID, "foo/bar", 7, "etag-1", "sha256:aa",
	); err != nil {
		t.Fatalf("insert s3_object: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx,
		`INSERT INTO s3_objects(bucket_id,key,size_bytes,etag,sha256) VALUES (?,?,?,?,?)`,
		bucketID, "foo/bar", 8, "etag-2", "sha256:bb",
	)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("expected UNIQUE violation on s3_objects(bucket_id,key), got %v", err)
	}

	// s3_access_keys UNIQUE(access_key_id) — seed a user so FK holds.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(login,email,password_hash) VALUES ('alice','a@b.c','x')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var userID int64
	if err := db.Reader.QueryRow(`SELECT id FROM users WHERE login='alice'`).Scan(&userID); err != nil {
		t.Fatalf("find user: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_access_keys(project_id,access_key_id,secret_enc,label,created_by_user_id) VALUES (?,?,?,?,?)`,
		1, "AKIA-test-01", []byte("enc"), "lbl", userID,
	); err != nil {
		t.Fatalf("insert s3 access key: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx,
		`INSERT INTO s3_access_keys(project_id,access_key_id,secret_enc,label,created_by_user_id) VALUES (?,?,?,?,?)`,
		1, "AKIA-test-01", []byte("enc"), "lbl2", userID,
	)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("expected UNIQUE violation on s3_access_keys.access_key_id, got %v", err)
	}

	// s3_multipart_uploads UNIQUE(upload_id).
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_multipart_uploads(upload_id,bucket_id,key,initiated_by_user_id) VALUES (?,?,?,?)`,
		"uid-1", bucketID, "mp/obj", userID,
	); err != nil {
		t.Fatalf("insert multipart upload: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx,
		`INSERT INTO s3_multipart_uploads(upload_id,bucket_id,key,initiated_by_user_id) VALUES (?,?,?,?)`,
		"uid-1", bucketID, "mp/obj2", userID,
	)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("expected UNIQUE violation on s3_multipart_uploads.upload_id, got %v", err)
	}

	// s3_multipart_parts UNIQUE(upload_id,part_number).
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_multipart_parts(upload_id,part_number,size_bytes,md5) VALUES (?,?,?,?)`,
		"uid-1", 1, 100, "md5a",
	); err != nil {
		t.Fatalf("insert multipart part: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx,
		`INSERT INTO s3_multipart_parts(upload_id,part_number,size_bytes,md5) VALUES (?,?,?,?)`,
		"uid-1", 1, 100, "md5b",
	)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("expected UNIQUE violation on s3_multipart_parts(upload_id,part_number), got %v", err)
	}

	// Reverse: apply each .down.sql in reverse order (019 first) and verify
	// the schema is scrubbed. Runner has no revert path (D-11), so exec directly.
	downOrder := []string{
		"019_s3_multipart.down.sql",
		"018_s3_objects.down.sql",
		"017_git_extensions.down.sql",
		"016_s3_access_keys.down.sql",
	}
	for _, f := range downOrder {
		b, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := db.Writer.ExecContext(ctx, string(b)); err != nil {
			t.Fatalf("exec %s: %v", f, err)
		}
	}
	// Every phase-4 table must now be gone.
	for _, tab := range []string{
		"s3_access_keys", "git_refs", "s3_objects",
		"s3_multipart_uploads", "s3_multipart_parts",
	} {
		var n int
		_ = db.Reader.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, tab,
		).Scan(&n)
		if n != 0 {
			t.Fatalf("%s still present after down: n=%d", tab, n)
		}
	}
	// git_max_push_bytes column must be gone.
	var cnt int
	if err := db.Reader.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('repos') WHERE name='git_max_push_bytes'`,
	).Scan(&cnt); err != nil || cnt != 0 {
		t.Fatalf("repos.git_max_push_bytes still present after 017 down: cnt=%d err=%v", cnt, err)
	}
}

func TestReposSizeBytesColumnExists(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	ctx := context.Background()
	applyReal(t, db)
	if _, err := db.Writer.ExecContext(ctx, "INSERT INTO projects(name) VALUES ('p1')"); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, "INSERT INTO repos(project_id,type,name) VALUES (1,'raw','r1')"); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	var size int64
	if err := db.Reader.QueryRow("SELECT size_bytes FROM repos WHERE name='r1'").Scan(&size); err != nil {
		t.Fatalf("select size_bytes: %v", err)
	}
	if size != 0 {
		t.Fatalf("default size_bytes = %d, want 0", size)
	}
}
