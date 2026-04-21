package pypi

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
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// handleLegacyUpload services POST /<project>/pypi/<repo>/legacy/.
//
// twine / uv publish send a multipart/form-data body with these fields:
//   - name, version          — informational; the parsed wheel/sdist
//     metadata is canonical.
//   - filetype               — "bdist_wheel" or "sdist".
//   - sha256_digest          — optional, client-precomputed digest.
//   - content                — the actual file payload.
//
// Flow:
//  1. Resolve repo + auth (project member).
//  2. Cap body via MaxBytesReader (T-03-03-04).
//  3. Parse multipart, stream "content" to a tmp file while hashing.
//  4. Verify client digest if supplied.
//  5. Parse wheel METADATA / sdist PKG-INFO from the tmp path.
//  6. Promote tmp into <repoRoot>/.../packages/<filename> via PathStore.
//  7. Single writer tx: pypi_files upsert + pypi_fts delete+insert +
//     SetMetadataState('dirty') + optional auto-scan enqueue.
//  8. Coalescer kick → triggers /simple/ regen.
//  9. Audit EvtPyPIUpload; respond 200 (twine expects 200).
func (h *Handler) handleLegacyUpload(w http.ResponseWriter, r *http.Request) {
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

	r.Body = http.MaxBytesReader(w, r.Body, h.maxPutBytes)
	defer func() { _ = r.Body.Close() }()

	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "expected multipart/form-data", http.StatusBadRequest)
		return
	}

	tmpDir := filepath.Join(h.repoRoot, ".tmp-pypi-uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		slog.ErrorContext(r.Context(), "pypi.legacy.mkdir_tmp_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	var (
		filetype     string
		clientDigest string
		filename     string
		tmpPath      string
		size         int64
		digest       string
	)
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			slog.ErrorContext(r.Context(), "pypi.legacy.multipart_failed",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.Any("err", err),
			)
			http.Error(w, "invalid multipart body", http.StatusBadRequest)
			return
		}
		switch part.FormName() {
		case "filetype":
			b, _ := io.ReadAll(io.LimitReader(part, 64))
			filetype = strings.TrimSpace(string(b))
		case "sha256_digest":
			b, _ := io.ReadAll(io.LimitReader(part, 128))
			clientDigest = strings.TrimSpace(strings.ToLower(string(b)))
		case "content":
			fname := part.FileName()
			if fname == "" {
				_ = part.Close()
				http.Error(w, "content part missing filename", http.StatusBadRequest)
				return
			}
			cleaned, ferr := validateFilename(fname)
			if ferr != nil {
				_ = part.Close()
				http.Error(w, "invalid filename", http.StatusBadRequest)
				return
			}
			filename = cleaned

			tf, ferr := os.CreateTemp(tmpDir, "pypi-upload-*")
			if ferr != nil {
				_ = part.Close()
				slog.ErrorContext(r.Context(), "pypi.legacy.tmp_create_failed",
					slog.String("incident_id", chimw.GetReqID(r.Context())),
					slog.Any("err", ferr),
				)
				http.Error(w, "storage error", http.StatusInternalServerError)
				return
			}
			tmpPath = tf.Name()
			hasher := sha256.New()
			n, ferr := io.Copy(io.MultiWriter(tf, hasher), part)
			_ = part.Close()
			_ = tf.Close()
			if ferr != nil {
				var maxErr *http.MaxBytesError
				if errors.As(ferr, &maxErr) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
				slog.ErrorContext(r.Context(), "pypi.legacy.tmp_write_failed",
					slog.String("incident_id", chimw.GetReqID(r.Context())),
					slog.String("tmp_path", tmpPath),
					slog.Any("err", ferr),
				)
				http.Error(w, "storage error", http.StatusInternalServerError)
				return
			}
			size = n
			digest = "sha256:" + hex.EncodeToString(hasher.Sum(nil))
		default:
			// Drain unknown parts without buffering.
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
		}
	}

	if filename == "" || tmpPath == "" {
		http.Error(w, "missing content part", http.StatusBadRequest)
		return
	}
	if clientDigest != "" {
		want := strings.TrimPrefix(clientDigest, "sha256:")
		got := strings.TrimPrefix(digest, "sha256:")
		if want != got {
			http.Error(w, "sha256_digest mismatch", http.StatusBadRequest)
			return
		}
	}

	// Parse wheel or sdist based on filename / filetype.
	var (
		parsed *File
		perr   error
	)
	switch {
	case strings.HasSuffix(filename, ".whl"), filetype == "bdist_wheel":
		parsed, perr = ParseWheelAs(tmpPath, filename)
	case strings.HasSuffix(filename, ".tar.gz"), strings.HasSuffix(filename, ".tgz"),
		strings.HasSuffix(filename, ".zip"), filetype == "sdist":
		parsed, perr = ParseSdistAs(tmpPath, filename)
	default:
		perr = fmt.Errorf("unknown filetype/extension")
	}
	if perr != nil {
		h.auditEvent(r, audit.EvtPyPIUpload, filename, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "invalid_package",
			"error":   perr.Error(),
		})
		http.Error(w, "invalid_package: "+perr.Error(), http.StatusBadRequest)
		return
	}
	parsed.Filename = filename
	parsed.Digest = digest
	parsed.SizeBytes = size

	// Promote tmp file into PathStore.
	tmpBytes, err := os.ReadFile(tmpPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "pypi.legacy.read_tmp_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	storageKey := packageStorageKey(res.project.Name, res.repo.Name, filename)
	if _, err := h.pathStore.Put(r.Context(), storageKey, bytes.NewReader(tmpBytes)); err != nil {
		slog.ErrorContext(r.Context(), "pypi.legacy.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	if err := h.commitPyPIRow(r, res, parsed); err != nil {
		// HI-02: roll back the on-disk artifact when metadata tx fails.
		_ = h.pathStore.Delete(r.Context(), storageKey)
		slog.ErrorContext(r.Context(), "pypi.legacy.commit_failed",
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

	h.auditEvent(r, audit.EvtPyPIUpload, filename, "ok", map[string]any{
		"project":            res.project.Name,
		"repo":               res.repo.Name,
		"project_normalized": parsed.ProjectNormalized,
		"version":            parsed.Version,
		"kind":               parsed.Kind,
		"size_bytes":         parsed.SizeBytes,
		"digest":             parsed.Digest,
		"filename":           filename,
	})

	// twine expects 200, not 201.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","filename":%q}`, filename)
}

// commitPyPIRow performs the standard writer tx: upsert pypi_files,
// refresh pypi_fts (delete + insert keyed on
// repo_id+name+version+arch_or_runtime), flip metadata_state to dirty,
// optionally enqueue a scan.
func (h *Handler) commitPyPIRow(r *http.Request, res resolved, p *File) error {
	return h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := h.pypiFiles.Insert(r.Context(), tx, &metadata.PyPIFile{
			RepoID:            res.repo.ID,
			ProjectNormalized: p.ProjectNormalized,
			Version:           p.Version,
			Filename:          p.Filename,
			Kind:              p.Kind,
			RequiresPython:    p.RequiresPython,
			SizeBytes:         p.SizeBytes,
			Digest:            p.Digest,
			CoreMetadataJSON:  p.MarshalCoreMetadata(),
		}); err != nil {
			return err
		}
		if err := metadata.IndexPyPIDelete(r.Context(), tx, res.repo.ID,
			p.ProjectNormalized, p.Version, p.RequiresPython); err != nil {
			return err
		}
		if err := metadata.IndexPyPI(r.Context(), tx, res.repo.ID,
			p.ProjectNormalized, p.Version, p.RequiresPython, p.Summary); err != nil {
			return err
		}
		if err := h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty); err != nil {
			return err
		}
		if res.repo.AutoScan && h.scans != nil {
			if _, err := h.scans.Enqueue(r.Context(), tx, res.repo.ID, "pypi", p.Filename); err != nil {
				return err
			}
		}
		return nil
	})
}

