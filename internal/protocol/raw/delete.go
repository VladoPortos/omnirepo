package raw

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// delete handles DELETE /<project>/raw/<repo>/<path...>.
//
// Flow:
//  1. Resolve project + repo + path.
//  2. Authorize (project member or super-admin).
//  3. Move the file to trash via storage.Trash.Move (Phase 1 D-31). When
//     the file does not exist on disk we still try to clean up the row so
//     a partial state heals.
//  4. In a single writer tx: delete raw_files row + IndexArtifactDelete +
//     audit.
//  5. 204 No Content.
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
	if !h.actorIsProjectMember(r.Context(), actor, res.project.ID) {
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
		http.Error(w, fmt.Sprintf("stat: %v", err), http.StatusInternalServerError)
		return
	}

	// Soft-delete via trash. Use repo.id as the trash holder id so listing
	// reveals which raw repo a file came from.
	if _, err := h.trash.Move(r.Context(), absPath, "raw-file", res.repo.ID); err != nil {
		http.Error(w, fmt.Sprintf("trash: %v", err), http.StatusInternalServerError)
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.files.Delete(r.Context(), tx, res.repo.ID, res.relPath); err != nil {
			return err
		}
		return metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, res.relPath)
	}); err != nil {
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
		return
	}

	h.auditEvent(r, audit.EvtRawDelete, res.repo, res.relPath, "ok", map[string]any{
		"project": res.project.Name,
		"repo":    res.repo.Name,
		"path":    res.relPath,
	})

	w.WriteHeader(http.StatusNoContent)
}
