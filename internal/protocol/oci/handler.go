package oci

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// Deps bundles the subsystems the /v2 handler needs. Fields that downstream
// plans (02-06 blobs, 02-07 manifests/tags/catalog) will reach for are
// declared here as placeholders so their Mount wiring can land without
// churning this constructor's shape.
type Deps struct {
	DB         *metadata.DB
	Users      *metadata.UsersRepo
	APIKeys    *metadata.APIKeysRepo
	Repos      *metadata.ReposRepo
	Projects   *metadata.ProjectsRepo
	Sessions   *metadata.SessionsRepo // for BasicOrAPIKey middleware
	HMACSecret []byte                 // 32 random bytes (D-06)
	JWTTTL     time.Duration          // default 3600s
}

// Handler serves /v2. Its public API is just New + Mount; everything else
// stays package-private so downstream plans extend behavior via internal
// helpers rather than by importing unexported fields.
type Handler struct {
	db         *metadata.DB
	users      *metadata.UsersRepo
	apiKeys    *metadata.APIKeysRepo
	repos      *metadata.ReposRepo
	projects   *metadata.ProjectsRepo
	sessions   *metadata.SessionsRepo
	hmacSecret []byte
	jwtTTL     time.Duration
}

// New constructs a Handler from deps.
func New(d Deps) *Handler {
	ttl := d.JWTTTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Handler{
		db:         d.DB,
		users:      d.Users,
		apiKeys:    d.APIKeys,
		repos:      d.Repos,
		projects:   d.Projects,
		sessions:   d.Sessions,
		hmacSecret: d.HMACSecret,
		jwtTTL:     ttl,
	}
}

// Mount registers the /v2 subrouter on parent. Layout (plan 02-05):
//
//	GET  /v2/         → ping (anonymous-accessible per spec)
//	GET  /v2/token    → BasicOrAPIKey → issueToken
//	...               → AnonymousReadOK → VerifyBearer(Basic fallback) → downstream routes
//
// Plans 02-06 (blobs) and 02-07 (manifests/tags/catalog) register routes
// inside the guarded subroute (the Group that sits behind
// AnonymousReadOK + VerifyBearer). Doing so is mandatory — T-02-05-05
// requires every /v2 sub-path to pass through both middlewares.
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Route("/v2", func(r chi.Router) {
		// OCI-spec ping: anonymous-accessible, no middleware.
		r.Get("/", h.ping)
		// Token issue: Basic or API-key creds → identity-only JWT.
		r.With(authmw.BasicOrAPIKey(midDeps)).Get("/token", h.issueToken)

		// Everything else runs under the guarded chain. Plans 02-06 and
		// 02-07 plug their routes into this subrouter via h.ProtectedGroup.
		r.Group(func(r chi.Router) {
			r.Use(httpx.AnonymousReadOK(h.lookupRepoPublicRead, h.extractRepoFromV2URL, attachAnonymous))
			r.Use(h.VerifyBearer)
			// Placeholder so the subrouter is not empty; 02-06/02-07
			// will register real /_catalog + /<name>/... routes.
			r.Get("/_catalog", h.notImplementedYet)
		})
	})
}

// ping is the OCI Distribution /v2/ endpoint. Spec requires:
//   - 200 OK on success
//   - Header "Docker-Distribution-API-Version: registry/2.0"
func (h *Handler) ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

// notImplementedYet is the placeholder for routes downstream plans own.
// Returns 501 with an OCI error envelope so test suites flag it clearly.
func (h *Handler) notImplementedYet(w http.ResponseWriter, r *http.Request) {
	writeOCIErr(w, http.StatusNotImplemented, ErrCodeUnsupported,
		errors.New("route not implemented in plan 02-05; will land in 02-06/02-07"))
}

// attachAnonymous is the AnonymousReadOK callback that wires an anonymous
// Actor into ctx. Declared as a package-level var rather than inlined so
// test code can reach for the same reference when exercising the middleware
// in isolation.
var attachAnonymous httpx.AttachAnonymousFn = func(ctx context.Context) context.Context {
	return auth.WithActor(ctx, auth.Actor{Kind: auth.ActorKindAnonymous})
}

// extractRepoFromV2URL parses /v2/<project>/<type>/<repo>/... URLs into
// the (project, type, repo) triple. The V2 distribution spec allows
// arbitrary "<name>" path components separated by "/"; OmniRepo narrows
// that to <project>/<type>/<repo>/... so AnonymousReadOK can look the
// repo up.
//
// Returns ok=false when the URL is /v2/ itself or doesn't have enough path
// components to identify a repo yet.
func (h *Handler) extractRepoFromV2URL(r *http.Request) (project, repoType, repo string, ok bool) {
	// Strip the /v2 prefix. chi's r.URL.Path still carries it because
	// Route("/v2", ...) does NOT rewrite it (unlike r.Mount). Test the
	// prefix explicitly to keep this function callable in a plain net/http
	// context (middleware unit tests).
	p := r.URL.Path
	p = strings.TrimPrefix(p, "/v2")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", "", "", false
	}
	parts := strings.SplitN(p, "/", 4) // project / type / repo / rest
	if len(parts) < 3 {
		return "", "", "", false
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	// /_catalog and /token are excluded (they aren't repo-scoped); the
	// ping /v2/ is already handled by the empty-path case above.
	if parts[0] == "_catalog" || parts[0] == "token" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// lookupRepoPublicRead is the AnonymousReadOK callback that resolves
// (project, type, repo) → (public_read, found) via the Phase 1
// ProjectsRepo + ReposRepo pair. Missing ProjectsRepo or ReposRepo at
// construction time surfaces as found=false (the middleware then falls
// through to the real auth chain, which 401s safely).
func (h *Handler) lookupRepoPublicRead(ctx context.Context, project, repoType, repoName string) (bool, bool) {
	if h.projects == nil || h.repos == nil {
		return false, false
	}
	p, err := h.projects.FindByName(ctx, project)
	if err != nil || p == nil {
		return false, false
	}
	rr, err := h.repos.FindByTriple(ctx, p.ID, repoType, repoName)
	if err != nil || rr == nil {
		return false, false
	}
	return rr.PublicRead, true
}
