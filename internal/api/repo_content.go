package api

// Repo content listing — WALKTHROUGH-FINDINGS F-3.
//
// Before this file, the per-repo detail pages (RpmRepoPage, DebRepoPage,
// PypiRepoPage, HelmRepoPage, RawRepoPage) all used hardcoded empty arrays
// and rendered "No X found" even after successful PUTs. These endpoints fill
// the gap with a single route:
//
//	GET /api/v1/projects/{name}/repos/{type}/{repo}/content
//	  → []RepoContentEntry, shape depends on repo type
//
// Every row shares a small envelope (name, version, size_bytes, uploaded_at,
// scan_severity) so the UI can render a generic table; type-specific fields
// are carried in the Extra map. Paginated via limit/offset, capped at 500.

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// RepoContentEntry is the JSON row shape for GET /repos/.../content.
//
// Name + Version are always present (for RAW, Name = path, Version = "");
// every other per-type field lives under Extra so the frontend can destructure
// what it needs without a new DTO per protocol.
type RepoContentEntry struct {
	ID            int64          `json:"id"`
	Name          string         `json:"name"`
	Version       string         `json:"version,omitempty"`
	SizeBytes     int64          `json:"size_bytes"`
	UploadedAt    string         `json:"uploaded_at"`
	ScanSeverity  string         `json:"scan_severity,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

func (d Deps) mountRepoContent(r chi.Router) {
	r.Get("/projects/{name}/repos/{type}/{repo}/content", d.handleListRepoContent)
}

// handleListRepoContent dispatches to the per-type query. Membership is
// enforced once here rather than inside each query builder so the short
// path is uniform and the repo-lookup hits sqlite once.
func (d Deps) handleListRepoContent(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}
	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	if _, ok := validRepoTypes[repoType]; !ok {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil || p == nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}
	repo, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil || repo == nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}
	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "not a project member")
		return
	}

	limit, offset := parseLimitOffset(r)

	var entries []RepoContentEntry
	var qerr error
	switch repoType {
	case "rpm":
		entries, qerr = d.listRPMContent(r, repo.ID, limit, offset)
	case "deb":
		entries, qerr = d.listDebContent(r, repo.ID, limit, offset)
	case "pypi":
		entries, qerr = d.listPypiContent(r, repo.ID, limit, offset)
	case "helm":
		entries, qerr = d.listHelmContent(r, repo.ID, limit, offset)
	case "raw":
		entries, qerr = d.listRawContent(r, repo.ID, limit, offset)
	case "git", "docker":
		// Git and Docker have their own dedicated listing surfaces (git
		// tree/refs under /projects/.../git/..., docker tags under /v2).
		// Return an empty list so the UI doesn't error, but the tabs there
		// should link to the protocol-specific views instead.
		entries = []RepoContentEntry{}
	default:
		entries = []RepoContentEntry{}
	}
	if qerr != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if entries == nil {
		entries = []RepoContentEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func parseLimitOffset(r *http.Request) (int64, int64) {
	limit := int64(100)
	if q := r.URL.Query().Get("limit"); q != "" {
		if v, err := strconv.ParseInt(q, 10, 64); err == nil && v > 0 {
			if v > 500 {
				v = 500
			}
			limit = v
		}
	}
	offset := int64(0)
	if q := r.URL.Query().Get("offset"); q != "" {
		if v, err := strconv.ParseInt(q, 10, 64); err == nil && v >= 0 {
			offset = v
		}
	}
	return limit, offset
}

func (d Deps) listRPMContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, name, version, release, arch, size_bytes, filename, uploaded_at
		FROM rpm_packages WHERE repo_id=?
		ORDER BY name ASC, version ASC, release ASC
		LIMIT ? OFFSET ?
	`, repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id int64
		var name, version, release, arch, filename, uploadedAt string
		var size int64
		if err := rows.Scan(&id, &name, &version, &release, &arch, &size, &filename, &uploadedAt); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:         id,
			Name:       name,
			Version:    version,
			SizeBytes:  size,
			UploadedAt: uploadedAt,
			Extra: map[string]any{
				"release":  release,
				"arch":     arch,
				"filename": filename,
			},
		})
	}
	return out, rows.Err()
}

