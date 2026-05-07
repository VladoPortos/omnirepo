package oci

import (
	"context"
	"net/http"
	"path/filepath"
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

// Deps bundles the subsystems the /v2 handler needs. Fields that downstream
// plans (02-06 blobs, 02-07 manifests/tags/catalog) will reach for are
// declared here as placeholders so their Mount wiring can land without
// churning this constructor's shape.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Sessions *metadata.SessionsRepo // for BasicOrAPIKey middleware
	Members  *metadata.MembersRepo  // for resolving project membership on /v2 actions

	// Phase 02-06 dependencies. Plan 02-05 declared placeholder fields so
	// this constructor shape does not have to change between plans;
	// making them concrete here keeps the Handler struct the single
	// source of truth for every wiring concern.
	CAS             storage.CAS                      // blob content-addressed store
	Blobs           *metadata.DockerBlobsRepo        // docker_blobs refcount table
	BlobUploads     *metadata.BlobUploadsRepo        // blob_uploads GC-exclusion set
	Sess            *metadata.BlobUploadSessionsRepo // blob_upload_sessions
	Audit           audit.Logger                     // audit logger (best-effort)
	DataRoot        string                           // /var/lib/omnirepo (tmp uploads live under it)
	ChunkMaxBytes   int64                            // per-chunk cap; default 64 MiB
	SessionMaxBytes int64                            // per-session cap; default 10 GiB

	// Phase 02-07 dependencies: manifests + tags + catalog + cosign.
	Manifests *metadata.DockerManifestsRepo
	Tags      *metadata.DockerTagsRepo
	// Scans (optional): when non-nil and the target repo has auto_scan=true,
	// manifestPut enqueues a scan row in the same writer tx.
	Scans *metadata.ScansRepo
	// ScanKick (optional): invoked after a scan enqueue tx commits so the
	// scan pool picks it up without waiting for the next poll. nil → no-op.
	ScanKick func()
	// SeverityGate (optional): 02-09 plugs in the block_on_severity check.
	// When nil, manifestGet is unrestricted. The hook receives the resolved
	// repo id + manifest digest and returns a non-nil error to block the
	// response; the manifestGet path then writes a 403 and aborts.
	SeverityGate SeverityGateFn

	HMACSecret []byte        // 32 random bytes (D-06)
	JWTTTL     time.Duration // default 3600s

	// ExternalHostnames is the trust-list from config (server.external_hostnames).
	// When non-empty, the WWW-Authenticate challenge uses the configured first
	// entry rather than r.Host, closing WR-01 (host-header injection in the
	// Docker bearer realm).
	ExternalHostnames []string

	// HelmMirror (optional) is the hook the /v2 manifestPut handler calls
	// after successfully committing a manifest whose config.mediaType is
	// MediaTypeHelmChartConfigV1 on a helm-type repo. Plan 07-04 S-03b:
	// implementations fetch the chart-content layer blob from the CAS and
	// mirror it into the traditional helm charts tree via
	// helm.Mirror.MirrorToTraditional. A nil HelmMirror is a no-op (the
	// mirror simply does not run — OCI push semantics are unaffected).
	// Failure is logged by the caller; the OCI push continues regardless.
	HelmMirror HelmMirrorHook
}

// HelmMirrorHook is the post-commit callback the /v2 manifestPut handler
// invokes when it detects a Helm OCI chart push. The adapter (wired from
// internal/app) resolves the chart-content layer blob from the CAS and
// delegates to helm.Mirror.MirrorToTraditional. Plan 07-04 keeps this an
// interface instead of a concrete type so the oci package stays free of a
// direct helm package import (which would introduce a cycle at the wiring
// site).
type HelmMirrorHook interface {
	Mirror(ctx context.Context, projectName, repoName, chartBlobDigest string) error
}

// SeverityGateFn is the block_on_severity hook signature. 02-09 will plug in
// a real implementation; until then a nil handler field is treated as an
// always-allow no-op.
type SeverityGateFn func(ctx context.Context, repoID int64, digest string) error

