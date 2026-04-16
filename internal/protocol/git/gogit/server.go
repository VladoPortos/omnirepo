package gogit

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage"
)

// Server is the primary (gogit) implementation of git.GitServer.
//
// It wraps go-git v6's plumbing/transport.{AdvertiseRefs,UploadPack,ReceivePack}
// primitives in a minimal Smart-HTTP HTTP handler — the production promotion
// of the Phase 1 spike per D-28. No per-repo mutex here (Plan 10 adds it as
// separate middleware for write serialization); no MaxBytesReader here
// (Plan 10 adds it as separate middleware for push-size enforcement).
type Server struct{}

// New constructs a stateless gogit.Server.
func New() *Server { return &Server{} }

// BackendName returns "gogit".
func (s *Server) BackendName() string { return "gogit" }

// Handler returns an http.Handler that serves info/refs + git-upload-pack +
// git-receive-pack for the bare repo at repoPath. repoPath must be an
// absolute path to an existing bare repo directory (use git.InitBare to
// create one).
//
// The handler uses a per-call FilesystemLoader rooted at the absolute
// repoPath, so it works regardless of whether the router strips the URL
// prefix or not (we only look at the URL path suffix).
func (s *Server) Handler(repoPath string) http.Handler {
	return &repoHandler{repoPath: repoPath}
}

type repoHandler struct {
	repoPath string
}

// resolve opens the bare repo at repoPath as a storage.Storer using a
// fresh FilesystemLoader. Called per request — cheap, no cache (go-git's
// Storer already caches objects via NewObjectLRUDefault internally).
func (h *repoHandler) resolve() (storage.Storer, error) {
	fs := osfs.New("")
	loader := transport.NewFilesystemLoader(fs, true)
	return loader.Load(&url.URL{Path: h.repoPath})
}

func (h *repoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/info/refs") && r.Method == http.MethodGet:
		h.handleInfoRefs(w, r)
	case strings.HasSuffix(r.URL.Path, "/git-upload-pack") && r.Method == http.MethodPost:
		h.handleService(w, r, transport.UploadPackService)
	case strings.HasSuffix(r.URL.Path, "/git-receive-pack") && r.Method == http.MethodPost:
		h.handleService(w, r, transport.ReceivePackService)
	default:
		http.NotFound(w, r)
	}
}

func (h *repoHandler) handleInfoRefs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if service != transport.UploadPackService && service != transport.ReceivePackService {
		http.Error(w, "only smart HTTP is supported", http.StatusForbidden)
		return
	}
	st, err := h.resolve()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-advertisement", service))
	w.Header().Set("Cache-Control", "no-cache")

	// AdvertiseRefs with smart=true writes the Smart-HTTP SmartReply preamble
	// ("# service=<svc>\n" + flush) plus the capability/ref advertisement.
	// Per spike-results.md: the preamble is emitted by AdvertiseRefs itself
	// when smart=true — the caller MUST NOT pktline-encode it again.
	_ = transport.AdvertiseRefs(r.Context(), st, w, service, true)
}

func (h *repoHandler) handleService(w http.ResponseWriter, r *http.Request, service string) {
	st, err := h.resolve()
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
// transport.UploadPack/ReceivePack want an io.WriteCloser so they can
// half-close the stream; on HTTP the response is closed by net/http.
type nopCloseWriter struct{ io.Writer }

func (*nopCloseWriter) Close() error { return nil }
