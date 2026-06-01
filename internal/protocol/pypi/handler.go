package pypi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	authmw "github.com/vladoportos/omnirepo/internal/auth/middleware"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// SeverityGateFn is the scan-severity gate hook. Plug nil for tests; app.Run
// wires a real gate that inspects repos.block_on_severity + scans.
type SeverityGateFn func(ctx context.Context, repoID int64, artifactKind, artifactID string) (blocked bool, severity string, scanID int64)

// Deps bundles the dependencies the PyPI handler needs at construction time.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Sessions *metadata.SessionsRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo

	PyPIFiles *metadata.PyPIFilesRepo
	Scans     *metadata.ScansRepo
	Coalescer *regen.Registry
	PEP694    *PEP694Sessions

	Path  storage.PathStore
	Trash storage.Trash
	Audit audit.Logger

	SeverityGate SeverityGateFn

	MaxPutBytes int64
	RepoRoot    string
}

// Handler serves the PyPI protocol surface (PEP 503/691 reads + twine-legacy
// /legacy/ multipart upload + PEP 694 /+upload/ session API).
type Handler struct {
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	sessions *metadata.SessionsRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	members  *metadata.MembersRepo

	pypiFiles *metadata.PyPIFilesRepo
	scans     *metadata.ScansRepo
	coalescer *regen.Registry
	pep694    *PEP694Sessions

	pathStore    storage.PathStore
	trash        storage.Trash
	auditLogger  audit.Logger
	severityGate SeverityGateFn

	maxPutBytes int64
	repoRoot    string

	// Per-storageKey upload serialization.
	// Two concurrent first-uploads of the same filename would otherwise
	// both pass the pre-check, both `PathStore.Put` (last-rename-wins on
	// disk), and then only one would commit a pypi_files row — leaving
	// the DB winner's digest pointing at the loser's bytes. Holding this
	// mutex from pre-check through commit/rollback serializes the critical
	// section so the second-comer always observes the committed row and
	// takes the 409 branch without touching disk. Entries are kept for
	// the process lifetime (tiny, ~40 B each, bounded by distinct
	// filenames ever uploaded this run) — deleting on unlock would race
	// against goroutines that LoadOrStore'd the same key between our
	// Unlock and Delete.
	uploadLocks sync.Map
}

// lockUpload serializes the pre-check → Put → commit → rollback critical
// section for storageKey across concurrent handler goroutines. Returns
// the release func the caller must defer.
func (h *Handler) lockUpload(key string) func() {
	v, _ := h.uploadLocks.LoadOrStore(key, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// defaultMaxPutBytes is the spec-default 5 GiB cap on a single PUT body.
const defaultMaxPutBytes = int64(5) << 30

// New constructs a PyPI Handler from deps.
func New(d Deps) *Handler {
	max := d.MaxPutBytes
	if max <= 0 {
		max = defaultMaxPutBytes
	}
	pep := d.PEP694
	if pep == nil {
		pep = NewPEP694Sessions(1 * time.Hour)
	}
	return &Handler{
		db:           d.DB,
		users:        d.Users,
		apiKeys:      d.APIKeys,
		sessions:     d.Sessions,
		repos:        d.Repos,
		projects:     d.Projects,
		members:      d.Members,
		pypiFiles:    d.PyPIFiles,
		scans:        d.Scans,
		coalescer:    d.Coalescer,
		pep694:       pep,
		pathStore:    d.Path,
		trash:        d.Trash,
		auditLogger:  d.Audit,
		severityGate: d.SeverityGate,
		maxPutBytes:  max,
		repoRoot:     d.RepoRoot,
	}
}

// Mount registers the PyPI routes on parent. Reads honor AnonymousReadOK
// for public_read repos; writes always require an authenticated actor.
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(h.lookupRepoPublicRead, h.extractRepoFromPyPIURL, attachAnonymous))
		r.Use(skipIfActor(authmw.BasicOrAPIKey(midDeps)))

		// Reads: PEP 503 HTML + PEP 691 JSON via content negotiation.
		r.Get("/{project}/pypi/{repo}/simple/", h.getSimpleIndex)
		r.Get("/{project}/pypi/{repo}/simple/{name}/", h.getProjectIndex)

		// File serving.
		r.Get("/{project}/pypi/{repo}/packages/{filename}", h.getPackage)

		// Gate write paths (twine-legacy + PEP 694 session API + file
		// delete) behind MirrorGuardFixed so mirror repos reject mutating
		// ops with 403 repo.repo_is_mirror.
		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "pypi"))
			// File delete — a mutating op that must not be available on
			// mirror repos (the DELETE route previously sat outside the
			// guard group).
			rw.Delete("/{project}/pypi/{repo}/packages/{filename}", h.deletePackage)
			// twine / uv publish — multipart legacy.
			rw.Post("/{project}/pypi/{repo}/legacy/", h.handleLegacyUpload)

			// PEP 694 upload session API.
			rw.Post("/{project}/pypi/{repo}/+upload/", h.handleCreateSession)
			rw.Put("/{project}/pypi/{repo}/+upload/{session_id}/{filename}", h.handleUploadFile)
			rw.Post("/{project}/pypi/{repo}/+upload/{session_id}/commit", h.handleCommit)
		})
	})
}

