// Package api — dashboard endpoint (Phase 05-04).
//
// GET /api/v1/dashboard — returns storage stats, repo/user counts, scan
// findings summary, and recent audit activity.
// GET /api/v1/dashboard/storage — returns detailed per-repo storage breakdown.
package api

import (
	"log/slog"
	"net/http"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// mountDashboard installs the dashboard endpoints.
func (d Deps) mountDashboard(r chi.Router) {
	r.Get("/dashboard", d.handleDashboard)
	r.Get("/dashboard/storage", d.handleStorageDetail)
}

// repoSizeExpr is a SQL scalar subquery that computes the live storage used
// by one repo row, summing across every artifact table. The `repos.size_bytes`
// column is currently never written (WALKTHROUGH-FINDINGS F-5) so reading it
// directly reported 0 B on every dashboard. Until a counter-update hook is
// added to each protocol's PUT path, we recompute on read.
//
// The inner correlated subqueries all filter by `r.id` which is the outer
// `repos` alias; every caller MUST alias the repos table as `r` for this
// fragment to bind.
//
// The docker sub-expression sums the manifest bodies themselves plus the
// de-duplicated config+layer blobs they reference. Blobs are ref-counted
// globally in `docker_blobs`, but the (repo_id → blob digest) graph is
// only carried in each manifest's JSON body — no join table exists — so
// we walk the `layers` array and `config.digest` with SQLite's JSON1
// functions.
//
// W-02 — ref-counted shared-blob bytes: each blob's bytes are split across
// referencing repos by COUNT(DISTINCT repo_id) of manifests referencing it.
// A blob of size S referenced by N distinct repos contributes ~S/N to each,
// matching how operators think about stored bytes per logical repo. This
// replaces the prior over-count (each repo was fully charged for every
// shared blob it touched). Billing-grade per-repo attribution is NOT in
// scope (deferred to v2.0); this trades precision for explanation — the
// aggregate `dashboard.storage_used_bytes` now reflects actual disk usage
// rather than an N× inflation when N repos share a blob.
//
// Arithmetic: `* 1.0` forces floating-point division so small blobs
// referenced by many repos don't truncate to zero per-repo. The inner SUM
// produces a REAL; we CAST back to INTEGER at the ref-counted blob
// sub-expression boundary so the outer addition and the Go-side Scan into
// int64 both stay integer-typed. modernc.org/sqlite's driver refuses to
// implicitly convert REAL→int64 on Scan (returns an error rather than
// silently truncating), so the explicit CAST here is required for
// correctness — not just formatting. Sub-byte fractional remainders are
// discarded at the CAST; fine at storage scale.
const repoSizeExpr = `(
	COALESCE((SELECT SUM(size_bytes) FROM rpm_packages  WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM deb_packages  WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM pypi_files    WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM helm_charts   WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM raw_files     WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM docker_manifests WHERE repo_id = r.id), 0) +
	COALESCE((
		SELECT CAST(SUM(b.size_bytes * 1.0 / b.distinct_repos) AS INTEGER)
		FROM (
			SELECT
				db.digest,
				db.size_bytes,
				COUNT(DISTINCT m2.repo_id) AS distinct_repos
			FROM docker_blobs db
			JOIN docker_manifests m2 ON db.digest IN (
				SELECT json_extract(jl.value, '$.digest')
				FROM json_each(m2.body, '$.layers') jl
				UNION
				SELECT json_extract(m2.body, '$.config.digest')
				WHERE json_extract(m2.body, '$.config.digest') IS NOT NULL
			)
			WHERE db.digest IN (
				SELECT json_extract(jl.value, '$.digest')
				FROM docker_manifests m, json_each(m.body, '$.layers') jl
				WHERE m.repo_id = r.id
				UNION
				SELECT json_extract(m.body, '$.config.digest')
				FROM docker_manifests m
				WHERE m.repo_id = r.id AND json_extract(m.body, '$.config.digest') IS NOT NULL
			)
			GROUP BY db.digest
		) b
	), 0)
)`

// dashboardResponse is the JSON shape returned by GET /dashboard.
type dashboardResponse struct {
	StorageUsedBytes  int64         `json:"storage_used_bytes"`
	StorageTotalBytes int64         `json:"storage_total_bytes"`
	ProjectCount      int64         `json:"project_count"`
	RepoCount         int64         `json:"repo_count"`
	UserCount         int64         `json:"user_count"`
	ScanFindings      scanFindings  `json:"scan_findings"`
	HighSeverity      []vulnRow     `json:"high_severity"`
	RecentActivity    []activityRow `json:"recent_activity"`
}

type vulnRow struct {
	CVEID    string `json:"cve_id"`
	Severity string `json:"severity"`
	Package  string `json:"package"`
	Project  string `json:"project"`
	Repo     string `json:"repo"`
	RepoType string `json:"repo_type"`
}

type scanFindings struct {
	Critical int64 `json:"critical"`
	High     int64 `json:"high"`
	Medium   int64 `json:"medium"`
	Low      int64 `json:"low"`
}

type activityRow struct {
	ID        int64  `json:"id"`
	Action    string `json:"action"`
	TargetID  string `json:"target_id"`
	CreatedAt string `json:"created_at"`
}

func (d Deps) handleDashboard(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	// Super-admins see global stats; regular users see only stats from
	// projects they are members of.
	var scopeClause string
	var scopeArgs []any

	// Audit finding #8: visibleProjectIDs handles super-admins,
	// user actors, AND project-scoped API keys uniformly.
	if ids := visibleProjectIDs(r.Context(), d.Members, actor); ids != nil {
		if len(ids) == 0 {
			// No visible projects: return zeros.
			writeJSON(w, http.StatusOK, dashboardResponse{
				RecentActivity: make([]activityRow, 0),
			})
			return
		}
		placeholders := make([]string, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			scopeArgs = append(scopeArgs, id)
		}
		scopeClause = " AND project_id IN (" + strings.Join(placeholders, ",") + ")"
	}

	// ME-11: log DB errors instead of silently swallowing. COALESCE still
	// guarantees zero on missing rows, but a driver/connection failure now
	// surfaces in the server log rather than masquerading as "empty data."
	logDashErr := func(err error, query string) {
		if err != nil {
			slog.WarnContext(r.Context(), "dashboard.query_failed", "query", query, "err", err)
		}
	}

	// Every dashboard aggregate that crosses into `repos`, `vulnerabilities`,
	// or `s3_buckets` now joins `projects` and filters
	// `p.deleted_at IS NULL` so a soft-deleted project stops contributing
	// immediately — without this, the tile math silently includes ghost
	// repos / ghost CVEs under projects that were "deleted" from the list
	// view (F-1/F-2/F-3 from Codex review of F-4).

	// Repo count.
	var repoCount int64
	repoArgs := make([]any, len(scopeArgs))
	copy(repoArgs, scopeArgs)
	logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM repos r JOIN projects p ON p.id = r.project_id
		 WHERE r.deleted_at IS NULL AND p.deleted_at IS NULL`+strings.Replace(scopeClause, "project_id", "r.project_id", 1),
		repoArgs...).Scan(&repoCount),
		"repo_count")

	// User count (always global — not sensitive).
	userCount, userErr := d.Users.Count(r.Context())
	logDashErr(userErr, "user_count")

	// Storage: live sum across artifact tables per-repo (F-5). `repos.size_bytes`
	// is never written, so we rebuild it at read time using repoSizeExpr.
	storageArgs := make([]any, len(scopeArgs))
	copy(storageArgs, scopeArgs)
	var storageUsed int64
	logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(`+repoSizeExpr+`), 0)
		 FROM repos r JOIN projects p ON p.id = r.project_id
		 WHERE r.deleted_at IS NULL AND p.deleted_at IS NULL`+strings.Replace(scopeClause, "project_id", "r.project_id", 1),
		storageArgs...).Scan(&storageUsed),
		"storage_used")

	// S3 buckets are project-scoped, not repo-scoped, so they don't fit
	// into repoSizeExpr. Add the total of stored objects in every bucket
	// owned by a visible, live project (F-5). Deleted buckets and buckets
	// under soft-deleted projects are skipped.
	s3Args := make([]any, len(scopeArgs))
	copy(s3Args, scopeArgs)
	var s3Used int64
	s3Scope := strings.Replace(scopeClause, "project_id", "b.project_id", 1)
	logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(o.size_bytes), 0)
		 FROM s3_objects o
		 JOIN s3_buckets b ON b.id = o.bucket_id
		 JOIN projects p ON p.id = b.project_id
		 WHERE b.deleted_at IS NULL AND p.deleted_at IS NULL`+s3Scope,
		s3Args...).Scan(&s3Used),
		"storage_used_s3")
	storageUsed += s3Used

	// Scan findings: count all severity levels.
	var critical, high, medium, low int64
	vulnArgs := make([]any, len(scopeArgs))
	copy(vulnArgs, scopeArgs)
	// Same join shape for both branches so the global case also filters
	// soft-deleted projects/repos. The scopeClause is empty for super-admin,
	// so strings.Replace is a no-op there.
	logDashErr(d.DB.Reader.QueryRowContext(r.Context(), `
		SELECT
			COALESCE(SUM(CASE WHEN v.severity='CRITICAL' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN v.severity='HIGH' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN v.severity='MEDIUM' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN v.severity='LOW' THEN 1 ELSE 0 END), 0)
		FROM vulnerabilities v
		JOIN scans s ON s.id = v.scan_id
		JOIN repos r ON r.id = s.repo_id
		JOIN projects p ON p.id = r.project_id
		WHERE r.deleted_at IS NULL AND p.deleted_at IS NULL`+strings.Replace(scopeClause, "project_id", "r.project_id", 1),
		vulnArgs...).Scan(&critical, &high, &medium, &low), "scan_findings")

	// Project count. Exclude soft-deleted projects so the dashboard tile
	// matches /api/v1/projects list semantics (F-4).
	var projectCount int64
	if scopeClause == "" {
		logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM projects WHERE deleted_at IS NULL`).Scan(&projectCount), "project_count")
	} else {
		projArgs := make([]any, len(scopeArgs))
		copy(projArgs, scopeArgs)
		ph := make([]string, len(scopeArgs))
		for i := range ph {
			ph[i] = "?"
		}
		logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM projects WHERE deleted_at IS NULL AND id IN (`+strings.Join(ph, ",")+`)`, projArgs...).Scan(&projectCount),
			"project_count_scoped")
	}

	// High-severity findings: top 20 CRITICAL/HIGH vulnerabilities with repo context.
	// Single SQL shape for both global and scoped — scopeClause is empty for
	// super-admin, so the strings.Replace below no-ops there. The
	// p.deleted_at IS NULL join is what's new vs the pre-F-2 version.
	highSev := make([]vulnRow, 0)
	{
		hsArgs := make([]any, len(scopeArgs))
		copy(hsArgs, scopeArgs)
		hsSQL := `
			SELECT v.cve_id, v.severity, v.package_name, p.name, r.name, r.type
			FROM vulnerabilities v
			JOIN scans s ON s.id = v.scan_id
			JOIN repos r ON r.id = s.repo_id
			JOIN projects p ON p.id = r.project_id
			WHERE v.severity IN ('CRITICAL','HIGH')
			  AND r.deleted_at IS NULL
			  AND p.deleted_at IS NULL` + strings.Replace(scopeClause, "project_id", "r.project_id", 1) + `
			ORDER BY CASE v.severity WHEN 'CRITICAL' THEN 0 ELSE 1 END, v.id DESC
			LIMIT 20`
		if hsRowsQ, err := d.DB.Reader.QueryContext(r.Context(), hsSQL, hsArgs...); err == nil {
			defer func() { _ = hsRowsQ.Close() }()
			for hsRowsQ.Next() {
				var v vulnRow
				if err := hsRowsQ.Scan(&v.CVEID, &v.Severity, &v.Package, &v.Project, &v.Repo, &v.RepoType); err != nil {
					break
				}
				highSev = append(highSev, v)
			}
		}
	}

	// Recent activity: last 20 audit events.
	var activitySQL string
	var activityArgs []any
	if scopeClause == "" {
		activitySQL = `
			SELECT id, event_kind, COALESCE(target_id, ''), occurred_at
			FROM audit_log
			ORDER BY id DESC
			LIMIT 20`
	} else {
		// Scope to audit events whose target_id references a repo in the member projects.
		activityArgs = make([]any, len(scopeArgs))
		copy(activityArgs, scopeArgs)
		activitySQL = `
			SELECT a.id, a.event_kind, COALESCE(a.target_id, ''), a.occurred_at
			FROM audit_log a
			JOIN repos r ON CAST(a.target_id AS INTEGER) = r.id
			WHERE r.deleted_at IS NULL` + strings.Replace(scopeClause, "project_id", "r.project_id", 1) + `
			ORDER BY a.id DESC
			LIMIT 20`
	}

	rows, err := d.DB.Reader.QueryContext(r.Context(), activitySQL, activityArgs...)
	activity := make([]activityRow, 0)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a activityRow
			if err := rows.Scan(&a.ID, &a.Action, &a.TargetID, &a.CreatedAt); err != nil {
				break
			}
			activity = append(activity, a)
		}
		_ = rows.Err()
	}

	// Storage total: try filesystem Statfs first, fall back to settings.
	storageTotalBytes := statfsTotal(d.DataRoot)
	if storageTotalBytes == 0 {
		var totalStr string
		if err := d.DB.Reader.QueryRowContext(r.Context(),
			`SELECT value FROM settings WHERE key='storage_total_bytes'`).Scan(&totalStr); err == nil {
			var n int64
			for _, c := range totalStr {
				if c >= '0' && c <= '9' {
					n = n*10 + int64(c-'0')
				}
			}
			storageTotalBytes = n
		}
	}

	writeJSON(w, http.StatusOK, dashboardResponse{
		StorageUsedBytes:  storageUsed,
		StorageTotalBytes: storageTotalBytes,
		ProjectCount:      projectCount,
		RepoCount:         repoCount,
		UserCount:         userCount,
		ScanFindings:      scanFindings{Critical: critical, High: high, Medium: medium, Low: low},
		HighSeverity:      highSev,
		RecentActivity:    activity,
	})
}

// storageDetailResponse is the JSON shape for GET /dashboard/storage.
type storageDetailResponse struct {
	TotalBytes int64            `json:"total_bytes"`
	UsedBytes  int64            `json:"used_bytes"`
	Repos      []storageRepoRow `json:"repos"`
}

type storageRepoRow struct {
	Project   string `json:"project"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	SizeBytes int64  `json:"size_bytes"`
}

