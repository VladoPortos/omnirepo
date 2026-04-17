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
// Docker is an underestimate: we count manifest body bytes only, not the
// ref-counted blob tree (blobs are global and attributing them per-repo
// requires a manifest→blob join that costs too much per dashboard load).
// Good enough for "is storage growing?" — not for billing.
// The docker sub-expression sums the manifest bodies themselves plus the
// de-duplicated config+layer blobs they reference. Blobs are ref-counted
// globally in `docker_blobs`, but the (repo_id → blob digest) graph is
// only carried in each manifest's JSON body — no join table exists — so
// we walk the `layers` array and `config.digest` with SQLite's JSON1
// functions. Per-repo shared blobs are NOT split across repos: a blob
// referenced by two repos is fully counted in both (overestimate), which
// matches how operators think about stored bytes per logical repo.
const repoSizeExpr = `(
	COALESCE((SELECT SUM(size_bytes) FROM rpm_packages  WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM deb_packages  WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM pypi_files    WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM helm_charts   WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM raw_files     WHERE repo_id = r.id), 0) +
	COALESCE((SELECT SUM(size_bytes) FROM docker_manifests WHERE repo_id = r.id), 0) +
	COALESCE((
		SELECT SUM(size_bytes) FROM docker_blobs WHERE digest IN (
			SELECT DISTINCT json_extract(jl.value, '$.digest')
			FROM docker_manifests m, json_each(m.body, '$.layers') jl
			WHERE m.repo_id = r.id
			UNION
			SELECT DISTINCT json_extract(m.body, '$.config.digest')
			FROM docker_manifests m
			WHERE m.repo_id = r.id AND json_extract(m.body, '$.config.digest') IS NOT NULL
		)
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
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
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

	// Repo count.
	var repoCount int64
	repoArgs := make([]any, len(scopeArgs))
	copy(repoArgs, scopeArgs)
	logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM repos WHERE deleted_at IS NULL`+scopeClause, repoArgs...).Scan(&repoCount),
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
		`SELECT COALESCE(SUM(`+repoSizeExpr+`), 0) FROM repos r WHERE r.deleted_at IS NULL`+strings.Replace(scopeClause, "project_id", "r.project_id", 1),
		storageArgs...).Scan(&storageUsed),
		"storage_used")

	// S3 buckets are project-scoped, not repo-scoped, so they don't fit
	// into repoSizeExpr. Add the total of stored objects in every bucket
	// owned by a visible project (F-5). Deleted buckets are skipped.
	s3Args := make([]any, len(scopeArgs))
	copy(s3Args, scopeArgs)
	var s3Used int64
	s3Scope := strings.Replace(scopeClause, "project_id", "b.project_id", 1)
	logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(o.size_bytes), 0)
		 FROM s3_objects o
		 JOIN s3_buckets b ON b.id = o.bucket_id
		 WHERE b.deleted_at IS NULL`+s3Scope,
		s3Args...).Scan(&s3Used),
		"storage_used_s3")
	storageUsed += s3Used

	// Scan findings: count all severity levels.
	var critical, high, medium, low int64
	if scopeClause == "" {
		logDashErr(d.DB.Reader.QueryRowContext(r.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN severity='CRITICAL' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN severity='HIGH' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN severity='MEDIUM' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN severity='LOW' THEN 1 ELSE 0 END), 0)
			FROM vulnerabilities
		`).Scan(&critical, &high, &medium, &low), "scan_findings_global")
	} else {
		vulnArgs := make([]any, len(scopeArgs))
		copy(vulnArgs, scopeArgs)
		logDashErr(d.DB.Reader.QueryRowContext(r.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN v.severity='CRITICAL' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN v.severity='HIGH' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN v.severity='MEDIUM' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN v.severity='LOW' THEN 1 ELSE 0 END), 0)
			FROM vulnerabilities v
			JOIN scans s ON s.id = v.scan_id
			JOIN repos r ON r.id = s.repo_id
			WHERE r.deleted_at IS NULL`+strings.Replace(scopeClause, "project_id", "r.project_id", 1),
			vulnArgs...).Scan(&critical, &high, &medium, &low), "scan_findings_scoped")
	}

	// Project count.
	var projectCount int64
	if scopeClause == "" {
		logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM projects`).Scan(&projectCount), "project_count")
	} else {
		projArgs := make([]any, len(scopeArgs))
		copy(projArgs, scopeArgs)
		ph := make([]string, len(scopeArgs))
		for i := range ph {
			ph[i] = "?"
		}
		logDashErr(d.DB.Reader.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM projects WHERE id IN (`+strings.Join(ph, ",")+`)`, projArgs...).Scan(&projectCount),
			"project_count_scoped")
	}

	// High-severity findings: top 20 CRITICAL/HIGH vulnerabilities with repo context.
	highSev := make([]vulnRow, 0)
	{
		var hsSQL string
		var hsArgs []any
		if scopeClause == "" {
			hsSQL = `
				SELECT v.cve_id, v.severity, v.package_name, p.name, r.name, r.type
				FROM vulnerabilities v
				JOIN scans s ON s.id = v.scan_id
				JOIN repos r ON r.id = s.repo_id
				JOIN projects p ON p.id = r.project_id
				WHERE v.severity IN ('CRITICAL','HIGH') AND r.deleted_at IS NULL
				ORDER BY CASE v.severity WHEN 'CRITICAL' THEN 0 ELSE 1 END, v.id DESC
				LIMIT 20`
		} else {
			hsArgs = make([]any, len(scopeArgs))
			copy(hsArgs, scopeArgs)
			hsSQL = `
				SELECT v.cve_id, v.severity, v.package_name, p.name, r.name, r.type
				FROM vulnerabilities v
				JOIN scans s ON s.id = v.scan_id
				JOIN repos r ON r.id = s.repo_id
				JOIN projects p ON p.id = r.project_id
				WHERE v.severity IN ('CRITICAL','HIGH') AND r.deleted_at IS NULL` + strings.Replace(scopeClause, "project_id", "r.project_id", 1) + `
				ORDER BY CASE v.severity WHEN 'CRITICAL' THEN 0 ELSE 1 END, v.id DESC
				LIMIT 20`
		}
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
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
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
	usedArgs := make([]any, len(scopeArgs))
	copy(usedArgs, scopeArgs)
	var usedBytes int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(`+repoSizeExpr+`), 0) FROM repos r WHERE r.deleted_at IS NULL`+scopeClause, usedArgs...).Scan(&usedBytes)

	s3Args := make([]any, len(scopeArgs))
	copy(s3Args, scopeArgs)
	var s3Used int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(o.size_bytes), 0)
		 FROM s3_objects o
		 JOIN s3_buckets b ON b.id = o.bucket_id
		 WHERE b.deleted_at IS NULL`+strings.Replace(scopeClause, "r.project_id", "b.project_id", 1),
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
	repoArgs := make([]any, len(scopeArgs))
	copy(repoArgs, scopeArgs)
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT p.name, r.name, r.type, `+repoSizeExpr+` AS bytes
		FROM repos r
		JOIN projects p ON p.id = r.project_id
		WHERE r.deleted_at IS NULL`+scopeClause+`
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
		WHERE b.deleted_at IS NULL`+strings.Replace(scopeClause, "r.project_id", "b.project_id", 1)+`
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
