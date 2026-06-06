package metadata_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestFTS_IndexRepoRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexRepo(ctx, tx, 42, "infra", "acme", "Primary infrastructure repo", "git")
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
		return metadata.IndexRepo(ctx, tx, 42, "infra", "acme", "Updated description", "git")
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
	// Parameterized exec; malicious-looking content stays data.
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
	if n := countVulnRows(t, db, scan1); n != 0 {
		t.Fatalf("vulnerabilities rows for scan1 remain: %d", n)
	}
}

// Per-protocol FTS5 round-trips.

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

// -----------------------------------------------------------------------------
// PruneRepoFTS + FTSReindexer tests.
// -----------------------------------------------------------------------------

// seedFTSPerProtocolRowsForRepo writes one row of every per-protocol FTS table
// for repoID. Each test that exercises PruneRepoFTS uses this to assert
// targeted-only deletion.
func seedFTSPerProtocolRowsForRepo(t *testing.T, db *metadata.DB, repoID int64, repoName, projectName string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := metadata.IndexRepo(ctx, tx, repoID, repoName, projectName, "test repo", "rpm"); err != nil {
			return err
		}
		if err := metadata.IndexArtifact(ctx, tx, repoID, "art-"+repoName+"-1", "1.0", "sha256:art1-r"+repoName); err != nil {
			return err
		}
		if err := metadata.IndexArtifact(ctx, tx, repoID, "art-"+repoName+"-2", "2.0", "sha256:art2-r"+repoName); err != nil {
			return err
		}
		if err := metadata.IndexRPM(ctx, tx, repoID, "pkg-"+repoName, "1.0", "x86_64", "summary"); err != nil {
			return err
		}
		if err := metadata.IndexDEB(ctx, tx, repoID, "pkg-"+repoName, "1.0", "amd64", "summary"); err != nil {
			return err
		}
		if err := metadata.IndexPyPI(ctx, tx, repoID, "pkg-"+repoName, "1.0", ">=3.8", ""); err != nil {
			return err
		}
		return metadata.IndexHelm(ctx, tx, repoID, "pkg-"+repoName, "1.0", "1.0", "summary")
	}); err != nil {
		t.Fatalf("seed FTS for repo=%d: %v", repoID, err)
	}
}

