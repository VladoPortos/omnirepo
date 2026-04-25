package pypi

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
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/httperr"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// errPyPIFileExists signals that a legacy/PEP 694 upload targets a filename
// that already has a pypi_files row in this repo. PyPI semantics (and
// walkthrough-3 §7.7) require the mutation to fail with 409 rather than
// silently overwriting a released artifact (F-07.1). The sentinel is
// returned from commitPyPIRow and matched at the call sites to emit a
// protocol-native envelope. The sync path uses PyPIFilesRepo.Insert
// directly (not commitPyPIRow) so idempotent re-mirror remains unaffected.
var errPyPIFileExists = errors.New("pypi: file already exists in repo")

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

	storageKey := packageStorageKey(res.project.Name, res.repo.Name, filename)

	// F-07.1 (wt3) Codex follow-up #2: serialize pre-check → Put →
	// commit → rollback for this (repo, filename) pair. Without the
	// lock, two concurrent first-uploads of the same filename would both
	// pass FindByFilename, both `PathStore.Put` (last-rename-wins on
	// disk), and then only one would commit a pypi_files row — leaving
	// the DB winner's digest pointing at the loser's bytes. The lock is
	// keyed by storageKey so different filenames don't contend.
	unlock := h.lockUpload(storageKey)
	defer unlock()

	// F-07.1 (wt3) Codex follow-up: pre-check existence BEFORE PathStore.Put
	// because pathstore.Put is not content-addressed — it overwrites any
	// prior blob at the same storage key, and the rollback path's Delete
	// would then unlink the **winning** upload's on-disk file. With the
	// storageKey mutex above held, this read + the writer-tx commit below
	// form one atomic section per-(repo, filename).
	if existing, ferr := h.pypiFiles.FindByFilename(r.Context(), res.repo.ID, filename); ferr == nil && existing != nil {
		h.auditEvent(r, audit.EvtPyPIUpload, filename, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "file_exists",
		})
		httperr.Write(w, r, httperr.Validation(
			"pypi.file_exists",
			"That filename already exists in this repo — delete it first if you need to replace it.",
			httperr.WithStatus(http.StatusConflict),
		))
		return
	}

	// Promote tmp file into PathStore. Re-open the staged temp file as
	// *os.File — pathstore.Put accepts io.Reader and io.Copy's the bytes
	// straight to the destination. Avoids the prior os.ReadFile + reader
	// pattern that pulled the full body back into RAM (STREAMIO-04 /
	// audit finding #3).
	putF, err := os.Open(tmpPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "pypi.legacy.tmp_reopen_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if _, err := h.pathStore.Put(r.Context(), storageKey, putF); err != nil {
		_ = putF.Close()
		slog.ErrorContext(r.Context(), "pypi.legacy.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	_ = putF.Close()

	if err := h.commitPyPIRow(r, res, parsed); err != nil {
		// F-07.1 (wt3) Codex follow-up: only delete the blob we just Put
		// when the commit failed AND we did not race into an existing row.
		// The inner Tx-check fires only on the narrow race where another
		// upload committed between our pre-check and our Insert — those
		// bytes now belong to the winner, so deleting would destroy their
		// data. Plain commit failures (DB error, etc.) still rollback.
		if errors.Is(err, errPyPIFileExists) {
			slog.WarnContext(r.Context(), "pypi.legacy.dup_race_kept_winner_blob",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("filename", filename),
			)
			h.auditEvent(r, audit.EvtPyPIUpload, filename, "rejected", map[string]any{
				"project": res.project.Name,
				"repo":    res.repo.Name,
				"reason":  "file_exists",
				"race":    true,
			})
			httperr.Write(w, r, httperr.Validation(
				"pypi.file_exists",
				"That filename already exists in this repo — delete it first if you need to replace it.",
				httperr.WithStatus(http.StatusConflict),
			))
			return
		}
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

// commitPyPIRow performs the standard writer tx: refuse duplicate filenames
// (F-07.1: PyPI semantics make released artifacts immutable — returns
// errPyPIFileExists so the caller emits 409), insert pypi_files, refresh
// pypi_fts (delete + insert keyed on repo_id+name+version+arch_or_runtime),
// flip metadata_state to dirty, optionally enqueue a scan.
//
// The duplicate check runs inside the WriteTx so a concurrent upload can't
// race around it (SQLite write serialisation plus the row-lookup-by-unique
// index means two competing uploads with the same filename both observe
// the same pre-state and the loser's Insert still trips the ON CONFLICT
// upsert, but we only reach Insert when the row was truly absent).
//
// Mirror sync path writes via PyPIFilesRepo.Insert directly (upsert-by-
// (repo_id, filename)) and never enters commitPyPIRow, so re-syncs remain
// idempotent.
func (h *Handler) commitPyPIRow(r *http.Request, res resolved, p *File) error {
	return h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if existing, err := h.pypiFiles.FindByFilenameTx(r.Context(), tx, res.repo.ID, p.Filename); err != nil {
			return err
		} else if existing != nil {
			return errPyPIFileExists
		}
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
	// Atomic delete ordering (CONTEXT D-01, D-02; audit finding #6): stat
	// first to set fileOnDisk (preserves partial-state heal). Commit the
	// DB tx BEFORE trash.Move so a tx rollback never moves the file. The
	// prior silent-discard of trash.Move's error is replaced with explicit
	// error propagation (CONTEXT D-02 — operator must see trash failures).
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(
		packageStorageKey(res.project.Name, res.repo.Name, filename),
	))
	fileOnDisk := false
	if _, statErr := os.Stat(abs); statErr == nil {
		fileOnDisk = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		slog.ErrorContext(r.Context(), "pypi.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", filename),
			slog.Any("err", statErr),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	// (else ENOENT → fileOnDisk stays false; the tx heals the orphaned row.)

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

	// Tx committed → row is gone. If the file was on disk at the start of
	// the request, move it to trash now. A failure here leaves the file
	// orphaned at abs (no row pointing at it); we surface 500 so the
	// operator notices (CONTEXT D-02, D-05).
	if fileOnDisk {
		if _, err := h.trash.Move(r.Context(), abs, "pypi-file", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.ErrorContext(r.Context(), "pypi.delete.trash_failed_post_commit",
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
	h.auditEvent(r, audit.EvtPyPIDelete, filename, "ok", map[string]any{
		"project":            res.project.Name,
		"repo":               res.repo.Name,
		"project_normalized": row.ProjectNormalized,
		"version":            row.Version,
		"filename":           filename,
	})
	w.WriteHeader(http.StatusNoContent)
}

// DeleteREST is the exported wrapper that the session-authed /api/v1 shim
// mounts at DELETE /api/v1/projects/{name}/repos/pypi/{repo}/packages/{filename}
// (F-07.2). It dispatches to the same internal logic as the protocol-native
// DELETE route; resolveRepo handles the {name} → {project} URL-param fallback.
func (h *Handler) DeleteREST(w http.ResponseWriter, r *http.Request) {
	h.deletePackage(w, r)
}
