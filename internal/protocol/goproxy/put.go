package goproxy

import (
	"archive/zip"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/common"
)

// maxGoModBytes caps the go.mod extracted from an uploaded zip; matches
// x/mod/zip.MaxGoMod (16 MiB).
const maxGoModBytes = int64(16) << 20

// put handles PUT /<project>/go/<repo>/<module>/@v/<version>.zip.
//
// Flow:
//  1. Resolve repo + parse module/version; only .zip targets accepted.
//  2. Auth: maintainer-required (ActionGoUpload).
//  3. Stage body via common.StageBody (sha256 in one pass).
//  4. Validate the archive against <module>@<version> with
//     x/mod/zip.CheckZip — wrong prefix, oversized files, traversal, and
//     case-collisions are all rejected → 400 invalid_module.
//  5. Extract go.mod (synthesize "module <path>" when absent).
//  6. Promote zip via PathStore; write .mod sidecar.
//  7. Writer tx: go_modules upsert + artifacts_fts refresh.
//  8. Audit EvtGoUpload → 201.
func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if res.req.Op != "zip" {
		http.Error(w, "publish by PUT of <module>/@v/<version>.zip", http.StatusBadRequest)
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionGoUpload) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	defer func() { _ = r.Body.Close() }()
	target := res.req.ModulePath + "@" + res.req.Version

	st, ok := common.StageBody(w, r, h.repoRoot, "go", "go-upload-*.zip", target, h.maxPutBytes)
	if !ok {
		return
	}
	tmpPath := st.TmpPath
	defer func() { _ = os.Remove(tmpPath) }()

	mv := module.Version{Path: res.req.ModulePath, Version: res.req.Version}
	if _, err := modzip.CheckZip(mv, tmpPath); err != nil {
		h.auditEvent(r, audit.EvtGoUpload, target, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "invalid_module",
			"error":   err.Error(),
		})
		http.Error(w, "invalid_module: "+err.Error(), http.StatusBadRequest)
		return
	}

	goMod, err := extractGoMod(tmpPath, mv)
	if err != nil {
		h.auditEvent(r, audit.EvtGoUpload, target, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "invalid_gomod",
			"error":   err.Error(),
		})
		http.Error(w, "invalid_module: "+err.Error(), http.StatusBadRequest)
		return
	}

	digest := "sha256:" + st.Sum256
	zipKey := storageKeyFor(res.project.Name, res.repo.Name, res.req.EscapedPath, res.req.Version, "zip")
	modKey := storageKeyFor(res.project.Name, res.repo.Name, res.req.EscapedPath, res.req.Version, "mod")

	if !common.PromoteStaged(w, r, h.pathStore, "go", zipKey, tmpPath, target) {
		return
	}
	if _, err := h.pathStore.Put(r.Context(), modKey, strings.NewReader(goMod)); err != nil {
		_ = h.pathStore.Delete(r.Context(), zipKey)
		slog.ErrorContext(r.Context(), "goproxy.put.mod_write_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("module", target),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := h.goModules.Insert(r.Context(), tx, &metadata.GoModule{
			RepoID:     res.repo.ID,
			ModulePath: res.req.ModulePath,
			Version:    res.req.Version,
			SizeBytes:  st.Size,
			Digest:     digest,
		}); err != nil {
			return err
		}
		if err := metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, digest); err != nil {
			return err
		}
		return metadata.IndexArtifact(r.Context(), tx, res.repo.ID, res.req.ModulePath, res.req.Version, digest)
	}); err != nil {
		// Roll back the on-disk artifacts when the metadata tx fails.
		_ = h.pathStore.Delete(r.Context(), zipKey)
		_ = h.pathStore.Delete(r.Context(), modKey)
		slog.ErrorContext(r.Context(), "goproxy.put.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("module", target),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	h.auditEvent(r, audit.EvtGoUpload, target, "ok", map[string]any{
		"project":    res.project.Name,
		"repo":       res.repo.Name,
		"module":     res.req.ModulePath,
		"version":    res.req.Version,
		"size_bytes": st.Size,
		"digest":     digest,
	})

	w.Header().Set("Location", r.URL.Path)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
}

// extractGoMod pulls <module>@<version>/go.mod out of the validated zip.
// Modules without a go.mod (pre-modules tags) get a synthesized minimal
// one, mirroring what the go command does for legacy modules.
func extractGoMod(zipPath string, mv module.Version) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	defer func() { _ = zr.Close() }()

	want := mv.Path + "@" + mv.Version + "/go.mod"
	for _, f := range zr.File {
		if f.Name != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("open go.mod: %w", err)
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(io.LimitReader(rc, maxGoModBytes+1))
		if err != nil {
			return "", fmt.Errorf("read go.mod: %w", err)
		}
		if int64(len(b)) > maxGoModBytes {
			return "", fmt.Errorf("go.mod exceeds %d bytes", maxGoModBytes)
		}
		return string(b), nil
	}
	return "module " + mv.Path + "\n", nil
}
