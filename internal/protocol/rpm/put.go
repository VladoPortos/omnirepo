package rpm

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// put handles PUT /<project>/rpm/<repo>/packages/<filename>.
//
// Flow:
//  1. Resolve repo + auth (project member).
//  2. Cap body via MaxBytesReader.
//  3. Stream body straight to a tmp file under the repo root via
//     io.MultiWriter(tmpF, sha256.Hasher) so rpm.Parse can open it
//     (memory bounded by OS write buffer, not artifact size).
//  4. Parse RPM header → reject 400 invalid_package on failure.
//  5. Validate filename matches NEVRA — defense in depth.
//  6. Re-open tmp file and promote via PathStore.Put (atomic temp+fsync+rename).
//  7. Single writer tx: rpm_packages upsert + IndexRPMDelete + IndexRPM +
//     SetMetadataState(dirty) + optional auto-scan enqueue.
//  8. After commit: audit + coalescer.Kick → 201.
func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r, true)
	if !ok {
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionRPMUpload) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxPutBytes)
	defer func() { _ = r.Body.Close() }()

	// Stream the request body straight to a temp file under the repo root
	// while computing sha256 in one pass. Memory consumption stays bounded
	// by the OS write buffer (~256 KiB) regardless of artifact size — the
	// previous full-body in-memory staging pattern let an authenticated
	// project member drive container RSS toward the 5 GiB upload cap.
	tmpDir := filepath.Join(h.repoRoot, ".tmp-rpm-uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		slog.ErrorContext(r.Context(), "rpm.put.mkdir_tmp_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpF, err := os.CreateTemp(tmpDir, "rpm-upload-*.rpm")
	if err != nil {
		slog.ErrorContext(r.Context(), "rpm.put.tmp_create_failed",
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
		slog.ErrorContext(r.Context(), "rpm.put.read_body_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if err := tmpF.Close(); err != nil {
		slog.ErrorContext(r.Context(), "rpm.put.tmp_close_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	parsed, perr := Parse(tmpPath)
	if perr != nil {
		h.auditEvent(r, audit.EvtRPMUpload, res.filename, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "invalid_package",
			"error":   perr.Error(),
		})
		http.Error(w, "invalid_package: "+perr.Error(), http.StatusBadRequest)
		return
	}
	// Enforce the NEVRA-filename contract promised by the step-5 doc comment
	// above. primary.xml's <location href> is built
	// from canonicalFilename() (NEVRA-derived), but the on-disk storage key
	// and the GET route use the URL-path filename verbatim. If the two drift,
	// dnf clients 404 on every package download — the metadata says `get
	// packages/<NEVRA>.rpm`, the server has `packages/<uploaded-name>.rpm`.
	// Reject at upload time so the mismatch can never land in the repo.
	expected := parsed.canonicalFilename()
	if res.filename != expected {
		h.auditEvent(r, audit.EvtRPMUpload, res.filename, "rejected", map[string]any{
			"project":           res.project.Name,
			"repo":              res.repo.Name,
			"reason":            "filename_nevra_mismatch",
			"expected_filename": expected,
		})
		http.Error(w,
			"filename_mismatch: RPM header NEVRA requires filename "+expected,
			http.StatusBadRequest)
		return
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	parsed.Digest = digest

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.filename)
	// Re-open the temp file as the source for PathStore.Put — the OS page
	// cache covers the bytes we just wrote, and *os.File satisfies io.Reader
	// without allocating a full-body buffer.
	putF, err := os.Open(tmpPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "rpm.put.tmp_reopen_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if _, err := h.pathStore.Put(r.Context(), storageKey, putF); err != nil {
		_ = putF.Close()
		slog.ErrorContext(r.Context(), "rpm.put.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	_ = putF.Close()

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := h.rpmPackages.Insert(r.Context(), tx, &metadata.RPMPackage{
			RepoID:      res.repo.ID,
			Name:        parsed.Name,
			Epoch:       parsed.Epoch,
			Version:     parsed.Version,
			Release:     parsed.Release,
			Arch:        parsed.Arch,
			Summary:     parsed.Summary,
			Description: parsed.Description,
			License:     parsed.License,
			URL:         parsed.URL,
			SourceRPM:   parsed.SourceRPM,
			SizeBytes:   size,
			Digest:      "sha256:" + digest,
			Filename:    res.filename,
			FilesJSON:   MarshalFiles(parsed.Files),
		}); err != nil {
			return err
		}
		if err := metadata.IndexRPMDelete(r.Context(), tx, res.repo.ID, parsed.Name, parsed.Version, parsed.Arch); err != nil {
			return err
		}
		if err := metadata.IndexRPM(r.Context(), tx, res.repo.ID, parsed.Name, parsed.Version, parsed.Arch, parsed.Summary); err != nil {
			return err
		}
		if err := h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty); err != nil {
			return err
		}
		if res.repo.AutoScan && h.scans != nil {
			if _, err := h.scans.Enqueue(r.Context(), tx, res.repo.ID, "rpm", res.filename); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		// Roll back the on-disk artifact when the metadata tx fails.
		_ = h.pathStore.Delete(r.Context(), storageKey)
		slog.ErrorContext(r.Context(), "rpm.put.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	if h.coalescer != nil {
		h.coalescer.Get(res.repo.ID).Kick()
	}

	h.auditEvent(r, audit.EvtRPMUpload, res.filename, "ok", map[string]any{
		"project":  res.project.Name,
		"repo":     res.repo.Name,
		"name":     parsed.Name,
		"epoch":    parsed.Epoch,
		"version":  parsed.Version,
		"release":  parsed.Release,
		"arch":     parsed.Arch,
		"size":     size,
		"sha256":   digest,
		"filename": res.filename,
	})

	w.Header().Set("Location", r.URL.Path)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
}