// attachAnonymous wires an anonymous Actor into ctx (for AnonymousReadOK).
var attachAnonymous httpx.AttachAnonymousFn = func(ctx context.Context) context.Context {
	return auth.WithActor(ctx, auth.Actor{Kind: auth.ActorKindAnonymous})
}

// skipIfActor wraps a middleware so it pass-throughs when an Actor is
// already in ctx (the anonymous fast path set by AnonymousReadOK).
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

// extractRepoFromPyPIURL returns (project, "pypi", repo, ok=true) when
// the URL matches /<project>/pypi/<repo>/.... Used by AnonymousReadOK.
func (h *Handler) extractRepoFromPyPIURL(r *http.Request) (project, repoType, repo string, ok bool) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(p, "/", 4)
	if len(parts) < 3 {
		return "", "", "", false
	}
	if parts[1] != "pypi" {
		return "", "", "", false
	}
	if parts[0] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], "pypi", parts[2], true
}

// lookupRepoPublicRead resolves (project, "pypi", repo) → (public_read, found).
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

// resolved bundles the result of resolveRepo.
type resolved struct {
	project *metadata.Project
	repo    *metadata.Repo
}

// resolveRepo resolves project + repo URL params; writes 404 to w on miss.
//
// Protocol routes use {project}; the /api/v1 session-authed
// row-delete shim uses {name}. Fall back so one handler serves
// both mount points.
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
	rr, err := h.repos.FindByTriple(r.Context(), proj.ID, "pypi", repoName)
	if err != nil || rr == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return resolved{}, false
	}
	return resolved{project: proj, repo: rr}, true
}

// validateFilename rejects path traversal, NUL bytes, separators, and
// non-canonical names. PyPI files live in a single packages/ dir per
// repo; no nested layout.
func validateFilename(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty filename")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("nul byte in filename")
	}
	if strings.ContainsAny(raw, "/\\") {
		return "", errors.New("filename must not contain separators")
	}
	if raw == "." || raw == ".." {
		return "", errors.New("invalid filename")
	}
	if path.Clean(raw) != raw {
		return "", errors.New("non-canonical filename")
	}
	return raw, nil
}

// packageStorageKey builds the PathStore key for a given filename.
func packageStorageKey(project, repo, filename string) string {
	return strings.Join([]string{project, "pypi", repo, "packages", filename}, "/")
}

// auditEvent records an audit row with actor + request fields filled in.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	if h.auditLogger == nil {
		return
	}
	e := audit.Event{
		Kind:       kind,
		IP:         r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		TargetKind: "pypi_file",
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

// requireRepoWrite enforces the maintainer-required policy for artifact
// writes/deletes via auth.Can: super-admin bypasses, the project key's / user's
// role is resolved through ResolveMembership, and viewers + viewer-scoped keys
// are denied. Replaces the former role-blind membership check (which let any
// member, and any project-scoped key regardless of role, publish/delete).
func (h *Handler) requireRepoWrite(ctx context.Context, actor auth.Actor, projectID int64, action auth.Action) bool {
	mctx := auth.ResolveMembership(ctx, actor, h.members)
	allowed, _ := auth.Can(mctx, actor, action, auth.Target{
		Kind:      "repo",
		ProjectID: projectID,
	})
	return allowed
}

// actorCanRead checks ActionRepoRead via auth.Can with ctx-resolved
// project membership.
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

// -----------------------------------------------------------------------------
// Read handlers
// -----------------------------------------------------------------------------

// wantsJSON returns true if the request prefers PEP 691 JSON over HTML
// (Accept header includes ContentTypeJSON before ContentTypeHTML, OR
// JSON appears at all and HTML does not).
func wantsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return false
	}
	// Simple precedence scan; quality values are ignored — rare in practice
	// from pip/uv which set explicit single-type Accept headers.
	hasJSON := strings.Contains(accept, ContentTypeJSON)
	hasHTML := strings.Contains(accept, ContentTypeHTML)
	if hasJSON && !hasHTML {
		return true
	}
	if hasJSON && hasHTML {
		// Whichever appears first wins.
		return strings.Index(accept, ContentTypeJSON) < strings.Index(accept, ContentTypeHTML)
	}
	return false
}

