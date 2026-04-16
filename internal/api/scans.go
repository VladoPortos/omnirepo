// Package api — scan REST endpoints (Phase 02-09, SCAN-04 + SCAN-08).
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
// same membership check (T-02-09-05).
package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
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
type scanRowResponse struct {
	ID                  int64     `json:"id"`
	RepoID              int64     `json:"repo_id"`
	ArtifactKind        string    `json:"artifact_kind"`
	ArtifactID          string    `json:"artifact_id"`
	Status              string    `json:"status"`
	Attempts            int64     `json:"attempts"`
	LastError           string    `json:"last_error,omitempty"`
	SeveritySummaryJSON string    `json:"severity_summary_json,omitempty"`
	SBOMPath            string    `json:"sbom_path,omitempty"`
	TrivyDBVersion      string    `json:"trivy_db_version,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	StartedAt           time.Time `json:"started_at,omitempty"`
	FinishedAt          time.Time `json:"finished_at,omitempty"`
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
	// from the router so requests fell through to the SPA (WALKTHROUGH-
	// FINDINGS F-2b). Populates the "Scan Results" tab on the repo page.
	r.Get("/projects/{name}/repos/{type}/{repo}/scans", d.handleListRepoScans)
	r.Get("/scans/{id}", d.handleGetScan)
	r.Get("/scans/{id}/vulnerabilities", d.handleListScanVulns)
	r.Get("/scans/{id}/sbom", d.handleGetSBOM)
}

// handleListRepoScans returns scans rows for an entire repo, newest first.
// Filters optional `?status=` and `?limit=` (default 100, max 500).
func (d Deps) handleListRepoScans(w http.ResponseWriter, r *http.Request) {
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
	limit := int64(100)
	if q := r.URL.Query().Get("limit"); q != "" {
		if v, perr := strconv.ParseInt(q, 10, 64); perr == nil && v > 0 {
			if v > 500 {
				v = 500
			}
			limit = v
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
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.DB.Reader.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		if startedAt.Valid {
			s.StartedAt = startedAt.Time
		}
		if finishedAt.Valid {
			s.FinishedAt = finishedAt.Time
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveArtifactRepo resolves project + repo from the URL params and
// enforces project membership. Returns (project, repo, artifactID, ok).
func (d Deps) resolveArtifactRepo(w http.ResponseWriter, r *http.Request) (*metadata.Project, *metadata.Repo, string, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return nil, nil, "", false
	}
	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	artifactID := chi.URLParam(r, "id")
	if _, ok := validRepoTypes[repoType]; !ok {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
		return nil, nil, "", false
	}
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil || p == nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return nil, nil, "", false
	}
	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil || rr == nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
		return nil, nil, "", false
	}
	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "not a project member")
		return nil, nil, "", false
	}
	return p, rr, artifactID, true
}

// actorIsProjectMember verifies actor is a project member. Super-admin
// bypasses; project-scoped API keys check ProjectScope; user actors hit
// project_members.
func (d Deps) actorIsProjectMember(ctx context.Context, actor auth.Actor, projectID int64) bool {
	if actor.IsSuperAdmin {
		return true
	}
	if actor.Kind == auth.ActorKindAPIKey && actor.ProjectScope != nil {
		return *actor.ProjectScope == projectID
	}
	if actor.Kind != auth.ActorKindUser || actor.ID == 0 || d.DB == nil {
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
	_, repo, artifactID, ok := d.resolveArtifactRepo(w, r)
	if !ok {
		return
	}
	kind := artifactKindForRepoType(repo.Type)
	if kind == "" {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed,
			"rescan only supported for docker + raw artifacts in Phase 2")
		return
	}
	var sid int64
	if err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		s, err := d.ScanDeps.Scans.Enqueue(r.Context(), tx, repo.ID, kind, artifactID)
		sid = s
		return err
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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

func artifactKindForRepoType(t string) string {
	switch t {
	case "docker":
		return "docker"
	case "raw":
		return "raw"
	}
	return ""
}

// handleListArtifactScans returns scans rows for one artifact, newest first.
func (d Deps) handleListArtifactScans(w http.ResponseWriter, r *http.Request) {
	_, repo, artifactID, ok := d.resolveArtifactRepo(w, r)
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
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		if startedAt.Valid {
			s.StartedAt = startedAt.Time
		}
		if finishedAt.Valid {
			s.FinishedAt = finishedAt.Time
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid scan id")
		return nil, false
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
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
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "scan not found")
		return nil, false
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return nil, false
	}
	if startedAt.Valid {
		s.StartedAt = startedAt.Time
	}
	if finishedAt.Valid {
		s.FinishedAt = finishedAt.Time
	}
	repo, err := d.Repos.FindByID(r.Context(), s.RepoID)
	if err != nil || repo == nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "scan repo not found")
		return nil, false
	}
	if !d.actorIsProjectMember(r.Context(), actor, repo.ProjectID) {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "not a project member")
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
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()
	out := make([]vulnRowResponse, 0, 32)
	for rows.Next() {
		var v vulnRowResponse
		if err := rows.Scan(&v.ID, &v.ScanID, &v.CVEID, &v.Severity,
			&v.PackageName, &v.PackageVersion, &v.FixedVersion, &v.Title, &v.Description); err != nil {
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "sbom not generated for this scan")
		return
	}
	// Defense in depth: only serve files under the configured SBOM root.
	if d.ScanDeps != nil && d.ScanDeps.SBOMRoot != "" {
		clean, err := filepath.Abs(s.SBOMPath)
		if err != nil || !isUnder(clean, d.ScanDeps.SBOMRoot) {
			writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid sbom path")
			return
		}
	}
	f, err := os.Open(s.SBOMPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSONError(w, http.StatusNotFound, ErrNotFound, "sbom file missing on disk")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