// deletePackage handles DELETE /<project>/pypi/<repo>/packages/<filename>.
func (h *Handler) deletePackage(w http.ResponseWriter, r *http.Request) {
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
	filename, err := validateFilename(chi.URLParam(r, "filename"))
	if err != nil {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	row, err := h.pypiFiles.FindByFilename(r.Context(), res.repo.ID, filename)
	if err != nil || row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(
		packageStorageKey(res.project.Name, res.repo.Name, filename),
	))
	if _, statErr := os.Stat(abs); statErr == nil {
		_, _ = h.trash.Move(r.Context(), abs, "pypi-file", res.repo.ID, auth.ActorLoginFromContext(r.Context()))
	}
	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.pypiFiles.Delete(r.Context(), tx, row.ID); err != nil {
			return err
		}
		if err := metadata.IndexPyPIDelete(r.Context(), tx, res.repo.ID,
			row.ProjectNormalized, row.Version, row.RequiresPython); err != nil {
			return err
		}
		return h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		slog.ErrorContext(r.Context(), "pypi.delete.commit_failed",
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
	h.auditEvent(r, audit.EvtPyPIDelete, filename, "ok", map[string]any{
		"project":            res.project.Name,
		"repo":               res.repo.Name,
		"project_normalized": row.ProjectNormalized,
		"version":            row.Version,
		"filename":           filename,
	})
	w.WriteHeader(http.StatusNoContent)
}