// htmlContentType returns the HTML Content-Type header value to emit for
// the PEP 503 / PEP 691 Simple responses. Returns ContentTypeHTML
// (application/vnd.pypi.simple.v1+html) ONLY when the client's Accept
// header explicitly mentions that PEP 691 value. Otherwise returns
// text/html — older pip (<23), uv <0.1, and other clients reject the
// PEP 691 +html content-type outright ("The only supported Content-Type
// is text/html"). PEP 691 itself prescribes this backwards-compat
// default: "servers ... SHOULD default to text/html for backwards
// compatibility."
func htmlContentType(r *http.Request) string {
	if strings.Contains(r.Header.Get("Accept"), ContentTypeHTML) {
		return ContentTypeHTML
	}
	return "text/html; charset=utf-8"
}

// getSimpleIndex serves the top-level /simple/ index. Honors PEP 691
// content negotiation. On a fresh repo (no on-disk index yet), renders an
// empty index synthetically so `pip install --index-url` doesn't 404.
func (h *Handler) getSimpleIndex(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	json := wantsJSON(r)
	simpleDir := filepath.Join(h.repoRoot, res.project.Name, "pypi", res.repo.Name, "simple")
	var name, ct string
	if json {
		name = "index.json"
		ct = ContentTypeJSON
	} else {
		name = "index.html"
		ct = htmlContentType(r)
	}
	abs := filepath.Join(simpleDir, name)
	if data, err := os.ReadFile(abs); err == nil {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		// Don't leak filesystem paths / driver text.
		slog.ErrorContext(r.Context(), "pypi.read_index_failed", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	// Fall back to synthetic empty index so fresh repos serve cleanly.
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(http.StatusOK)
	if json {
		_ = RenderSimpleJSON(w, nil)
	} else {
		_ = RenderSimpleHTML(w, nil)
	}
}

// getProjectIndex serves /simple/<name>/. Redirects 301 if {name} is not
// already PEP 503 normalized.
func (h *Handler) getProjectIndex(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rawName := chi.URLParam(r, "name")
	decoded, err := url.PathUnescape(rawName)
	if err != nil {
		http.Error(w, "invalid project name", http.StatusBadRequest)
		return
	}
	norm := Normalize(decoded)
	if norm == "" {
		http.Error(w, "invalid project name", http.StatusBadRequest)
		return
	}
	if decoded != norm {
		// 301 to canonical normalized URL — PEP 503 compliance.
		target := fmt.Sprintf("/%s/pypi/%s/simple/%s/",
			res.project.Name, res.repo.Name, norm)
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}

	json := wantsJSON(r)
	simpleDir := filepath.Join(h.repoRoot, res.project.Name, "pypi", res.repo.Name, "simple", norm)
	var name, ct string
	if json {
		name = "index.json"
		ct = ContentTypeJSON
	} else {
		name = "index.html"
		ct = htmlContentType(r)
	}
	abs := filepath.Join(simpleDir, name)
	data, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Render an empty index inline so fresh projects (no files yet)
			// don't 404 — PEP 503 says an empty page is valid.
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(http.StatusOK)
			if json {
				_ = RenderProjectJSON(w, norm, nil)
			} else {
				_ = RenderProjectHTML(w, norm, nil)
			}
			return
		}
		slog.ErrorContext(r.Context(), "pypi.read_project_index_failed", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// getPackage serves a wheel or sdist file from /packages/.
func (h *Handler) getPackage(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	filename, err := validateFilename(chi.URLParam(r, "filename"))
	if err != nil {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(
		packageStorageKey(res.project.Name, res.repo.Name, filename),
	))
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "pypi.stat_failed", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if h.severityGate != nil {
		blocked, severity, scanID := h.severityGate(r.Context(), res.repo.ID, "pypi", filename)
		if blocked {
			h.auditEvent(r, audit.EvtPyPIUpload, filename, "blocked", map[string]any{
				"severity": severity,
				"scan_id":  scanID,
			})
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"blocked_by_scan","severity":%q,"scan_id":%d}`, severity, scanID)
			return
		}
	}
	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "pypi.open_failed", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	if strings.HasSuffix(filename, ".whl") {
		w.Header().Set("Content-Type", "application/octet-stream")
	} else if strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".tgz") {
		w.Header().Set("Content-Type", "application/x-gzip")
	} else if strings.HasSuffix(filename, ".zip") {
		w.Header().Set("Content-Type", "application/zip")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
