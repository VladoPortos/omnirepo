package rpm

import (
	"context"
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
	if !h.actorIsProjectMember(r.Context(), actor, res.project.ID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Locate the rpm_packages row by filename.
	row, err := h.findByFilename(r.Context(), res.repo.ID, res.filename)
	if err != nil || row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	abs := filepath.Join(h.repoRoot, filepath.FromSlash(storageKeyFor(res.project.Name, res.repo.Name, res.filename)))
	if _, err := os.Stat(abs); err == nil {
		if _, err := h.trash.Move(r.Context(), abs, "rpm-package", res.repo.ID); err != nil {
			http.Error(w, fmt.Sprintf("trash: %v", err), http.StatusInternalServerError)
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, fmt.Sprintf("stat: %v", err), http.StatusInternalServerError)
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.rpmPackages.Delete(r.Context(), tx, row.ID); err != nil {
			return err
		}
		if err := metadata.IndexRPMDelete(r.Context(), tx, res.repo.ID, row.Name, row.Version, row.Arch); err != nil {
			return err
		}
		return h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
		return
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