func (d Deps) listDebContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	// Join apt_suites so the UI filter chips (Suite / Component) have the
	// values they need — deb_packages stores only suite_id, not the names.
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT d.id, d.package, d.version, d.architecture,
		       COALESCE(s.suite, ''), COALESCE(s.component, ''), COALESCE(d.section, ''),
		       d.size_bytes, d.filename, d.uploaded_at
		FROM deb_packages d
		LEFT JOIN apt_suites s ON s.id = d.suite_id
		WHERE d.repo_id=?
		ORDER BY d.package ASC, d.version ASC
		LIMIT ? OFFSET ?
	`, repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id int64
		var pkg, version, arch, suite, component, section, filename, uploadedAt string
		var size int64
		if err := rows.Scan(&id, &pkg, &version, &arch, &suite, &component, &section,
			&size, &filename, &uploadedAt); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:         id,
			Name:       pkg,
			Version:    version,
			SizeBytes:  size,
			UploadedAt: uploadedAt,
			Extra: map[string]any{
				"architecture": arch,
				"suite":        suite,
				"component":    component,
				"section":      section,
				"filename":     filename,
			},
		})
	}
	return out, rows.Err()
}

func (d Deps) listPypiContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, project_normalized, version, filename, kind,
		       COALESCE(requires_python, ''), size_bytes, uploaded_at
		FROM pypi_files WHERE repo_id=?
		ORDER BY project_normalized ASC, version ASC, filename ASC
		LIMIT ? OFFSET ?
	`, repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id int64
		var project, version, filename, kind, reqPy, uploadedAt string
		var size int64
		if err := rows.Scan(&id, &project, &version, &filename, &kind, &reqPy, &size, &uploadedAt); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:         id,
			Name:       project,
			Version:    version,
			SizeBytes:  size,
			UploadedAt: uploadedAt,
			Extra: map[string]any{
				"filename":        filename,
				"kind":            kind,
				"requires_python": reqPy,
			},
		})
	}
	return out, rows.Err()
}

func (d Deps) listHelmContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, name, version, app_version, description, size_bytes, filename, uploaded_at
		FROM helm_charts WHERE repo_id=?
		ORDER BY name ASC, version ASC
		LIMIT ? OFFSET ?
	`, repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id int64
		var name, version, appVersion, description, filename, uploadedAt string
		var size int64
		if err := rows.Scan(&id, &name, &version, &appVersion, &description, &size, &filename, &uploadedAt); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:         id,
			Name:       name,
			Version:    version,
			SizeBytes:  size,
			UploadedAt: uploadedAt,
			Extra: map[string]any{
				"app_version": appVersion,
				"description": description,
				"filename":    filename,
			},
		})
	}
	return out, rows.Err()
}

func (d Deps) listRawContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT path, size_bytes, COALESCE(mime, ''), COALESCE(sha256, ''), modified
		FROM raw_files WHERE repo_id=?
		ORDER BY path ASC
		LIMIT ? OFFSET ?
	`, repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var path, mime, sha string
		var size int64
		var modified sql.NullTime
		if err := rows.Scan(&path, &size, &mime, &sha, &modified); err != nil {
			return nil, err
		}
		uploaded := ""
		if modified.Valid {
			uploaded = modified.Time.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		out = append(out, RepoContentEntry{
			// RAW rows have no surrogate id — the path is the primary key,
			// so the UI keys its list rows off name instead.
			Name:       path,
			SizeBytes:  size,
			UploadedAt: uploaded,
			Extra: map[string]any{
				"mime":   mime,
				"sha256": sha,
			},
		})
	}
	return out, rows.Err()
}
