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
//  3. Single writer tx: helm_charts.Delete + IndexHelmDelete + metadata_state=dirty.
//     A failed tx leaves both row AND file in place (CONTEXT D-01).
//  4. After tx commits, move the .tgz to trash. A failure here surfaces
//     HTTP 500; the row is already gone (CONTEXT D-05 orphan trade-off).
//  5. Move matching .prov to trash (best-effort; CONTEXT D-03 — provenance
//     sidecar, slog.WarnContext on failure but request still succeeds).
//  6. coalescer.Kick so index.yaml regenerates without the deleted chart.
//  7. Audit EvtHelmDelete; 204.
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

	// Atomic delete ordering (CONTEXT D-01, audit finding #6): stat first
	// to learn whether the .tgz is on disk (preserves the partial-state
	// heal — when the file is missing but the row is present, we still
	// drop the row in tx and skip trash.Move). Then commit the DB tx
	// BEFORE moving the .tgz so a tx rollback never moves the file.
	chartKey := storageKeyFor(res.project.Name, res.repo.Name, res.filename)
	chartAbs := filepath.Join(h.repoRoot, filepath.FromSlash(chartKey))
	chartOnDisk := false
	if _, err := os.Stat(chartAbs); err == nil {
		chartOnDisk = true
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.ErrorContext(r.Context(), "helm.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	// (else ENOENT → chartOnDisk stays false; the tx heals the orphaned row.)

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

	// Tx committed → row is gone. Move the .tgz to trash. A failure here
	// leaves the file orphaned at chartAbs (no row pointing at it); we
	// surface 500 so the operator notices (CONTEXT D-05).
	if chartOnDisk {
		if _, err := h.trash.Move(r.Context(), chartAbs, "helm-chart", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.ErrorContext(r.Context(), "helm.delete.trash_failed_post_commit",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("filename", res.filename),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}

	// Matching provenance file: best-effort move to trash (CONTEXT D-03).
	// .prov is not load-bearing — failure logs a warning but does not fail
	// the request. Replaces the prior silent `_, _ = ` discard.
	provAbs := chartAbs + ".prov"
	if _, statErr := os.Stat(provAbs); statErr == nil {
		if _, err := h.trash.Move(r.Context(), provAbs, "helm-prov", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.WarnContext(r.Context(), "helm.delete.prov_trash_failed_post_commit",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("filename", res.filename+".prov"),
				slog.Any("err", err),
			)
			// .prov is provenance metadata, not load-bearing — proceed.
		}
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

// DeleteREST is the exported wrapper that the session-authed /api/v1 shim
// mounts at DELETE /api/v1/projects/{name}/repos/helm/{repo}/charts/{filename}
// (F-08.1). It dispatches to the same internal logic as the protocol-native
// DELETE route; resolveRepo handles the {name} → {project} URL-param fallback.
func (h *Handler) DeleteREST(w http.ResponseWriter, r *http.Request) {
	h.delete(w, r)
}
