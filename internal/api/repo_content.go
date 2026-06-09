package api

// Repo content listing.
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

	"github.com/vladoportos/omnirepo/internal/auth"
)

// severityCounts parses the Trivy summary JSON into an explicit
// {critical,high,medium,low} map. Returned as map[string]int64 so the JSON
// serialisation the frontend consumes is predictable regardless of JSON
// key ordering or missing fields.
func severityCounts(summaryJSON string) map[string]int64 {
	var s struct {
		Critical int64 `json:"critical"`
		High     int64 `json:"high"`
		Medium   int64 `json:"medium"`
		Low      int64 `json:"low"`
	}
	if summaryJSON != "" {
		_ = json.Unmarshal([]byte(summaryJSON), &s)
	}
	return map[string]int64{
		"critical": s.Critical,
		"high":     s.High,
		"medium":   s.Medium,
		"low":      s.Low,
	}
}

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
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	SizeBytes    int64  `json:"size_bytes"`
	UploadedAt   string `json:"uploaded_at"`
	ScanSeverity string `json:"scan_severity,omitempty"`
	// LatestScanID is the id of the newest scan row for this artifact,
	// omitted when the artifact has never been scanned. The UI uses it
	// to deep-link to the standalone scan report page.
	LatestScanID *int64         `json:"latest_scan_id,omitempty"`
	Extra        map[string]any `json:"extra,omitempty"`
}

// RepoContentPage wraps a paginated slice of RepoContentEntry.
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
	case "go":
		entries, qerr = d.listGoContent(r, repo.ID, limit, offset)
	case "npm":
		entries, qerr = d.listNpmContent(r, repo.ID, limit, offset)
	case "maven":
		entries, qerr = d.listMavenContent(r, repo.ID, limit, offset)
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

	// Total row-count so the UI can render "Showing N of M" and know when
	// load-more has reached the end. Best-effort — a failing count query
	// falls back to len(entries) + offset so the response still serialises.
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
// header count and the content-tab total stay consistent.
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
	case "go":
		query = `SELECT COUNT(*) FROM go_modules WHERE repo_id=?`
	case "npm":
		query = `SELECT COUNT(*) FROM npm_packages WHERE repo_id=?`
	case "maven":
		query = `SELECT COUNT(*) FROM maven_artifacts WHERE repo_id=?`
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
		       rp.filename, rp.digest,
		       COALESCE(rp.summary, ''), COALESCE(rp.license, ''),
		       rp.uploaded_at,
		       ls.id, COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
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
		var name, version, release, arch, filename, digest, summary, license, uploadedAt string
		var scanStatus, scanSummary string
		var scanID sql.NullInt64
		var size int64
		if err := rows.Scan(&id, &name, &version, &release, &arch, &size,
			&filename, &digest, &summary, &license, &uploadedAt,
			&scanID, &scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:           id,
			Name:         name,
			Version:      version,
			SizeBytes:    size,
			UploadedAt:   uploadedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
			LatestScanID: scanIDPtr(scanID),
			Extra: map[string]any{
				"release":         release,
				"arch":            arch,
				"filename":        filename,
				"digest":          digest,
				"summary":         summary,
				"license":         license,
				"scan_status":     scanStatus,
				"severity_counts": severityCounts(scanSummary),
			},
		})
	}
	return out, rows.Err()
}

// scanIDPtr converts a LEFT-JOINed NullInt64 latest-scan id into the
// optional *int64 the API exposes. Returns nil when there is no
// matching scan row.
func scanIDPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
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
		       d.size_bytes, d.filename, d.digest,
		       COALESCE(d.storage_pool_path, ''),
		       COALESCE(d.maintainer, ''), COALESCE(d.depends, ''),
		       d.uploaded_at,
		       ls.id, COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
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
		var pkg, version, arch, suite, component, section, filename, digest, poolPath, maintainer, depends, uploadedAt string
		var scanStatus, scanSummary string
		var scanID sql.NullInt64
		var size int64
		if err := rows.Scan(&id, &pkg, &version, &arch, &suite, &component, &section,
			&size, &filename, &digest, &poolPath, &maintainer, &depends, &uploadedAt,
			&scanID, &scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:           id,
			Name:         pkg,
			Version:      version,
			SizeBytes:    size,
			UploadedAt:   uploadedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
			LatestScanID: scanIDPtr(scanID),
			Extra: map[string]any{
				"architecture":      arch,
				"suite":             suite,
				"component":         component,
				"section":           section,
				"filename":          filename,
				"digest":            digest,
				"storage_pool_path": poolPath,
				"maintainer":        maintainer,
				"depends":           depends,
				"scan_status":       scanStatus,
				"severity_counts":   severityCounts(scanSummary),
			},
		})
	}
	return out, rows.Err()
}

