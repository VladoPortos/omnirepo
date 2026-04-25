package metadata_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func seedSearchData(t *testing.T, db *metadata.DB) {
	t.Helper()
	ctx := context.Background()
	// Phase 01 Plan 01-03 (LIFECYCLE-08): all 7 search arms now INNER JOIN
	// repos+projects + filter `r.deleted_at IS NULL AND p.deleted_at IS NULL`
	// as defense-in-depth. Tests must seed projects + repos rows that match
	// the FTS rowid/repo_id keys, otherwise the JOIN drops every result.
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(id, name) VALUES (1, 'dxc')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(id, project_id, type, name) VALUES (1, 1, 'docker', 'nginx-proxy'), (2, 1, 'git', 'infra-tools')`,
	); err != nil {
		t.Fatalf("seed repos: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		// repos_fts
		if err := metadata.IndexRepo(ctx, tx, 1, "nginx-proxy", "dxc", "Reverse proxy repo", "docker"); err != nil {
			return err
		}
		if err := metadata.IndexRepo(ctx, tx, 2, "infra-tools", "dxc", "Internal tools", "git"); err != nil {
			return err
		}
		// artifacts_fts
		if err := metadata.IndexArtifact(ctx, tx, 1, "nginx", "1.25.0", "sha256:abc"); err != nil {
			return err
		}
		// cves_fts. Also seed the scans + vulnerabilities chain so the cve
		// arm's EXISTS clause (which walks vulnerabilities → scans → repos
		// → projects looking for a live owner) sees a live repo. Without
		// this the post-LIFECYCLE-08 cve arm correctly drops the row.
		if err := metadata.IndexVulnerability(ctx, tx, "CVE-2026-0001", "openssl", "buffer overflow"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO scans(id, repo_id, artifact_kind, artifact_id, status) VALUES (1, 1, 'docker', 'sha256:seed', 'done')`,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vulnerabilities(scan_id, cve_id, severity, package_name) VALUES (1, 'CVE-2026-0001', 'HIGH', 'openssl')`,
		); err != nil {
			return err
		}
		// rpm_fts
		if err := metadata.IndexRPM(ctx, tx, 1, "httpd", "2.4.62", "x86_64", "Apache HTTP Server"); err != nil {
			return err
		}
		// deb_fts
		if err := metadata.IndexDEB(ctx, tx, 1, "curl", "7.88", "amd64", "command line transfer tool"); err != nil {
			return err
		}
		// pypi_fts
		if err := metadata.IndexPyPI(ctx, tx, 1, "requests", "2.32.0", ">=3.8", "HTTP library"); err != nil {
			return err
		}
		// helm_fts
		if err := metadata.IndexHelm(ctx, tx, 1, "nginx-ingress", "4.9.0", "1.10.0", "ingress controller"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed search data: %v", err)
	}
}

func TestSearchAll_AllTables(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchData(t, db)

	results, err := db.SearchAll(context.Background(), metadata.SearchParams{
		Query: "nginx",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'nginx'")
	}
	// Should find results in repos_fts (nginx-proxy), artifacts_fts (nginx),
	// and helm_fts (nginx-ingress).
	kinds := map[string]bool{}
	for _, r := range results {
		kinds[r.Kind] = true
	}
	if !kinds["repo"] {
		t.Error("expected repo result")
	}
	if !kinds["artifact"] {
		t.Error("expected artifact result")
	}
	if !kinds["helm"] {
		t.Error("expected helm result")
	}
}

func TestSearchAll_KindFilter(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchData(t, db)

	results, err := db.SearchAll(context.Background(), metadata.SearchParams{
		Query: "nginx",
		Kind:  "repo",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchAll kind=repo: %v", err)
	}
	for _, r := range results {
		if r.Kind != "repo" {
			t.Fatalf("expected kind=repo, got %q", r.Kind)
		}
	}
	if len(results) == 0 {
		t.Fatal("expected at least one repo result")
	}
}

func TestSearchAll_EmptyQuery(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchData(t, db)

	results, err := db.SearchAll(context.Background(), metadata.SearchParams{
		Query: "",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchAll empty: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil for empty query, got %d results", len(results))
	}
}

func TestSearchAll_SpecialCharsEscaped(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchData(t, db)

	// FTS5 special characters should be stripped, not cause a parse error.
	// Use a query with only the target term plus special chars that get
	// stripped, so FTS5 still matches the data.
	results, err := db.SearchAll(context.Background(), metadata.SearchParams{
		Query: `"nginx" + * ^ ()`,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchAll special chars: %v", err)
	}
	// Should still find nginx matches (special chars stripped).
	if len(results) == 0 {
		t.Fatal("expected results even with special characters")
	}
}

func TestSearchAll_FTS5KeywordsQuoted(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchData(t, db)

	// FTS5 keywords (AND, OR, NOT, NEAR) should not cause parse errors.
	// They get quoted as literal match tokens, which won't match data
	// but shouldn't crash.
	_, err := db.SearchAll(context.Background(), metadata.SearchParams{
		Query: `nginx OR proxy AND NOT something NEAR test`,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchAll keywords: %v", err)
	}
}

func TestSearchAll_OnlySpecialChars(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)

	// Pure operator characters should return no results without error.
	results, err := db.SearchAll(context.Background(), metadata.SearchParams{
		Query: `"" + - * ()`,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchAll only special: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil for all-special query, got %d", len(results))
	}
}

func TestSearchAll_CVESearch(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchData(t, db)

	results, err := db.SearchAll(context.Background(), metadata.SearchParams{
		Query: "CVE-2026",
		Kind:  "cve",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchAll CVE: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected CVE search results")
	}
	if results[0].Kind != "cve" {
		t.Fatalf("expected kind=cve, got %q", results[0].Kind)
	}
}

// TestSearchAll_SeverityFilterCaseInsensitive guards F-13: Trivy writes
// vulnerability rows with uppercase severities ("HIGH", "MEDIUM", …) but
// the API surface (and UI chips) send lowercase. Searching by severity
// must normalize so `severity=high` matches `v.severity='HIGH'`.
func TestSearchAll_SeverityFilterCaseInsensitive(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchData(t, db)

	// Seed a scan + two vulnerabilities for the CVE indexed by seedSearchData.
	// CVE-2026-0001 is indexed in cves_fts; attach one HIGH and one MEDIUM
	// vuln row so the LEFT JOIN surfaces severity. Raw SQL avoids depending
	// on the scans-enqueue FK chain (seedSearchData only populates FTS).
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('sevp')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'rpm','sevr')`); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO scans(repo_id,artifact_kind,artifact_id,status) VALUES (1,'rpm','dummy','done')`)
	if err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	scanID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("scan id: %v", err)
	}
	vulnsRepo := metadata.NewVulnerabilitiesRepo(db)
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return vulnsRepo.InsertBatch(ctx, tx, scanID, []metadata.Vuln{
			{CVEID: "CVE-2026-0001", Severity: "HIGH", PackageName: "openssl"},
			{CVEID: "CVE-2026-0001", Severity: "MEDIUM", PackageName: "openssl"},
		}, 0)
	}); err != nil {
		t.Fatalf("seed vulnerabilities: %v", err)
	}

	for _, tc := range []struct {
		name string
		sev  string
	}{
		{"lowercase", "high"},
		{"uppercase", "HIGH"},
		{"mixed", "HiGh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, err := db.SearchAll(ctx, metadata.SearchParams{
				Query:    "openssl",
				Kind:     "cve",
				Severity: tc.sev,
				Limit:    50,
			})
			if err != nil {
				t.Fatalf("SearchAll severity=%q: %v", tc.sev, err)
			}
			if len(results) != 1 {
				t.Fatalf("severity=%q: want 1 result, got %d: %+v", tc.sev, len(results), results)
			}
			if results[0].Severity != "HIGH" {
				t.Fatalf("severity=%q: want HIGH row, got %q", tc.sev, results[0].Severity)
			}
		})
	}
}

func TestSearchAll_RPMSearch(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchData(t, db)

	results, err := db.SearchAll(context.Background(), metadata.SearchParams{
		Query: "httpd",
		Kind:  "rpm",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchAll RPM: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 rpm result, got %d", len(results))
	}
	if results[0].Name != "httpd" {
		t.Fatalf("expected name=httpd, got %q", results[0].Name)
	}
}

// -----------------------------------------------------------------------------
// Phase 01 Plan 01-03 (LIFECYCLE-08): every search arm filters
// `r.deleted_at IS NULL AND p.deleted_at IS NULL`. The cve arm uses an EXISTS
// subquery via the verified vulnerabilities → scans → repos → projects chain.
// -----------------------------------------------------------------------------

// seedSearchTwoRepos seeds two live projects + repos:
//   project pA / repo rA (id=1, type=rpm, name='alpha')
//   project pB / repo rB (id=2, type=rpm, name='bravo')
// FTS rows for both repos are populated for repos_fts, artifacts_fts, rpm_fts,
// deb_fts, pypi_fts, helm_fts. Caller can then soft-delete rB or pB to
// exercise the arm filter.
func seedSearchTwoRepos(t *testing.T, db *metadata.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(id, name) VALUES (1, 'pA'), (2, 'pB')`,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(id, project_id, type, name) VALUES (1, 1, 'rpm', 'alpha'), (2, 2, 'rpm', 'bravo')`,
	); err != nil {
		t.Fatalf("seed repos: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, rid := range []int64{1, 2} {
			name := "alpha"
			pname := "pA"
			if rid == 2 {
				name = "bravo"
				pname = "pB"
			}
			if err := metadata.IndexRepo(ctx, tx, rid, name, pname, "", "rpm"); err != nil {
				return err
			}
			if err := metadata.IndexArtifact(ctx, tx, rid, "art-"+name, "1.0", "sha256:art-"+name); err != nil {
				return err
			}
			if err := metadata.IndexRPM(ctx, tx, rid, "pkg-"+name, "1.0", "x86_64", ""); err != nil {
				return err
			}
			if err := metadata.IndexDEB(ctx, tx, rid, "pkg-"+name, "1.0", "amd64", ""); err != nil {
				return err
			}
			if err := metadata.IndexPyPI(ctx, tx, rid, "pkg-"+name, "1.0", ">=3.8", ""); err != nil {
				return err
			}
			if err := metadata.IndexHelm(ctx, tx, rid, "pkg-"+name, "1.0", "1.0", ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed FTS: %v", err)
	}
}

// TestSearchAll_FiltersDeletedRepos — soft-delete repo r2 directly in repos
// table (bypass Prune so we test the arm filter in isolation), search for a
// term matching both repos, expect only r1's results.
func TestSearchAll_FiltersDeletedRepos(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchTwoRepos(t, db)
	ctx := context.Background()

	// Soft-delete r2 directly (no FTS prune — testing arm filter).
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE repos SET deleted_at=CURRENT_TIMESTAMP WHERE id=2`,
	); err != nil {
		t.Fatalf("soft-delete r2: %v", err)
	}

	// Search "bravo" — matches r2's repos_fts row (which is still in the FTS
	// table because we bypassed Prune). The arm filter must reject it.
	results, err := db.SearchAll(ctx, metadata.SearchParams{Query: "bravo", Limit: 50})
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	for _, r := range results {
		if r.ProjectName == "pB" {
			t.Errorf("got result owned by deleted repo r2: %+v", r)
		}
	}

	// Search "pkg-bravo" — matches every per-protocol arm's row for r2.
	// All must be filtered.
	results, err = db.SearchAll(ctx, metadata.SearchParams{Query: "pkg-bravo", Limit: 50})
	if err != nil {
		t.Fatalf("SearchAll pkg-bravo: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for soft-deleted repo, got %d: %+v", len(results), results)
	}
}

