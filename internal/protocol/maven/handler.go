// Package maven implements a hosted Maven repository at
// /<project>/maven/<repo>/...
//
// A Maven repository is files-over-HTTP with a fixed directory layout:
//
//	<groupId dirs>/<artifactId>/<version>/<artifactId>-<version>[-classifier].<ext>
//
// `mvn deploy` (and Gradle's maven-publish) PUT the primary artifacts,
// their checksum sidecars (.sha1/.md5/...), and maven-metadata.xml —
// the deploy plugin downloads, merges, and re-uploads metadata itself,
// so the server stores files verbatim and never generates metadata.
//
// Rows in maven_artifacts are created only for primary artifact files
// (jar/pom/war/... — anything that is not a checksum or metadata file);
// sidecars live on disk only.
//
// Consume/publish with the standard <repository>/<distributionManagement>
// blocks pointing at https://<host>/<project>/maven/<repo>.
package maven

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	authmw "github.com/vladoportos/omnirepo/internal/auth/middleware"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/common"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// Deps bundles the dependencies the Maven handler needs.
type Deps struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	APIKeys  *metadata.APIKeysRepo
	Sessions *metadata.SessionsRepo
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Members  *metadata.MembersRepo

	Artifacts *metadata.MavenArtifactsRepo

	Path  storage.PathStore
	Trash storage.Trash
	Audit audit.Logger

	MaxPutBytes int64
	RepoRoot    string
}

// Handler serves the Maven repository surface.
type Handler struct {
	db       *metadata.DB
	users    *metadata.UsersRepo
	apiKeys  *metadata.APIKeysRepo
	sessions *metadata.SessionsRepo
	repos    *metadata.ReposRepo
	projects *metadata.ProjectsRepo
	members  *metadata.MembersRepo

	artifacts *metadata.MavenArtifactsRepo

	pathStore   storage.PathStore
	trash       storage.Trash
	auditLogger audit.Logger

	maxPutBytes int64
	repoRoot    string
}

// defaultMaxPutBytes caps a single artifact PUT (spec-default 5 GiB,
// matching the other file-based protocols).
const defaultMaxPutBytes = int64(5) << 30

// New constructs a Maven Handler from deps.
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
		artifacts:   d.Artifacts,
		pathStore:   d.Path,
		trash:       d.Trash,
		auditLogger: d.Audit,
		maxPutBytes: max,
		repoRoot:    d.RepoRoot,
	}
}

// Mount registers the Maven routes on parent. Mirrors the rpm/raw
// middleware chain.
func (h *Handler) Mount(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.Group(func(r chi.Router) {
		r.Use(httpx.AnonymousReadOK(common.RepoPublicReadLookup(h.projects, h.repos), common.RepoURLExtractor("maven"), common.AttachAnonymous))
		r.Use(common.SkipIfActor(authmw.BasicOrAPIKey(midDeps)))

		r.Get("/{project}/maven/{repo}/*", h.get)
		r.Head("/{project}/maven/{repo}/*", h.get)

		r.Group(func(rw chi.Router) {
			rw.Use(httpx.MirrorGuardFixed(h.repos, h.projects, "maven"))
			rw.Put("/{project}/maven/{repo}/*", h.put)
			rw.Delete("/{project}/maven/{repo}/*", h.delete)
		})
	})
}

// resolved wraps a successful project+repo lookup plus the validated
// repo-relative path.
type resolved struct {
	project *metadata.Project
	repo    *metadata.Repo
	relPath string
}

// resolveRepo validates {project}+{repo} URL params, looks up the repo
// row, and validates the wildcard path. Writes 404/400 on miss.
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
	rr, err := h.repos.FindByTriple(r.Context(), proj.ID, "maven", repoName)
	if err != nil || rr == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return resolved{}, false
	}
	rest := chi.URLParam(r, "*")
	if dec, derr := url.PathUnescape(rest); derr == nil {
		rest = dec
	}
	cleaned, perr := validateRepoPath(rest)
	if perr != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return resolved{}, false
	}
	return resolved{project: proj, repo: rr, relPath: cleaned}, true
}

// validateRepoPath rejects traversal, NUL bytes, backslashes, empty/dot
// segments, and non-canonical paths (deb validatePoolSubpath shape).
func validateRepoPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\\') {
		return "", errors.New("invalid byte in path")
	}
	p := strings.TrimPrefix(raw, "/")
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("invalid segment")
		}
	}
	cleaned := path.Clean("/" + p)
	if strings.HasPrefix(cleaned, "/..") {
		return "", errors.New("path escape")
	}
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned != p {
		return "", errors.New("non-canonical")
	}
	return cleaned, nil
}

