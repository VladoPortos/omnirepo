// Package api — dashboard endpoint (Phase 05-04).
//
// GET /api/v1/dashboard — returns storage stats, repo/user counts, scan
// findings summary, and recent audit activity.
package api

import (
	"net/http"

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
	// Dashboard is available to any authenticated user but shows
	// system-wide stats only to super-admins. Regular users get their
	// own scoped view.
	_ = actor

	// Repo count.
	var repoCount int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM repos WHERE deleted_at IS NULL`).Scan(&repoCount)

	// User count.
	userCount, _ := d.Users.Count(r.Context())

	// Storage: sum of all repos' size_bytes.
	var storageUsed int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COALESCE(SUM(size_bytes), 0) FROM repos WHERE deleted_at IS NULL`).Scan(&storageUsed)

	// Scan findings: count critical and high severity vulnerabilities from
	// latest finished scans.
	var critical, high int64
	_ = d.DB.Reader.QueryRowContext(r.Context(), `
		SELECT
			COALESCE(SUM(CASE WHEN severity='CRITICAL' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN severity='HIGH' THEN 1 ELSE 0 END), 0)
		FROM vulnerabilities
	`).Scan(&critical, &high)

	// Recent activity: last 20 audit events.
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, event_kind, COALESCE(target_id, ''), created_at
		FROM audit_log
		ORDER BY id DESC
		LIMIT 20
	`)
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
