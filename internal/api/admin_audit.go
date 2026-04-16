// Package api — admin audit log endpoint (Phase 05-03, OPS-03).
//
// GET /api/v1/admin/audit — super-admin only. Returns paginated, filterable
// audit events from the audit_log table with cursor-based pagination.
package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// mountAdminAudit installs GET /admin/audit on r.
func (d Deps) mountAdminAudit(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)). // reuse super-admin gate
								Get("/admin/audit", d.handleListAudit)
}

// auditRow mirrors a single audit_log row for scanning.
type auditRow struct {
	ID         int64
	OccurredAt time.Time
	ActorLogin *string
	IP         *string
	UserAgent  *string
	EventKind  string
	TargetKind *string
	TargetID   *string
	Outcome    *string
	Details    *string
}

func (d Deps) handleListAudit(w http.ResponseWriter, r *http.Request) {
	pp := ParsePaginationParams(r)
	q := r.URL.Query()

	// Build dynamic WHERE with parameterized queries (T-05-03-03).
	var clauses []string
	var args []any

	if v := q.Get("actor"); v != "" {
		clauses = append(clauses, `a.actor_user_id IN (SELECT id FROM users WHERE login=?)`)
		args = append(args, v)
	}
	if v := q.Get("action"); v != "" {
		clauses = append(clauses, `a.event_kind=?`)
		args = append(args, v)
	}
	if v := q.Get("target_kind"); v != "" {
		clauses = append(clauses, `a.target_kind=?`)
		args = append(args, v)
	}
	if v := q.Get("outcome"); v != "" {
		clauses = append(clauses, `a.outcome=?`)
		args = append(args, v)
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			clauses = append(clauses, `a.occurred_at >= ?`)
			args = append(args, t.UTC().Format(time.RFC3339))
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			clauses = append(clauses, `a.occurred_at <= ?`)
			args = append(args, t.UTC().Format(time.RFC3339))
		}
	}

	// Cursor pagination: keyset on (occurred_at DESC, id DESC).
	if pp.Cursor != nil {
		clauses = append(clauses, `(a.occurred_at < ? OR (a.occurred_at = ? AND a.id < ?))`)
		args = append(args, pp.Cursor.SortValue, pp.Cursor.SortValue, pp.Cursor.ID)
	}

	where := "1=1"
	if len(clauses) > 0 {
		where = strings.Join(clauses, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT a.id, a.occurred_at, u.login, a.ip, a.user_agent,
		       a.event_kind, a.target_kind, a.target_id, a.outcome, a.details_json
		FROM audit_log a
		LEFT JOIN users u ON u.id = a.actor_user_id
		WHERE %s
		ORDER BY a.occurred_at DESC, a.id DESC
		LIMIT ?
	`, where)
	args = append(args, pp.Limit+1) // fetch one extra for next_cursor detection

	rows, err := d.DB.Reader.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()

	type item struct {
		ID         int64           `json:"id"`
		Timestamp  string          `json:"timestamp"`
		Actor      *string         `json:"actor,omitempty"`
		IP         *string         `json:"ip,omitempty"`
		UserAgent  *string         `json:"user_agent,omitempty"`
		Action     string          `json:"action"`
		TargetKind *string         `json:"target_kind,omitempty"`
		TargetID   *string         `json:"target_id,omitempty"`
		Outcome    *string         `json:"outcome,omitempty"`
		Details    *string         `json:"details,omitempty"`
	}

	var items []item
	for rows.Next() {
		var row auditRow
		if err := rows.Scan(&row.ID, &row.OccurredAt, &row.ActorLogin,
			&row.IP, &row.UserAgent, &row.EventKind, &row.TargetKind,
			&row.TargetID, &row.Outcome, &row.Details); err != nil {
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		items = append(items, item{
			ID:         row.ID,
			Timestamp:  row.OccurredAt.Format(time.RFC3339),
			Actor:      row.ActorLogin,
			IP:         row.IP,
			UserAgent:  row.UserAgent,
			Action:     row.EventKind,
			TargetKind: row.TargetKind,
			TargetID:   row.TargetID,
			Outcome:    row.Outcome,
			Details:    row.Details,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Build response with optional next_cursor.
	var nextCursor *string
	if len(items) > pp.Limit {
		items = items[:pp.Limit]
		last := items[len(items)-1]
		c := EncodeCursor(Cursor{ID: last.ID, SortValue: last.Timestamp})
		nextCursor = &c
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
	})
}