// gav is the GroupId/ArtifactId/Version triple parsed from a layout path.
type gav struct {
	GroupID    string
	ArtifactID string
	Version    string
	Classifier string
	Extension  string
}

// classifyPath decides whether relPath names a primary artifact (→ row)
// or a sidecar (checksums, signatures, maven-metadata.xml → disk only).
// Primary requires the canonical layout depth: group dirs / artifact /
// version / file.
func classifyPath(relPath string) (g gav, primary bool) {
	base := path.Base(relPath)
	for _, suffix := range []string{".sha1", ".md5", ".sha256", ".sha512", ".asc"} {
		if strings.HasSuffix(base, suffix) {
			return gav{}, false
		}
	}
	if strings.HasPrefix(base, "maven-metadata.xml") {
		return gav{}, false
	}
	segs := strings.Split(relPath, "/")
	if len(segs) < 4 {
		// Need at least one group segment + artifact + version + file.
		return gav{}, false
	}
	file := segs[len(segs)-1]
	version := segs[len(segs)-2]
	artifact := segs[len(segs)-3]
	group := strings.Join(segs[:len(segs)-3], ".")

	g = gav{GroupID: group, ArtifactID: artifact, Version: version}
	if dot := strings.LastIndexByte(file, '.'); dot > 0 {
		g.Extension = file[dot+1:]
		stem := file[:dot]
		// classifier = whatever follows "<artifact>-<version>-". Snapshot
		// timestamped stems don't match the prefix; classifier stays "".
		if rest, ok := strings.CutPrefix(stem, artifact+"-"+version+"-"); ok {
			g.Classifier = rest
		}
	}
	return g, g.Extension != ""
}

// storageKeyFor builds the PathStore-relative key for a repo file.
func storageKeyFor(project, repo, relPath string) string {
	return strings.Join([]string{project, "maven", repo, relPath}, "/")
}

// auditEvent records a maven_artifact audit event via common.AuditEvent.
func (h *Handler) auditEvent(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	common.AuditEvent(h.auditLogger, r, kind, "maven_artifact", targetID, outcome, details)
}

// requireRepoWrite enforces the maintainer-required policy (see
// common.RequireRepoWrite).
func (h *Handler) requireRepoWrite(ctx context.Context, actor auth.Actor, projectID int64, action auth.Action) bool {
	return common.RequireRepoWrite(ctx, actor, h.members, projectID, action)
}

// actorCanRead consults auth.Can for ActionRepoRead (see common.ActorCanRead).
func (h *Handler) actorCanRead(r *http.Request, repo *metadata.Repo) bool {
	return common.ActorCanRead(r, h.members, repo)
}

