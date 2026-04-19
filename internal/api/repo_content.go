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
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// deriveScanSeverity maps the latest scan row (status + severity summary
// JSON) onto the single string the content table renders:
//
//	""         — no scan row ever
//	"scanning" — pending or running
//	"failed"   — last scan failed
//	"clean"    — done with 0 findings, OR done with an empty/unparseable
//	             summary (treated as "found nothing worth reporting")
//	"critical"|"high"|"medium"|"low" — worst severity from the summary
//
// The UI's SeverityBadge already handles critical..low; the extra
// "scanning"/"failed"/"clean" values are rendered by the repo pages'
// ContentScanBadge shim so status is visible at a glance without
// forcing the user to open the Scan Results tab.
func deriveScanSeverity(status, summaryJSON string) string {
	switch status {
	case "":
		return ""
	case "pending", "running":
		return "scanning"
	case "failed":
		return "failed"
	}
	// status == "done" (or anything non-standard — treat as done so we
	// still surface severity rather than silently dropping the scan).
	var summary struct {
		Critical int `json:"critical"`
		High     int `json:"high"`
		Medium   int `json:"medium"`
		Low      int `json:"low"`
	}
	if summaryJSON != "" {
		_ = json.Unmarshal([]byte(summaryJSON), &summary)
	}
	switch {
	case summary.Critical > 0:
		return "critical"
	case summary.High > 0:
		return "high"
	case summary.Medium > 0:
		return "medium"
	case summary.Low > 0:
		return "low"
	}
	return "clean"
}

// RepoContentEntry is the JSON row shape for GET /repos/.../content.
//
// Name + Version are always present (for RAW, Name = path, Version = "");
// every other per-type field lives under Extra so the frontend can destructure
// what it needs without a new DTO per protocol.
type RepoContentEntry struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version,omitempty"`
	SizeBytes    int64          `json:"size_bytes"`
	UploadedAt   string         `json:"uploaded_at"`
	ScanSeverity string         `json:"scan_severity,omitempty"`
	Extra        map[string]any `json:"extra,omitempty"`
}

// RepoContentPage wraps a paginated slice of RepoContentEntry (F-T18).
//
//   - Items   — the slice for this page.
//   - Total   — total row count for the repo/type (NOT filtered by
//     limit/offset). UI uses this for "Showing N of M".
//   - NextOffset — offset to pass next; nil when the caller has reached
//     the end. Avoids a "+1 probe" round-trip on the frontend.
type RepoContentPage struct {
	Items      []RepoContentEntry `json:"items"`
	Total      int64              `json:"total"`
	NextOffset *int64             `json:"next_offset,omitempty"`
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
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}
	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	if _, ok := validRepoTypes[repoType]; !ok {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil || p == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}
	repo, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil || repo == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}
	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, "not a project member")
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
	case "docker":
		entries, qerr = d.listDockerContent(r, repo.ID, limit, offset)
	case "git":
		// Git has its own dedicated listing surface (tree/refs under
		// /projects/.../git/...). Return an empty list here so the UI's
		// generic content tab doesn't error; repo page routes direct the
		// user to the protocol-specific view.
		entries = []RepoContentEntry{}
	default:
		entries = []RepoContentEntry{}
	}
	if qerr != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if entries == nil {
		entries = []RepoContentEntry{}
	}

	// F-T18: total row-count so the UI can render "Showing N of M" and
	// know when load-more has reached the end. Best-effort — a failing
	// count query falls back to len(entries) + offset so the response
	// still serialises.
	total, terr := d.countRepoContent(r, repoType, repo.ID)
	if terr != nil {
		total = int64(len(entries)) + offset
	}

	var nextOffset *int64
	consumed := offset + int64(len(entries))
	if int64(len(entries)) >= limit && consumed < total {
		next := consumed
		nextOffset = &next
	}

	writeJSON(w, http.StatusOK, RepoContentPage{
		Items:      entries,
		Total:      total,
		NextOffset: nextOffset,
	})
}

