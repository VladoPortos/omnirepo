// Package api — search endpoint (SRCH-03, SRCH-04).
//
// GET /api/v1/search?q=&kind=&severity=&project=&limit=&cursor=
//
// Calls DB.SearchAll with FTS5 MATCH across all virtual tables. Results
// are filtered by project membership — non-super-admin actors only see
// results from projects they belong to.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// mountSearch installs GET /api/v1/search (any authenticated user).
func (d Deps) mountSearch(r chi.Router) {
	r.Get("/search", d.handleSearch)
}

// searchResultItem is the JSON projection of one search result.
type searchResultItem struct {
	Kind     string  `json:"kind"`
	EntityID int64   `json:"entity_id"`
	Name     string  `json:"name"`
	Location string  `json:"location"`
	Severity string  `json:"severity,omitempty"`
	Score    float64 `json:"score"`
}

func (d Deps) handleSearch(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "next_cursor": ""})
		return
	}

	pp := ParsePaginationParams(r)
	params := metadata.SearchParams{
		Query:    q,
		Kind:     r.URL.Query().Get("kind"),
		Severity: r.URL.Query().Get("severity"),
		Project:  r.URL.Query().Get("project"),
		Limit:    pp.Limit,
	}

	results, err := d.DB.SearchAll(r.Context(), params)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Filter by project membership for non-super-admin actors.
	// Build a set of project names the actor can see.
	var memberProjectNames map[string]struct{}
	if !actor.IsSuperAdmin {
		ids, _ := d.Members.ListProjectIDsForUser(r.Context(), actor.ID)
		memberProjectNames = make(map[string]struct{}, len(ids))
		for _, pid := range ids {
			p, err := d.Projects.FindByID(r.Context(), pid)
			if err == nil {
				memberProjectNames[p.Name] = struct{}{}
			}
		}
	}

	items := make([]searchResultItem, 0, len(results))
	for _, res := range results {
		// Filter: if actor is not super-admin, only include results from
		// projects the actor is a member of. ProjectName is populated by
		// JOINing each FTS arm back to repos/projects.
		if memberProjectNames != nil {
			if res.ProjectName == "" {
				// CVE results have no project association; skip for non-admins.
				continue
			}
			if _, ok := memberProjectNames[res.ProjectName]; !ok {
				continue
			}
		}
		items = append(items, searchResultItem{
			Kind:     res.Kind,
			EntityID: res.EntityID,
			Name:     res.Name,
			Location: res.Location,
			Severity: res.Severity,
			Score:    res.Score,
		})
	}

	var nextCursor string
	if len(items) >= pp.Limit {
		// There may be more results; encode cursor from last filtered item.
		last := items[len(items)-1]
		nextCursor = EncodeCursor(Cursor{ID: 0, SortValue: last.Name})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
	})
}
