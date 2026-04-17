package deb

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/audit"
)

// servePublicKey GET /<project>/deb/<repo>/public-key.asc.
// Lock-free on cache hit. Public keys are public by definition, but we still
// gate through actorCanRead so truly private repos don't leak existence to
// unauthenticated callers.
func (h *Handler) servePublicKey(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
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

// serveDistsFile GET /<project>/deb/<repo>/dists/* — lock-free disk serve of
// regen-produced files (InRelease, Release, Release.gpg, Packages, Packages.gz).
func (h *Handler) serveDistsFile(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// chi captures wildcard after "dists/" — reconstruct the full path for
	// validation but the on-disk join uses the tail alone (dists/ is a
	// literal prefix of the filesystem layout too).
	clean, perr := validateDistsSubpath(res.rest)
	if perr != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	abs := filepath.Join(h.repoRoot, res.project.Name, "deb", res.repo.Name, "dists", filepath.FromSlash(clean))
	serveFile(w, r, abs, contentTypeForDists(clean))
}

// servePoolPackage GET/HEAD /<project>/deb/<repo>/pool/* — .deb download
// (severity-gated).
func (h *Handler) servePoolPackage(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	poolPath, perr := validatePoolSubpath("pool/" + res.rest)
	if perr != nil {
		http.Error(w, "invalid pool path", http.StatusBadRequest)
		return
	}
	filename := poolPath[strings.LastIndex(poolPath, "/")+1:]

	if h.severityGate != nil {
		blocked, severity, scanID := h.severityGate(r.Context(), res.repo.ID, "deb", filename)
		if blocked {
			h.auditEvent(r, audit.EvtDEBUpload, filename, "blocked", map[string]any{
				"severity": severity, "scan_id": scanID,
			})
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"blocked_by_scan","severity":%q,"scan_id":%d}`, severity, scanID)
			return
		}
	}
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(storageKeyForPool(res.project.Name, res.repo.Name, poolPath)))
	serveFile(w, r, abs, "application/vnd.debian.binary-package")
}

// serveFile is the shared lock-free disk-serve helper.
func serveFile(w http.ResponseWriter, r *http.Request, abs, contentType string) {
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "deb.serve.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", abs),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "deb.serve.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", abs),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

// contentTypeForDists dispatches on the last path segment for the metadata
// files under dists/.
func contentTypeForDists(cleanPath string) string {
	base := cleanPath
	if i := strings.LastIndex(cleanPath, "/"); i >= 0 {
		base = cleanPath[i+1:]
	}
	switch {
	case base == "InRelease":
		return "text/plain; charset=utf-8"
	case base == "Release":
		return "text/plain; charset=utf-8"
	case base == "Release.gpg":
		return "application/pgp-signature"
	case base == "Packages":
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(base, ".gz"):
		return "application/gzip"
	default:
		return "application/octet-stream"
	}
}
