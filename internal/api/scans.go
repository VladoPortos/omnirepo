// Package api — scan REST endpoints.
//
// Endpoints (all under /api/v1, behind SessionOrAPIKey + project member
// auth):
//
//	POST /api/v1/projects/{name}/repos/{type}/{repo}/artifacts/{id}/rescan
//	    → enqueue a fresh scans row; returns 202 + {scan_id}
//	GET  /api/v1/projects/{name}/repos/{type}/{repo}/artifacts/{id}/scans
//	    → list scans for an artifact (newest first, no body)
//	GET  /api/v1/scans/{id}
//	    → single scan row JSON
//	GET  /api/v1/scans/{id}/vulnerabilities
//	    → vulnerabilities list (paginated)
//	GET  /api/v1/scans/{id}/sbom
//	    → streams SBOM file with Content-Type: application/json
//
// Cross-project access is denied — every artifact lookup re-resolves the
// repo via FindByTriple and verifies the actor is a member of that
// project. /api/v1/scans/{id} resolves the owning repo and applies the
// same membership check.
package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// ScansDeps carries the dependencies the scan REST endpoints need beyond
// api.Deps. Wired by app.Run via Deps.ScanDeps.
type ScansDeps struct {
	Scans    *metadata.ScansRepo
	Vulns    *metadata.VulnerabilitiesRepo
	ScanKick func() // pool kick; nil-safe
	SBOMRoot string // <DataRoot>/sboms
}

// scanRowResponse is the JSON projection of a scans row (no body of vulns).
// StartedAt and FinishedAt are pointers so json:",omitempty" actually
// omits them when the scan is still pending/running. Otherwise the Go
// zero time serializes to "0001-01-01T00:00:00Z", which the SPA then
// renders as "2026 years ago".
type scanRowResponse struct {
	ID                  int64      `json:"id"`
	RepoID              int64      `json:"repo_id"`
	ArtifactKind        string     `json:"artifact_kind"`
	ArtifactID          string     `json:"artifact_id"`
	Status              string     `json:"status"`
	Attempts            int64      `json:"attempts"`
	LastError           string     `json:"last_error,omitempty"`
	SeveritySummaryJSON string     `json:"severity_summary_json,omitempty"`
	SBOMPath            string     `json:"sbom_path,omitempty"`
	TrivyDBVersion      string     `json:"trivy_db_version,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
}

type vulnRowResponse struct {
	ID             int64  `json:"id"`
	ScanID         int64  `json:"scan_id"`
	CVEID          string `json:"cve_id"`
	Severity       string `json:"severity"`
	PackageName    string `json:"package_name"`
	PackageVersion string `json:"package_version"`
	FixedVersion   string `json:"fixed_version"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
}

// mountScans installs the scan endpoints. Called by Mount when ScanDeps
// is non-nil.
func (d Deps) mountScans(r chi.Router) {
	if d.ScanDeps == nil || d.ScanDeps.Scans == nil {
		return
	}
	r.Post("/projects/{name}/repos/{type}/{repo}/artifacts/{id}/rescan", d.handleRescan)
	r.Get("/projects/{name}/repos/{type}/{repo}/artifacts/{id}/scans", d.handleListArtifactScans)
	// Repo-level scans list — declared in openapi.yaml, previously absent
	// from the router so requests fell through to the SPA. Populates the
	// "Scan Results" tab on the repo page.
	r.Get("/projects/{name}/repos/{type}/{repo}/scans", d.handleListRepoScans)
	// Repo-level "rescan all artifacts" — the per-artifact endpoint above
	// handles retries one-by-one, but operators also want a single button
	// after a Trivy DB refresh or after past scans failed en masse (e.g.
	// DB not yet installed when uploads landed).
	r.Post("/projects/{name}/repos/{type}/{repo}/rescan", d.handleRescanRepo)
	// Prune historical scan rows, keeping only the latest per
	// (artifact_kind, artifact_id). Operators repeatedly rescanning after
	// DB updates otherwise accumulate long history in the Scan Results
	// tab; this button makes the history reflect current state.
	r.Post("/projects/{name}/repos/{type}/{repo}/scans/prune", d.handleScanPrune)
	r.Get("/scans/{id}", d.handleGetScan)
	r.Get("/scans/{id}/vulnerabilities", d.handleListScanVulns)
	r.Get("/scans/{id}/sbom", d.handleGetSBOM)
}

