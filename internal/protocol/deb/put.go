package deb

import (
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
//  3. Cap body via MaxBytesReader.
//  4. Stream body to tmp file under the repo root via
//     io.MultiWriter(tmpF, sha256.Hasher); re-open tmp as io.Reader for
//     ParseDeb (memory bounded by OS write buffer — STREAMIO-02).
//     Parse → 400 invalid_package on failure.
//  5. Architecture comes from ctrl.Architecture (NOT the client). suite/
//     component come from ?suite= / ?component= query params, with sensible
//     defaults (first-declared suite, "main").
//  6. apt_suites.FindByTuple(repo, suite, component, arch) → 400
//     unknown_suite_or_component on miss.
//  7. Re-open tmp file and promote to pool path via PathStore.Put (atomic).
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

	// Stream the request body to a temp file under the repo root while
	// computing sha256 in one pass. ParseDeb takes io.Reader, so after the
	// stream-write completes we re-open the file (twice — once for
	// ParseDeb, once for PathStore.Put). Two opens, not two reads of the
	// body — the OS page cache covers the actual bytes (STREAMIO-02 /
	// audit finding #3).
	tmpDir := filepath.Join(h.repoRoot, ".tmp-deb-uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		slog.ErrorContext(r.Context(), "deb.put.mkdir_tmp_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpF, err := os.CreateTemp(tmpDir, "deb-upload-*.deb")
	if err != nil {
		slog.ErrorContext(r.Context(), "deb.put.tmp_create_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpPath := tmpF.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmpF, hasher), r.Body)
	if err != nil {
		_ = tmpF.Close()
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
	if err := tmpF.Close(); err != nil {
		slog.ErrorContext(r.Context(), "deb.put.tmp_close_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Parse control from the staged tmp file (re-opened as io.Reader).
	parseF, perr := os.Open(tmpPath)
	if perr != nil {
		slog.ErrorContext(r.Context(), "deb.put.tmp_reopen_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", perr),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	ctrl, perr := ParseDeb(parseF)
	_ = parseF.Close()
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
	// Re-open the temp file as the source — *os.File satisfies io.Reader
	// without allocating a full-body buffer (STREAMIO-02).
	storageKey := storageKeyForPool(res.project.Name, res.repo.Name, poolPath)
	putF, err := os.Open(tmpPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "deb.put.tmp_reopen_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if _, err := h.pathStore.Put(r.Context(), storageKey, putF); err != nil {
		_ = putF.Close()
		slog.ErrorContext(r.Context(), "deb.put.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	_ = putF.Close()

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := h.debPackages.Insert(r.Context(), tx, &metadata.DEBPackage{
			RepoID:          res.repo.ID,
			SuiteID:         suiteRow.ID,
			Package:         ctrl.Package,
			Version:         ctrl.Version,
			Architecture:    ctrl.Architecture,
			Maintainer:      ctrl.Maintainer,
			Section:         ctrl.Section,
			Priority:        ctrl.Priority,
			Depends:         ctrl.Depends,
			Description:     ctrl.Description,
			SizeBytes:       size,
			Digest:          "sha256:" + digest,
			Filename:        filename,
			// F-T6: store the real pool-relative path so Packages.gz can emit
			// it verbatim as the Filename field (apt needs it to fetch the
			// .deb — the old synthesised path dropped `main/` and broke apt).
			StoragePoolPath: poolPath,
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