func (d Deps) listPypiContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	query := `
		SELECT pf.id, pf.project_normalized, pf.version, pf.filename, pf.kind,
		       COALESCE(pf.requires_python, ''), pf.size_bytes, pf.uploaded_at,
		       ls.id, COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
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
		var scanID sql.NullInt64
		var size int64
		if err := rows.Scan(&id, &project, &version, &filename, &kind, &reqPy, &size, &uploadedAt,
			&scanID, &scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:           id,
			Name:         project,
			Version:      version,
			SizeBytes:    size,
			UploadedAt:   uploadedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
			LatestScanID: scanIDPtr(scanID),
			Extra: map[string]any{
				"filename":        filename,
				"kind":            kind,
				"requires_python": reqPy,
				"scan_status":     scanStatus,
				"severity_counts": severityCounts(scanSummary),
			},
		})
	}
	return out, rows.Err()
}

func (d Deps) listHelmContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	query := `
		SELECT hc.id, hc.name, hc.version, hc.app_version, hc.description,
		       hc.size_bytes, hc.filename, hc.uploaded_at,
		       ls.id, COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
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
		var scanID sql.NullInt64
		var size int64
		if err := rows.Scan(&id, &name, &version, &appVersion, &description, &size, &filename, &uploadedAt,
			&scanID, &scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:           id,
			Name:         name,
			Version:      version,
			SizeBytes:    size,
			UploadedAt:   uploadedAt,
			ScanSeverity: deriveScanSeverity(scanStatus, scanSummary),
			LatestScanID: scanIDPtr(scanID),
			Extra: map[string]any{
				"app_version":     appVersion,
				"description":     description,
				"filename":        filename,
				"scan_status":     scanStatus,
				"severity_counts": severityCounts(scanSummary),
			},
		})
	}
	return out, rows.Err()
}

