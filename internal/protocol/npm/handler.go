// Package npm implements a hosted npm registry at /<project>/npm/<repo>/...
//
// Served endpoints (package names arrive plain or with the scope slash
// URL-encoded — npm sends @scope%2fname):
//
//	GET    /-/ping                      — liveness for `npm ping`
//	GET    /<name>                      — packument (dist-tags + versions)
//	GET    /<name>/-/<file>.tgz         — tarball download
//	PUT    /<name>                      — `npm publish` (JSON body with
//	                                      base64 _attachments tarball)
//	DELETE /<name>/-/<version>          — row delete (UI/REST surface; the
//	                                      public registry's -rev unpublish
//	                                      protocol is not implemented)
//
// Publishing is npm-native: point .npmrc at the repo and `npm publish`.
// Versions are immutable — publishing over an existing (name, version)
// is rejected with 403, matching registry.npmjs.org semantics.
//
// Consume with:
//
//	npm config set registry https://<host>/<project>/npm/<repo>/
//	npm config set //<host>/<project>/npm/<repo>/:_auth $(echo -n user:apikey | base64)
package npm

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	authmw "github.com/vladoportos/omnirepo/internal/auth/middleware"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/common"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// Deps bundles the dependencies the npm handler needs.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Sessions *metadata.SessionsRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo

	Packages *metadata.NPMPackagesRepo

	Path  storage.PathStore
	Trash storage.Trash
	Audit audit.Logger

	MaxPutBytes int64
	RepoRoot    string
}

// Handler serves the npm registry surface. Constructed by New, mounted
// on a chi router via Mount.
type Handler struct {
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	sessions *metadata.SessionsRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	members  *metadata.MembersRepo

	packages *metadata.NPMPackagesRepo

	pathStore   storage.PathStore
	trash       storage.Trash
	auditLogger audit.Logger

	maxPutBytes int64
	repoRoot    string
}

// defaultMaxPutBytes caps a publish body. npm tarballs are base64-inlined
// in the publish JSON (×1.37 size), so 256 MiB of body ≈ 180 MiB tarball —
// far beyond any sane package.
const defaultMaxPutBytes = int64(256) << 20

// New constructs an npm Handler from deps.
func New(d Deps) *Handler {
	max := d.MaxPutBytes
	if max <= 0 {
		max = defaultMaxPutBytes
	}
	return &Handler{
		db:          d.DB,
		users:       d.Users,
		apiKeys:     d.APIKeys,
		sessions:    d.Sessions,
		repos:       d.Repos,
		projects:    d.Projects,
		members:     d.Members,
		packages:    d.Packages,
		pathStore:   d.Path,
		trash:       d.Trash,
		auditLogger: d.Audit,
		maxPutBytes: max,
		repoRoot:    d.RepoRoot,
	}
}

// Mount registers the npm routes on parent. Mirrors the rpm/goproxy
// middleware chain (AnonymousReadOK + SkipIfActor(BasicOrAPIKey)).
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(common.RepoPublicReadLookup(h.projects, h.repos), common.RepoURLExtractor("npm"), common.AttachAnonymous))
		r.Use(common.SkipIfActor(authmw.BasicOrAPIKey(midDeps)))

		r.Get("/{project}/npm/{repo}/*", h.get)

		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "npm"))
			rw.Put("/{project}/npm/{repo}/*", h.publish)
			rw.Delete("/{project}/npm/{repo}/*", h.delete)
		})
	})
}

// npmRequest is the parsed registry URL tail.
type npmRequest struct {
	// Op is one of "ping", "packument", "tarball", "publish", "delete".
	Op string
	// Name is the decoded package name (may carry an @scope/ prefix).
	Name string
	// File is the tarball filename for Op=tarball.
	File string
	// Version is the version for Op=delete.
	Version string
}

var errBadNpmURL = errors.New("malformed npm request path")

