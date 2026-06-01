package raw

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// delete handles DELETE /<project>/raw/<repo>/<path...>.
//
// Flow:
//  1. Resolve project + repo + path.
//  2. Authorize (project member or super-admin).
//  3. In a single writer tx: delete raw_files row + IndexArtifactDelete.
//     A failed tx leaves the row AND the file in place — operator can retry.
//  4. After tx commits, move the file to trash via storage.Trash.Move.
//     A failure here surfaces as HTTP 500; the row is already gone, so the
//     file becomes orphaned at its original storage path (operator-
//     recoverable via `find`).
//  5. Audit + 204 No Content.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepoAndPath(w, r, true)
	if !ok {
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionUpdateRepo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.relPath)
	absPath := filepath.Join(h.repoRoot, filepath.FromSlash(storageKey))

	if _, err := os.Stat(absPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "raw.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Atomic delete ordering: commit the DB tx FIRST. If the tx fails, the
	// file remains at its original path and the row is intact — operator
	// can retry the DELETE. Only after the tx commits do we move the file
	// to trash. If the post-commit trash.Move fails, we return 500 but the
	// row is already gone — the file becomes "abandoned" at its original
	// storage path (operator can locate via standard `find` and clean up).
	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.files.Delete(r.Context(), tx, res.repo.ID, res.relPath); err != nil {
			return err
		}
		return metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, res.relPath)
	}); err != nil {
		slog.ErrorContext(r.Context(), "raw.delete.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Tx committed → row is gone. Move the file to trash. A failure here
	// leaves the file orphaned at absPath (no row pointing at it); we
	// surface 500 so the operator notices.
	if _, err := h.trash.Move(r.Context(), absPath, "raw-file", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
		slog.WarnContext(r.Context(), "raw.delete.trash_failed_post_commit",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	h.auditEvent(r, audit.EvtRawDelete, res.repo, res.relPath, "ok", map[string]any{
		"project": res.project.Name,
		"repo":    res.repo.Name,
		"path":    res.relPath,
	})

	w.WriteHeader(http.StatusNoContent)
}