// ManifestMaxBytes caps manifest body size (T-02-07-03). Pushing >4 MiB
// manifest JSON yields 413 MANIFEST_INVALID.
const ManifestMaxBytes int64 = 4 << 20

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
	members    *metadata.MembersRepo
	hmacSecret []byte
	jwtTTL     time.Duration

	// Blob wiring (02-06).
	cas             storage.CAS
	blobs           *metadata.DockerBlobsRepo
	blobUploads     *metadata.BlobUploadsRepo
	sess            *metadata.BlobUploadSessionsRepo
	auditLogger     audit.Logger
	dataRoot        string
	chunkMaxBytes   int64
	sessionMaxBytes int64

	// Manifests + tags + catalog wiring (02-07).
	manifests    *metadata.DockerManifestsRepo
	tags         *metadata.DockerTagsRepo
	scans        *metadata.ScansRepo
	scanKick     func()
	severityGate SeverityGateFn

	// externalHostnames: first entry used for WWW-Authenticate realm in
	// preference to r.Host when non-empty (WR-01 fix).
	externalHostnames []string

	// helmMirror: plan 07-04 S-03b post-commit hook. nil-safe — when unset,
	// manifestPut's helm-detection branch is a silent no-op.
	helmMirror HelmMirrorHook
}

// New constructs a Handler from deps.
func New(d Deps) *Handler {
	ttl := d.JWTTTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	// F-T3: default lifted from 64 MiB to 512 MiB so docker clients that
	// fall back to single-PUT push don't hard-fail on common base images
	// (mariadb, postgres, golang) whose largest layer is 80–200 MiB. Admins
	// can tune via docker.chunk_max_bytes in config.yaml.
	chunk := d.ChunkMaxBytes
	if chunk <= 0 {
		chunk = 512 << 20 // 512 MiB
	}
	sess := d.SessionMaxBytes
	if sess <= 0 {
		sess = 10 << 30 // 10 GiB
	}
	return &Handler{
		db:                d.DB,
		users:             d.Users,
		apiKeys:           d.APIKeys,
		repos:             d.Repos,
		projects:          d.Projects,
		sessions:          d.Sessions,
		members:           d.Members,
		hmacSecret:        d.HMACSecret,
		jwtTTL:            ttl,
		cas:               d.CAS,
		blobs:             d.Blobs,
		blobUploads:       d.BlobUploads,
		sess:              d.Sess,
		auditLogger:       d.Audit,
		dataRoot:          d.DataRoot,
		chunkMaxBytes:     chunk,
		sessionMaxBytes:   sess,
		manifests:         d.Manifests,
		tags:              d.Tags,
		scans:             d.Scans,
		scanKick:          d.ScanKick,
		severityGate:      d.SeverityGate,
		externalHostnames: d.ExternalHostnames,
		helmMirror:        d.HelmMirror,
	}
}

