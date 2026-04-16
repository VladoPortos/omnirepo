// Package api — dashboard endpoint (Phase 05-04).
//
// GET /api/v1/dashboard — returns storage stats, repo/user counts, scan
// findings summary, and recent audit activity.
package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// mountDashboard installs the dashboard endpoint.
func (d Deps) mountDashboard(r chi.Router) {
	r.Get("/dashboard", d.handleDashboard)
}

// dashboardResponse is the JSON shape returned by GET /dashboard.
type dashboardResponse struct {
	StorageUsedBytes  int64          `json:"storage_used_bytes"`
	StorageTotalBytes int64          `json:"storage_total_bytes"`
	RepoCount         int64          `json:"repo_count"`
	UserCount         int64          `json:"user_count"`
	ScanFindings      scanFindings   `json:"scan_findings"`
	RecentActivity    []activityRow  `json:"recent_activity"`
}

type scanFindings struct {
	Critical int64 `json:"critical"`
	High     int64 `json:"high"`
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

	// Scan findings: count critical and high severity vulnerabilities.
	var critical, high int64
	if scopeClause == "" {
		_ = d.DB.Reader.QueryRowContext(r.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN severity='CRITICAL' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN severity='HIGH' THEN 1 ELSE 0 END), 0)
			FROM vulnerabilities
		`).Scan(&critical, &high)
	} else {
		vulnArgs := make([]any, len(scopeArgs))
		copy(vulnArgs, scopeArgs)
		_ = d.DB.Reader.QueryRowContext(r.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN v.severity='CRITICAL' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN v.severity='HIGH' THEN 1 ELSE 0 END), 0)
			FROM vulnerabilities v
			JOIN scans s ON s.id = v.scan_id
			JOIN repos r ON r.id = s.repo_id
			WHERE r.deleted_at IS NULL`+strings.Replace(scopeClause, "project_id", "r.project_id", 1),
			vulnArgs...).Scan(&critical, &high)
	}

	// Recent activity: last 20 audit events.
	var activitySQL string
	var activityArgs []any
	if scopeClause == "" {
		activitySQL = `
			SELECT id, event_kind, COALESCE(target_id, ''), created_at
			FROM audit_log
			ORDER BY id DESC
			LIMIT 20`
	} else {
		// Scope to audit events whose target_id references a repo in the member projects.
		activityArgs = make([]any, len(scopeArgs))
		copy(activityArgs, scopeArgs)
		activitySQL = `
			SELECT a.id, a.event_kind, COALESCE(a.target_id, ''), a.created_at
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

	writeJSON(w, http.StatusOK, dashboardResponse{
		StorageUsedBytes:  storageUsed,
		StorageTotalBytes: 0, // filesystem df not available in pure Go without syscall; 0 = unknown
		RepoCount:         repoCount,
		UserCount:         userCount,
		ScanFindings:      scanFindings{Critical: critical, High: high},
		RecentActivity:    activity,
	})
}
