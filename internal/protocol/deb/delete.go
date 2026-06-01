package deb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// delete handles DELETE /<project>/deb/<repo>/pool/<c>/<pkg>/<filename>.deb.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
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
	poolPath, perr := validatePoolSubpath("pool/" + res.rest)
	if perr != nil {
		http.Error(w, "invalid pool path", http.StatusBadRequest)
		return
	}
	filename := poolPath[strings.LastIndex(poolPath, "/")+1:]

	// Look up by the exact pool path (storage_pool_path), not the basename: two
	// packages can share a filename under different pool paths, and the rows we
	// delete must be exactly the ones backing the file we trash. storage_pool_path
	// is stored as this same validated pool path at upload (put.go).
	row, err := h.findByPoolPath(r.Context(), res.repo.ID, poolPath)
	if err != nil || row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Atomic delete ordering: stat first
	// to learn whether the file is on disk (preserves the partial-state
	// heal — when the file is missing but the row is present, we still
	// drop the row in tx and skip trash.Move). Then commit the DB tx
	// BEFORE trash.Move so a tx rollback never moves the file.
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(storageKeyForPool(res.project.Name, res.repo.Name, poolPath)))
	fileOnDisk := false
	if _, err := os.Stat(abs); err == nil {
		fileOnDisk = true
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.ErrorContext(r.Context(), "deb.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	// (else ENOENT → fileOnDisk stays false; the tx heals the orphaned row.)

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		// A pool file is shared by every suite the package is published to
		// (one deb_packages row per suite, same storage_pool_path). Remove
		// ALL of them before the file is trashed below — deleting only the
		// arbitrary LIMIT 1 row left the other suites pointing at a trashed
		// file (downloads 404'd while the package still appeared in those
		// suites' Packages indexes).
		if _, err := h.debPackages.DeleteByStoragePoolPath(r.Context(), tx, res.repo.ID, poolPath); err != nil {
			return err
		}
		if err := metadata.IndexDEBDelete(r.Context(), tx, res.repo.ID, row.Package, row.Version, row.Architecture); err != nil {
			return err
		}
		return h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		slog.ErrorContext(r.Context(), "deb.delete.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
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
		if _, err := h.trash.Move(r.Context(), abs, "deb-package", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.WarnContext(r.Context(), "deb.delete.trash_failed_post_commit",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("filename", filename),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}

	if h.coalescer != nil {
		h.coalescer.Get(res.repo.ID).Kick()
	}

	h.auditEvent(r, audit.EvtDEBDelete, filename, "ok", map[string]any{
		"project":  res.project.Name,
		"repo":     res.repo.Name,
		"package":  row.Package,
		"version":  row.Version,
		"arch":     row.Architecture,
		"filename": filename,
	})

	w.WriteHeader(http.StatusNoContent)
}

// DeleteREST is the exported wrapper that the session-authed /api/v1 shim
// mounts at DELETE /api/v1/projects/{name}/repos/deb/{repo}/pool/*.
// It dispatches to the same internal logic as the protocol-native DELETE
// route; resolveRepo handles the {name} → {project} URL-param fallback.
func (h *Handler) DeleteREST(w http.ResponseWriter, r *http.Request) {
	h.delete(w, r)
}

// findByPoolPath returns any one deb_packages row backing the on-disk pool file
// at storagePoolPath (rows for the same file across suites share it; LIMIT 1
// gives the shared package/version/arch metadata). Returns (nil, nil) on miss.
func (h *Handler) findByPoolPath(ctx context.Context, repoID int64, storagePoolPath string) (*metadata.DEBPackage, error) {
	var p metadata.DEBPackage
	var uploaded string
	err := h.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, suite_id, package, version, architecture,
		       maintainer, section, priority, depends, description,
		       size_bytes, digest, filename, storage_pool_path, uploaded_at
		FROM deb_packages WHERE repo_id=? AND storage_pool_path=? LIMIT 1
	`, repoID, storagePoolPath).Scan(
		&p.ID, &p.RepoID, &p.SuiteID, &p.Package, &p.Version, &p.Architecture,
		&p.Maintainer, &p.Section, &p.Priority, &p.Depends, &p.Description,
		&p.SizeBytes, &p.Digest, &p.Filename, &p.StoragePoolPath, &uploaded,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// patchSuites handles PATCH /<project>/deb/<repo>/suites.
//
// Request body:
//
//	{"add": [{"suite":"unstable","component":"main","architecture":"i386"}]}
//
// Adds rows to apt_suites. Idempotent: the underlying UNIQUE constraint means
// duplicate triples are silently merged (InsertBatch upserts).
func (h *Handler) patchSuites(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
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
	var req struct {
		Add []struct {
			Suite        string `json:"suite"`
			Component    string `json:"component"`
			Architecture string `json:"architecture"`
		} `json:"add"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(req.Add) == 0 {
		http.Error(w, "empty add list", http.StatusBadRequest)
		return
	}
	rows := make([]metadata.AptSuite, 0, len(req.Add))
	for _, e := range req.Add {
		if e.Suite == "" || e.Component == "" || e.Architecture == "" {
			http.Error(w, "suite/component/architecture required", http.StatusBadRequest)
			return
		}
		rows = append(rows, metadata.AptSuite{
			RepoID: res.repo.ID, Suite: e.Suite, Component: e.Component, Architecture: e.Architecture,
		})
	}
	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		return h.aptSuites.InsertBatch(r.Context(), tx, res.repo.ID, rows)
	}); err != nil {
		slog.ErrorContext(r.Context(), "deb.suites.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"added": len(rows)})
}
