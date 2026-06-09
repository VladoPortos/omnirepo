package helm

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/protocol/common"
)

// getIndex serves GET /<project>/helm/<repo>/index.yaml. The file is written
// to disk by the regen coalescer (regen.go) via atomic rename; this handler
// serves the current on-disk bytes lock-free.
func (h *Handler) getIndex(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r, false)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	abs := filepath.Join(h.repoRoot, res.project.Name, "helm", res.repo.Name, "index.yaml")
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Serve an empty Helm index so `helm repo add` succeeds against
			// a fresh repo with no charts yet. Matches the helm SDK's
			// NewIndexFile output shape.
			empty := "apiVersion: v1\nentries: {}\n"
			w.Header().Set("Content-Type", "application/yaml")
			w.Header().Set("Content-Length", strconv.Itoa(len(empty)))
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, empty)
			return
		}
		slog.ErrorContext(r.Context(), "helm.index.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("repo", res.repo.Name),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "helm.index.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("repo", res.repo.Name),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// get serves GET /<project>/helm/<repo>/charts/<filename>. Chart .tgz files
// pass through the severity gate; .prov files and index files do not (they
// carry no executable content).
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r, true)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	abs := filepath.Join(h.repoRoot, filepath.FromSlash(
		storageKeyFor(res.project.Name, res.repo.Name, res.filename),
	))
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "helm.chart.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Severity gate: chart archives only.
	if h.severityGate != nil && isChartArchive(res.filename) {
		blocked, severity, scanID := h.severityGate(r.Context(), res.repo.ID, "helm", res.filename)
		if blocked {
			h.auditEvent(r, audit.EvtHelmUpload, res.filename, "blocked", map[string]any{
				"severity": severity,
				"scan_id":  scanID,
			})
			common.WriteSeverityBlocked(w, severity, scanID)
			return
		}
	}

	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "helm.chart.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	ct := contentTypeFor(res.filename)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// contentTypeFor returns the MIME type for a chart or provenance filename.
func contentTypeFor(filename string) string {
	if strings.HasSuffix(filename, ".prov") {
		return "application/pgp-signature"
	}
	if strings.HasSuffix(filename, ".tgz") {
		return "application/gzip"
	}
	return "application/octet-stream"
}
