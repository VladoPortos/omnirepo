// Package api — admin trash management endpoint (Phase 05-03, OPS-07).
//
// GET    /api/v1/admin/trash           — list soft-deleted items
// POST   /api/v1/admin/trash/{id}/restore — restore a trash entry
// DELETE /api/v1/admin/trash/{id}      — hard-delete (purge) a trash entry
package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// mountAdminTrash installs trash management endpoints on r.
func (d Deps) mountAdminTrash(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/trash", d.handleListTrash)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Post("/admin/trash/{id}/restore", d.handleRestoreTrash)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Delete("/admin/trash/{id}", d.handlePurgeTrash)
}

func (d Deps) handleListTrash(w http.ResponseWriter, r *http.Request) {
	if d.Trash == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "next_cursor": nil})
		return
	}
	pp := ParsePaginationParams(r)

	entries, err := d.Trash.List(r.Context())
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Also include soft-deleted repos from DB.
	type trashItem struct {
		ID               string `json:"id"`
		Kind             string `json:"type"`
		Name             string `json:"name,omitempty"`
		OriginalLocation string `json:"original_location,omitempty"`
		DeletedAt        string `json:"deleted_at"`
		Source           string `json:"source"` // "filesystem" or "database"
	}

	var items []trashItem

	// Filesystem trash entries. F-15: Surface OriginalPath so the Trash UI
	// can show where the item lived pre-delete (stored in the per-entry
	// sidecar by storage.Move). Empty for legacy entries pre-dating the
	// sidecar fix — those degrade gracefully to blank in the UI.
	for _, e := range entries {
		items = append(items, trashItem{
			ID:               filepath.Base(e.Path),
			Kind:             e.Kind,
			Name:             filepath.Base(e.Path),
			OriginalLocation: e.OriginalPath,
			DeletedAt:        e.MovedAt.UTC().Format(time.RFC3339),
			Source:           "filesystem",
		})
	}

	// Sort by deleted_at descending.
	sort.Slice(items, func(i, j int) bool {
		return items[i].DeletedAt > items[j].DeletedAt
	})

	// Apply cursor-based pagination (cursor = index-based for simplicity).
	startIdx := 0
	if pp.Cursor != nil {
		// Find the item after the cursor position.
		for i, item := range items {
			if item.ID == pp.Cursor.SortValue {
				startIdx = i + 1
				break
			}
		}
	}

	if startIdx >= len(items) {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "next_cursor": nil})
		return
	}

	end := startIdx + pp.Limit
	var nextCursor *string
	if end < len(items) {
		last := items[end-1]
		c := EncodeCursor(Cursor{ID: int64(end - 1), SortValue: last.ID})
		nextCursor = &c
		items = items[startIdx:end]
	} else {
		items = items[startIdx:]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
	})
}

func (d Deps) handleRestoreTrash(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "missing id")
		return
	}
	if d.Trash == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "trash not configured")
		return
	}

	// List entries and find matching one.
	entries, err := d.Trash.List(r.Context())
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	for _, e := range entries {
		dirName := filepath.Base(e.Path)
		if dirName != id {
			continue
		}
		// Audit finding #2: prefer the sidecar-persisted OriginalPath so the
		// restore lands at the exact pre-delete location. Previous behavior
		// reconstructed only the basename, losing project/type context and
		// risking collision with unrelated content.
		var childPath, dstPath string
		if e.OriginalPath != "" {
			childPath = filepath.Join(e.Path, filepath.Base(e.OriginalPath))
			dstPath = e.OriginalPath
		} else {
			// Legacy pre-fix entries: no sidecar, best-effort old behavior.
			children, rdErr := os.ReadDir(e.Path)
			if rdErr != nil {
				writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "cannot read trash entry")
				return
			}
			var firstContent string
			for _, c := range children {
				// Skip the sidecar (defensive; legacy entries won't have it).
				if c.Name() == "omnirepo-trash.json" {
					continue
				}
				firstContent = c.Name()
				break
			}
			if firstContent == "" {
				writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "trash entry is empty")
				return
			}
			childPath = filepath.Join(e.Path, firstContent)
			dstPath = filepath.Join(d.DataRoot, "repos", firstContent)
		}
		if err := d.Trash.Restore(r.Context(), childPath, dstPath); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "restore failed")
			return
		}

		// Also restore DB soft-deleted repo if applicable.
		if (e.Kind == "repo" || e.Kind == "git-repo") && e.OriginalID != 0 {
			_ = d.Repos.Restore(r.Context(), e.OriginalID)
		}

		if a, ok := auth.ActorFromContext(r.Context()); ok {
			uid := a.ID
			d.recordAudit(r, audit.Event{
				Kind:        audit.EvtRepoUpdated,
				ActorUserID: &uid,
				TargetKind:  "trash",
				TargetID:    id,
				Outcome:     "restored",
			})
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "trash entry not found")
}

func (d Deps) handlePurgeTrash(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "missing id")
		return
	}
	if d.Trash == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "trash not configured")
		return
	}

	entries, err := d.Trash.List(r.Context())
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	for _, e := range entries {
		dirName := filepath.Base(e.Path)
		if dirName == id {
			if err := os.RemoveAll(e.Path); err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "purge failed")
				return
			}
			if a, ok := auth.ActorFromContext(r.Context()); ok {
				uid := a.ID
				d.recordAudit(r, audit.Event{
					Kind:        audit.EvtRepoDeleted,
					ActorUserID: &uid,
					TargetKind:  "trash",
					TargetID:    id,
					Outcome:     "purged",
				})
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
	}
	writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "trash entry not found")
}

// trashEntryID builds a stable identifier for trash entries using the
// unix timestamp, kind, and original ID.
func trashEntryID(ts int64, kind string, origID int64) string {
	return strconv.FormatInt(ts, 10) + "-" + kind + "-" + strconv.FormatInt(origID, 10)
}