// handleListRepoScans returns scans rows for an entire repo, newest first.
// Filters optional `?status=` and `?limit=` (default 100, max 500).
func (d Deps) handleListRepoScans(w http.ResponseWriter, r *http.Request) {
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
	limit := int64(100)
	if q := r.URL.Query().Get("limit"); q != "" {
		if v, perr := strconv.ParseInt(q, 10, 64); perr == nil && v > 0 {
			if v > 500 {
				v = 500
			}
			limit = v
		}
	}
	offset := int64(0)
	if q := r.URL.Query().Get("offset"); q != "" {
		if v, perr := strconv.ParseInt(q, 10, 64); perr == nil && v >= 0 {
			offset = v
		}
	}
	status := r.URL.Query().Get("status")

	query := `
		SELECT id, repo_id, artifact_kind, artifact_id, status, attempts,
		       COALESCE(last_error, ''),
		       COALESCE(severity_summary_json, ''),
		       COALESCE(sbom_path, ''),
		       COALESCE(trivy_db_version, ''),
		       created_at, started_at, finished_at
		FROM scans
		WHERE repo_id=?`
	args := []any{repo.ID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := d.DB.Reader.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()
	out := make([]scanRowResponse, 0, 32)
	for rows.Next() {
		var s scanRowResponse
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&s.ID, &s.RepoID, &s.ArtifactKind, &s.ArtifactID, &s.Status,
			&s.Attempts, &s.LastError, &s.SeveritySummaryJSON, &s.SBOMPath,
			&s.TrivyDBVersion, &s.CreatedAt, &startedAt, &finishedAt); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		if startedAt.Valid {
			t := startedAt.Time
			s.StartedAt = &t
		}
		if finishedAt.Valid {
			t := finishedAt.Time
			s.FinishedAt = &t
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveArtifactRepo resolves project + repo from the URL params and
// enforces project membership. Returns (repo, artifactID, ok).
func (d Deps) resolveArtifactRepo(w http.ResponseWriter, r *http.Request) (*metadata.Repo, string, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return nil, "", false
	}
	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	artifactID := chi.URLParam(r, "id")
	// Percent-decode the {id} param so clients can safely encode reserved
	// characters. Docker rescans pass the manifest digest (sha256:<hex>);
	// encodeURIComponent() on the frontend turns the ":" into "%3A" and
	// chi leaves that as-is, so without this decode the scan looked up
	// "sha256%3A<hex>" and failed "manifest … not found in repo".
	if dec, err := url.PathUnescape(artifactID); err == nil {
		artifactID = dec
	}
	if _, ok := validRepoTypes[repoType]; !ok {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return nil, "", false
	}
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil || p == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return nil, "", false
	}
	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil || rr == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return nil, "", false
	}
	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, "not a project member")
		return nil, "", false
	}
	return rr, artifactID, true
}