// countRepoContent returns the total row count for a repo's content tab.
// Dispatches per type on the same tables the listX helpers query; the
// expressions below mirror repoItemCountExpr's per-type semantics so the
// header count (F-T15) and the content-tab total stay consistent.
func (d Deps) countRepoContent(r *http.Request, repoType string, repoID int64) (int64, error) {
	var (
		query string
		n     int64
	)
	switch repoType {
	case "rpm":
		query = `SELECT COUNT(*) FROM rpm_packages WHERE repo_id=?`
	case "deb":
		query = `SELECT COUNT(*) FROM deb_packages WHERE repo_id=?`
	case "pypi":
		query = `SELECT COUNT(*) FROM pypi_files WHERE repo_id=?`
	case "helm":
		query = `SELECT COUNT(*) FROM helm_charts WHERE repo_id=?`
	case "raw":
		query = `SELECT COUNT(*) FROM raw_files WHERE repo_id=?`
	case "docker":
		query = `SELECT COUNT(*) FROM docker_tags WHERE repo_id=?`
	default:
		return 0, nil
	}
	if err := d.DB.Reader.QueryRowContext(r.Context(), query, repoID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
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

// latestScanJoin is the SQL fragment each content query LEFT JOINs to
// pick up the latest scan for its artifact. The inner subquery picks
// MAX(id) per (repo_id, kind, artifact_id) — scans are append-only, so
// MAX(id) == most recent.
const latestScanJoin = `
	LEFT JOIN scans ls ON ls.id = (
		SELECT MAX(s.id) FROM scans s
		WHERE s.repo_id = ? AND s.artifact_kind = ? AND s.artifact_id = %s
	)`

func (d Deps) listRPMContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	// Artifact-id for RPM scans is the filename — see rpm/put.go
	// where Enqueue gets called with res.filename.
	query := `
		SELECT rp.id, rp.name, rp.version, rp.release, rp.arch, rp.size_bytes,
		       rp.filename, rp.uploaded_at,
		       COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
		FROM rpm_packages rp` + fmtLatestScanJoin("rp.filename") + `
		WHERE rp.repo_id=?
		ORDER BY rp.name ASC, rp.version ASC, rp.release ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query,
		repoID, "rpm", repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id int64
		var name, version, release, arch, filename, uploadedAt string
		var scanStatus, scanSummary string
		var size int64
		if err := rows.Scan(&id, &name, &version, &release, &arch, &size, &filename, &uploadedAt,
			&scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:           id,
			Name:         name,
			Version:      version,
			SizeBytes:    size,
			UploadedAt:   uploadedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
			Extra: map[string]any{
				"release":  release,
				"arch":     arch,
				"filename": filename,
			},
		})
	}
	return out, rows.Err()
}

// fmtLatestScanJoin inserts the right artifact-id expression into the
// latestScanJoin template. We could sprintf at call-time too, but
// keeping the glue in one place keeps the SQL legible.
func fmtLatestScanJoin(artifactIDExpr string) string {
	// Replace the %s in the template; we can't use fmt.Sprintf because
	// it would convert the SQL identifier as if it were a string literal
	// — plain string replace is fine here since the template has exactly
	// one %s and the call sites pass trusted column references.
	const marker = "%s"
	out := latestScanJoin
	for i := 0; i < len(out); i++ {
		if i+len(marker) <= len(out) && out[i:i+len(marker)] == marker {
			return out[:i] + artifactIDExpr + out[i+len(marker):]
		}
	}
	return out
}

func (d Deps) listDebContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	// Join apt_suites so the UI filter chips (Suite / Component) have the
	// values they need — deb_packages stores only suite_id, not the names.
	query := `
		SELECT d.id, d.package, d.version, d.architecture,
		       COALESCE(s.suite, ''), COALESCE(s.component, ''), COALESCE(d.section, ''),
		       d.size_bytes, d.filename, d.uploaded_at,
		       COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
		FROM deb_packages d
		LEFT JOIN apt_suites s ON s.id = d.suite_id` + fmtLatestScanJoin("d.filename") + `
		WHERE d.repo_id=?
		ORDER BY d.package ASC, d.version ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query,
		repoID, "deb", repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id int64
		var pkg, version, arch, suite, component, section, filename, uploadedAt string
		var scanStatus, scanSummary string
		var size int64
		if err := rows.Scan(&id, &pkg, &version, &arch, &suite, &component, &section,
			&size, &filename, &uploadedAt, &scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:           id,
			Name:         pkg,
			Version:      version,
			SizeBytes:    size,
			UploadedAt:   uploadedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
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
	query := `
		SELECT pf.id, pf.project_normalized, pf.version, pf.filename, pf.kind,
		       COALESCE(pf.requires_python, ''), pf.size_bytes, pf.uploaded_at,
		       COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
		FROM pypi_files pf` + fmtLatestScanJoin("pf.filename") + `
		WHERE pf.repo_id=?
		ORDER BY pf.project_normalized ASC, pf.version ASC, pf.filename ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query,
		repoID, "pypi", repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id int64
		var project, version, filename, kind, reqPy, uploadedAt string
		var scanStatus, scanSummary string
		var size int64
		if err := rows.Scan(&id, &project, &version, &filename, &kind, &reqPy, &size, &uploadedAt,
			&scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:           id,
			Name:         project,
			Version:      version,
			SizeBytes:    size,
			UploadedAt:   uploadedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
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
	query := `
		SELECT hc.id, hc.name, hc.version, hc.app_version, hc.description,
		       hc.size_bytes, hc.filename, hc.uploaded_at,
		       COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
		FROM helm_charts hc` + fmtLatestScanJoin("hc.filename") + `
		WHERE hc.repo_id=?
		ORDER BY hc.name ASC, hc.version ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query,
		repoID, "helm", repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id int64
		var name, version, appVersion, description, filename, uploadedAt string
		var scanStatus, scanSummary string
		var size int64
		if err := rows.Scan(&id, &name, &version, &appVersion, &description, &size, &filename, &uploadedAt,
			&scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:           id,
			Name:         name,
			Version:      version,
			SizeBytes:    size,
			UploadedAt:   uploadedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
			Extra: map[string]any{
				"app_version": appVersion,
				"description": description,
				"filename":    filename,
			},
		})
	}
	return out, rows.Err()
}

