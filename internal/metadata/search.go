// Package metadata — FTS5 UNION search (SRCH-03, SRCH-04).
//
// SearchAll runs a ranked FTS5 MATCH across all 7 virtual tables
// (repos_fts, artifacts_fts, cves_fts, rpm_fts, deb_fts, pypi_fts,
// helm_fts). Per-arm LIMIT prevents any single table from dominating
// results. The caller (API handler) enforces project-scope auth
// filtering AFTER the DB returns results (T-05-01-03).
//
// sanitizeFTS5Query escapes special FTS5 operators and wraps every
// word as a prefix-match token. The MATCH value is always passed as
// a parameterised ? binding — never string-interpolated (T-05-01-01).
package metadata

import (
	"context"
	"strings"
)

// SearchResult holds one merged result from the FTS5 UNION query.
type SearchResult struct {
	Kind        string  // "repo", "artifact", "cve", "rpm", "deb", "pypi", "helm"
	EntityID    int64   // rowid or repo_id depending on Kind
	Name        string  // primary display name
	Location    string  // secondary context (project, version, etc.)
	Severity    string  // non-empty for CVE results
	Score       float64 // FTS5 rank (lower = more relevant)
	ProjectName string  // owning project name for auth filtering
}

// SearchParams captures the request filters for SearchAll.
type SearchParams struct {
	Query    string // raw user input
	Kind     string // filter by kind; empty = all
	Severity string // filter by severity; empty = all (CVE only)
	Project  string // filter by project; empty = all
	Limit    int    // max results; caller clamps to [1, 200]
}

