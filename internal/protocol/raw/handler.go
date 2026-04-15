// Package raw implements the RAW pass-through repository protocol
// (D-27..D-31, RAW-01..05). One file = one HTTP path under
// /<project>/raw/<repo>/<path...>; PUT writes via PathStore (atomic
// temp+fsync+rename) and inserts a raw_files row + FTS5 artifact entry +
// optional auto-scan enqueue in the same writer transaction; GET serves the
// file or a directory listing; DELETE moves the file to trash and removes
// the row.
//
// The package's public API is just New + Mount; everything else stays
// package-private so downstream plans extend behavior via internal helpers.
package raw

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// SeverityGateFn lets 02-09 plug in the block_on_severity gate without
// raw needing to import scan packages. nil = no-op (default).
type SeverityGateFn func(ctx context.Context, repoID int64, artifactKind, artifactID string) (blocked bool, severity string, scanID int64)

// Deps is the dependency bundle the RAW handler needs at construction time.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Sessions *metadata.SessionsRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Files    *metadata.RawFilesRepo
	Scans    *metadata.ScansRepo
	Path     storage.PathStore
	Trash    storage.Trash
	Audit    audit.Logger

	// SeverityGate is plugged in by 02-09; nil = no-op.
	SeverityGate SeverityGateFn

	// MaxPutBytes caps PUT body size. Zero = default 5 GiB.
	MaxPutBytes int64

	// RepoRoot is the absolute filesystem root the PathStore writes under
	// (typically <DataRoot>/repos). The handler uses it to resolve directory
	// listings via os.ReadDir on absolute paths.
	RepoRoot string
}

// Handler serves the RAW protocol surface. Constructed by New, mounted on a
// chi router via Mount.
type Handler struct {
	db           *metadata.DB
	users        *metadata.UsersRepo
	apiKeys      *metadata.APIKeysRepo
	sessions     *metadata.SessionsRepo
	repos        *metadata.ReposRepo
	projects     *metadata.ProjectsRepo
	files        *metadata.RawFilesRepo
	scans        *metadata.ScansRepo
	pathStore    storage.PathStore
	trash        storage.Trash
	auditLogger  audit.Logger
	severityGate SeverityGateFn
	maxPutBytes  int64
	repoRoot     string
}

// defaultMaxPutBytes is the spec-default 5 GiB cap on a single PUT body.
const defaultMaxPutBytes = int64(5) << 30

// New constructs a Handler from deps.
func New(d Deps) *Handler {
	max := d.MaxPutBytes
	if max <= 0 {
		max = defaultMaxPutBytes
	}
	return &Handler{
		db:           d.DB,
		users:        d.Users,
		apiKeys:      d.APIKeys,
		sessions:     d.Sessions,
		repos:        d.Repos,
		projects:     d.Projects,
		files:        d.Files,
		scans:        d.Scans,
		pathStore:    d.Path,
		trash:        d.Trash,
		auditLogger:  d.Audit,
		severityGate: d.SeverityGate,
		maxPutBytes:  max,
		repoRoot:     d.RepoRoot,
	}
}

// Mount registers the RAW routes on parent. Routes live at the URL root
// because the path includes the project slug — there is no /api/v1 prefix
// (D-27).
//
// Middleware chain (per request):
//  1. AnonymousReadOK — for public_read=true repos on GET/HEAD without an
//     Authorization header, attaches an anonymous Actor and lets the request
//     proceed without 401. Otherwise falls through.
//  2. BasicOrAPIKey — authenticates via Basic creds or omr_<u|p>_... API
//     key. Skipped when an Actor is already on ctx (the anonymous fast path).
//  3. Per-handler authorization via auth.Can.
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(h.lookupRepoPublicRead, h.extractRepoFromRawURL, attachAnonymous))
		r.Use(skipIfActor(authmw.BasicOrAPIKey(midDeps)))
		r.Put("/{project}/raw/{repo}/*", h.put)
		r.Get("/{project}/raw/{repo}/*", h.get)
		r.Head("/{project}/raw/{repo}/*", h.head)
		r.Delete("/{project}/raw/{repo}/*", h.delete)
	})
}

// attachAnonymous is the AnonymousReadOK callback that wires an anonymous
// Actor into ctx. Mirrors the OCI handler's identical helper.
var attachAnonymous httpx.AttachAnonymousFn = func(ctx context.Context) context.Context {
	return auth.WithActor(ctx, auth.Actor{Kind: auth.ActorKindAnonymous})
}

// skipIfActor wraps a middleware so it pass-throughs when an Actor is already
// in ctx (the anonymous fast path set by AnonymousReadOK). Without this the
// BasicOrAPIKey middleware would 401 anonymous-read requests because they
// have no Authorization header.
func skipIfActor(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.ActorFromContext(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

// extractRepoFromRawURL returns (project, "raw", repo, ok=true) when the URL
// matches /<project>/raw/<repo>/.... Used by AnonymousReadOK.
func (h *Handler) extractRepoFromRawURL(r *http.Request) (project, repoType, repo string, ok bool) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(p, "/", 4)
	if len(parts) < 3 {
		return "", "", "", false
	}
	if parts[1] != "raw" {
		return "", "", "", false
	}
	if parts[0] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], "raw", parts[2], true
}

