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
type repoListItem struct {
	ID              int64     `json:"id"`
	Type            string    `json:"type"`
	Name            string    `json:"name"`
	DescriptionMD   string    `json:"description_md"`
	SizeBytes       int64     `json:"size_bytes"`
	AutoScan        bool      `json:"auto_scan"`
	PublicRead      bool      `json:"public_read"`
	BlockOnSeverity string    `json:"block_on_severity"`
	CreatedAt       time.Time `json:"created_at"`
}

func (d Deps) handleListRepos(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	name := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}

	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "not a project member")
		return
	}

	repos, err := d.Repos.ListByProject(r.Context(), p.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
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
			AutoScan: rr.AutoScan, PublicRead: rr.PublicRead,
			BlockOnSeverity: rr.BlockOnSeverity, CreatedAt: rr.CreatedAt,
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
type syncJobItem struct {
	ID          int64   `json:"id"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	Attempts    int64   `json:"attempts"`
	LastError   string  `json:"last_error,omitempty"`
	PayloadJSON string  `json:"payload_json,omitempty"`
	Log         string  `json:"log,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func (d Deps) handleListSyncJobs(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")

	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}

	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "not a project member")
		return
	}

	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}

	pp := ParsePaginationParams(r)
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, kind, status, attempts,
		       COALESCE(last_error, ''),
		       COALESCE(payload_json, ''),
		       COALESCE(log, ''),
		       created_at, updated_at
		FROM sync_jobs
		WHERE repo_id=?
		ORDER BY id DESC
		LIMIT ?
	`, rr.ID, pp.Limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()

	items := make([]syncJobItem, 0)
	for rows.Next() {
		var item syncJobItem
		if err := rows.Scan(&item.ID, &item.Kind, &item.Status, &item.Attempts,
			&item.LastError, &item.PayloadJSON, &item.Log,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (d Deps) handleGetSyncJob(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	idStr := chi.URLParam(r, "id")

	jobID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || jobID <= 0 {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid job id")
		return
	}

	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}

	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "not a project member")
		return
	}

	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}

	var item syncJobItem
	err = d.DB.Reader.QueryRowContext(r.Context(), `
		SELECT id, kind, status, attempts,
		       COALESCE(last_error, ''),
		       COALESCE(payload_json, ''),
		       COALESCE(log, ''),
		       created_at, updated_at
		FROM sync_jobs
		WHERE id=? AND repo_id=?
	`, jobID, rr.ID).Scan(&item.ID, &item.Kind, &item.Status, &item.Attempts,
		&item.LastError, &item.PayloadJSON, &item.Log,
		&item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "sync job not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	writeJSON(w, http.StatusOK, item)
}