// listDockerContent produces one row per (image, tag) pair in a docker repo.
// Previously returned [] because the listing was delegated to /v2, but the
// generic Content tab had no link to that view so repos looked empty even
// when populated with 16+ manifests.
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
	// dockerLayerCountExpr returns the number of layers referenced by a
	// manifest body. Returns 0 when the body is missing or has no layers
	// array (e.g., an empty image or a malformed manifest).
	const dockerLayerCountExpr = `(
		SELECT COUNT(*) FROM json_each(dm.body, '$.layers')
	)`
	query := `
		SELECT dt.image, dt.tag, dt.digest, dm.media_type, dm.body,
		       ` + dockerImageSizeExpr + ` AS total_size,
		       COALESCE(` + dockerLayerCountExpr + `, 0) AS layer_count,
		       dt.updated_at,
		       ls.id, COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
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
		var body []byte
		var size sql.NullInt64
		var layerCount sql.NullInt64
		var scanStatus, scanSummary string
		var scanID sql.NullInt64
		if err := rows.Scan(&image, &tag, &digest, &mediaType, &body, &size, &layerCount, &updatedAt,
			&scanID, &scanStatus, &scanSummary); err != nil {
			return nil, err
		}
		// Multi-arch scan aggregation. OCI image indexes / Docker
		// manifest lists are explicitly skipped for scanning (see
		// scan/manifest_classify.go:52) because they're metadata, not
		// rootfs — their child manifests are scanned instead. Before this
		// aggregation the tag row would show "Not scanned" forever
		// because the scan-status LEFT JOIN looked up by dt.digest (== the
		// index digest) and never found a row. Here, when the tag points
		// at an index and no direct scan exists, we roll up the latest
		// scans of the referenced child manifests:
		//   status   = worst transitive (scanning > failed > done > "")
		//   counts   = sum per severity across the latest-per-child scans
		// The resulting JSON feeds deriveScanSeverity + severityCounts
		// exactly as if it were a direct scan row.
		if scanStatus == "" && isIndexMediaType(mediaType) && len(body) > 0 {
			if aggStatus, aggSummary, ok := d.aggregateIndexScan(r, repoID, body); ok {
				scanStatus = aggStatus
				scanSummary = aggSummary
			}
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
			LatestScanID: scanIDPtr(scanID),
			Extra: map[string]any{
				"image":           image,
				"tag":             tag,
				"digest":          digest,
				"media_type":      mediaType,
				"layer_count":     layerCount.Int64,
				"scan_status":     scanStatus,
				"severity_counts": severityCounts(scanSummary),
			},
		})
	}
	return out, rows.Err()
}

// isIndexMediaType matches the two OCI/Docker media types that identify
// an image index (aka manifest list). Kept inline here so the Docker
// listing doesn't reach into the oci protocol package for a one-string
// check — changing the set in both places is obvious since the
// classifier in scan/manifest_classify.go has the canonical list.
func isIndexMediaType(mt string) bool {
	switch mt {
	case "application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json":
		return true
	}
	return false
}

// aggregateIndexScan walks the child manifests referenced by an image
// index's body, pulls the latest scan row for each, and returns a synthetic
// status + severity-summary JSON that mirrors the shape deriveScanSeverity
// expects on a native scan row. Returns ok=false when the body can't be
// parsed or no children have been scanned yet — callers then fall back to
// the empty-status path (which renders "Not scanned").
//
// Aggregation rules:
//
//   - Counts are summed per severity bucket across every child's latest
//     scan (one row per child).
//   - Status precedence: "scanning" > "failed" > "done" > "". A tag is
//     "scanning" if ANY child is still pending/running; "failed" if no
//     children are in-flight but at least one failed; "done" only when
//     every scanned child is done. Children with no scan row are ignored
//     — a multi-arch push usually races N+1 scan enqueues, and we want
//     the UI to unstick from "Not scanned" as soon as the first arch
//     completes.
func (d Deps) aggregateIndexScan(r *http.Request, repoID int64, body []byte) (status, summary string, ok bool) {
	var idx struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return "", "", false
	}
	if len(idx.Manifests) == 0 {
		return "", "", false
	}
	digests := make([]string, 0, len(idx.Manifests))
	for _, m := range idx.Manifests {
		if m.Digest != "" {
			digests = append(digests, m.Digest)
		}
	}
	if len(digests) == 0 {
		return "", "", false
	}

	// Build: SELECT artifact_id, status, severity_summary_json FROM scans
	// WHERE id IN (latest-per-child). Stable ordering via the grouped MAX(id)
	// subquery so duplicate scans on the same child collapse to one row.
	placeholders := make([]byte, 0, 2*len(digests))
	args := make([]any, 0, 2+len(digests)*2)
	args = append(args, repoID, "docker")
	for i, dg := range digests {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, dg)
	}
	q := `
		SELECT s.artifact_id, s.status, COALESCE(s.severity_summary_json, '')
		FROM scans s
		INNER JOIN (
			SELECT artifact_id, MAX(id) AS max_id
			FROM scans
			WHERE repo_id = ? AND artifact_kind = ? AND artifact_id IN (` + string(placeholders) + `)
			GROUP BY artifact_id
		) latest ON s.id = latest.max_id`
	rows, err := d.DB.Reader.QueryContext(r.Context(), q, args...)
	if err != nil {
		return "", "", false
	}
	defer func() { _ = rows.Close() }()

	var crit, high, med, low int64
	anyScanning, anyFailed, anyDone := false, false, false
	for rows.Next() {
		var aid, st, sum string
		if err := rows.Scan(&aid, &st, &sum); err != nil {
			return "", "", false
		}
		switch st {
		case "pending", "running":
			anyScanning = true
		case "failed":
			anyFailed = true
		case "done":
			anyDone = true
			var s struct {
				Critical int64 `json:"critical"`
				High     int64 `json:"high"`
				Medium   int64 `json:"medium"`
				Low      int64 `json:"low"`
			}
			if sum != "" {
				_ = json.Unmarshal([]byte(sum), &s)
			}
			crit += s.Critical
			high += s.High
			med += s.Medium
			low += s.Low
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", false
	}

	switch {
	case anyScanning:
		status = "pending"
	case anyFailed && !anyDone:
		status = "failed"
	case anyDone:
		status = "done"
	default:
		// No scanned children → nothing to report.
		return "", "", false
	}
	summaryJSON, _ := json.Marshal(map[string]int64{
		"critical": crit,
		"high":     high,
		"medium":   med,
		"low":      low,
	})
	return status, string(summaryJSON), true
}

// listGoContent produces one row per hosted Go module version. Go module
// zips are not scanned (no scan materialization for the type yet), so the
// scan columns are left at their zero values.
func (d Deps) listGoContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	query := `
		SELECT gm.id, gm.module_path, gm.version, gm.size_bytes, gm.digest, gm.uploaded_at
		FROM go_modules gm
		WHERE gm.repo_id=?
		ORDER BY gm.module_path ASC, gm.version ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query, repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id, size int64
		var modulePath, version, digest, uploadedAt string
		if err := rows.Scan(&id, &modulePath, &version, &size, &digest, &uploadedAt); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:         id,
			Name:       modulePath,
			Version:    version,
			SizeBytes:  size,
			UploadedAt: uploadedAt,
			Extra: map[string]any{
				"digest": digest,
			},
		})
	}
	return out, rows.Err()
}

