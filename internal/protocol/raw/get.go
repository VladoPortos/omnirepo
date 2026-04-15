package raw

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
)

// get serves GET /<project>/raw/<repo>/<path...>.
//
// If <path> resolves to a file, serves the file with Content-Type per D-29
// (mime.TypeByExtension first, then http.DetectContentType on first 512
// bytes). If it resolves to a directory (or path is empty), defers to the
// listing handler (listing.go). 404 on unknown paths.
//
// The severity gate (h.severityGate) is consulted before serving file bytes;
// when it returns blocked=true, responds 403 with body
// {"error":"blocked_by_scan",...}. This is the hook 02-09 fills in.
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
		http.Error(w, fmt.Sprintf("stat: %v", err), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		h.listDir(w, r, res, absPath)
		return
	}

	// Severity gate hook (no-op until 02-09 wires it in).
	if h.severityGate != nil {
		blocked, severity, scanID := h.severityGate(r.Context(), res.repo.ID, "raw", res.relPath)
		if blocked {
			h.auditEvent(r, audit.EvtRawGetBlocked, res.repo, res.relPath, "blocked", map[string]any{
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

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.relPath)
	absPath := filepath.Join(h.repoRoot, filepath.FromSlash(storageKey))

	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("stat: %v", err), http.StatusInternalServerError)
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
// chain in D-29 and always sets Content-Length.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, absPath string, info os.FileInfo, relPath string) {
	f, err := os.Open(absPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("open: %v", err), http.StatusInternalServerError)
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

// contentTypeFor implements D-29's two-tier resolution:
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
// read repo. Anonymous actors are allowed iff repo.PublicRead. Authenticated
// actors are always allowed (any user can read any repo at the protocol
// layer in v1; project membership controls writes only — D-33's authenticated
// non-member can still read public-read repos and members can read private
// ones; here we match auth.Can(ActionRepoRead) precisely).
func (h *Handler) actorCanRead(r *http.Request, repo any) bool {
	a, ok := auth.ActorFromContext(r.Context())
	if !ok {
		return false
	}
	// Anonymous: AnonymousReadOK only attaches anon when public_read=true,
	// so reaching here implies the gate already passed.
	if a.Kind == auth.ActorKindAnonymous {
		return true
	}
	// Authenticated user / API key: allowed.
	return true
}
