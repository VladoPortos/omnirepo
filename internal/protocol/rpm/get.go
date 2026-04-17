package rpm

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/audit"
)

// servePublicKey GET /<project>/rpm/<repo>/public-key.asc.
// Lock-free on cache hit (D-04). Always served regardless of public_read
// state — public keys are public by definition (T-03-04-07).
func (h *Handler) servePublicKey(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r, false)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.publicKeyCache == nil {
		http.Error(w, "no signing key", http.StatusNotFound)
		return
	}
	h.publicKeyCache.ServePublicKey(w, r, res.repo.ID)
}

// serveRepodata GET /<project>/rpm/<repo>/repodata/* — lock-free disk serve
// of the regen-produced files.
func (h *Handler) serveRepodata(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r, false)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rest := chi.URLParam(r, "*")
	clean, perr := validateRepodataSubpath(rest)
	if perr != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	abs := filepath.Join(h.repoRoot, res.project.Name, "rpm", res.repo.Name, "repodata", filepath.FromSlash(clean))
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "rpm.repodata.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", clean),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "rpm.repodata.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", clean),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", contentTypeFor(clean))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

// servePackage GET/HEAD /<project>/rpm/<repo>/packages/<filename>.
func (h *Handler) servePackage(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r, true)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.severityGate != nil {
		blocked, severity, scanID := h.severityGate(r.Context(), res.repo.ID, "rpm", res.filename)
		if blocked {
			h.auditEvent(r, audit.EvtRPMUpload, res.filename, "blocked", map[string]any{
				"severity": severity,
				"scan_id":  scanID,
			})
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"blocked_by_scan","severity":%q,"scan_id":%d}`, severity, scanID)
			return
		}
	}

	abs := filepath.Join(h.repoRoot, filepath.FromSlash(storageKeyFor(res.project.Name, res.repo.Name, res.filename)))
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "rpm.package.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "rpm.package.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("filename", res.filename),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/x-rpm")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

// validateRepodataSubpath rejects "..", absolute paths, and NUL bytes.
func validateRepodataSubpath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("nul byte")
	}
	p := strings.TrimPrefix(raw, "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("invalid segment")
		}
	}
	cleaned := path.Clean("/" + p)
	if strings.HasPrefix(cleaned, "/..") {
		return "", errors.New("path escape")
	}
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned != p {
		return "", errors.New("non-canonical")
	}
	return cleaned, nil
}

func contentTypeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".xml.gz"):
		return "application/x-gzip"
	case strings.HasSuffix(name, ".xml"):
		return "application/xml"
	case strings.HasSuffix(name, ".asc"):
		return "application/pgp-signature"
	default:
		return "application/octet-stream"
	}
}
