// Package api — dashboard endpoint (Phase 05-04).
//
// GET /api/v1/dashboard — returns storage stats, repo/user counts, scan
// findings summary, and recent audit activity.
// GET /api/v1/dashboard/storage — returns detailed per-repo storage breakdown.
package api

import (
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

// dashboardResponse is the JSON shape returned by GET /dashboard.
type dashboardResponse struct {
	StorageUsedBytes  int64          `json:"storage_used_bytes"`
	StorageTotalBytes int64          `json:"storage_total_bytes"`
	ProjectCount      int64          `json:"project_count"`
	RepoCount         int64          `json:"repo_count"`
	UserCount         int64          `json:"user_count"`
	ScanFindings      scanFindings   `json:"scan_findings"`
	HighSeverity      []vulnRow      `json:"high_severity"`
	RecentActivity    []activityRow  `json:"recent_activity"`
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

	if !actor.IsSuperAdmin {
		ids, _ := d.Members.ListProjectIDsForUser(r.Context(), actor.ID)
		if len(ids) == 0 {
			// No memberships: return zeros.
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

	// Repo count.
	var repoCount int64
	repoArgs := make([]any, len(scopeArgs))
	copy(repoArgs, scopeArgs)
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM repos WHERE deleted_at IS NULL`+scopeClause, repoArgs...).Scan(&repoCount)

	// User count (always global — not sensitive).
	userCount, _ := d.Users.Count(r.Context())

	// Storage: sum of repos' size_bytes.
	storageArgs := make([]any, len(scopeArgs))
	copy(storageArgs, scopeArgs)
	var storageUsed int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(size_bytes), 0) FROM repos WHERE deleted_at IS NULL`+scopeClause, storageArgs...).Scan(&storageUsed)

	// Scan findings: count all severity levels.
	var critical, high, medium, low int64
	if scopeClause == "" {
		_ = d.DB.Reader.QueryRowContext(r.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN severity='CRITICAL' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN severity='HIGH' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN severity='MEDIUM' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN severity='LOW' THEN 1 ELSE 0 END), 0)
			FROM vulnerabilities
		`).Scan(&critical, &high, &medium, &low)
	} else {
		vulnArgs := make([]any, len(scopeArgs))
		copy(vulnArgs, scopeArgs)
		_ = d.DB.Reader.QueryRowContext(r.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN v.severity='CRITICAL' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN v.severity='HIGH' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN v.severity='MEDIUM' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN v.severity='LOW' THEN 1 ELSE 0 END), 0)
			FROM vulnerabilities v
			JOIN scans s ON s.id = v.scan_id
			JOIN repos r ON r.id = s.repo_id
			WHERE r.deleted_at IS NULL`+strings.Replace(scopeClause, "project_id", "r.project_id", 1),
			vulnArgs...).Scan(&critical, &high, &medium, &low)
	}

	// Project count.
	var projectCount int64
	if scopeClause == "" {
		_ = d.DB.Reader.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM projects`).Scan(&projectCount)
	} else {
		projArgs := make([]any, len(scopeArgs))
		copy(projArgs, scopeArgs)
		ph := make([]string, len(scopeArgs))
		for i := range ph {
			ph[i] = "?"
		}
		_ = d.DB.Reader.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM projects WHERE id IN (`+strings.Join(ph, ",")+`)`, projArgs...).Scan(&projectCount)
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
	TotalBytes int64             `json:"total_bytes"`
	UsedBytes  int64             `json:"used_bytes"`
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

	// Used bytes.
	usedArgs := make([]any, len(scopeArgs))
	copy(usedArgs, scopeArgs)
	var usedBytes int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(r.size_bytes), 0) FROM repos r WHERE r.deleted_at IS NULL`+scopeClause, usedArgs...).Scan(&usedBytes)

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

	// Per-repo breakdown sorted by size DESC.
	repoArgs := make([]any, len(scopeArgs))
	copy(repoArgs, scopeArgs)
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT p.name, r.name, r.type, r.size_bytes
		FROM repos r
		JOIN projects p ON p.id = r.project_id
		WHERE r.deleted_at IS NULL`+scopeClause+`
		ORDER BY r.size_bytes DESC
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