// seedRepoLive inserts a minimal projects + repos row pair so PruneRepoFTS's
// cves_fts NOT_IN-live-repo subquery has real data to test against. Returns
// the resulting repoID.
func seedRepoLive(t *testing.T, db *metadata.DB, projectName, repoName string) int64 {
	t.Helper()
	ctx := context.Background()
	var pid, rid int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO projects(name) VALUES (?)`, projectName)
		if err != nil {
			return err
		}
		pid, _ = res.LastInsertId()
		res, err = tx.ExecContext(ctx,
			`INSERT INTO repos(project_id,type,name) VALUES (?,?,?)`, pid, "rpm", repoName)
		if err != nil {
			return err
		}
		rid, _ = res.LastInsertId()
		return nil
	}); err != nil {
		t.Fatalf("seedRepoLive: %v", err)
	}
	return rid
}

func TestPruneRepoFTS_RemovesAllPerProtocolRows(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedRepoLive(t, db, "p42", "r42")
	seedFTSPerProtocolRowsForRepo(t, db, repoID, "r42", "p42")

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.PruneRepoFTS(ctx, tx, repoID)
	}); err != nil {
		t.Fatalf("PruneRepoFTS: %v", err)
	}

	for _, q := range []struct {
		name, sql string
	}{
		{"repos_fts", `SELECT COUNT(*) FROM repos_fts WHERE rowid=?`},
		{"artifacts_fts", `SELECT COUNT(*) FROM artifacts_fts WHERE repo_id=?`},
		{"rpm_fts", `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`},
		{"deb_fts", `SELECT COUNT(*) FROM deb_fts WHERE repo_id=?`},
		{"pypi_fts", `SELECT COUNT(*) FROM pypi_fts WHERE repo_id=?`},
		{"helm_fts", `SELECT COUNT(*) FROM helm_fts WHERE repo_id=?`},
	} {
		var n int
		if err := db.Reader.QueryRowContext(ctx, q.sql, repoID).Scan(&n); err != nil {
			t.Fatalf("%s count: %v", q.name, err)
		}
		if n != 0 {
			t.Errorf("%s after prune count=%d want 0", q.name, n)
		}
	}
}

func TestPruneRepoFTS_DoesNotTouchOtherRepos(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	r42 := seedRepoLive(t, db, "p42", "r42")
	r99 := seedRepoLive(t, db, "p99", "r99")
	seedFTSPerProtocolRowsForRepo(t, db, r42, "r42", "p42")
	seedFTSPerProtocolRowsForRepo(t, db, r99, "r99", "p99")

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.PruneRepoFTS(ctx, tx, r42)
	}); err != nil {
		t.Fatalf("PruneRepoFTS: %v", err)
	}

	for _, q := range []struct {
		name, sql string
	}{
		{"repos_fts", `SELECT COUNT(*) FROM repos_fts WHERE rowid=?`},
		{"artifacts_fts", `SELECT COUNT(*) FROM artifacts_fts WHERE repo_id=?`},
		{"rpm_fts", `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`},
		{"deb_fts", `SELECT COUNT(*) FROM deb_fts WHERE repo_id=?`},
		{"pypi_fts", `SELECT COUNT(*) FROM pypi_fts WHERE repo_id=?`},
		{"helm_fts", `SELECT COUNT(*) FROM helm_fts WHERE repo_id=?`},
	} {
		var n int
		if err := db.Reader.QueryRowContext(ctx, q.sql, r99).Scan(&n); err != nil {
			t.Fatalf("%s count: %v", q.name, err)
		}
		// repos_fts has 1 row (rowid=r99); per-protocol artifact/protocol arms
		// vary — repos_fts/rpm/deb/pypi/helm = 1 each; artifacts = 2.
		want := 1
		if q.name == "artifacts_fts" {
			want = 2
		}
		if n != want {
			t.Errorf("%s for r99 count=%d want %d (other repo must survive)", q.name, n, want)
		}
	}
}

func TestPruneRepoFTS_Idempotent(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedRepoLive(t, db, "p42", "r42")
	seedFTSPerProtocolRowsForRepo(t, db, repoID, "r42", "p42")

	for i := 0; i < 2; i++ {
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			return metadata.PruneRepoFTS(ctx, tx, repoID)
		}); err != nil {
			t.Fatalf("PruneRepoFTS pass %d: %v", i, err)
		}
	}
	// All FTS5 tables should remain empty for this repo (no double-error).
	var n int
	_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, repoID).Scan(&n)
	if n != 0 {
		t.Fatalf("rpm_fts after 2 prunes: %d want 0", n)
	}
}

// TestPruneRepoFTS_KeepsSharedCVEButDropsExclusiveCVE — shared-CVE invariant.
// Two repos repoA (id=42) and repoB (id=43). One scan each. Vulnerabilities:
//
//	scanA → CVE-A (exclusive to repoA), CVE-B (shared), CVE-C (exclusive to repoA)
//	scanB → CVE-B (shared)
//
// PruneRepoFTS(repoA) must keep CVE-B (still owned by repoB live), drop CVE-A
// and CVE-C. cves_fts ends with 1 row.
func TestPruneRepoFTS_KeepsSharedCVEButDropsExclusiveCVE(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	repoA := seedRepoLive(t, db, "projA", "repoA")
	repoB := seedRepoLive(t, db, "projB", "repoB")

	scans := metadata.NewScansRepo(db)
	vulns := metadata.NewVulnerabilitiesRepo(db)

	var scanA, scanB int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scanA, err = scans.Enqueue(ctx, tx, repoA, "rpm", "artA")
		return err
	}); err != nil {
		t.Fatalf("enqueue scanA: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scanB, err = scans.Enqueue(ctx, tx, repoB, "rpm", "artB")
		return err
	}); err != nil {
		t.Fatalf("enqueue scanB: %v", err)
	}

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := vulns.InsertBatch(ctx, tx, scanA, []metadata.Vuln{
			{CVEID: "CVE-A", Severity: "HIGH", PackageName: "p"},
			{CVEID: "CVE-B", Severity: "HIGH", PackageName: "p"},
			{CVEID: "CVE-C", Severity: "LOW", PackageName: "p"},
		}, 0); err != nil {
			return err
		}
		if err := vulns.InsertBatch(ctx, tx, scanB, []metadata.Vuln{
			{CVEID: "CVE-B", Severity: "HIGH", PackageName: "p"},
		}, 0); err != nil {
			return err
		}
		if err := metadata.IndexVulnerability(ctx, tx, "CVE-A", "p", "exclusive to A"); err != nil {
			return err
		}
		if err := metadata.IndexVulnerability(ctx, tx, "CVE-B", "p", "shared A+B"); err != nil {
			return err
		}
		return metadata.IndexVulnerability(ctx, tx, "CVE-C", "p", "exclusive to A")
	}); err != nil {
		t.Fatalf("seed vulns + cves_fts: %v", err)
	}

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.PruneRepoFTS(ctx, tx, repoA)
	}); err != nil {
		t.Fatalf("PruneRepoFTS(repoA): %v", err)
	}

	// CVE-A: gone. CVE-B: survives. CVE-C: gone.
	for _, c := range []struct {
		cve  string
		want int
	}{
		{"CVE-A", 0},
		{"CVE-B", 1},
		{"CVE-C", 0},
	} {
		var n int
		if err := db.Reader.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM cves_fts WHERE cve_id = ?`, c.cve,
		).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", c.cve, err)
		}
		if n != c.want {
			t.Errorf("%s count=%d want %d", c.cve, n, c.want)
		}
	}

	// Total cves_fts row count should be exactly 1.
	var total int
	_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM cves_fts`).Scan(&total)
	if total != 1 {
		t.Errorf("total cves_fts after prune=%d want 1", total)
	}
}

// TestPruneRepoFTS_DropsCVESharedOnlyAcrossSoftDeletedRepos — soft-deleted-only case.
// Three repos: A (live → about to soft-delete), B (already soft-deleted), C
// (live, no scans). CVE-X is referenced by scanA and scanB only — both their
// owning repos are/become soft-deleted. After PruneRepoFTS(repoA), CVE-X has
// zero live owners and must be removed from cves_fts. Guards the production
// SQL's `r2.deleted_at IS NULL` clause in the NOT-IN-live-owners subquery.
func TestPruneRepoFTS_DropsCVESharedOnlyAcrossSoftDeletedRepos(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	repoA := seedRepoLive(t, db, "projA2", "repoA2")
	repoB := seedRepoLive(t, db, "projB2", "repoB2")

	scans := metadata.NewScansRepo(db)
	vulns := metadata.NewVulnerabilitiesRepo(db)
	repos := metadata.NewReposRepo(db)

	var scanA, scanB int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scanA, err = scans.Enqueue(ctx, tx, repoA, "rpm", "artA")
		return err
	}); err != nil {
		t.Fatalf("enqueue scanA: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scanB, err = scans.Enqueue(ctx, tx, repoB, "rpm", "artB")
		return err
	}); err != nil {
		t.Fatalf("enqueue scanB: %v", err)
	}

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := vulns.InsertBatch(ctx, tx, scanA, []metadata.Vuln{
			{CVEID: "CVE-X", Severity: "HIGH", PackageName: "p"},
		}, 0); err != nil {
			return err
		}
		if err := vulns.InsertBatch(ctx, tx, scanB, []metadata.Vuln{
			{CVEID: "CVE-X", Severity: "HIGH", PackageName: "p"},
		}, 0); err != nil {
			return err
		}
		return metadata.IndexVulnerability(ctx, tx, "CVE-X", "p", "shared only across deleted repos")
	}); err != nil {
		t.Fatalf("seed vulns + cves_fts: %v", err)
	}

	// Soft-delete repoB FIRST, so when PruneRepoFTS(repoA) runs, the only other
	// repo holding CVE-X is already soft-deleted (no live owner remains).
	if err := repos.SoftDelete(ctx, repoB); err != nil {
		t.Fatalf("soft-delete repoB: %v", err)
	}

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.PruneRepoFTS(ctx, tx, repoA)
	}); err != nil {
		t.Fatalf("PruneRepoFTS(repoA): %v", err)
	}

	var n int
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cves_fts WHERE cve_id = ?`, "CVE-X",
	).Scan(&n); err != nil {
		t.Fatalf("count CVE-X: %v", err)
	}
	if n != 0 {
		t.Errorf("CVE-X count=%d want 0 (no live owner remains across A and B)", n)
	}
}