// lookupRepoPublicRead resolves (project, "raw", repo) → (public_read, found).
func (h *Handler) lookupRepoPublicRead(ctx context.Context, project, repoType, repo string) (bool, bool) {
	if h.projects == nil || h.repos == nil {
		return false, false
	}
	p, err := h.projects.FindByName(ctx, project)
	if err != nil || p == nil {
		return false, false
	}
	rr, err := h.repos.FindByTriple(ctx, p.ID, repoType, repo)
	if err != nil || rr == nil {
		return false, false
	}
	return rr.PublicRead, true
}

// resolved wraps a successful repo+path lookup.
type resolved struct {
	project *metadata.Project
	repo    *metadata.Repo
	relPath string // cleaned path under the repo root, "" allowed for repo root listings
}

// resolveRepoAndPath validates the project/repo URL params, looks up the repo
// row, validates the rest path, and returns the resolved triple. Writes a
// 404 to w on miss and returns ok=false.
func (h *Handler) resolveRepoAndPath(w http.ResponseWriter, r *http.Request, requirePath bool) (resolved, bool) {
	projectName := chi.URLParam(r, "project")
	repoName := chi.URLParam(r, "repo")
	rest := chi.URLParam(r, "*")

	if projectName == "" || repoName == "" {
		http.Error(w, "missing project or repo", http.StatusNotFound)
		return resolved{}, false
	}
	proj, err := h.projects.FindByName(r.Context(), projectName)
	if err != nil || proj == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return resolved{}, false
	}
	rr, err := h.repos.FindByTriple(r.Context(), proj.ID, "raw", repoName)
	if err != nil || rr == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return resolved{}, false
	}

	// For directory-listing GETs the URL may end with a trailing slash
	// ("/dir/"); strip it before validation so "dir/" is treated as "dir".
	// For PUT/DELETE (requirePath=true) we keep trailing-slash rejection
	// strict — trailing slash on a write is meaningless.
	checkRest := rest
	if !requirePath {
		checkRest = strings.TrimSuffix(rest, "/")
	}

	// Validate the rest path. "" is allowed (repo-root listing) when
	// requirePath is false.
	cleaned, perr := validateRawPath(checkRest)
	if perr != nil {
		// Allow empty path only for non-required (GET listing).
		if checkRest == "" && !requirePath {
			cleaned = ""
		} else {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return resolved{}, false
		}
	}
	if requirePath && cleaned == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return resolved{}, false
	}
	return resolved{project: proj, repo: rr, relPath: cleaned}, true
}

// validateRawPath cleans a raw URL path component and rejects traversal /
// reserved characters. Returns the cleaned slash-relative path or an error.
//
// Rules (T-02-08-01):
//   - leading slashes are stripped
//   - empty input or input that cleans to "" → error
//   - any segment that is "", ".", or ".." → error
//   - NUL byte anywhere → error
//   - cleaned path must not start with "/" (post-trim) — defense in depth
func validateRawPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("nul byte in path")
	}
	p := strings.TrimPrefix(raw, "/")
	if p == "" {
		return "", errors.New("empty path after trim")
	}
	// Reject explicit traversal first — path.Clean would silently collapse
	// "a/../b" to "b", which is NOT what we want.
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == ".." || seg == "." {
			return "", errors.New("invalid path segment")
		}
	}
	// Ensure path.Clean is a no-op on the already-segmented input. If it
	// rewrites the path it implies a traversal slipped through.
	cleaned := path.Clean("/" + p)
	if strings.HasPrefix(cleaned, "/..") {
		return "", errors.New("path escape")
	}
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned != p {
		return "", errors.New("non-canonical path")
	}
	return cleaned, nil
}

// auditEvent is a tiny helper around d.Audit.Record that fills in actor +
// request fields uniformly. Best-effort: errors are swallowed.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, target *metadata.Repo, targetID, outcome string, details map[string]any) {
	if h.auditLogger == nil {
		return
	}
	e := audit.Event{
		Kind:       kind,
		IP:         r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		TargetKind: "raw_file",
		TargetID:   targetID,
		Outcome:    outcome,
		Details:    details,
		OccurredAt: time.Now().UTC(),
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		switch a.Kind {
		case auth.ActorKindUser:
			id := a.ID
			e.ActorUserID = &id
		case auth.ActorKindAPIKey:
			id := a.APIKeyID
			e.ActorAPIKeyID = &id
		}
	}
	_ = h.auditLogger.Record(r.Context(), e)
}