// actorIsProjectMember verifies actor is a project member. Super-admin
// bypasses; project-scoped API keys check ProjectScope; user actors and
// user-owned API keys both hit project_members via Actor.ID (which is the
// owning user id for user-owned keys per the Actor.ID doc comment).
func (d Deps) actorIsProjectMember(ctx context.Context, actor auth.Actor, projectID int64) bool {
	if actor.Kind == auth.ActorKindAnonymous {
		return false
	}
	if actor.IsSuperAdmin {
		return true
	}
	if actor.Kind == auth.ActorKindAPIKey && actor.ProjectScope != nil {
		return *actor.ProjectScope == projectID
	}
	if actor.ID == 0 || d.DB == nil {
		return false
	}
	var n int
	err := d.DB.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id=? AND user_id=?`,
		projectID, actor.ID,
	).Scan(&n)
	if err != nil {
		return false
	}
	return n > 0
}

// handleRescan enqueues a fresh scan row for the artifact and kicks the pool.
func (d Deps) handleRescan(w http.ResponseWriter, r *http.Request) {
	repo, artifactID, ok := d.resolveArtifactRepo(w, r)
	if !ok {
		return
	}
	kind := artifactKindForRepoType(repo.Type)
	if kind == "" {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed,
			"rescan not supported for "+repo.Type+" repos")
		return
	}
	// Pre-flight: if the Trivy DB has never been installed, fail fast with an
	// actionable message instead of enqueueing a scan that will crash with
	// "--skip-db-update cannot be specified on the first run". This mirrors
	// the check the scan pool would hit, but surfaces it to the user *now*
	// instead of after a job failure.
	if !d.trivyDBInstalled() {
		writeJSONError(w, r, http.StatusPreconditionFailed, "trivy_db_missing",
			"Vulnerability scanning is not available: the Trivy database has not been installed. An administrator must upload a DB tarball or pull the latest DB at /admin/trivy before the first scan.")
		return
	}
	// Package-style repos (rpm/deb/pypi/helm) store scans.artifact_id as the
	// on-disk filename (auto-scan at upload time uses `res.filename`), but the
	// REST URL pins the {id} param to the DB row's PK for stable linkability.
	// Translate row-id → filename so the scan pool's materializePackage can
	// find the archive on disk. Docker / raw keep the URL param as-is (digest
	// / raw path respectively).
	switch kind {
	case "rpm", "deb", "pypi", "helm":
		fn, lookupErr := d.lookupPackageFilename(r.Context(), kind, repo.ID, artifactID)
		if lookupErr != nil {
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "artifact not found")
			return
		}
		artifactID = fn
	}
	var sid int64
	if err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		s, err := d.ScanDeps.Scans.Enqueue(r.Context(), tx, repo.ID, kind, artifactID)
		sid = s
		return err
	}); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if d.ScanDeps.ScanKick != nil {
		d.ScanDeps.ScanKick()
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtScanStarted,
			ActorUserID: &uid,
			TargetKind:  "scan",
			TargetID:    strconv.FormatInt(sid, 10),
			Outcome:     "enqueued",
			Details: map[string]any{
				"repo_id":       repo.ID,
				"artifact_kind": kind,
				"artifact_id":   artifactID,
				"reason":        "manual_rescan",
			},
		})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"scan_id": sid})
}

// trivyDBInstalled returns true when the Trivy DB directory contains the
// core `trivy.db` file. Used by handleRescan to fail fast with a friendly
// 412 instead of enqueueing a job that will crash in the scan pool.
func (d Deps) trivyDBInstalled() bool {
	dir := d.trivyDBDir()
	if _, err := os.Stat(filepath.Join(dir, "trivy.db")); err == nil {
		return true
	}
	return false
}

// lookupPackageFilename resolves the on-disk filename for a package-style
// artifact given (kind, repo_id, row-id). Returns the filename the scan
// pipeline expects as its artifact_id token. The mapping mirrors the
// auto-scan enqueue sites (e.g. rpm/put.go passes res.filename).
func (d Deps) lookupPackageFilename(ctx context.Context, kind string, repoID int64, rowID string) (string, error) {
	id, perr := strconv.ParseInt(rowID, 10, 64)
	if perr != nil || id <= 0 {
		return "", fmt.Errorf("invalid artifact id %q", rowID)
	}
	var table string
	switch kind {
	case "rpm":
		table = "rpm_packages"
	case "deb":
		table = "deb_packages"
	case "pypi":
		table = "pypi_files"
	case "helm":
		table = "helm_charts"
	default:
		return "", fmt.Errorf("no package table for kind %q", kind)
	}
	var filename string
	//nolint:gosec // table is whitelisted by the switch above; rowID + repoID are parameterized.
	err := d.DB.Reader.QueryRowContext(ctx,
		"SELECT filename FROM "+table+" WHERE id=? AND repo_id=?",
		id, repoID,
	).Scan(&filename)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("artifact %d not found in repo %d", id, repoID)
	}
	if err != nil {
		return "", fmt.Errorf("%s lookup: %w", table, err)
	}
	return filename, nil
}

// handleRescanRepo enqueues a fresh scan for every artifact currently in
// the repo and kicks the pool. Idempotent in the sense that each call
// adds a new scans row per artifact — the pool dedupes via its leasing
// model, so stacking "Rescan all" presses is cheap, not a landslide.
//
// POST /projects/{name}/repos/{type}/{repo}/rescan → 202 {enqueued,
// repo_type, pool_kicked}. Git repos return 400 (nothing to scan).
func (d Deps) handleRescanRepo(w http.ResponseWriter, r *http.Request) {
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
	kind := artifactKindForRepoType(repo.Type)
	if kind == "" {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed,
			"rescan not supported for "+repo.Type+" repos")
		return
	}
	if !d.trivyDBInstalled() {
		writeJSONError(w, r, http.StatusPreconditionFailed, "trivy_db_missing",
			"Vulnerability scanning is not available: the Trivy database has not been installed. An administrator must upload a DB tarball or pull the latest DB at /admin/trivy before the first scan.")
		return
	}

	ids, err := d.listRepoArtifactIDs(r.Context(), kind, repo.ID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"enqueued":    0,
			"repo_type":   repo.Type,
			"pool_kicked": false,
		})
		return
	}

	// Batch the inserts in one writer tx so SQLite takes one lock cycle,
	// not N. On error we roll back the whole batch — partial rescans would
	// surprise operators more than a clear failure.
	enqueued := 0
	if err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := d.ScanDeps.Scans.Enqueue(r.Context(), tx, repo.ID, kind, id); err != nil {
				return err
			}
			enqueued++
		}
		return nil
	}); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if d.ScanDeps.ScanKick != nil {
		d.ScanDeps.ScanKick()
	}

	if userID := actor.ID; userID != 0 {
		uid := userID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtScanStarted,
			ActorUserID: &uid,
			TargetKind:  "repo",
			TargetID:    strconv.FormatInt(repo.ID, 10),
			Outcome:     "rescan_all_enqueued",
			Details: map[string]any{
				"repo_id":       repo.ID,
				"artifact_kind": kind,
				"enqueued":      enqueued,
				"reason":        "manual_rescan_all",
			},
		})
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"enqueued":    enqueued,
		"repo_type":   repo.Type,
		"pool_kicked": d.ScanDeps.ScanKick != nil,
	})
}

// handleScanPrune deletes every scan row for the repo except the newest
// per (artifact_kind, artifact_id). Never touches rows in pending/running
// state — deleting those would orphan in-flight jobs. Returns the number
// of rows deleted so the UI can confirm the result.
//
// POST /projects/{name}/repos/{type}/{repo}/scans/prune → 200 {deleted, kept}
func (d Deps) handleScanPrune(w http.ResponseWriter, r *http.Request) {
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

	// Prune every finished (done/failed) scan except the newest per
	// artifact; pending/running rows are explicitly preserved. The prune
	// set is collected first so each scan's vulnerabilities rows AND the
	// cves_fts rows they orphan can be removed via
	// metadata.DeleteVulnerabilitiesByScan — the scans→vulnerabilities FK
	// cascade would drop the rows, but cves_fts is an FTS5 table with no
	// FK, so without the explicit sweep its rows leak on every prune.
	var deleted int64
	if err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(r.Context(), `
			SELECT id FROM scans
			WHERE repo_id = ?
			  AND status IN ('done','failed')
			  AND id NOT IN (
				SELECT MAX(id) FROM scans
				WHERE repo_id = ? AND status IN ('done','failed')
				GROUP BY artifact_kind, artifact_id
			  )
		`, repo.ID, repo.ID)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()

		for _, id := range ids {
			if err := metadata.DeleteVulnerabilitiesByScan(r.Context(), tx, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(),
				`DELETE FROM scans WHERE id = ?`, id); err != nil {
				return err
			}
		}
		deleted = int64(len(ids))
		return nil
	}); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Count what's left so the UI can display "Kept N / deleted M".
	var kept int64
	_ = d.DB.Reader.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM scans WHERE repo_id = ?`, repo.ID,
	).Scan(&kept)

	if actor.ID != 0 {
		uid := actor.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtScanPrune,
			ActorUserID: &uid,
			TargetKind:  "repo",
			TargetID:    strconv.FormatInt(repo.ID, 10),
			Outcome:     "scans_pruned",
			Details: map[string]any{
				"repo_id": repo.ID,
				"deleted": deleted,
				"kept":    kept,
				"reason":  "manual_prune",
			},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted,
		"kept":    kept,
	})
}