// listNpmContent produces one row per published npm package version.
// npm packages are not scanned, so scan columns stay at zero values.
func (d Deps) listNpmContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	query := `
		SELECT np.id, np.name, np.version, COALESCE(np.description, ''),
		       np.tarball, np.size_bytes, np.shasum, np.uploaded_at
		FROM npm_packages np
		WHERE np.repo_id=?
		ORDER BY np.name ASC, np.version ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query, repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id, size int64
		var name, version, description, tarball, shasum, uploadedAt string
		if err := rows.Scan(&id, &name, &version, &description, &tarball, &size, &shasum, &uploadedAt); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:         id,
			Name:       name,
			Version:    version,
			SizeBytes:  size,
			UploadedAt: uploadedAt,
			Extra: map[string]any{
				"description": description,
				"tarball":     tarball,
				"shasum":      shasum,
			},
		})
	}
	return out, rows.Err()
}

// listMavenContent produces one row per deployed primary Maven artifact.
// Maven artifacts are not scanned, so scan columns stay at zero values.
func (d Deps) listMavenContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	query := `
		SELECT ma.id, ma.group_id, ma.artifact_id, ma.version,
		       COALESCE(ma.classifier, ''), ma.extension, ma.filename,
		       ma.path, ma.size_bytes, ma.sha256, ma.uploaded_at
		FROM maven_artifacts ma
		WHERE ma.repo_id=?
		ORDER BY ma.group_id ASC, ma.artifact_id ASC, ma.version ASC, ma.filename ASC
		LIMIT ? OFFSET ?`
	rows, err := d.DB.Reader.QueryContext(r.Context(), query, repoID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RepoContentEntry
	for rows.Next() {
		var id, size int64
		var groupID, artifactID, version, classifier, extension, filename, pth, sha, uploadedAt string
		if err := rows.Scan(&id, &groupID, &artifactID, &version, &classifier, &extension,
			&filename, &pth, &size, &sha, &uploadedAt); err != nil {
			return nil, err
		}
		out = append(out, RepoContentEntry{
			ID:         id,
			Name:       groupID + ":" + artifactID,
			Version:    version,
			SizeBytes:  size,
			UploadedAt: uploadedAt,
			Extra: map[string]any{
				"group_id":    groupID,
				"artifact_id": artifactID,
				"classifier":  classifier,
				"extension":   extension,
				"filename":    filename,
				"path":        pth,
				"sha256":      sha,
			},
		})
	}
	return out, rows.Err()
}

func (d Deps) listRawContent(r *http.Request, repoID, limit, offset int64) ([]RepoContentEntry, error) {
	query := `
		SELECT rf.path, rf.size_bytes, COALESCE(rf.mime, ''), COALESCE(rf.sha256, ''), rf.modified,
		       ls.id, COALESCE(ls.status, ''), COALESCE(ls.severity_summary_json, '')
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
		var scanID sql.NullInt64
		var size int64
		var modified sql.NullTime
		if err := rows.Scan(&path, &size, &mime, &sha, &modified, &scanID, &scanStatus, &scanSummary); err != nil {
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
			LatestScanID: scanIDPtr(scanID),
			Extra: map[string]any{
				"mime":            mime,
				"sha256":          sha,
				"scan_status":     scanStatus,
				"severity_counts": severityCounts(scanSummary),
			},
		})
	}
	return out, rows.Err()
}
