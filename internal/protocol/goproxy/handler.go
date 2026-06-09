// Package goproxy implements the GOPROXY protocol (go.dev/ref/mod#goproxy-protocol)
// for hosted Go modules at /<project>/go/<repo>/...
//
// Served endpoints (module paths arrive GOPROXY-escaped: uppercase →
// "!"+lowercase):
//
//	GET    /<module>/@v/list             — newline-separated versions
//	GET    /<module>/@v/<version>.info   — {"Version","Time"} JSON
//	GET    /<module>/@v/<version>.mod    — go.mod bytes
//	GET    /<module>/@v/<version>.zip    — module zip
//	GET    /<module>/@latest             — info JSON for the best version
//	PUT    /<module>/@v/<version>.zip    — publish (validated module zip)
//	DELETE /<module>/@v/<version>        — soft-delete via trash
//
// Publishing is a single PUT of the module zip; the server validates the
// zip layout against the declared module@version (x/mod/zip.CheckZip),
// extracts go.mod out of the archive for the .mod endpoint (synthesizing
// a minimal one when absent, mirroring the go command's behavior for
// legacy modules), and records the version row. .info and /@v/list are
// computed from rows; .mod and .zip are served from disk.
//
// Consume with:
//
//	GOPROXY=https://<host>/<project>/go/<repo> GONOSUMCHECK=1 go get <module>@<version>
//
// (GONOSUMDB/GONOSUMCHECK or GOPRIVATE/GONOSUMDB equivalents are required
// because sum.golang.org cannot know private modules; air-gapped installs
// disable the sumdb anyway.)
package goproxy

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	authmw "github.com/vladoportos/omnirepo/internal/auth/middleware"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/common"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// Deps bundles the dependencies the GOPROXY handler needs.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Sessions *metadata.SessionsRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo

	GoModules *metadata.GoModulesRepo

	Path  storage.PathStore
	Trash storage.Trash
	Audit audit.Logger

	MaxPutBytes int64
	RepoRoot    string
}

// Handler serves the GOPROXY protocol surface. Constructed by New,
// mounted on a chi router via Mount.
type Handler struct {
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	sessions *metadata.SessionsRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	members  *metadata.MembersRepo

	goModules *metadata.GoModulesRepo

	pathStore   storage.PathStore
	trash       storage.Trash
	auditLogger audit.Logger

	maxPutBytes int64
	repoRoot    string
}

// defaultMaxPutBytes caps a single module-zip PUT body. The GOPROXY spec
// caps module zips at 500 MiB (x/mod/zip.MaxZipFile); the body cap is set
// slightly above so the zip validator produces the descriptive error
// instead of a generic 413.
const defaultMaxPutBytes = int64(512) << 20

// New constructs a GOPROXY Handler from deps.
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
		goModules:   d.GoModules,
		pathStore:   d.Path,
		trash:       d.Trash,
		auditLogger: d.Audit,
		maxPutBytes: max,
		repoRoot:    d.RepoRoot,
	}
}

// Mount registers the GOPROXY routes on parent. Mirrors the rpm/helm
// middleware chain (AnonymousReadOK + SkipIfActor(BasicOrAPIKey)).
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(common.RepoPublicReadLookup(h.projects, h.repos), common.RepoURLExtractor("go"), common.AttachAnonymous))
		r.Use(common.SkipIfActor(authmw.BasicOrAPIKey(midDeps)))

		r.Get("/{project}/go/{repo}/*", h.get)

		// Write paths gated behind the mirror guard like every other
		// protocol (go repos have no mirror mode yet; the guard is inert
		// until they do).
		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "go"))
			rw.Put("/{project}/go/{repo}/*", h.put)
			rw.Delete("/{project}/go/{repo}/*", h.delete)
		})
	})
}

// resolved wraps a successful project+repo lookup plus the parsed
// GOPROXY tail.
type resolved struct {
	project *metadata.Project
	repo    *metadata.Repo
	req     moduleRequest
}

// moduleRequest is the parsed GOPROXY URL tail.
type moduleRequest struct {
	// ModulePath is the DECODED module path (e.g. github.com/Azure/x).
	ModulePath string
	// EscapedPath is the wire/on-disk form (uppercase → "!"+lowercase).
	EscapedPath string
	// Op is one of "list", "latest", "info", "mod", "zip",
	// "delete" (bare version, DELETE only).
	Op string
	// Version is the canonical semver for info/mod/zip/delete; "" for
	// list/latest.
	Version string
}

var errBadModuleURL = errors.New("malformed GOPROXY request path")

