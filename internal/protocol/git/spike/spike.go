//go:build spike

// Package spike is a stand-alone proof-of-concept that the go-git v6 server
// primitives (UploadPack / ReceivePack / AdvertiseRefs) can serve real git(1)
// CLI clients correctly over Smart HTTP. Covered by D-38 / D-39.
//
// DEVIATION from RESEARCH.md §A2: the assumed import
// `github.com/go-git/go-git/v6/backend` with a ready-made backend.New(loader)
// constructor does NOT exist in the v6 pseudo-version we vendor
// (v6.0.0-alpha.1.0.20260414225401-98cdae44aed0). v6 ships lower-level server
// primitives in `plumbing/transport` instead. This spike wires them into a
// ~150-LOC chi sub-router that speaks Smart HTTP, which is functionally the
// same gate — "does v6 serve real git clients correctly?" — at the cost of
// extra glue code. See `spike-results.md` for the Phase 4 decision.
//
// The whole package is compiled only under `-tags=spike` so the main binary
// does not link go-git v6's pack-protocol code paths in production.
package spike

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage"
)

// SimpleLoader maps URL paths (e.g. "/dxc/infra.git") to bare-repo directories
// on disk. Implements transport.Loader by delegating each resolved path to a
// v6 FilesystemLoader. Used in the spike test to hold `t.TempDir()` repos.
type SimpleLoader struct {
	mu    sync.RWMutex
	Repos map[string]string // URL path -> bare repo directory on disk
}

// NewSimpleLoader copies the input map so callers can mutate after construction.
func NewSimpleLoader(repos map[string]string) *SimpleLoader {
	m := make(map[string]string, len(repos))
	for k, v := range repos {
		m[k] = v
	}
	return &SimpleLoader{Repos: m}
}

// Load resolves u.Path against the in-memory map, then returns a storage.Storer
// rooted at the matching bare repo directory via v6's FilesystemLoader.
func (l *SimpleLoader) Load(u *url.URL) (storage.Storer, error) {
	l.mu.RLock()
	dir, ok := l.Repos[u.Path]
	l.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("repo not found: %s", u.Path)
	}
	fs := osfs.New("")
	fsLoader := transport.NewFilesystemLoader(fs, true)
	return fsLoader.Load(&url.URL{Path: dir})
}

// Compile-time assertion: SimpleLoader must satisfy transport.Loader.
var _ transport.Loader = (*SimpleLoader)(nil)

// MountSpike attaches the spike Smart-HTTP handler under `/git/` on r.
// Returns r for chaining convenience.
func MountSpike(r chi.Router, loader transport.Loader) http.Handler {
	h := &httpHandler{loader: loader}
	r.Mount("/git", h)
	return r
}

// --- Smart HTTP handler ---------------------------------------------------

type httpHandler struct {
	loader transport.Loader
}

// repoPathFromRequest strips the /git prefix and the trailing service suffix,
// e.g. "/git/dxc/infra.git/info/refs" -> "/dxc/infra.git".
func (h *httpHandler) repoPathFromRequest(r *http.Request, suffix string) string {
	p := strings.TrimPrefix(r.URL.Path, "/git")
	p = strings.TrimSuffix(p, suffix)
	return p
}

func (h *httpHandler) resolve(repoPath string) (storage.Storer, error) {
	return h.loader.Load(&url.URL{Path: repoPath})
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/info/refs") && r.Method == http.MethodGet:
		h.handleInfoRefs(w, r)
	case strings.HasSuffix(r.URL.Path, "/git-upload-pack") && r.Method == http.MethodPost:
		h.handleService(w, r, transport.UploadPackService, "/git-upload-pack")
	case strings.HasSuffix(r.URL.Path, "/git-receive-pack") && r.Method == http.MethodPost:
		h.handleService(w, r, transport.ReceivePackService, "/git-receive-pack")
	default:
		http.NotFound(w, r)
	}
}

func (h *httpHandler) handleInfoRefs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if service != transport.UploadPackService && service != transport.ReceivePackService {
		http.Error(w, "only smart HTTP is supported", http.StatusForbidden)
		return
	}
	repoPath := h.repoPathFromRequest(r, "/info/refs")
	st, err := h.resolve(repoPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-advertisement", service))
	w.Header().Set("Cache-Control", "no-cache")

	// AdvertiseRefs with smart=true writes the Smart-HTTP SmartReply preamble
	// ("# service=<svc>\n" + flush) plus the capability/ref advertisement.
	_ = transport.AdvertiseRefs(r.Context(), st, w, service, true)
}

func (h *httpHandler) handleService(w http.ResponseWriter, r *http.Request, service, suffix string) {
	repoPath := h.repoPathFromRequest(r, suffix)
	st, err := h.resolve(repoPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Request body may be gzip-encoded (git CLI does this for larger payloads).
	var body io.ReadCloser = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, gerr := gzip.NewReader(r.Body)
		if gerr != nil {
			http.Error(w, gerr.Error(), http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = io.NopCloser(gz)
	}

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-result", service))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	wc := &nopCloseWriter{Writer: w}

	switch service {
	case transport.UploadPackService:
		_ = transport.UploadPack(ctx, st, body, wc, &transport.UploadPackRequest{StatelessRPC: true})
	case transport.ReceivePackService:
		_ = transport.ReceivePack(ctx, st, body, wc, &transport.ReceivePackRequest{StatelessRPC: true})
	}
}

// nopCloseWriter adapts an io.Writer to io.WriteCloser (Close is a no-op).
type nopCloseWriter struct{ io.Writer }

func (*nopCloseWriter) Close() error { return nil }