// get serves GET/HEAD of any stored file.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if !h.actorCanRead(r, res.repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	abs := filepath.Join(h.repoRoot, filepath.FromSlash(storageKeyFor(res.project.Name, res.repo.Name, res.relPath)))
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		if err == nil || errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.ErrorContext(r.Context(), "maven.get.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		slog.ErrorContext(r.Context(), "maven.get.open_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", contentTypeFor(res.relPath))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

// contentTypeFor maps common Maven file suffixes onto MIME types.
func contentTypeFor(p string) string {
	switch {
	case strings.HasSuffix(p, ".pom"), strings.HasSuffix(p, ".xml"):
		return "application/xml"
	case strings.HasSuffix(p, ".jar"), strings.HasSuffix(p, ".war"),
		strings.HasSuffix(p, ".ear"), strings.HasSuffix(p, ".aar"):
		return "application/java-archive"
	case strings.HasSuffix(p, ".sha1"), strings.HasSuffix(p, ".md5"),
		strings.HasSuffix(p, ".sha256"), strings.HasSuffix(p, ".sha512"):
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// put handles PUT of any layout file — primary artifacts get a
// maven_artifacts row + FTS entry; sidecars are stored verbatim.
func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionMavenUpload) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	defer func() { _ = r.Body.Close() }()
	st, ok := common.StageBody(w, r, h.repoRoot, "maven", "maven-upload-*", path.Base(res.relPath), h.maxPutBytes)
	if !ok {
		return
	}
	defer func() { _ = os.Remove(st.TmpPath) }()

	// Captured before the overwrite so the failure path knows whether the
	// on-disk file still backs a committed row.
	prior, priorErr := h.artifacts.FindByPath(r.Context(), res.repo.ID, res.relPath)
	hadRow := priorErr == nil && prior != nil

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.relPath)
	if !common.PromoteStaged(w, r, h.pathStore, "maven", storageKey, st.TmpPath, path.Base(res.relPath)) {
		return
	}

	g, primary := classifyPath(res.relPath)
	if primary {
		if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
			if _, err := h.artifacts.Upsert(r.Context(), tx, &metadata.MavenArtifact{
				RepoID:     res.repo.ID,
				GroupID:    g.GroupID,
				ArtifactID: g.ArtifactID,
				Version:    g.Version,
				Classifier: g.Classifier,
				Extension:  g.Extension,
				Filename:   path.Base(res.relPath),
				Path:       res.relPath,
				SizeBytes:  st.Size,
				SHA256:     st.Sum256,
			}); err != nil {
				return err
			}
			// FTS rows are keyed on the layout path (raw's convention) so
			// identical-content artifacts at different paths never delete
			// each other's search entries, and redeploys replace in place.
			if err := metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, res.relPath); err != nil {
				return err
			}
			return metadata.IndexArtifact(r.Context(), tx,
				res.repo.ID, g.GroupID+":"+g.ArtifactID, g.Version, res.relPath)
		}); err != nil {
			// Fresh upload: the file is an orphan — remove it. Redeploy: the
			// new bytes already replaced the old file, so deleting would
			// leave the surviving row with NO file; keep the bytes (a PUT
			// retry heals the row/file mismatch via the upsert).
			if !hadRow {
				_ = h.pathStore.Delete(r.Context(), storageKey)
			}
			slog.ErrorContext(r.Context(), "maven.put.commit_failed",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("path", res.relPath),
				slog.Bool("kept_file", hadRow),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		h.auditEvent(r, audit.EvtMavenUpload, res.relPath, "ok", map[string]any{
			"project":     res.project.Name,
			"repo":        res.repo.Name,
			"group_id":    g.GroupID,
			"artifact_id": g.ArtifactID,
			"version":     g.Version,
			"size_bytes":  st.Size,
		})
	}

	w.WriteHeader(http.StatusCreated)
}

// delete handles DELETE of a stored file. Primary artifacts drop their
// row + FTS entry in a tx committed BEFORE the trash move (rpm/goproxy
// ordering); sidecars are trash-moved directly.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionUpdateRepo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	abs := filepath.Join(h.repoRoot, filepath.FromSlash(storageKeyFor(res.project.Name, res.repo.Name, res.relPath)))
	row, rowErr := h.artifacts.FindByPath(r.Context(), res.repo.ID, res.relPath)
	if rowErr != nil && !errors.Is(rowErr, metadata.ErrNotFound) {
		// A real lookup failure must not be treated as "row absent" — that
		// would trash the file while a stale row survives.
		slog.ErrorContext(r.Context(), "maven.delete.row_lookup_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", rowErr),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	onDisk := false
	if _, err := os.Stat(abs); err == nil {
		onDisk = true
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.ErrorContext(r.Context(), "maven.delete.stat_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("path", res.relPath),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if rowErr != nil && !onDisk {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if rowErr == nil && row != nil {
		if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
			if err := h.artifacts.Delete(r.Context(), tx, row.ID); err != nil {
				return err
			}
			return metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, res.relPath)
		}); err != nil {
			slog.ErrorContext(r.Context(), "maven.delete.commit_failed",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("path", res.relPath),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}

	if onDisk {
		if _, err := h.trash.Move(r.Context(), abs, "maven-artifact", res.repo.ID, auth.ActorLoginFromContext(r.Context())); err != nil {
			slog.WarnContext(r.Context(), "maven.delete.trash_failed_post_commit",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.String("path", res.relPath),
				slog.Any("err", err),
			)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
	}

	h.auditEvent(r, audit.EvtMavenDelete, res.relPath, "ok", map[string]any{
		"project": res.project.Name,
		"repo":    res.repo.Name,
		"path":    res.relPath,
	})

	w.WriteHeader(http.StatusNoContent)
}

// DeleteREST is the exported wrapper for the session-authed /api/v1 shim.
func (h *Handler) DeleteREST(w http.ResponseWriter, r *http.Request) {
	h.delete(w, r)
}