func TestFTSReindexer_FromBaseTables(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedRepoLive(t, db, "rxp", "rxr")

	rpmRepo := metadata.NewRPMPackagesRepo(db)
	debRepo := metadata.NewDEBPackagesRepo(db)
	pypiRepo := metadata.NewPyPIFilesRepo(db)
	helmRepo := metadata.NewHelmChartsRepo(db)
	rawRepo := metadata.NewRawFilesRepo(db)
	manifests := metadata.NewDockerManifestsRepo(db)
	tagsRepo := metadata.NewDockerTagsRepo(db)

	// Seed an APT suite row so deb_packages.suite_id FK is valid.
	var suiteID int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO apt_suites(repo_id, suite, component, architecture) VALUES (?, 'stable', 'main', 'amd64')`, repoID)
		if err != nil {
			return err
		}
		suiteID, _ = res.LastInsertId()
		return nil
	}); err != nil {
		t.Fatalf("seed suite: %v", err)
	}

	// Seed: 1 rpm + 1 deb + 1 pypi + 1 helm + 1 raw + 1 docker manifest with tag.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := rpmRepo.Insert(ctx, tx, &metadata.RPMPackage{
			RepoID: repoID, Name: "httpd", Epoch: 0, Version: "2.4.62", Release: "1",
			Arch: "x86_64", Summary: "Apache HTTPD", Digest: "sha256:rpm", Filename: "httpd.rpm",
		}); err != nil {
			return err
		}
		if _, err := debRepo.Insert(ctx, tx, &metadata.DEBPackage{
			RepoID: repoID, SuiteID: suiteID, Package: "curl", Version: "7.88",
			Architecture: "amd64", Description: "Transfer\nMore", Digest: "sha256:deb",
			Filename: "curl.deb",
		}); err != nil {
			return err
		}
		if _, err := pypiRepo.Insert(ctx, tx, &metadata.PyPIFile{
			RepoID: repoID, ProjectNormalized: "requests", Version: "2.32.0",
			Filename: "requests-2.32.0-py3-none-any.whl", Kind: "wheel",
			RequiresPython: ">=3.8", Digest: "sha256:pypi",
		}); err != nil {
			return err
		}
		if _, err := helmRepo.Insert(ctx, tx, &metadata.HelmChart{
			RepoID: repoID, Name: "nginx-ingress", Version: "4.9.0", AppVersion: "1.10.0",
			Description: "ingress", Digest: "sha256:helm", Filename: "nginx-ingress-4.9.0.tgz",
		}); err != nil {
			return err
		}
		if err := rawRepo.Insert(ctx, tx, repoID, "path/to/raw.bin", 100, "application/octet-stream", "sha256:raw"); err != nil {
			return err
		}
		if err := manifests.Insert(ctx, tx, repoID, "sha256:m1",
			"application/vnd.oci.image.manifest.v1+json", []byte(`{"schemaVersion":2}`)); err != nil {
			return err
		}
		if _, err := tagsRepo.Upsert(ctx, tx, repoID, "", "v1", "sha256:m1"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed base tables: %v", err)
	}

	// Prune (FTS may already be empty since we did not pre-index, but PruneRepoFTS
	// must be a safe no-op on already-empty state). Then reindex.
	rx := metadata.NewFTSReindexer(db, rpmRepo, debRepo, pypiRepo, helmRepo)
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := metadata.PruneRepoFTS(ctx, tx, repoID); err != nil {
			return err
		}
		return rx.ReindexRepo(ctx, tx, repoID)
	}); err != nil {
		t.Fatalf("Prune+Reindex: %v", err)
	}

	for _, q := range []struct {
		name string
		sql  string
		want int
	}{
		{"repos_fts", `SELECT COUNT(*) FROM repos_fts WHERE rowid=?`, 1},
		{"rpm_fts", `SELECT COUNT(*) FROM rpm_fts WHERE repo_id=?`, 1},
		{"deb_fts", `SELECT COUNT(*) FROM deb_fts WHERE repo_id=?`, 1},
		{"pypi_fts", `SELECT COUNT(*) FROM pypi_fts WHERE repo_id=?`, 1},
		{"helm_fts", `SELECT COUNT(*) FROM helm_fts WHERE repo_id=?`, 1},
		{"artifacts_fts", `SELECT COUNT(*) FROM artifacts_fts WHERE repo_id=?`, 2},
	} {
		var n int
		if err := db.Reader.QueryRowContext(ctx, q.sql, repoID).Scan(&n); err != nil {
			t.Fatalf("%s count: %v", q.name, err)
		}
		if n != q.want {
			t.Errorf("%s after reindex=%d want %d", q.name, n, q.want)
		}
	}
}

func TestFTSReindexer_EmptyRepo(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedRepoLive(t, db, "ep", "er")

	rpmRepo := metadata.NewRPMPackagesRepo(db)
	debRepo := metadata.NewDEBPackagesRepo(db)
	pypiRepo := metadata.NewPyPIFilesRepo(db)
	helmRepo := metadata.NewHelmChartsRepo(db)
	rx := metadata.NewFTSReindexer(db, rpmRepo, debRepo, pypiRepo, helmRepo)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return rx.ReindexRepo(ctx, tx, repoID)
	}); err != nil {
		t.Fatalf("ReindexRepo empty: %v", err)
	}

	var n int
	_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos_fts WHERE rowid=?`, repoID).Scan(&n)
	if n != 1 {
		t.Errorf("repos_fts for empty repo=%d want 1 (metadata row)", n)
	}
	for _, table := range []string{"rpm_fts", "deb_fts", "pypi_fts", "helm_fts", "artifacts_fts"} {
		_ = db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE repo_id=?`, repoID).Scan(&n)
		if n != 0 {
			t.Errorf("%s for empty repo=%d want 0", table, n)
		}
	}
}