// listDockerContent produces one row per (image, tag) pair in a docker repo.
// F-T10: previously returned [] because the listing was delegated to /v2,
// but the generic Content tab had no link to that view so repos looked empty
// even when populated with 16+ manifests.
//
// Row shape:
//
//	Name       = "<image>:<tag>"  (or just ":<tag>" for the legacy
//	                               image-less rows pre-migration 021)
//	Version    = tag               (so sort/filter by version chips work)
//	SizeBytes  = manifest body + config blob + all layer blobs. Walks the
//	            manifest JSON via JSON1 to resolve each referenced digest
//	            against docker_blobs. Matches what `docker pull` actually
//	            transfers — the manifest-body-only number (few KB) would
//	            mislead operators inspecting repo contents.
//	Extra.image, Extra.tag, Extra.digest, Extra.media_type
//
// The scan hook uses artifact_id=digest since that's what the oci push path
// enqueues (see internal/protocol/oci/manifests.go).
func (d Deps) listDockerContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	// dockerImageSize = manifest body + config blob + sum(layer blobs).
	// Correlated subqueries with json_each walk the manifest body against
	// docker_blobs. COALESCE each subquery to 0 so manifests with zero
	// layers (empty config-only images) still produce a size = manifest
	// bytes only.
	const dockerImageSizeExpr = `(
		COALESCE(dm.size_bytes, 0)
		+ COALESCE((
			SELECT db.size_bytes FROM docker_blobs db
			WHERE db.digest = json_extract(dm.body, '$.config.digest')
		), 0)
		+ COALESCE((
			SELECT SUM(db.size_bytes)
			FROM json_each(dm.body, '$.layers') jl
			JOIN docker_blobs db ON db.digest = json_extract(jl.value, '$.digest')
		), 0)
	)`
	query := `
		SELECT dt.image, dt.tag, dt.digest, dm.media_type,
		       ` + dockerImageSizeExpr + ` AS total_size,
		       dt.updated_at,
		       COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
		FROM docker_tags dt
		LEFT JOIN docker_manifests dm ON dm.repo_id = dt.repo_id AND dm.digest = dt.digest
		` + fmtLatestScanJoin("dt.digest") + `
		WHERE dt.repo_id=?
		ORDER BY dt.image ASC, dt.tag ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query,
		repoID, "docker", repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var image, tag, digest, mediaType, updatedAt string
		var size sql.NullInt64
		var scanStatus, scanSummary string
		if err := rows.Scan(&image, &tag, &digest, &mediaType, &size, &updatedAt,
			&scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		name := tag
		if image != "" {
			name = image + ":" + tag
		}
		out = append(out, RepoContentEntry{
			Name:         name,
			Version:      tag,
			SizeBytes:    size.Int64,
			UploadedAt:   updatedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
			Extra: map[string]any{
				"image":      image,
				"tag":        tag,
				"digest":     digest,
				"media_type": mediaType,
			},
		})
	}
	return out, rows.Err()
}

func (d Deps) listRawContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	query := `
		SELECT rf.path, rf.size_bytes, COALESCE(rf.mime, ''), COALESCE(rf.sha256, ''), rf.modified,
		       COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
		FROM raw_files rf` + fmtLatestScanJoin("rf.path") + `
		WHERE rf.repo_id=?
		ORDER BY rf.path ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query,
		repoID, "raw", repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var path, mime, sha string
		var scanStatus, scanSummary string
		var size int64
		var modified sql.NullTime
		if err := rows.Scan(&path, &size, &mime, &sha, &modified, &scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		uploaded := ""
		if modified.Valid {
			uploaded = modified.Time.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		out = append(out, RepoContentEntry{
			// RAW rows have no surrogate id — the path is the primary key,
			// so the UI keys its list rows off name instead.
			Name:         path,
			SizeBytes:    size,
			UploadedAt:   uploaded,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
			Extra: map[string]any{
				"mime":   mime,
				"sha256": sha,
			},
		})
	}
	return out, rows.Err()
}
