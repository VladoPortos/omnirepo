// Package gitkit is the fallback Git Smart-HTTP backend — a thin wrapper
// around github.com/sosedoff/gitkit v0.4.0 that shells to the `git` binary
// shipped in the Docker image. Selected via config key server.git_backend
// = "gitkit" (default "gogit").
//
// gitkit.Server parses the URL path's last two segments as
// <namespace>/<repo> under cfg.Dir. Since our routes are
// "/git/<project>/<repo>.git/..." and Handler(repoPath) receives the
// already-resolved absolute path, we set cfg.Dir = filepath.Dir(repoPath)
// and rewrite the inbound URL path to "/<basename-of-repoPath>/<suffix>"
// so gitkit's parser finds the right bare repo on disk.
//
// Auth is disabled here — the BasicOrAPIKey middleware runs upstream,
// so gitkit MUST NOT double-check.
package gitkit

import (
	"net/http"
	"path/filepath"
	"strings"

	gk "github.com/sosedoff/gitkit"
)

// Server is the gitkit-based implementation of git.GitServer.
type Server struct{}

// New constructs a stateless gitkit.Server.
func New() *Server { return &Server{} }

// BackendName returns "gitkit".
func (s *Server) BackendName() string { return "gitkit" }

// Handler returns an http.Handler that delegates to sosedoff/gitkit for the
// bare repo at repoPath. Because gitkit expects cfg.Dir as the *parent* of
// the bare repo and resolves the URL's path to find "<basename>", we wrap
// it in a URL-rewriter that normalizes the inbound path before delegating.
func (s *Server) Handler(repoPath string) http.Handler {
	parent := filepath.Dir(repoPath)
	base := filepath.Base(repoPath)
	cfg := gk.Config{
		Dir:        parent,
		AutoCreate: false, // repos are pre-created by the repo-create hook
		Auth:       false, // auth middleware handles auth upstream
	}
	inner := gk.New(cfg)
	return &rewriteHandler{inner: inner, base: base}
}

// rewriteHandler rewrites r.URL.Path to "/<base>/<suffix>" where <suffix>
// is one of "info/refs", "git-upload-pack", "git-receive-pack" — stripping
// whatever prefix the outer router (chi / http.StripPrefix / raw) left on
// the request. gitkit's handler then parses <base> out of the path and
// joins it with cfg.Dir to reach the on-disk bare repo.
type rewriteHandler struct {
	inner *gk.Server
	base  string
}

func (h *rewriteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	var suffix string
	switch {
	case strings.HasSuffix(path, "/info/refs"):
		suffix = "/info/refs"
	case strings.HasSuffix(path, "/git-upload-pack"):
		suffix = "/git-upload-pack"
	case strings.HasSuffix(path, "/git-receive-pack"):
		suffix = "/git-receive-pack"
	default:
		http.NotFound(w, r)
		return
	}

	// Clone the request with a rewritten URL so gitkit sees exactly
	// "/<base>/<suffix>" regardless of the outer mount path.
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.Path = "/" + h.base + suffix
	r2.URL = &u
	h.inner.ServeHTTP(w, r2)
}
