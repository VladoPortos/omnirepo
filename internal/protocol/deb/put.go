package deb

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// put handles PUT /<project>/deb/<repo>/pool/<c>/<pkg>/<filename>.deb.
//
// Flow:
//  1. Resolve repo + auth (project member).
//  2. Validate pool subpath (traversal, NUL, .deb suffix).
//  3. Cap body via MaxBytesReader; tee into sha256 + in-memory buffer.
//  4. Stage to tmp file; Parse → 400 invalid_package on failure.
//  5. Architecture comes from ctrl.Architecture (NOT the client). suite/
//     component come from ?suite= / ?component= query params, with sensible
//     defaults (first-declared suite, "main").
//  6. apt_suites.FindByTuple(repo, suite, component, arch) → 400
//     unknown_suite_or_component on miss.
//  7. Promote tmp → pool path via PathStore.Put (atomic).
//  8. One writer tx: deb_packages upsert + IndexDEBDelete + IndexDEB +
//     SetMetadataState(dirty) + optional auto-scan enqueue.
//  9. Audit + coalescer.Kick → 201.
func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
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

	// chi's wildcard captures only what follows "pool/", so res.rest is the
	// relative tail (e.g. "m/mypkg/mypkg_1.0-1_amd64.deb"). Rebuild the full
	// pool-relative path here for storage key + validation.
	tail, perr := validatePoolSubpath("pool/" + res.rest)
	if perr != nil {
		http.Error(w, "invalid pool path: "+perr.Error(), http.StatusBadRequest)
		return
	}
	poolPath := tail
	filename := path.Base(poolPath)

	r.Body = http.MaxBytesReader(w, r.Body, h.maxPutBytes)
	defer func() { _ = r.Body.Close() }()

	var buf bytes.Buffer
	hasher := sha256.New()
	tee := io.TeeReader(r.Body, hasher)
	size, err := io.Copy(&buf, tee)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		slog.ErrorContext(r.Context(), "deb.put.read_body_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Parse control from the in-memory body.
	ctrl, perr := ParseDeb(bytes.NewReader(buf.Bytes()))
	if perr != nil {
		h.auditEvent(r, audit.EvtDEBUpload, filename, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "invalid_package",
			"error":   perr.Error(),
		})
		http.Error(w, "invalid_package: "+perr.Error(), http.StatusBadRequest)
		return
	}

	// Suite/component come from query params. Defaults per D-24: first-declared
	// suite from apt_suites for this repo, component="main".
	suite := strings.TrimSpace(r.URL.Query().Get("suite"))
	component := strings.TrimSpace(r.URL.Query().Get("component"))
	if component == "" {
		component = "main"
	}
	if suite == "" {
		rows, err := h.aptSuites.ListByRepo(r.Context(), res.repo.ID)
		if err == nil && len(rows) > 0 {
			suite = rows[0].Suite
		} else {
			suite = "stable"
		}
	}

	// Arch from control paragraph (NOT client). Validated against apt_suites.
	suiteRow, err := h.aptSuites.FindByTuple(r.Context(), res.repo.ID, suite, component, ctrl.Architecture)
	if err != nil || suiteRow == nil {
		h.auditEvent(r, audit.EvtDEBUpload, filename, "rejected", map[string]any{
			"project":   res.project.Name,
			"repo":      res.repo.Name,
			"reason":    "unknown_suite_or_component",
			"suite":     suite,
			"component": component,
			"arch":      ctrl.Architecture,
		})
		http.Error(w,
			fmt.Sprintf("unknown_suite_or_component: (%s, %s, %s) not declared", suite, component, ctrl.Architecture),
			http.StatusBadRequest)
		return
	}

	digest := hex.EncodeToString(hasher.Sum(nil))

	// Write to pool via PathStore (atomic tmp+fsync+rename underneath).
	storageKey := storageKeyForPool(res.project.Name, res.repo.Name, poolPath)
	if _, err := h.pathStore.Put(r.Context(), storageKey, bytes.NewReader(buf.Bytes())); err != nil {
		slog.ErrorContext(r.Context(), "deb.put.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := h.debPackages.Insert(r.Context(), tx, &metadata.DEBPackage{
			RepoID:       res.repo.ID,
			SuiteID:      suiteRow.ID,
			Package:      ctrl.Package,
			Version:      ctrl.Version,
			Architecture: ctrl.Architecture,
			Maintainer:   ctrl.Maintainer,
			Section:      ctrl.Section,
			Priority:     ctrl.Priority,
			Depends:      ctrl.Depends,
			Description:  ctrl.Description,
			SizeBytes:    size,
			Digest:       "sha256:" + digest,
			Filename:     filename,
		}); err != nil {
			return err
		}
		if err := metadata.IndexDEBDelete(r.Context(), tx, res.repo.ID, ctrl.Package, ctrl.Version, ctrl.Architecture); err != nil {
			return err
		}
		if err := metadata.IndexDEB(r.Context(), tx, res.repo.ID, ctrl.Package, ctrl.Version, ctrl.Architecture, firstLine(ctrl.Description)); err != nil {
			return err
		}
		if err := h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty); err != nil {
			return err
		}
		if res.repo.AutoScan && h.scans != nil {
			if _, err := h.scans.Enqueue(r.Context(), tx, res.repo.ID, "deb", filename); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		// Best-effort: attempt to remove the pool file that made it to disk.
		_ = os.Remove(filepath.Join(h.repoRoot, filepath.FromSlash(storageKey)))
		slog.ErrorContext(r.Context(), "deb.put.commit_failed",
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

	h.auditEvent(r, audit.EvtDEBUpload, filename, "ok", map[string]any{
		"project":   res.project.Name,
		"repo":      res.repo.Name,
		"package":   ctrl.Package,
		"version":   ctrl.Version,
		"arch":      ctrl.Architecture,
		"suite":     suite,
		"component": component,
		"size":      size,
		"sha256":    digest,
		"filename":  filename,
	})

	w.Header().Set("Location", r.URL.Path)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
}

// firstLine returns the first line of s (up to the first newline).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