// parseNpmRequest splits the wildcard tail of a registry URL:
//
//	-/ping
//	<name>                       (GET packument / PUT publish)
//	<name>/-/<file>.tgz          (GET tarball)
//	<name>/-/<version>           (DELETE)
//
// npm URL-encodes the scope separator (@scope%2fname); decode once, then
// validate. Validation rejects traversal, NUL, backslash, and any name
// outside npm's charset before DB or filesystem access.
func parseNpmRequest(rest, method string) (npmRequest, error) {
	unesc, err := url.PathUnescape(rest)
	if err != nil || strings.ContainsRune(unesc, '\x00') {
		return npmRequest{}, errBadNpmURL
	}
	if unesc == "-/ping" {
		return npmRequest{Op: "ping"}, nil
	}
	name, file, found := strings.Cut(unesc, "/-/")
	if err := validatePackageName(name); err != nil {
		return npmRequest{}, errBadNpmURL
	}
	if !found {
		switch method {
		case http.MethodPut:
			return npmRequest{Op: "publish", Name: name}, nil
		default:
			return npmRequest{Op: "packument", Name: name}, nil
		}
	}
	if file == "" || strings.ContainsAny(file, "/\\") {
		return npmRequest{}, errBadNpmURL
	}
	if method == http.MethodDelete {
		return npmRequest{Op: "delete", Name: name, Version: file}, nil
	}
	if !strings.HasSuffix(file, ".tgz") {
		return npmRequest{}, errBadNpmURL
	}
	return npmRequest{Op: "tarball", Name: name, File: file}, nil
}

// validatePackageName enforces npm's name rules (lowercase, URL-safe,
// optional @scope/ prefix, ≤214 chars) — a strict superset of what path
// safety needs: "..", "/", and uppercase are all unrepresentable.
func validatePackageName(name string) error {
	if name == "" || len(name) > 214 {
		return errBadNpmURL
	}
	parts := []string{name}
	if strings.HasPrefix(name, "@") {
		scope, base, ok := strings.Cut(name[1:], "/")
		if !ok || scope == "" || base == "" {
			return errBadNpmURL
		}
		parts = []string{scope, base}
	}
	for _, p := range parts {
		if p == "" || strings.HasPrefix(p, ".") || strings.HasPrefix(p, "_") {
			return errBadNpmURL
		}
		for _, c := range p {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9',
				c == '-', c == '_', c == '.', c == '~':
			default:
				return errBadNpmURL
			}
		}
	}
	return nil
}

// validateVersion enforces a conservative version charset (semver chars
// only); npm versions never contain separators or traversal sequences.
func validateVersion(v string) error {
	if v == "" || len(v) > 256 {
		return errBadNpmURL
	}
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '+':
		default:
			return errBadNpmURL
		}
	}
	if strings.Contains(v, "..") {
		return errBadNpmURL
	}
	return nil
}

// tarballName derives the canonical tarball filename for (name, version):
// the scope-less base name + "-" + version + ".tgz" (npm convention).
func tarballName(name, version string) string {
	base := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		base = name[i+1:]
	}
	return base + "-" + version + ".tgz"
}

// resolved wraps a successful project+repo lookup plus the parsed tail.
type resolved struct {
	project *metadata.Project
	repo    *metadata.Repo
	req     npmRequest
}

// resolveRepo validates {project}+{repo} URL params, looks up the repo
// row, parses the registry tail, and returns the resolved request.
// Writes a 404/400 to w on miss and returns ok=false.
func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (resolved, bool) {
	projectName := chi.URLParam(r, "project")
	if projectName == "" {
		projectName = chi.URLParam(r, "name")
	}
	repoName := chi.URLParam(r, "repo")
	if projectName == "" || repoName == "" {
		http.Error(w, "missing project or repo", http.StatusNotFound)
		return resolved{}, false
	}
	proj, err := h.projects.FindByName(r.Context(), projectName)
	if err != nil || proj == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return resolved{}, false
	}
	rr, err := h.repos.FindByTriple(r.Context(), proj.ID, "npm", repoName)
	if err != nil || rr == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return resolved{}, false
	}
	req, perr := parseNpmRequest(chi.URLParam(r, "*"), r.Method)
	if perr != nil {
		http.Error(w, "malformed package path", http.StatusBadRequest)
		return resolved{}, false
	}
	return resolved{project: proj, repo: rr, req: req}, true
}

// storageKeyFor builds the PathStore-relative key for a tarball. The
// package name's scope slash (if any) becomes a directory level.
func storageKeyFor(project, repo, name, file string) string {
	return strings.Join([]string{project, "npm", repo, name, "-", file}, "/")
}

// auditEvent records an npm_package audit event via common.AuditEvent.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	common.AuditEvent(h.auditLogger, r, kind, "npm_package", targetID, outcome, details)
}

// requireRepoWrite enforces the maintainer-required policy for publishes
// and deletes (see common.RequireRepoWrite).
func (h *Handler) requireRepoWrite(ctx context.Context, actor auth.Actor, projectID int64, action auth.Action) bool {
	return common.RequireRepoWrite(ctx, actor, h.members, projectID, action)
}

// actorCanRead consults auth.Can for ActionRepoRead (see common.ActorCanRead).
func (h *Handler) actorCanRead(r *http.Request, repo *metadata.Repo) bool {
	return common.ActorCanRead(r, h.members, repo)
}
