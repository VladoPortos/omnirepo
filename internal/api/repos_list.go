// Package api — repos list + sync job log endpoints (Phase 05-04, SYNC-06).
//
// GET /api/v1/projects/{name}/repos                                  — list repos
// GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs          — list sync jobs
// GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}     — single sync job
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// mountReposList installs repos list + sync job log endpoints.
func (d Deps) mountReposList(r chi.Router) {
	r.Get("/projects/{name}/repos", d.handleListRepos)
	r.Get("/projects/{name}/repos/{type}/{repo}/sync-jobs", d.handleListSyncJobs)
	r.Get("/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}", d.handleGetSyncJob)
}

// repoListItem is the JSON projection for repo listing.
//
// Phase 8 Plan 04 (MIRROR-16..21): mirror fields echoed so the UI can
// filter / badge mirror repos in list views. Same shape as repoResponse
// for the single-repo endpoint; keeping the projections aligned lets
// the UI reuse one TypeScript interface for both.
type repoListItem struct {
	ID            int64  `json:"id"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	DescriptionMD string `json:"description_md"`
	SizeBytes     int64  `json:"size_bytes"`
	// F-T15: ItemCount is the per-type artifact count (see repoItemCountExpr).
	ItemCount       int64     `json:"item_count"`
	AutoScan        bool      `json:"auto_scan"`
	PublicRead      bool      `json:"public_read"`
	BlockOnSeverity string    `json:"block_on_severity"`
	CreatedAt       time.Time `json:"created_at"`

	// Phase 8 Plan 04 (MIRROR-16..21) mirror fields.
	IsMirror          bool   `json:"is_mirror"`
	MirrorUpstreamURL string `json:"mirror_upstream_url"`
	MirrorFilterJSON  string `json:"mirror_filter_json"`
	MirrorCredID      *int64 `json:"mirror_cred_id"`
	ScanOnSync        bool   `json:"scan_on_sync"`
}

func (d Deps) handleListRepos(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	name := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}

	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, "not a project member")
		return
	}

	repos, err := d.Repos.ListByProject(r.Context(), p.ID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	pp := ParsePaginationParams(r)
	items := make([]repoListItem, 0, len(repos))
	skipping := pp.Cursor != nil
	count := 0

	ids := make([]int64, 0, len(repos))
	for _, rr := range repos {
		ids = append(ids, rr.ID)
	}
	sizes := d.liveRepoSizes(r.Context(), ids)
	itemCounts := d.liveRepoItemCounts(r.Context(), ids)
	for _, rr := range repos {
		if skipping {
			if rr.ID == pp.Cursor.ID {
				skipping = false
			}
			continue
		}
		if count >= pp.Limit {
			break
		}
		items = append(items, repoListItem{
			ID: rr.ID, Type: rr.Type, Name: rr.Name,
			DescriptionMD: rr.DescriptionMD, SizeBytes: sizes[rr.ID],
			ItemCount: itemCounts[rr.ID],
			AutoScan:  rr.AutoScan, PublicRead: rr.PublicRead,
			BlockOnSeverity: rr.BlockOnSeverity, CreatedAt: rr.CreatedAt,

			// Phase 8 Plan 04 (MIRROR-16..21) mirror fields.
			IsMirror:          rr.IsMirror,
			MirrorUpstreamURL: rr.MirrorUpstreamURL,
			MirrorFilterJSON:  rr.MirrorFilterJSON,
			MirrorCredID:      rr.MirrorCredID,
			ScanOnSync:        rr.ScanOnSync,
		})
		count++
	}

	var nextCursor string
	if len(items) >= pp.Limit && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor = EncodeCursor(Cursor{ID: last.ID, SortValue: last.Name})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
	})
}

// syncJobItem is the JSON projection for sync job listing.
//
// Phase 8 Plan 02 (M2.2) extends the projection with the byte-level
// progress triple (progress_bytes, total_bytes, current_step) populated
// by the ProgressWriter helper in internal/jobs/progress.go. The UI
// polls this endpoint every 500 ms while a Sync Now / Docker clone modal
// is open (D-10). progress_bytes and total_bytes are always emitted
// (serialized as 0 when no progress yet) so the UI renders a deterministic
// `0 / N bytes` at job start rather than an "n/a". current_step is emitted
// as "" when empty for the same reason.
type syncJobItem struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Attempts      int64  `json:"attempts"`
	LastError     string `json:"last_error,omitempty"`
	PayloadJSON   string `json:"payload_json,omitempty"`
	Log           string `json:"log,omitempty"`
	ProgressBytes int64  `json:"progress_bytes"`
	TotalBytes    int64  `json:"total_bytes"`
	CurrentStep   string `json:"current_step"`
	// Quick task 260420-d03: files newly added during sync. Written once
	// at sync completion by each protocol handler via
	// SyncJobsRepo.SetFilesSynced (NOT through the throttled progress
	// path). 0 for running jobs; the UI pill renders the "N files" piece
	// of "Sync complete · N files · X MB" when this is > 0.
	FilesSynced int64 `json:"files_synced"`
	// Summary is the raw JSON blob from sync_jobs.summary (migration 035,
	// default '{}' so the field is always emittable). Currently carries
	// `drift_purged: int` (DRIFTPURGE-03) and, when the v1.7 percent-
	// threshold guard tripped, `drift_blocked: int` (UIBACK-03). Future
	// summary writers add sibling keys via json_set so consumers can
	// parse only the keys they care about.
	Summary   string `json:"summary"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (d Deps) handleListSyncJobs(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")

	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}

	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, "not a project member")
		return
	}

	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}

	pp := ParsePaginationParams(r)
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, kind, status, attempts,
		       COALESCE(last_error, ''),
		       COALESCE(payload_json, ''),
		       COALESCE(log, ''),
		       COALESCE(progress_bytes, 0),
		       COALESCE(total_bytes, 0),
		       COALESCE(current_step, ''),
		       COALESCE(files_synced, 0),
		       COALESCE(summary, '{}'),
		       created_at, updated_at
		FROM sync_jobs
		WHERE repo_id=?
		ORDER BY id DESC
		LIMIT ?
	`, rr.ID, pp.Limit)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()

	items := make([]syncJobItem, 0)
	for rows.Next() {
		var item syncJobItem
		if err := rows.Scan(&item.ID, &item.Kind, &item.Status, &item.Attempts,
			&item.LastError, &item.PayloadJSON, &item.Log,
			&item.ProgressBytes, &item.TotalBytes, &item.CurrentStep,
			&item.FilesSynced, &item.Summary,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (d Deps) handleGetSyncJob(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	idStr := chi.URLParam(r, "id")

	jobID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || jobID <= 0 {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid job id")
		return
	}

	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}

	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, "not a project member")
		return
	}

	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}

	var item syncJobItem
	err = d.DB.Reader.QueryRowContext(r.Context(), `
		SELECT id, kind, status, attempts,
		       COALESCE(last_error, ''),
		       COALESCE(payload_json, ''),
		       COALESCE(log, ''),
		       COALESCE(progress_bytes, 0),
		       COALESCE(total_bytes, 0),
		       COALESCE(current_step, ''),
		       COALESCE(files_synced, 0),
		       COALESCE(summary, '{}'),
		       created_at, updated_at
		FROM sync_jobs
		WHERE id=? AND repo_id=?
	`, jobID, rr.ID).Scan(&item.ID, &item.Kind, &item.Status, &item.Attempts,
		&item.LastError, &item.PayloadJSON, &item.Log,
		&item.ProgressBytes, &item.TotalBytes, &item.CurrentStep,
		&item.FilesSynced, &item.Summary,
		&item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "sync job not found")
		return
	}
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	writeJSON(w, http.StatusOK, item)
}
