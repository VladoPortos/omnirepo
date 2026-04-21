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
		// cves_fts
		if err := metadata.IndexVulnerability(ctx, tx, "CVE-2026-0001", "openssl", "buffer overflow"); err != nil {
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
