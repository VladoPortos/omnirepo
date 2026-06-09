package goproxy

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

// delete handles DELETE /<project>/go/<repo>/<module>/@v/<version>.
//
// Flow mirrors the rpm/helm delete ordering: stat first (preserves the
// partial-state heal when the zip is missing but the row exists), commit
// the DB tx BEFORE trash.Move so a rollback never moves files, then move
// the .zip to trash (500 on failure — orphaned file, operator notified)
// and the .mod sidecar best-effort.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if res.req.Op != "delete" {
		http.Error(w, "delete addresses <module>/@v/<version>", http.StatusBadRequest)
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

	target := res.req.ModulePath + "@" + res.req.Version
	row, err := h.goModules.FindByModuleVersion(r.Context(), res.repo.ID, res.req.ModulePath, res.req.Version)
	if err != nil || row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	zipKey := storageKeyFor(res.project.Name, res.repo.Name, res.req.EscapedPath, res.req.Version, "zip")
	zipAbs := filepath.Join(h.repoRoot, filepath.FromSlash(zipKey))
	zipOnDisk := false
	if _, err := os.Stat(zipAbs); err == nil {
		zipOnDisk = true
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.ErrorContext(r.Context(), "goproxy.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("module", target),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	// (else ENOENT → zipOnDisk stays false; the tx heals the orphaned row.)

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.goModules.Delete(r.Context(), tx, row.ID); err != nil {
			return err
		}
		return metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, row.Digest)
	}); err != nil {
		slog.ErrorContext(r.Context(), "goproxy.delete.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("module", target),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Tx committed → row is gone. Move the .zip to trash. A failure here
	// leaves the file orphaned (no row pointing at it); surface 500 so the
	// operator notices.
	if zipOnDisk {
		if _, err := h.trash.Move(r.Context(), zipAbs, "go-module", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.WarnContext(r.Context(), "goproxy.delete.trash_failed_post_commit",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("module", target),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}

	// The .mod sidecar is derived data — best-effort move, warn on failure.
	modAbs := filepath.Join(h.repoRoot, filepath.FromSlash(
		storageKeyFor(res.project.Name, res.repo.Name, res.req.EscapedPath, res.req.Version, "mod")))
	if _, statErr := os.Stat(modAbs); statErr == nil {
		if _, err := h.trash.Move(r.Context(), modAbs, "go-mod", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.WarnContext(r.Context(), "goproxy.delete.mod_trash_failed_post_commit",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("module", target),
				slog.Any("err", err),
			)
		}
	}

	h.auditEvent(r, audit.EvtGoDelete, target, "ok", map[string]any{
		"project": res.project.Name,
		"repo":    res.repo.Name,
		"module":  res.req.ModulePath,
		"version": res.req.Version,
	})

	w.WriteHeader(http.StatusNoContent)
}

// DeleteREST is the exported wrapper that the session-authed /api/v1 shim
// mounts at DELETE /api/v1/projects/{name}/repos/go/{repo}/*. It
// dispatches to the same internal logic as the protocol-native DELETE
// route; resolveRepo handles the {name} → {project} URL-param fallback.
func (h *Handler) DeleteREST(w http.ResponseWriter, r *http.Request) {
	h.delete(w, r)
}
