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
	Kind     string  // "repo", "artifact", "cve", "rpm", "deb", "pypi", "helm"
	EntityID int64   // rowid or repo_id depending on Kind
	Name     string  // primary display name
	Location string  // secondary context (project, version, etc.)
	Severity string  // non-empty for CVE results
	Score    float64 // FTS5 rank (lower = more relevant)
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

	type arm struct {
		sql string
	}
	var arms []arm
	var args []any

	addArm := func(a arm) {
		arms = append(arms, a)
		args = append(args, ftsQuery, perArmLimit)
	}

	if p.Kind == "" || p.Kind == "repo" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'repo' AS kind, rowid AS entity_id, repo_name AS name, project_name AS location, '' AS severity, rank * 1.5 AS score FROM repos_fts WHERE repos_fts MATCH ? LIMIT ?)`,
		})
	}
	if p.Kind == "" || p.Kind == "artifact" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'artifact' AS kind, rowid AS entity_id, name, version AS location, '' AS severity, rank AS score FROM artifacts_fts WHERE artifacts_fts MATCH ? LIMIT ?)`,
		})
	}
	if p.Kind == "" || p.Kind == "cve" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'cve' AS kind, rowid AS entity_id, cve_id AS name, package AS location, '' AS severity, rank AS score FROM cves_fts WHERE cves_fts MATCH ? LIMIT ?)`,
		})
	}
	if p.Kind == "" || p.Kind == "rpm" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'rpm' AS kind, rowid AS entity_id, name, version AS location, '' AS severity, rank AS score FROM rpm_fts WHERE rpm_fts MATCH ? LIMIT ?)`,
		})
	}
	if p.Kind == "" || p.Kind == "deb" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'deb' AS kind, rowid AS entity_id, name, version AS location, '' AS severity, rank AS score FROM deb_fts WHERE deb_fts MATCH ? LIMIT ?)`,
		})
	}
	if p.Kind == "" || p.Kind == "pypi" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'pypi' AS kind, rowid AS entity_id, name, version AS location, '' AS severity, rank AS score FROM pypi_fts WHERE pypi_fts MATCH ? LIMIT ?)`,
		})
	}
	if p.Kind == "" || p.Kind == "helm" {
		addArm(arm{
			sql: `SELECT * FROM (SELECT 'helm' AS kind, rowid AS entity_id, name, version AS location, '' AS severity, rank AS score FROM helm_fts WHERE helm_fts MATCH ? LIMIT ?)`,
		})
	}

	if len(arms) == 0 {
		return nil, nil
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
		if err := rows.Scan(&r.Kind, &r.EntityID, &r.Name, &r.Location, &r.Severity, &r.Score); err != nil {
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