// SearchAll performs a UNION ALL across all enabled FTS5 virtual tables,
// returning at most p.Limit results ranked by FTS5 score. When p.Kind
// is non-empty, only the corresponding table is queried.
func (db *DB) SearchAll(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	ftsQuery := sanitizeFTS5Query(p.Query)
	if ftsQuery == "" {
		return nil, nil
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}

	perArmLimit := p.Limit * 2 // over-fetch per arm, trim after merge-sort

	// ME-03: per-arm severity/project filters. Severity only meaningfully
	// applies to CVE results (joined back to vulnerabilities for the latest
	// finding). Project filter applies to every project-scoped arm.
	type arm struct {
		sql  string
		vals []any
	}
	var arms []arm

	addArm := func(a arm) {
		arms = append(arms, a)
	}

	// Phase 01 Plan 01-03 (LIFECYCLE-08): every arm filters
	// `r.deleted_at IS NULL AND p.deleted_at IS NULL` as a defense-in-depth
	// gate beyond PruneRepoFTS. The filter is the second independent
	// guarantee that search NEVER surfaces soft-deleted owner data, even if
	// a Prune missed a row or a row was inserted mid-soft-delete.
	//
	// All six per-protocol arms become INNER JOIN-based with a uniform
	// `p.name = ?` project filter. The CVE arm uses an EXISTS subquery via
	// the verified vulnerabilities → scans → repos → projects chain
	// (vulnerabilities has NO direct repo_id column).
	projectFilterExpr := ""
	projectVals := []any{}
	if p.Project != "" {
		projectFilterExpr = " AND p.name = ?"
		projectVals = []any{p.Project}
	}

	if p.Kind == "" || p.Kind == "repo" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'repo' AS kind, f.rowid AS entity_id, f.repo_name AS name, p.name AS location, '' AS severity, rank * 1.5 AS score, p.name AS project_name
				FROM repos_fts f
				INNER JOIN repos    r ON r.id = f.rowid
				INNER JOIN projects p ON p.id = r.project_id
				WHERE repos_fts MATCH ?
				  AND r.deleted_at IS NULL AND p.deleted_at IS NULL` + projectFilterExpr + ` LIMIT ?)`,
			vals: append(append([]any{ftsQuery}, projectVals...), perArmLimit),
		})
	}
	if p.Kind == "" || p.Kind == "artifact" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'artifact' AS kind, f.rowid AS entity_id, f.name, f.version AS location, '' AS severity, rank AS score, p.name AS project_name
				FROM artifacts_fts f
				INNER JOIN repos    r ON r.id = f.repo_id
				INNER JOIN projects p ON p.id = r.project_id
				WHERE artifacts_fts MATCH ?
				  AND r.deleted_at IS NULL AND p.deleted_at IS NULL` + projectFilterExpr + ` LIMIT ?)`,
			vals: append(append([]any{ftsQuery}, projectVals...), perArmLimit),
		})
	}
	if p.Kind == "" || p.Kind == "cve" {
		// CVE arm — chain filter via EXISTS subquery walking
		// vulnerabilities → scans → repos → projects. At least one live
		// repo must own the CVE for it to surface (defense-in-depth that
		// matches PruneRepoFTS's conditional cves_fts prune chain — D-11).
		// vulnerabilities has NO direct repo_id column; the chain is
		// load-bearing, NOT optional.
		//
		// LEFT JOIN vulnerabilities v ON v.cve_id = f.cve_id stays for the
		// severity surfacing (existing behavior preserved). The EXISTS
		// subquery is the new lifetime gate. Severity filter (if any) is
		// appended verbatim from the prior implementation.
		cveSeverityClause := ""
		cveVals := []any{ftsQuery}
		if p.Severity != "" {
			cveSeverityClause = " AND v.severity = ?"
			cveVals = append(cveVals, strings.ToUpper(p.Severity))
		}
		cveVals = append(cveVals, perArmLimit)
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'cve' AS kind, f.rowid AS entity_id, f.cve_id AS name, f.package AS location, COALESCE(v.severity, '') AS severity, rank AS score, '' AS project_name
				FROM cves_fts f
				LEFT JOIN vulnerabilities v ON v.cve_id = f.cve_id
				WHERE cves_fts MATCH ?
				  AND EXISTS (
				    SELECT 1 FROM vulnerabilities v2
				      INNER JOIN scans    s ON s.id = v2.scan_id
				      INNER JOIN repos    r ON r.id = s.repo_id
				      INNER JOIN projects p ON p.id = r.project_id
				     WHERE v2.cve_id = f.cve_id
				       AND r.deleted_at IS NULL
				       AND p.deleted_at IS NULL
				  )` + cveSeverityClause + ` GROUP BY f.cve_id LIMIT ?)`,
			vals: cveVals,
		})
	}
	if p.Kind == "" || p.Kind == "rpm" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'rpm' AS kind, f.rowid AS entity_id, f.name, f.version AS location, '' AS severity, rank AS score, p.name AS project_name
				FROM rpm_fts f
				INNER JOIN repos    r ON r.id = f.repo_id
				INNER JOIN projects p ON p.id = r.project_id
				WHERE rpm_fts MATCH ?
				  AND r.deleted_at IS NULL AND p.deleted_at IS NULL` + projectFilterExpr + ` LIMIT ?)`,
			vals: append(append([]any{ftsQuery}, projectVals...), perArmLimit),
		})
	}
	if p.Kind == "" || p.Kind == "deb" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'deb' AS kind, f.rowid AS entity_id, f.name, f.version AS location, '' AS severity, rank AS score, p.name AS project_name
				FROM deb_fts f
				INNER JOIN repos    r ON r.id = f.repo_id
				INNER JOIN projects p ON p.id = r.project_id
				WHERE deb_fts MATCH ?
				  AND r.deleted_at IS NULL AND p.deleted_at IS NULL` + projectFilterExpr + ` LIMIT ?)`,
			vals: append(append([]any{ftsQuery}, projectVals...), perArmLimit),
		})
	}
	if p.Kind == "" || p.Kind == "pypi" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'pypi' AS kind, f.rowid AS entity_id, f.name, f.version AS location, '' AS severity, rank AS score, p.name AS project_name
				FROM pypi_fts f
				INNER JOIN repos    r ON r.id = f.repo_id
				INNER JOIN projects p ON p.id = r.project_id
				WHERE pypi_fts MATCH ?
				  AND r.deleted_at IS NULL AND p.deleted_at IS NULL` + projectFilterExpr + ` LIMIT ?)`,
			vals: append(append([]any{ftsQuery}, projectVals...), perArmLimit),
		})
	}
	if p.Kind == "" || p.Kind == "helm" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'helm' AS kind, f.rowid AS entity_id, f.name, f.version AS location, '' AS severity, rank AS score, p.name AS project_name
				FROM helm_fts f
				INNER JOIN repos    r ON r.id = f.repo_id
				INNER JOIN projects p ON p.id = r.project_id
				WHERE helm_fts MATCH ?
				  AND r.deleted_at IS NULL AND p.deleted_at IS NULL` + projectFilterExpr + ` LIMIT ?)`,
			vals: append(append([]any{ftsQuery}, projectVals...), perArmLimit),
		})
	}

	if len(arms) == 0 {
		return nil, nil
	}

	// ME-03: if a severity filter was requested but the query asked for a
	// non-CVE kind, short-circuit to empty results — severity only applies
	// to CVEs.
	if p.Severity != "" && p.Kind != "" && p.Kind != "cve" {
		return nil, nil
	}

	args := []any{}
	for _, a := range arms {
		args = append(args, a.vals...)
	}

	// Build the UNION ALL query.
	parts := make([]string, len(arms))
	for i, a := range arms {
		parts[i] = a.sql
	}
	fullSQL := strings.Join(parts, " UNION ALL ") + " ORDER BY score LIMIT ?"
	args = append(args, p.Limit)

	rows, err := db.Reader.QueryContext(ctx, fullSQL, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Kind, &r.EntityID, &r.Name, &r.Location, &r.Severity, &r.Score, &r.ProjectName); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// sanitizeFTS5Query escapes special FTS5 characters and wraps each word
// as a prefix-match token. An empty string returns "".
func sanitizeFTS5Query(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	words := strings.Fields(q)
	out := make([]string, 0, len(words))
	for _, w := range words {
		// Strip characters that are FTS5 operators. Hyphens are kept
		// because they appear in CVE IDs and package names; inside
		// double-quotes FTS5 treats them as literal characters.
		w = strings.NewReplacer(
			`"`, ``,
			`'`, ``,
			`(`, ``,
			`)`, ``,
			`*`, ``,
			`+`, ``,
			`^`, ``,
			`:`, ``,
		).Replace(w)
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		// Skip pure FTS5 keywords that would cause parse errors.
		upper := strings.ToUpper(w)
		if upper == "AND" || upper == "OR" || upper == "NOT" || upper == "NEAR" {
			// Quote them so they become literal match tokens.
			out = append(out, `"`+w+`"`)
			continue
		}
		out = append(out, `"`+w+`"*`)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " ")
}
