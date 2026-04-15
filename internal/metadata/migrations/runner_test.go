package migrations_test

import (
	"bytes"
	"context"
	"database/sql"
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
