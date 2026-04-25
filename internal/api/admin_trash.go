// Package api — admin trash management endpoint (Phase 05-03, OPS-07).
//
// GET    /api/v1/admin/trash           — list soft-deleted items
// POST   /api/v1/admin/trash/{id}/restore — restore a trash entry
// DELETE /api/v1/admin/trash/{id}      — hard-delete (purge) a trash entry
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// codeRestoreConflictRepoMissing is the envelope code returned when a
// drift trash entry's snapshot.repo_id references an absent or
// soft-deleted repo (v1.5 Phase 6 / DRIFTPURGE-02, D-05). File stays
// in trash; admin hard-deletes or re-creates the repo first.
const codeRestoreConflictRepoMissing = "restore.conflict.repo_missing"

// projectTrashPrefix IDs soft-deleted projects in the Trash list. Picked to
// sort (lexicographically) AFTER filesystem entries whose IDs start with a
// unix timestamp digit. F-14.6: projects live in the DB, not the filesystem
// trash, so we surface them with a distinct id namespace.
const projectTrashPrefix = "project-"

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
		ID                 string `json:"id"`
		Kind               string `json:"type"`
		Name               string `json:"name,omitempty"`
		OriginalLocation   string `json:"original_location,omitempty"`
		DeletedBy          string `json:"deleted_by,omitempty"`
		DeletedAt          string `json:"deleted_at"`
		RetentionCountdown string `json:"retention_countdown,omitempty"`
		Source             string `json:"source"` // "filesystem" or "database"
	}

	var items []trashItem

	// Filesystem trash entries. F-15: Surface OriginalPath + DeletedByUser
	// so the Trash UI can show where the item lived pre-delete and who
	// triggered the soft-delete. Retention countdown is computed against
	// the default 7-day GC sweep window (internal/jobs/gc.go:83) — admins
	// that have overridden the window in config will see the display
	// countdown lag reality by the difference. An "expired" entry shows
	// negative countdown ("-2d") so users understand it's GC-pending.
	const defaultTrashRetention = 7 * 24 * time.Hour
	now := time.Now()
	for _, e := range entries {
		remaining := defaultTrashRetention - now.Sub(e.MovedAt)
		items = append(items, trashItem{
			ID:                 filepath.Base(e.Path),
			Kind:               e.Kind,
			Name:               filepath.Base(e.Path),
			OriginalLocation:   e.OriginalPath,
			DeletedBy:          e.DeletedByUser,
			DeletedAt:          e.MovedAt.UTC().Format(time.RFC3339),
			RetentionCountdown: formatRetention(remaining),
			Source:             "filesystem",
		})
	}

	// Database-sourced trash: soft-deleted projects (F-14.6). Projects
	// are soft-deleted by handleDeleteProject via Projects.SoftDelete
	// but never entered the filesystem Trash, so they were previously
	// unreachable from the Trash UI.
	if d.Projects != nil {
		deletedProjects, listErr := d.Projects.ListDeleted(r.Context())
		if listErr == nil {
			for _, p := range deletedProjects {
				var deletedAt string
				remaining := "—"
				if p.DeletedAt != nil {
					deletedAt = p.DeletedAt.UTC().Format(time.RFC3339)
					remaining = formatRetention(defaultTrashRetention - now.Sub(*p.DeletedAt))
				}
				items = append(items, trashItem{
					ID:                 projectTrashPrefix + strconv.FormatInt(p.ID, 10),
					Kind:               "project",
					Name:               p.Name,
					OriginalLocation:   "/projects/" + p.Name,
					DeletedAt:          deletedAt,
					RetentionCountdown: remaining,
					Source:             "database",
				})
			}
		}
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
	// Database-sourced trash: soft-deleted project. F-14.6.
	if strings.HasPrefix(id, projectTrashPrefix) {
		if d.Projects == nil {
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "projects not configured")
			return
		}
		pid, convErr := strconv.ParseInt(strings.TrimPrefix(id, projectTrashPrefix), 10, 64)
		if convErr != nil {
			writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid project id")
			return
		}
		// RestoreIfNameFree does the name-collision check + UPDATE in
		// one tx (Codex batch-14 Q2) so no TOCTOU window exists between
		// a separate FindByName and the UPDATE — projects.name is
		// UNIQUE WHERE deleted_at IS NULL and a parallel create of the
		// same name would otherwise turn into a transient 500.
		if err := d.Projects.RestoreIfNameFree(r.Context(), pid); err != nil {
			switch {
			case errors.Is(err, metadata.ErrNotFound):
				writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not in trash")
			case errors.Is(err, metadata.ErrNameTaken):
				writeJSONError(w, r, http.StatusConflict, ErrConflict,
					"a live project with this name already exists; purge the trashed copy or rename the live project first")
			default:
				writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "restore failed: "+err.Error())
			}
			return
		}
		if a, ok := auth.ActorFromContext(r.Context()); ok {
			uid := a.ID
			d.recordAudit(r, audit.Event{
				Kind: audit.EvtProjectUpdated, ActorUserID: &uid,
				TargetKind: "project", TargetID: strconv.FormatInt(pid, 10),
				Outcome: "restored",
			})
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

		// v1.5 Phase 6 — drift kind dispatch (DRIFTPURGE-02, D-06 + D-04).
		// Drift trash holders carry a row_snapshot in the sidecar; restore
		// re-inserts the row via the per-protocol Insert (which is an
		// UPSERT per D-04) and moves the on-disk file back. Unknown drift
		// kinds fall through to the generic path (which will fail via the
		// e.Empty / childPath branches — belt-and-braces).
		switch e.Kind {
		case "pypi_file_drift", "rpm_package_drift", "deb_package_drift", "helm_chart_drift":
			d.handleDriftRestore(w, r, e, id)
			return
		}

		// F-11 follow-up: metadata-only entries (soft-delete of a
		// never-synced git mirror etc.) have no tree to move back.
		// Restore the DB row + clean the holder dir; the sync handler
		// will re-create on-disk state on first success.
		if e.Empty {
			if e.Kind == "repo" || e.Kind == "git-repo" {
				_ = d.Repos.Restore(r.Context(), e.OriginalID)
			}
			_ = os.Remove(filepath.Join(e.Path, "omnirepo-trash.json"))
			_ = os.Remove(e.Path)
			if a, ok := auth.ActorFromContext(r.Context()); ok {
				uid := a.ID
				d.recordAudit(r, audit.Event{
					Kind:        audit.EvtRepoUpdated,
					ActorUserID: &uid,
					TargetKind:  "trash",
					TargetID:    id,
					Outcome:     "restored_empty",
				})
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
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
		// F-14.6 follow-up: detect destination collision up-front so the
		// user gets an actionable 409 instead of a generic transient 500
		// when a live item has since taken the same on-disk path.
		if _, statErr := os.Stat(dstPath); statErr == nil {
			writeJSONError(w, r, http.StatusConflict, ErrConflict,
				"destination "+dstPath+" already exists; purge the live item or rename it before restoring")
			return
		}
		if err := d.Trash.Restore(r.Context(), childPath, dstPath); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "restore failed: "+err.Error())
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
	// Database-sourced trash: hard-delete project row. F-14.6.
	if strings.HasPrefix(id, projectTrashPrefix) {
		if d.Projects == nil {
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "projects not configured")
			return
		}
		pid, convErr := strconv.ParseInt(strings.TrimPrefix(id, projectTrashPrefix), 10, 64)
		if convErr != nil {
			writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid project id")
			return
		}
		if err := d.Projects.HardDelete(r.Context(), pid); err != nil {
			if errors.Is(err, metadata.ErrProjectHasRepos) {
				writeJSONError(w, r, http.StatusConflict, ErrConflict,
					"project still has repos; purge each repo first (cascading delete is not supported from the Trash UI)")
				return
			}
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "purge failed: "+err.Error())
			return
		}
		if a, ok := auth.ActorFromContext(r.Context()); ok {
			uid := a.ID
			d.recordAudit(r, audit.Event{
				Kind: audit.EvtProjectDeleted, ActorUserID: &uid,
				TargetKind: "project", TargetID: strconv.FormatInt(pid, 10),
				Outcome: "purged",
			})
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

// formatRetention renders a Duration as a coarse "Nd Nh" / "Nh Nm" label
// for the admin Trash UI. Negative values render with a leading "-"
// (entry is GC-eligible; sweep is best-effort so it can linger).
// Intentionally coarse so the UI doesn't show a live-updating timer.
func formatRetention(d time.Duration) string {
	neg := ""
	if d < 0 {
		neg = "-"
		d = -d
	}
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	minutes := int(d/time.Minute) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%s%dd %dh", neg, days, hours)
	case hours > 0:
		return fmt.Sprintf("%s%dh %dm", neg, hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%s%dm", neg, minutes)
	case neg != "":
		// Entry just tipped into GC-eligible territory; avoid rendering
		// "-0m" which reads as a no-op. "<1m past" communicates intent.
		return "<1m past"
	default:
		return "<1m"
	}
}
