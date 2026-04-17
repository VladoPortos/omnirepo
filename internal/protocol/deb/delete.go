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

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
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
	if !h.actorIsProjectMember(r.Context(), actor, res.project.ID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	poolPath, perr := validatePoolSubpath("pool/" + res.rest)
	if perr != nil {
		http.Error(w, "invalid pool path", http.StatusBadRequest)
		return
	}
	filename := poolPath[strings.LastIndex(poolPath, "/")+1:]

	row, err := h.findByFilename(r.Context(), res.repo.ID, filename)
	if err != nil || row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	abs := filepath.Join(h.repoRoot, filepath.FromSlash(storageKeyForPool(res.project.Name, res.repo.Name, poolPath)))
	if _, err := os.Stat(abs); err == nil {
		if _, err := h.trash.Move(r.Context(), abs, "deb-package", res.repo.ID); err != nil {
			slog.ErrorContext(r.Context(), "deb.delete.trash_failed",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("filename", filename),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.ErrorContext(r.Context(), "deb.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.debPackages.Delete(r.Context(), tx, row.ID); err != nil {
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

// findByFilename returns the deb_packages row matching (repoID, filename).
// Returns (nil, nil) on miss.
func (h *Handler) findByFilename(ctx context.Context, repoID int64, filename string) (*metadata.DEBPackage, error) {
	var p metadata.DEBPackage
	var uploaded string
	err := h.db.Reader.QueryRowContext(ctx, `
		SELECT id, repo_id, suite_id, package, version, architecture,
		       maintainer, section, priority, depends, description,
		       size_bytes, digest, filename, uploaded_at
		FROM deb_packages WHERE repo_id=? AND filename=? LIMIT 1
	`, repoID, filename).Scan(
		&p.ID, &p.RepoID, &p.SuiteID, &p.Package, &p.Version, &p.Architecture,
		&p.Maintainer, &p.Section, &p.Priority, &p.Depends, &p.Description,
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
	if !h.actorIsProjectMember(r.Context(), actor, res.project.ID) {
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
