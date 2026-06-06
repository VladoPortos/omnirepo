package raw

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// get serves GET /<project>/raw/<repo>/<path...>.
//
// If <path> resolves to a file, serves the file with Content-Type from
// mime.TypeByExtension first, then http.DetectContentType on the first 512
// bytes. If it resolves to a directory (or path is empty), defers to the
// listing handler (listing.go). 404 on unknown paths.
//
// The severity gate (h.severityGate) is consulted before serving file bytes;
// when it returns blocked=true, responds 403 with body
// {"error":"blocked_by_scan",...}.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepoAndPath(w, r, false)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.relPath)
	absPath := filepath.Join(h.repoRoot, filepath.FromSlash(storageKey))

	info, err := os.Stat(absPath)
	if err != nil {
		// Empty rel path on a fresh repo with no files yet → directory listing
		// rooted at the repo dir, even if the dir hasn't been created yet.
		if errors.Is(err, os.ErrNotExist) && res.relPath == "" {
			h.listDir(w, r, res, absPath)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "raw.get.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		h.listDir(w, r, res, absPath)
		return
	}

	// Severity gate hook (no-op until it is wired in).
	if h.severityGate != nil {
		blocked, severity, scanID := h.severityGate(r.Context(), res.repo.ID, "raw", res.relPath)
		if blocked {
			h.auditEvent(r, audit.EvtRawGetBlocked, res.relPath, "blocked", map[string]any{
				"severity": severity,
				"scan_id":  scanID,
			})
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"blocked_by_scan","severity":%q,"scan_id":%d}`, severity, scanID)
			return
		}
	}

	h.serveFile(w, r, absPath, info, res.relPath)
}

// head serves HEAD with the same headers as GET but no body.
func (h *Handler) head(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepoAndPath(w, r, false)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if h.severityGate != nil {
		blocked, _, _ := h.severityGate(r.Context(), res.repo.ID, "raw", res.relPath)
		if blocked {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.relPath)
	absPath := filepath.Join(h.repoRoot, filepath.FromSlash(storageKey))

	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "raw.head.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		// A HEAD on a directory: just confirm 200, no listing body.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}

	ct := h.contentTypeFor(absPath, res.relPath)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
}

// serveFile writes the response body using the strict Content-Type discovery
// chain and always sets Content-Length.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, absPath string, info os.FileInfo, relPath string) {
	f, err := os.Open(absPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "raw.get.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	ct := h.contentTypeFor(absPath, relPath)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

// contentTypeFor implements a two-tier resolution:
//  1. mime.TypeByExtension(filepath.Ext(path)) — covers the long tail of
//     known extensions cheaply.
//  2. http.DetectContentType on the first 512 bytes — sniffs unknown files
//     by magic number.
//
// Returns "application/octet-stream" if both tiers fail (which they only
// do on a zero-length file with an unknown extension).
func (h *Handler) contentTypeFor(absPath, relPath string) string {
	if ct := detectMIMEFromExt(relPath); ct != "" && ct != "application/octet-stream" {
		return ct
	}
	// Magic-number fallback. Open + read 512 bytes; ignore errors and fall
	// through to octet-stream so a sniff failure never breaks the response.
	f, err := os.Open(absPath)
	if err != nil {
		return "application/octet-stream"
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

// actorCanRead returns true when the actor (already in ctx) is allowed to
// read repo. Delegates to auth.Can(ActionRepoRead) so project membership is
// enforced for private repos.
//
// Earlier this function unconditionally returned true for any
// authenticated actor, which leaked private RAW repo contents to cross-project
// API keys and to any logged-in user. The current form mirrors oci.canOnRepo exactly.
func (h *Handler) actorCanRead(r *http.Request, repo *metadata.Repo) bool {
	a, ok := auth.ActorFromContext(r.Context())
	if !ok {
		return false
	}
	ctx := auth.ResolveMembership(r.Context(), a, h.members)
	allowed, _ := auth.Can(ctx, a, auth.ActionRepoRead, auth.Target{
		Kind:       "repo",
		ProjectID:  repo.ProjectID,
		RepoID:     repo.ID,
		PublicRead: repo.PublicRead,
	})
	return allowed
}
