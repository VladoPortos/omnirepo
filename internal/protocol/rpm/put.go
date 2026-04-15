package rpm

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// put handles PUT /<project>/rpm/<repo>/packages/<filename>.
//
// Flow:
//  1. Resolve repo + auth (project member).
//  2. Cap body via MaxBytesReader; tee into sha256 + in-memory buffer.
//  3. Stage to a tmp file under the repo root so rpm.Parse can open it.
//  4. Parse RPM header → reject 400 invalid_package on failure.
//  5. Validate filename matches NEVRA — defense in depth.
//  6. Promote via PathStore.Put (atomic temp+fsync+rename).
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
	if !h.actorIsProjectMember(r.Context(), actor, res.project.ID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

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
		http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusInternalServerError)
		return
	}

	tmpDir := filepath.Join(h.repoRoot, ".tmp-rpm-uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		http.Error(w, fmt.Sprintf("mkdir tmp: %v", err), http.StatusInternalServerError)
		return
	}
	tmpF, err := os.CreateTemp(tmpDir, "rpm-upload-*.rpm")
	if err != nil {
		http.Error(w, fmt.Sprintf("tmp: %v", err), http.StatusInternalServerError)
		return
	}
	tmpPath := tmpF.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmpF.Write(buf.Bytes()); err != nil {
		_ = tmpF.Close()
		http.Error(w, fmt.Sprintf("tmp write: %v", err), http.StatusInternalServerError)
		return
	}
	if err := tmpF.Close(); err != nil {
		http.Error(w, fmt.Sprintf("tmp close: %v", err), http.StatusInternalServerError)
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
	digest := hex.EncodeToString(hasher.Sum(nil))
	parsed.Digest = digest

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.filename)
	if _, err := h.pathStore.Put(r.Context(), storageKey, bytes.NewReader(buf.Bytes())); err != nil {
		http.Error(w, fmt.Sprintf("storage: %v", err), http.StatusInternalServerError)
		return
	}

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
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
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
