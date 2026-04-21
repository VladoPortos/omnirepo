package helm

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// delete handles DELETE /<project>/helm/<repo>/charts/<filename>.
//
// Flow:
//  1. Resolve + auth (project member).
//  2. Locate helm_charts row by repo+filename; 404 if absent.
//  3. Move .tgz (and matching .prov, if present) to trash.
//  4. One writer tx: helm_charts.Delete + IndexHelmDelete + metadata_state=dirty.
//  5. coalescer.Kick so index.yaml regenerates without the deleted chart.
//  6. Audit EvtHelmDelete; 204.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r, true)
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

	if !isChartArchive(res.filename) {
		http.Error(w, "only .tgz charts may be deleted via this route", http.StatusBadRequest)
		return
	}

	row, err := h.helmCharts.FindByFilename(r.Context(), res.repo.ID, res.filename)
	if err != nil || row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	chartKey := storageKeyFor(res.project.Name, res.repo.Name, res.filename)
	chartAbs := filepath.Join(h.repoRoot, filepath.FromSlash(chartKey))
	if _, err := os.Stat(chartAbs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File missing on disk but row exists — reconcile by dropping the
			// row. Fall through to tx.
		} else {
			slog.ErrorContext(r.Context(), "helm.delete.stat_failed",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("filename", res.filename),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	} else {
		if _, err := h.trash.Move(r.Context(), chartAbs, "helm-chart", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.ErrorContext(r.Context(), "helm.delete.trash_failed",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("filename", res.filename),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}
	// Matching provenance file: best-effort move to trash.
	provAbs := chartAbs + ".prov"
	if _, err := os.Stat(provAbs); err == nil {
		_, _ = h.trash.Move(r.Context(), provAbs, "helm-prov", res.repo.ID, auth.ActorLoginFromContext(r.Context()))
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.helmCharts.Delete(r.Context(), tx, row.ID); err != nil {
			return err
		}
		if err := metadata.IndexHelmDelete(r.Context(), tx, res.repo.ID,
			row.Name, row.Version, row.AppVersion); err != nil {
			return err
		}
		return h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		slog.ErrorContext(r.Context(), "helm.delete.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	if h.coalescer != nil {
		h.coalescer.Get(res.repo.ID).Kick()
	}

	h.auditEvent(r, audit.EvtHelmDelete, res.filename, "ok", map[string]any{
		"project":  res.project.Name,
		"repo":     res.repo.Name,
		"name":     row.Name,
		"version":  row.Version,
		"filename": res.filename,
	})

	w.WriteHeader(http.StatusNoContent)
}
