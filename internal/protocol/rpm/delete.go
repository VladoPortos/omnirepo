package rpm

import (
	"context"
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

// delete handles DELETE /<project>/rpm/<repo>/packages/<filename>.
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
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionUpdateRepo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Locate the rpm_packages row by filename.
	row, err := h.findByFilename(r.Context(), res.repo.ID, res.filename)
	if err != nil || row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Atomic delete ordering: stat first to learn whether the file is on
	// disk (preserves the partial-state heal — when the file is missing but
	// the row is present, we still drop the row in tx and skip trash.Move).
	// Then commit the DB tx BEFORE trash.Move so a tx rollback never moves
	// the file.
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(storageKeyFor(res.project.Name, res.repo.Name, res.filename)))
	fileOnDisk := false
	if _, err := os.Stat(abs); err == nil {
		fileOnDisk = true
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.ErrorContext(r.Context(), "rpm.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	// (else ENOENT → fileOnDisk stays false; the tx heals the orphaned row.)

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.rpmPackages.Delete(r.Context(), tx, row.ID); err != nil {
			return err
		}
		if err := metadata.IndexRPMDelete(r.Context(), tx, res.repo.ID, row.Name, row.Version, row.Arch); err != nil {
			return err
		}
		return h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		slog.ErrorContext(r.Context(), "rpm.delete.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Tx committed → row is gone. If the file was on disk at the start of
	// the request, move it to trash now. A failure here leaves the file
	// orphaned at abs (no row pointing at it); we surface 500 so the
	// operator notices.
	if fileOnDisk {
		if _, err := h.trash.Move(r.Context(), abs, "rpm-package", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.WarnContext(r.Context(), "rpm.delete.trash_failed_post_commit",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("filename", res.filename),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}

	if h.coalescer != nil {
		h.coalescer.Get(res.repo.ID).Kick()
	}

	h.auditEvent(r, audit.EvtRPMDelete, res.filename, "ok", map[string]any{
		"project":  res.project.Name,
		"repo":     res.repo.Name,
		"name":     row.Name,
		"version":  row.Version,
		"arch":     row.Arch,
		"filename": res.filename,
	})

	w.WriteHeader(http.StatusNoContent)
}

// DeleteREST is the exported wrapper that the session-authed /api/v1 shim
// mounts at DELETE /api/v1/projects/{name}/repos/rpm/{repo}/packages/{filename}.
// It dispatches to the same internal logic as the protocol-native
// DELETE route; resolveRepo handles the {name} → {project} URL-param fallback.
func (h *Handler) DeleteREST(w http.ResponseWriter, r *http.Request) {
	h.delete(w, r)
}

// findByFilename queries rpm_packages by (repo, filename). Returns
// (nil, nil) when no row exists.
func (h *Handler) findByFilename(ctx context.Context, repoID int64, filename string) (*metadata.RPMPackage, error) {
	// Filename column has no UNIQUE constraint at the schema level so
	// we take the first match (filename is unique-by-convention because
	// canonical names are <name>-<ver>-<rel>.<arch>.rpm).
	var p metadata.RPMPackage
	var uploaded string
	err := h.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, name, epoch, version, release, arch,
		       summary, description, license, url, source_rpm,
		       size_bytes, digest, filename, uploaded_at
		FROM rpm_packages WHERE repo_id=? AND filename=? LIMIT 1
	`, repoID, filename).Scan(
		&p.ID, &p.RepoID, &p.Name, &p.Epoch, &p.Version, &p.Release, &p.Arch,
		&p.Summary, &p.Description, &p.License, &p.URL, &p.SourceRPM,
		&p.SizeBytes, &p.Digest, &p.Filename, &uploaded,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}
