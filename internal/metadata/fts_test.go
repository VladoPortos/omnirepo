package metadata_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestFTS_IndexRepoRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexRepo(ctx, tx, 42, "infra", "dxc", "Primary infrastructure repo", "git")
	}); err != nil {
		t.Fatalf("index: %v", err)
	}
	var rowid int64
	err := db.Reader.QueryRow(
		`SELECT rowid FROM repos_fts WHERE repos_fts MATCH ?`,
		"infra",
	).Scan(&rowid)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if rowid != 42 {
		t.Fatalf("got rowid=%d want 42", rowid)
	}
	// Re-index with new description: DELETE+INSERT semantics should not
	// leave a duplicate.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexRepo(ctx, tx, 42, "infra", "dxc", "Updated description", "git")
	})
	var n int
	_ = db.Reader.QueryRow(`SELECT COUNT(*) FROM repos_fts WHERE rowid = 42`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 row after reindex, got %d", n)
	}
	// Delete.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.DeleteRepoFTS(ctx, tx, 42)
	})
	_ = db.Reader.QueryRow(`SELECT COUNT(*) FROM repos_fts WHERE rowid = 42`).Scan(&n)
	if n != 0 {
		t.Fatalf("expected 0 after delete, got %d", n)
	}
}

func TestFTS_IndexArtifact(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := metadata.IndexArtifact(ctx, tx, 7, "nginx", "1.25.0", "sha256:abc"); err != nil {
			return err
		}
		return metadata.IndexArtifact(ctx, tx, 7, "alpine", "3.20", "sha256:def")
	})

	// Match nginx -> should find exactly one row; repo_id is UNINDEXED so it
	// returns its stored value even on a contentless FTS5 table.
	var repoID int64
	err := db.Reader.QueryRow(
		`SELECT repo_id FROM artifacts_fts WHERE artifacts_fts MATCH ?`, "nginx",
	).Scan(&repoID)
	if err != nil {
		t.Fatalf("match nginx: %v", err)
	}
	if repoID != 7 {
		t.Fatalf("got repo_id=%d want 7", repoID)
	}

	// Delete one and confirm.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexArtifactDelete(ctx, tx, 7, "sha256:abc")
	})
	var n int
	_ = db.Reader.QueryRow(
		`SELECT COUNT(*) FROM artifacts_fts WHERE artifacts_fts MATCH ?`, "nginx",
	).Scan(&n)
	if n != 0 {
		t.Fatalf("expected 0 nginx matches after delete, got %d", n)
	}
}

func TestFTS_IndexVulnerability_SQLInjectionShaped(t *testing.T) {
	// T-02-01-05: parameterized exec; malicious-looking content stays data.
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	evil := `"); DROP TABLE cves_fts; --`
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexVulnerability(ctx, tx, "CVE-2026-0001", "openssl", evil)
	})
	// Search for a safe word inside the payload to avoid FTS5 operator
	// collision on the quoting chars. Contentless FTS5 tables return the
	// cve_id via MATCH (it is a regular, searchable column) but summary
	// back-reads as NULL — we match on the indexed column value instead.
	var cveID string
	err := db.Reader.QueryRow(
		`SELECT cve_id FROM cves_fts WHERE cves_fts MATCH ?`, "DROP",
	).Scan(&cveID)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if cveID != "CVE-2026-0001" {
		t.Fatalf("cve_id=%q want CVE-2026-0001", cveID)
	}
	// Table should still exist.
	var n int
	if err := db.Reader.QueryRow(`SELECT COUNT(*) FROM cves_fts`).Scan(&n); err != nil {
		t.Fatalf("cves_fts table gone? %v", err)
	}
	if n != 1 {
		t.Fatalf("cves_fts count=%d want 1", n)
	}
}

func TestFTS_DeleteVulnerabilitiesByScan_CascadesFTSOnOrphansOnly(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	scans := metadata.NewScansRepo(db)
	vrepo := metadata.NewVulnerabilitiesRepo(db)
	ctx := context.Background()

	var scan1, scan2 int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scan1, err = scans.Enqueue(ctx, tx, 1, "docker", "sha256:a")
		return err
	})
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scan2, err = scans.Enqueue(ctx, tx, 1, "docker", "sha256:b")
		return err
	})

	// Seed vulnerabilities and cves_fts so both scans reference CVE-SHARED
	// but only scan1 references CVE-LONELY.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := vrepo.InsertBatch(ctx, tx, scan1, []metadata.Vuln{
			{CVEID: "CVE-SHARED", Severity: "HIGH", PackageName: "p"},
			{CVEID: "CVE-LONELY", Severity: "LOW", PackageName: "p"},
		}, 0); err != nil {
			return err
		}
		if err := vrepo.InsertBatch(ctx, tx, scan2, []metadata.Vuln{
			{CVEID: "CVE-SHARED", Severity: "HIGH", PackageName: "p"},
		}, 0); err != nil {
			return err
		}
		if err := metadata.IndexVulnerability(ctx, tx, "CVE-SHARED", "p", "shared"); err != nil {
			return err
		}
		return metadata.IndexVulnerability(ctx, tx, "CVE-LONELY", "p", "lonely")
	})

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.DeleteVulnerabilitiesByScan(ctx, tx, scan1)
	})

	// CVE-LONELY should now be absent from cves_fts; CVE-SHARED remains.
	// Contentless FTS5 can only be searched via MATCH, not with =, so use
	// MATCH (cve_id tokens are plain strings).
	var lonely, shared int
	_ = db.Reader.QueryRow(
		`SELECT COUNT(*) FROM cves_fts WHERE cve_id = ?`, "CVE-LONELY",
	).Scan(&lonely)
	_ = db.Reader.QueryRow(
		`SELECT COUNT(*) FROM cves_fts WHERE cve_id = ?`, "CVE-SHARED",
	).Scan(&shared)
	if lonely != 0 {
		t.Fatalf("CVE-LONELY should have been orphan-swept, count=%d", lonely)
	}
	if shared != 1 {
		t.Fatalf("CVE-SHARED should remain, count=%d", shared)
	}
	// And the vulnerabilities rows for scan1 are gone.
	n, _ := vrepo.CountByScan(ctx, scan1)
	if n != 0 {
		t.Fatalf("vulnerabilities rows for scan1 remain: %d", n)
	}
}