// TestSearchAll_FiltersDeletedProjects — same as FiltersDeletedRepos but
// soft-delete the project of r2 instead. The repo row stays live; the arm
// filter must still reject because p.deleted_at IS NOT NULL.
func TestSearchAll_FiltersDeletedProjects(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchTwoRepos(t, db)
	ctx := context.Background()

	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE projects SET deleted_at=CURRENT_TIMESTAMP WHERE id=2`,
	); err != nil {
		t.Fatalf("soft-delete pB: %v", err)
	}

	results, err := db.SearchAll(ctx, metadata.SearchParams{Query: "pkg-bravo", Limit: 50})
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for soft-deleted project, got %d", len(results))
	}
}

// TestSearchAll_LiveRepoLiveProjectStillReturnsResults — regression: live
// owner data must continue to surface (the filter must NOT over-reject).
func TestSearchAll_LiveRepoLiveProjectStillReturnsResults(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchTwoRepos(t, db)
	ctx := context.Background()

	results, err := db.SearchAll(ctx, metadata.SearchParams{Query: "pkg-alpha", Limit: 50})
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected non-empty results for live repo + live project")
	}
}

// TestSearchAll_CVEFilteredViaRepoChain — D-11 + LIFECYCLE-08 invariant.
// CVE shared between live repo r1 and soft-deleted repo r2 surfaces; CVE
// owned only by soft-deleted repos drops out.
func TestSearchAll_CVEFilteredViaRepoChain(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchTwoRepos(t, db)
	ctx := context.Background()

	// Seed two scans: scan1 in r1 (live), scan2 in r2 (about to be soft-deleted).
	// Vulnerabilities reference CVE-2024-1234 from both scans.
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO scans(id, repo_id, artifact_kind, artifact_id, status) VALUES (1, 1, 'rpm', 'a1', 'done'), (2, 2, 'rpm', 'a2', 'done')`,
	); err != nil {
		t.Fatalf("seed scans: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO vulnerabilities(scan_id, cve_id, severity, package_name) VALUES (1, 'CVE-2024-1234', 'HIGH', 'openssl'), (2, 'CVE-2024-1234', 'HIGH', 'openssl')`,
	); err != nil {
		t.Fatalf("seed vulns: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return metadata.IndexVulnerability(ctx, tx, "CVE-2024-1234", "openssl", "buffer overflow")
	}); err != nil {
		t.Fatalf("seed cves_fts: %v", err)
	}

	// Phase 1: r1 + r2 both live → CVE surfaces.
	results, err := db.SearchAll(ctx, metadata.SearchParams{Query: "1234", Kind: "cve", Limit: 50})
	if err != nil {
		t.Fatalf("SearchAll 1: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("phase 1 (both live): expected 1 cve result, got %d: %+v", len(results), results)
	}

	// Phase 2: soft-delete r2 — r1 still live → CVE must still surface.
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE repos SET deleted_at=CURRENT_TIMESTAMP WHERE id=2`,
	); err != nil {
		t.Fatalf("soft-delete r2: %v", err)
	}
	results, err = db.SearchAll(ctx, metadata.SearchParams{Query: "1234", Kind: "cve", Limit: 50})
	if err != nil {
		t.Fatalf("SearchAll 2: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("phase 2 (r1 live, r2 deleted): expected 1 cve result, got %d", len(results))
	}

	// Phase 3: soft-delete r1 too → no live owner remains → CVE filtered.
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE repos SET deleted_at=CURRENT_TIMESTAMP WHERE id=1`,
	); err != nil {
		t.Fatalf("soft-delete r1: %v", err)
	}
	results, err = db.SearchAll(ctx, metadata.SearchParams{Query: "1234", Kind: "cve", Limit: 50})
	if err != nil {
		t.Fatalf("SearchAll 3: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("phase 3 (both deleted): expected 0 cve results, got %d: %+v", len(results), results)
	}
}

// TestSearchAll_RepoArm_FiltersDeletedProject — the repo arm gets a project
// JOIN + filter so it doesn't surface a live repo whose project is deleted.
func TestSearchAll_RepoArm_FiltersDeletedProject(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedSearchTwoRepos(t, db)
	ctx := context.Background()

	// Soft-delete pA (repo r1's project). Search "alpha" (matches repos_fts
	// row for r1). The repo arm must filter — p.deleted_at IS NOT NULL.
	if _, err := db.Writer.ExecContext(ctx,
		`UPDATE projects SET deleted_at=CURRENT_TIMESTAMP WHERE id=1`,
	); err != nil {
		t.Fatalf("soft-delete pA: %v", err)
	}

	results, err := db.SearchAll(ctx, metadata.SearchParams{Query: "alpha", Kind: "repo", Limit: 50})
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	for _, r := range results {
		if r.Name == "alpha" {
			t.Errorf("repo arm surfaced 'alpha' whose project is soft-deleted: %+v", r)
		}
	}
}