// uploadTmpPath returns the tmp file path for a given blob upload session
// UUID. Lives under <dataRoot>/tmp/uploads/<uuid> so it sits on the same
// filesystem as the CAS tree and os.Rename between them is atomic.
func (h *Handler) uploadTmpPath(uuid string) string {
	return filepath.Join(h.dataRoot, "tmp", "uploads", uuid)
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

		// /_catalog is project-scoped but also accessible to anonymous
		// requesters (who see only public_read=true repos). It sits
		// OUTSIDE the guarded chain — the handler resolves the actor
		// itself, defaulting to anonymous when no Bearer is supplied.
		r.With(h.catalogAuth).Get("/_catalog", h.catalog)

		// Everything else runs under the guarded chain. Plans 02-06 and
		// 02-07 plug their routes into this subrouter via h.ProtectedGroup.
		r.Group(func(r chi.Router) {
			r.Use(httpx.AnonymousReadOK(h.lookupRepoPublicRead, h.extractRepoFromV2URL, attachAnonymous))
			r.Use(h.VerifyBearer)

			// Blob routes (02-06). The name parameter is a full
			// <project>/<type>/<repo> triple per the 02-05 URL
			// convention; the handlers validate it into that triple
			// and resolve the repo via FindByTriple.
			//
			// chi's {name:[^{}]+} would match any non-brace run, but
			// it does not greedily consume past the next static
			// segment. To route URLs like
			//     /v2/<project>/<type>/<repo>/blobs/uploads/<uuid>
			// we use three typed params project/type/repo and let the
			// handlers re-assemble them.
			//
			// Every route is also mounted in a 4-segment {image} form
			// (/v2/{project}/{type}/{repo}/{image}/...) so Helm OCI
			// works: `helm push chart.tgz oci://host/proj/helm/repo`
			// always appends the chart name as a 4th path segment.
			// Docker clients may also use the 4-segment form to host
			// multiple images under one OmniRepo repo. When the
			// 3-segment route matches, resolveRepo reads image = "".
			// Phase 8 Plan 01 (MIRROR-03): wrap OCI write paths (blob
			// uploads + manifest PUTs) in MirrorGuard so mirror-flagged
			// docker/helm-OCI repos reject uploads with 403
			// repo.repo_is_mirror. {type} comes from chi URL params so
			// the basic MirrorGuard variant is sufficient here.
			mirrorGuard := httpx.MirrorGuard(h.repos, h.projects)

			r.With(mirrorGuard).Post("/{project}/{type}/{repo}/blobs/uploads/", h.blobPostDispatch)
			r.With(mirrorGuard).Patch("/{project}/{type}/{repo}/blobs/uploads/{uuid}", h.blobUploadPatch)
			r.With(mirrorGuard).Put("/{project}/{type}/{repo}/blobs/uploads/{uuid}", h.blobUploadPut)
			r.Get("/{project}/{type}/{repo}/blobs/uploads/{uuid}", h.blobUploadStatus)
			r.Get("/{project}/{type}/{repo}/blobs/{digest}", h.blobGet)
			r.Head("/{project}/{type}/{repo}/blobs/{digest}", h.blobHead)
			r.With(mirrorGuard).Delete("/{project}/{type}/{repo}/blobs/{digest}", h.blobDelete)

			r.With(mirrorGuard).Post("/{project}/{type}/{repo}/{image}/blobs/uploads/", h.blobPostDispatch)
			r.With(mirrorGuard).Patch("/{project}/{type}/{repo}/{image}/blobs/uploads/{uuid}", h.blobUploadPatch)
			r.With(mirrorGuard).Put("/{project}/{type}/{repo}/{image}/blobs/uploads/{uuid}", h.blobUploadPut)
			r.Get("/{project}/{type}/{repo}/{image}/blobs/uploads/{uuid}", h.blobUploadStatus)
			r.Get("/{project}/{type}/{repo}/{image}/blobs/{digest}", h.blobGet)
			r.Head("/{project}/{type}/{repo}/{image}/blobs/{digest}", h.blobHead)
			r.With(mirrorGuard).Delete("/{project}/{type}/{repo}/{image}/blobs/{digest}", h.blobDelete)

			// Manifest routes (02-07). reference is either a tag or a
			// digest; handler disambiguates.
			r.Get("/{project}/{type}/{repo}/manifests/{reference}", h.manifestGet)
			r.Head("/{project}/{type}/{repo}/manifests/{reference}", h.manifestHead)
			r.With(mirrorGuard).Put("/{project}/{type}/{repo}/manifests/{reference}", h.manifestPut)
			r.With(mirrorGuard).Delete("/{project}/{type}/{repo}/manifests/{reference}", h.manifestDelete)

			r.Get("/{project}/{type}/{repo}/{image}/manifests/{reference}", h.manifestGet)
			r.Head("/{project}/{type}/{repo}/{image}/manifests/{reference}", h.manifestHead)
			r.With(mirrorGuard).Put("/{project}/{type}/{repo}/{image}/manifests/{reference}", h.manifestPut)
			r.With(mirrorGuard).Delete("/{project}/{type}/{repo}/{image}/manifests/{reference}", h.manifestDelete)

			// Tag routes (02-07). Plan 08-06 Codex rescue Q3b: DELETE is
			// a mutating operation and must be gated by MirrorGuard so
			// mirror-flagged repos reject it with 403 repo.repo_is_mirror
			// (GET /tags/list is a read and stays open).
			r.Get("/{project}/{type}/{repo}/tags/list", h.tagsList)
			r.With(mirrorGuard).Delete("/{project}/{type}/{repo}/tags/{tag}", h.tagDelete)

			r.Get("/{project}/{type}/{repo}/{image}/tags/list", h.tagsList)
			r.With(mirrorGuard).Delete("/{project}/{type}/{repo}/{image}/tags/{tag}", h.tagDelete)
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
