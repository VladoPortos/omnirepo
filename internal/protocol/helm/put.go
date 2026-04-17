package helm

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
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

// put handles PUT /<project>/helm/<repo>/charts/<filename>.
//
// Dispatches on the filename:
//   - <name>-<ver>.tgz.prov (or *.prov) → provenance pass-through (HELM-04)
//   - <name>-<ver>.tgz                  → chart upload: parse Chart.yaml, write
//     helm_charts + helm_fts + mark repo dirty + Kick coalescer.
//   - otherwise                          → 400.
//
// Flow mirrors internal/protocol/raw/put.go with these additions:
//   - Chart.yaml parse via helm SDK loader before promote; parse failure →
//     400 invalid_package.
//   - Writer tx: helm_charts upsert + helm_fts refresh + metadata_state='dirty'
//   - optional auto-scan enqueue.
//   - After commit: coalescer.Get(repoID).Kick().
//   - Audit EvtHelmUpload (with kind=chart|provenance in details).
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

	switch {
	case isProvenance(res.filename):
		h.putProvenance(w, r, res)
	case isChartArchive(res.filename):
		h.putChart(w, r, res)
	default:
		http.Error(w, "filename must end in .tgz or .tgz.prov", http.StatusBadRequest)
	}
}

// putChart implements the chart-archive upload path.
func (h *Handler) putChart(w http.ResponseWriter, r *http.Request, res resolved) {
	// Cap body. MaxBytesReader returns *MaxBytesError on overflow.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxPutBytes)
	defer func() { _ = r.Body.Close() }()

	// Buffer + hash in one pass. Charts are small (<5 MiB typical); the
	// memory buffer lets us parse-before-promote without a second read.
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
		slog.ErrorContext(r.Context(), "helm.put.read_body_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Write to a sibling tmp file under the repo root's uploads area so Parse
	// can open it; gofiles path avoids a second in-memory copy for the parser.
	tmpDir := filepath.Join(h.repoRoot, ".tmp-helm-uploads")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		slog.ErrorContext(r.Context(), "helm.put.mkdir_tmp_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpF, err := os.CreateTemp(tmpDir, "helm-upload-*.tgz")
	if err != nil {
		slog.ErrorContext(r.Context(), "helm.put.tmp_create_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpPath := tmpF.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmpF.Write(buf.Bytes()); err != nil {
		_ = tmpF.Close()
		slog.ErrorContext(r.Context(), "helm.put.tmp_write_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if err := tmpF.Close(); err != nil {
		slog.ErrorContext(r.Context(), "helm.put.tmp_close_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("tmp_path", tmpPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Parse Chart.yaml from the buffered tgz. Failure is a 400 per HELM-02.
	chartMeta, perr := Parse(tmpPath)
	if perr != nil {
		h.auditEvent(r, audit.EvtHelmUpload, res.filename, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "invalid_package",
			"error":   perr.Error(),
		})
		http.Error(w, "invalid_package: "+perr.Error(), http.StatusBadRequest)
		return
	}

	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	// Promote the buffered bytes into the canonical PathStore key via the
	// atomic path-store Put (temp+fsync+rename inside PathStore.Put).
	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.filename)
	if _, err := h.pathStore.Put(r.Context(), storageKey, bytes.NewReader(buf.Bytes())); err != nil {
		slog.ErrorContext(r.Context(), "helm.put.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Single writer tx: helm_charts upsert + FTS5 refresh + metadata_state
	// + optional auto-scan enqueue.
	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := h.helmCharts.Insert(r.Context(), tx, &metadata.HelmChart{
			RepoID:          res.repo.ID,
			Name:            chartMeta.Name,
			Version:         chartMeta.Version,
			AppVersion:      chartMeta.AppVersion,
			Description:     chartMeta.Description,
			KeywordsJSON:    chartMeta.KeywordsJSON(),
			MaintainersJSON: chartMeta.MaintainersJSON(),
			SizeBytes:       size,
			Digest:          digest,
			Filename:        res.filename,
		}); err != nil {
			return err
		}
		// Refresh FTS: delete then insert (composite key = repo,name,version,appVer).
		if err := metadata.IndexHelmDelete(r.Context(), tx, res.repo.ID,
			chartMeta.Name, chartMeta.Version, chartMeta.AppVersion); err != nil {
			return err
		}
		if err := metadata.IndexHelm(r.Context(), tx, res.repo.ID,
			chartMeta.Name, chartMeta.Version, chartMeta.AppVersion, chartMeta.Description); err != nil {
			return err
		}
		if err := h.repos.SetMetadataState(r.Context(), tx, res.repo.ID, metadata.MetadataStateDirty); err != nil {
			return err
		}
		if res.repo.AutoScan && h.scans != nil {
			if _, err := h.scans.Enqueue(r.Context(), tx, res.repo.ID, "helm", res.filename); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		// HI-02: roll back the chart tgz on disk when the metadata tx fails.
		_ = h.pathStore.Delete(r.Context(), storageKey)
		slog.ErrorContext(r.Context(), "helm.put.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Kick the coalescer so index.yaml gets regenerated.
	if h.coalescer != nil {
		h.coalescer.Get(res.repo.ID).Kick()
	}

	h.auditEvent(r, audit.EvtHelmUpload, res.filename, "ok", map[string]any{
		"project":     res.project.Name,
		"repo":        res.repo.Name,
		"kind":        "chart",
		"name":        chartMeta.Name,
		"version":     chartMeta.Version,
		"app_version": chartMeta.AppVersion,
		"size_bytes":  size,
		"digest":      digest,
		"filename":    res.filename,
	})

	w.Header().Set("Location", r.URL.Path)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
}

// putProvenance implements the .prov pass-through path (HELM-04). No parse,
// no DB row, no FTS, no coalescer kick — provenance files are opaque blobs
// that Helm clients verify out-of-band.
func (h *Handler) putProvenance(w http.ResponseWriter, r *http.Request, res resolved) {
	// Optional sanity check: the matching chart .tgz should exist on disk.
	// Not fatal if it doesn't (operators may upload .prov before .tgz during
	// mirroring); we just emit a warning in the audit details.
	chartName := strings.TrimSuffix(res.filename, ".prov")
	if !strings.HasSuffix(chartName, ".tgz") {
		http.Error(w, "provenance filename must match <chart>.tgz.prov", http.StatusBadRequest)
		return
	}
	chartKey := storageKeyFor(res.project.Name, res.repo.Name, chartName)
	chartAbs := filepath.Join(h.repoRoot, filepath.FromSlash(chartKey))
	chartExists := true
	if _, err := os.Stat(chartAbs); err != nil {
		chartExists = false
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxPutBytes)
	defer func() { _ = r.Body.Close() }()

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.filename)
	size, err := h.pathStore.Put(r.Context(), storageKey, r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		slog.ErrorContext(r.Context(), "helm.provenance.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	h.auditEvent(r, audit.EvtHelmUpload, res.filename, "ok", map[string]any{
		"project":      res.project.Name,
		"repo":         res.repo.Name,
		"kind":         "provenance",
		"filename":     res.filename,
		"size_bytes":   size,
		"chart_exists": chartExists,
	})

	w.Header().Set("Location", r.URL.Path)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
}