// listRepoArtifactIDs returns the artifact-ids the scan pipeline expects
// for a given repo — the same string each upload path passes to
// ScansRepo.Enqueue. Docker uses digests; raw uses paths; package repos
// use filenames.
func (d Deps) listRepoArtifactIDs(ctx context.Context, kind string, repoID int64) ([]string, error) {
	var query string
	switch kind {
	case "docker":
		// Dedup by digest — a single manifest can be tagged many times,
		// and we only need to scan each content-addressed manifest once.
		query = `SELECT DISTINCT digest FROM docker_manifests WHERE repo_id=?`
	case "raw":
		query = `SELECT path FROM raw_files WHERE repo_id=?`
	case "rpm":
		query = `SELECT filename FROM rpm_packages WHERE repo_id=?`
	case "deb":
		query = `SELECT filename FROM deb_packages WHERE repo_id=?`
	case "pypi":
		query = `SELECT filename FROM pypi_files WHERE repo_id=?`
	case "helm":
		query = `SELECT filename FROM helm_charts WHERE repo_id=?`
	default:
		return nil, fmt.Errorf("unsupported kind %q", kind)
	}
	rows, err := d.DB.Reader.QueryContext(ctx, query, repoID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0, 32)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// artifactKindForRepoType maps a repo.type onto the scans.artifact_kind
// string used by the Trivy runner. For docker/raw the mapping is
// canonical. rpm/deb/pypi/helm repos materialize the single uploaded
// archive and run Trivy filesystem-mode over it (see
// internal/scan/handler.go:168). Any new repo type that wants to be
// rescannable must be mirrored here AND in the scan handler's switch.
func artifactKindForRepoType(t string) string {
	switch t {
	case "docker":
		return "docker"
	case "raw":
		return "raw"
	case "rpm":
		return "rpm"
	case "deb":
		return "deb"
	case "pypi":
		return "pypi"
	case "helm":
		return "helm"
	}
	return ""
}

// handleListArtifactScans returns scans rows for one artifact, newest first.
func (d Deps) handleListArtifactScans(w http.ResponseWriter, r *http.Request) {
	repo, artifactID, ok := d.resolveArtifactRepo(w, r)
	if !ok {
		return
	}
	kind := artifactKindForRepoType(repo.Type)
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, repo_id, artifact_kind, artifact_id, status, attempts,
		       COALESCE(last_error, ''),
		       COALESCE(severity_summary_json, ''),
		       COALESCE(sbom_path, ''),
		       COALESCE(trivy_db_version, ''),
		       created_at, started_at, finished_at
		FROM scans
		WHERE repo_id=? AND artifact_kind=? AND artifact_id=?
		ORDER BY id DESC
		LIMIT 100
	`, repo.ID, kind, artifactID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()
	out := make([]scanRowResponse, 0, 16)
	for rows.Next() {
		var s scanRowResponse
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&s.ID, &s.RepoID, &s.ArtifactKind, &s.ArtifactID, &s.Status,
			&s.Attempts, &s.LastError, &s.SeveritySummaryJSON, &s.SBOMPath,
			&s.TrivyDBVersion, &s.CreatedAt, &startedAt, &finishedAt); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		if startedAt.Valid {
			t := startedAt.Time
			s.StartedAt = &t
		}
		if finishedAt.Valid {
			t := finishedAt.Time
			s.FinishedAt = &t
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// loadScanRowAndAuth loads a scan row by id and verifies the requesting
// actor is a member of the owning repo's project.
func (d Deps) loadScanRowAndAuth(w http.ResponseWriter, r *http.Request) (*scanRowResponse, bool) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid scan id")
		return nil, false
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return nil, false
	}
	var s scanRowResponse
	var startedAt, finishedAt sql.NullTime
	err = d.DB.Reader.QueryRowContext(r.Context(), `
		SELECT id, repo_id, artifact_kind, artifact_id, status, attempts,
		       COALESCE(last_error, ''),
		       COALESCE(severity_summary_json, ''),
		       COALESCE(sbom_path, ''),
		       COALESCE(trivy_db_version, ''),
		       created_at, started_at, finished_at
		FROM scans WHERE id=?`, id,
	).Scan(&s.ID, &s.RepoID, &s.ArtifactKind, &s.ArtifactID, &s.Status, &s.Attempts,
		&s.LastError, &s.SeveritySummaryJSON, &s.SBOMPath, &s.TrivyDBVersion,
		&s.CreatedAt, &startedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "scan not found")
		return nil, false
	}
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return nil, false
	}
	if startedAt.Valid {
		t := startedAt.Time
		s.StartedAt = &t
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		s.FinishedAt = &t
	}
	repo, err := d.Repos.FindByID(r.Context(), s.RepoID)
	if err != nil || repo == nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "scan repo not found")
		return nil, false
	}
	if !d.actorIsProjectMember(r.Context(), actor, repo.ProjectID) {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, "not a project member")
		return nil, false
	}
	return &s, true
}

func (d Deps) handleGetScan(w http.ResponseWriter, r *http.Request) {
	s, ok := d.loadScanRowAndAuth(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (d Deps) handleListScanVulns(w http.ResponseWriter, r *http.Request) {
	s, ok := d.loadScanRowAndAuth(w, r)
	if !ok {
		return
	}
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, scan_id, cve_id, severity, package_name, package_version,
		       fixed_version, title, COALESCE(description, '')
		FROM vulnerabilities WHERE scan_id=?
		ORDER BY id ASC
		LIMIT 1000
	`, s.ID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()
	out := make([]vulnRowResponse, 0, 32)
	for rows.Next() {
		var v vulnRowResponse
		if err := rows.Scan(&v.ID, &v.ScanID, &v.CVEID, &v.Severity,
			&v.PackageName, &v.PackageVersion, &v.FixedVersion, &v.Title, &v.Description); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetSBOM streams the SBOM file referenced by the scan row.
func (d Deps) handleGetSBOM(w http.ResponseWriter, r *http.Request) {
	s, ok := d.loadScanRowAndAuth(w, r)
	if !ok {
		return
	}
	if s.SBOMPath == "" {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "sbom not generated for this scan")
		return
	}
	// Defense in depth: only serve files under the configured SBOM root.
	if d.ScanDeps != nil && d.ScanDeps.SBOMRoot != "" {
		clean, err := filepath.Abs(s.SBOMPath)
		if err != nil || !isUnder(clean, d.ScanDeps.SBOMRoot) {
			writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid sbom path")
			return
		}
	}
	f, err := os.Open(s.SBOMPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "sbom file missing on disk")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.Header().Set("Content-Disposition",
		"attachment; filename=\"sbom-"+strconv.FormatInt(s.ID, 10)+".json\"")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// isUnder returns true when child resolves to a path inside parent.
func isUnder(child, parent string) bool {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(parentAbs, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if len(rel) >= 2 && rel[:2] == ".." {
		return false
	}
	return true
}