func (d Deps) handleStorageDetail(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	var scopeClause string
	var scopeArgs []any

	if !actor.IsSuperAdmin {
		ids, _ := d.Members.ListProjectIDsForUser(r.Context(), actor.ID)
		if len(ids) == 0 {
			writeJSON(w, http.StatusOK, storageDetailResponse{Repos: make([]storageRepoRow, 0)})
			return
		}
		placeholders := make([]string, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			scopeArgs = append(scopeArgs, id)
		}
		scopeClause = " AND r.project_id IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Used bytes — live computed (F-5). Also adds S3 bucket contents which
	// are project-scoped, not repo-scoped, and therefore outside repoSizeExpr.
	// p.deleted_at IS NULL filters repos/buckets whose project has been
	// soft-deleted (Codex F-1/F-3), matching the behaviour at /api/v1/dashboard.
	usedArgs := make([]any, len(scopeArgs))
	copy(usedArgs, scopeArgs)
	var usedBytes int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(`+repoSizeExpr+`), 0)
		 FROM repos r JOIN projects p ON p.id = r.project_id
		 WHERE r.deleted_at IS NULL AND p.deleted_at IS NULL`+scopeClause, usedArgs...).Scan(&usedBytes)

	s3Args := make([]any, len(scopeArgs))
	copy(s3Args, scopeArgs)
	var s3Used int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(o.size_bytes), 0)
		 FROM s3_objects o
		 JOIN s3_buckets b ON b.id = o.bucket_id
		 JOIN projects p ON p.id = b.project_id
		 WHERE b.deleted_at IS NULL AND p.deleted_at IS NULL`+strings.Replace(scopeClause, "r.project_id", "b.project_id", 1),
		s3Args...).Scan(&s3Used)
	usedBytes += s3Used

	// Total bytes: Statfs or settings fallback.
	totalBytes := statfsTotal(d.DataRoot)
	if totalBytes == 0 {
		var totalStr string
		if err := d.DB.Reader.QueryRowContext(r.Context(),
			`SELECT value FROM settings WHERE key='storage_total_bytes'`).Scan(&totalStr); err == nil {
			var n int64
			for _, c := range totalStr {
				if c >= '0' && c <= '9' {
					n = n*10 + int64(c-'0')
				}
			}
			totalBytes = n
		}
	}

	// Per-repo breakdown sorted by size DESC (live-computed, F-5).
	// Filter soft-deleted projects too — otherwise a hidden project's repos
	// keep appearing in the breakdown list (Codex F-1).
	repoArgs := make([]any, len(scopeArgs))
	copy(repoArgs, scopeArgs)
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT p.name, r.name, r.type, `+repoSizeExpr+` AS bytes
		FROM repos r
		JOIN projects p ON p.id = r.project_id
		WHERE r.deleted_at IS NULL AND p.deleted_at IS NULL`+scopeClause+`
		ORDER BY bytes DESC
	`, repoArgs...)
	repos := make([]storageRepoRow, 0)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var rr storageRepoRow
			if err := rows.Scan(&rr.Project, &rr.Name, &rr.Type, &rr.SizeBytes); err != nil {
				break
			}
			repos = append(repos, rr)
		}
		_ = rows.Err()
	}

	// S3 buckets as type="s3" rows (F-S3-B). Every non-deleted bucket is
	// surfaced, even if it currently has zero objects — the dashboard's
	// aggregate already charges them, so hiding empty buckets would make
	// the math inconsistent.
	bktArgs := make([]any, len(scopeArgs))
	copy(bktArgs, scopeArgs)
	bktRows, bErr := d.DB.Reader.QueryContext(r.Context(), `
		SELECT p.name, b.name, COALESCE(SUM(o.size_bytes), 0) AS bytes
		FROM s3_buckets b
		JOIN projects p ON p.id = b.project_id
		LEFT JOIN s3_objects o ON o.bucket_id = b.id
		WHERE b.deleted_at IS NULL AND p.deleted_at IS NULL`+strings.Replace(scopeClause, "r.project_id", "b.project_id", 1)+`
		GROUP BY b.id
		ORDER BY bytes DESC
	`, bktArgs...)
	if bErr == nil {
		defer func() { _ = bktRows.Close() }()
		for bktRows.Next() {
			var rr storageRepoRow
			rr.Type = "s3"
			if err := bktRows.Scan(&rr.Project, &rr.Name, &rr.SizeBytes); err != nil {
				break
			}
			repos = append(repos, rr)
		}
		_ = bktRows.Err()
	}

	writeJSON(w, http.StatusOK, storageDetailResponse{
		TotalBytes: totalBytes,
		UsedBytes:  usedBytes,
		Repos:      repos,
	})
}

// statfsTotal returns the total capacity of the filesystem containing path.
// Returns 0 on any error or on non-Linux platforms (graceful degradation).
func statfsTotal(path string) int64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	return statfsTotalLinux(path)
}