// parseModuleRequest splits the wildcard tail of a GOPROXY URL:
//
//	<escaped>/@latest
//	<escaped>/@v/list
//	<escaped>/@v/<version>.info|.mod|.zip
//	<escaped>/@v/<version>            (DELETE only)
//
// The escaped module path is unescaped and validated via x/mod/module, so
// traversal sequences, NUL bytes, backslashes, and non-module characters
// are all rejected before any DB or filesystem access.
func parseModuleRequest(rest string, isDelete bool) (moduleRequest, error) {
	if strings.ContainsRune(rest, '\x00') {
		return moduleRequest{}, errBadModuleURL
	}
	if esc, ok := strings.CutSuffix(rest, "/@latest"); ok {
		mp, err := decodeModulePath(esc)
		if err != nil {
			return moduleRequest{}, err
		}
		return moduleRequest{ModulePath: mp, EscapedPath: esc, Op: "latest"}, nil
	}
	esc, file, ok := strings.Cut(rest, "/@v/")
	if !ok || strings.Contains(file, "/") {
		return moduleRequest{}, errBadModuleURL
	}
	mp, err := decodeModulePath(esc)
	if err != nil {
		return moduleRequest{}, err
	}
	if file == "list" {
		return moduleRequest{ModulePath: mp, EscapedPath: esc, Op: "list"}, nil
	}
	if isDelete {
		// DELETE addresses the bare version, no extension.
		v, err := decodeVersion(file)
		if err != nil {
			return moduleRequest{}, err
		}
		return moduleRequest{ModulePath: mp, EscapedPath: esc, Op: "delete", Version: v}, nil
	}
	dot := strings.LastIndexByte(file, '.')
	if dot < 0 {
		return moduleRequest{}, errBadModuleURL
	}
	ver, ext := file[:dot], file[dot+1:]
	if ext != "info" && ext != "mod" && ext != "zip" {
		return moduleRequest{}, errBadModuleURL
	}
	v, err := decodeVersion(ver)
	if err != nil {
		return moduleRequest{}, err
	}
	return moduleRequest{ModulePath: mp, EscapedPath: esc, Op: ext, Version: v}, nil
}

// decodeModulePath unescapes and validates a GOPROXY-escaped module path.
func decodeModulePath(escaped string) (string, error) {
	if escaped == "" {
		return "", errBadModuleURL
	}
	mp, err := module.UnescapePath(escaped)
	if err != nil {
		return "", errBadModuleURL
	}
	if err := module.CheckPath(mp); err != nil {
		return "", errBadModuleURL
	}
	return mp, nil
}

// decodeVersion unescapes and validates a canonical semver version.
func decodeVersion(escaped string) (string, error) {
	v, err := module.UnescapeVersion(escaped)
	if err != nil {
		return "", errBadModuleURL
	}
	if !semver.IsValid(v) || module.CanonicalVersion(v) != v {
		return "", errBadModuleURL
	}
	return v, nil
}

// resolveRepo validates {project}+{repo} URL params, looks up the repo row,
// parses the GOPROXY tail, and returns the resolved request. Writes a
// 404/400 to w on miss and returns ok=false.
//
// Protocol routes use {project}; the /api/v1 session-authed shim uses
// {name}. Fall back so one handler serves both mount points.
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
	rr, err := h.repos.FindByTriple(r.Context(), proj.ID, "go", repoName)
	if err != nil || rr == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return resolved{}, false
	}
	req, perr := parseModuleRequest(chi.URLParam(r, "*"), r.Method == http.MethodDelete)
	if perr != nil {
		http.Error(w, "malformed module path", http.StatusBadRequest)
		return resolved{}, false
	}
	return resolved{project: proj, repo: rr, req: req}, true
}

// storageKeyFor builds the PathStore-relative key for a module file
// ("zip" / "mod" extension). Uses the ESCAPED path so on-disk names are
// case-collision-free, exactly like the module cache.
func storageKeyFor(project, repo, escapedPath, version, ext string) string {
	return strings.Join([]string{project, "go", repo, escapedPath, "@v", version + "." + ext}, "/")
}

// auditEvent records a go_module audit event via common.AuditEvent.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	common.AuditEvent(h.auditLogger, r, kind, "go_module", targetID, outcome, details)
}

// requireRepoWrite enforces the maintainer-required policy for module
// publishes/deletes (see common.RequireRepoWrite).
func (h *Handler) requireRepoWrite(ctx context.Context, actor auth.Actor, projectID int64, action auth.Action) bool {
	return common.RequireRepoWrite(ctx, actor, h.members, projectID, action)
}

// actorCanRead consults auth.Can for ActionRepoRead (see common.ActorCanRead).
func (h *Handler) actorCanRead(r *http.Request, repo *metadata.Repo) bool {
	return common.ActorCanRead(r, h.members, repo)
}