// Phase 3 Plan 01 (D-27): per-protocol FTS5 round-trips.

func TestIndexRPM(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexRPM(ctx, tx, 11, "nginx", "1.25.0", "x86_64", "web server")
	}); err != nil {
		t.Fatalf("insert rpm_fts: %v", err)
	}
	var repoID int64
	if err := db.Reader.QueryRow(
		`SELECT repo_id FROM rpm_fts WHERE rpm_fts MATCH ?`, "nginx",
	).Scan(&repoID); err != nil {
		t.Fatalf("match rpm_fts: %v", err)
	}
	if repoID != 11 {
		t.Fatalf("rpm_fts repo_id=%d want 11", repoID)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexRPMDelete(ctx, tx, 11, "nginx", "1.25.0", "x86_64")
	}); err != nil {
		t.Fatalf("delete rpm_fts: %v", err)
	}
	var n int
	_ = db.Reader.QueryRow(`SELECT COUNT(*) FROM rpm_fts`).Scan(&n)
	if n != 0 {
		t.Fatalf("rpm_fts count after delete=%d want 0", n)
	}
}

func TestIndexDEB(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexDEB(ctx, tx, 12, "curl", "7.88", "amd64", "transfer tool")
	}); err != nil {
		t.Fatalf("insert deb_fts: %v", err)
	}
	var repoID int64
	if err := db.Reader.QueryRow(
		`SELECT repo_id FROM deb_fts WHERE deb_fts MATCH ?`, "curl",
	).Scan(&repoID); err != nil {
		t.Fatalf("match deb_fts: %v", err)
	}
	if repoID != 12 {
		t.Fatalf("deb_fts repo_id=%d want 12", repoID)
	}
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexDEBDelete(ctx, tx, 12, "curl", "7.88", "amd64")
	})
	var n int
	_ = db.Reader.QueryRow(`SELECT COUNT(*) FROM deb_fts`).Scan(&n)
	if n != 0 {
		t.Fatalf("deb_fts count after delete=%d want 0", n)
	}
}

func TestIndexPyPI(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexPyPI(ctx, tx, 13, "requests", "2.32.0", ">=3.8", "http client")
	}); err != nil {
		t.Fatalf("insert pypi_fts: %v", err)
	}
	var repoID int64
	if err := db.Reader.QueryRow(
		`SELECT repo_id FROM pypi_fts WHERE pypi_fts MATCH ?`, "requests",
	).Scan(&repoID); err != nil {
		t.Fatalf("match pypi_fts: %v", err)
	}
	if repoID != 13 {
		t.Fatalf("pypi_fts repo_id=%d want 13", repoID)
	}
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexPyPIDelete(ctx, tx, 13, "requests", "2.32.0", ">=3.8")
	})
	var n int
	_ = db.Reader.QueryRow(`SELECT COUNT(*) FROM pypi_fts`).Scan(&n)
	if n != 0 {
		t.Fatalf("pypi_fts count after delete=%d want 0", n)
	}
}

func TestIndexHelm(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexHelm(ctx, tx, 14, "nginx-ingress", "4.9.0", "1.10.0", "ingress controller")
	}); err != nil {
		t.Fatalf("insert helm_fts: %v", err)
	}
	var repoID int64
	if err := db.Reader.QueryRow(
		`SELECT repo_id FROM helm_fts WHERE helm_fts MATCH ?`, "ingress",
	).Scan(&repoID); err != nil {
		t.Fatalf("match helm_fts: %v", err)
	}
	if repoID != 14 {
		t.Fatalf("helm_fts repo_id=%d want 14", repoID)
	}
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexHelmDelete(ctx, tx, 14, "nginx-ingress", "4.9.0", "1.10.0")
	})
	var n int
	_ = db.Reader.QueryRow(`SELECT COUNT(*) FROM helm_fts`).Scan(&n)
	if n != 0 {
		t.Fatalf("helm_fts count after delete=%d want 0", n)
	}
}
