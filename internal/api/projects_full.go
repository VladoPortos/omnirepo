// Package api — full projects list + detail + activity (Phase 05-04).
//
// GET  /api/v1/projects            — paginated list with member_count, repo_count
// GET  /api/v1/projects/{name}     — full detail with members + repos
// GET  /api/v1/projects/{name}/activity (OPS-04) — recent audit events
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// liveRepoSizes returns a map repoID → summed bytes across every artifact
// table (F-5). `repos.size_bytes` is never written, so the raw column reads
// 0. This helper gives callers (projects list / detail, repo list, repo
// detail) the real stored size.
//
// Returns an empty map for an empty input slice, never nil.
func (d Deps) liveRepoSizes(ctx context.Context, ids []int64) map[int64]int64 {
	out := make(map[int64]int64, len(ids))
	if len(ids) == 0 {
		return out
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	query := `SELECT r.id, ` + repoSizeExpr + ` FROM repos r WHERE r.id IN (` + strings.Join(ph, ",") + `)`
	rows, err := d.DB.Reader.QueryContext(ctx, query, args...)
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, sz int64
		if err := rows.Scan(&id, &sz); err == nil {
			out[id] = sz
		}
	}
	return out
}

// mountProjectsFull installs the full projects endpoints.
func (d Deps) mountProjectsFull(r chi.Router) {
	r.Get("/projects", d.handleListProjects)
	r.Get("/projects/{name}", d.handleGetProject)
	r.Get("/projects/{name}/activity", d.handleProjectActivity)
}

// projectListItem is the JSON projection for project listing.
type projectListItem struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	DescriptionMD string   `json:"description_md"`
	MemberCount   int       `json:"member_count"`
	RepoCount     int       `json:"repo_count"`
	SizeBytes     int64     `json:"size_bytes"`
	CreatedAt     time.Time `json:"created_at"`
}

// projectDetailResponse is the full project detail.
type projectDetailResponse struct {
	ID            int64            `json:"id"`
	Name          string           `json:"name"`
	DescriptionMD string           `json:"description_md"`
	CreatedAt     time.Time        `json:"created_at"`
	Members       []projectMember  `json:"members"`
	Repos         []projectRepo    `json:"repos"`
	// Buckets is the S3 bucket list for this project, with live
	// size_bytes / object_count (F-S3-B, walkthrough 2026-04-17). Absent
	// when the S3 backend is not wired into Deps.
	Buckets       []projectBucket  `json:"buckets"`
}

type projectMember struct {
	UserID int64  `json:"user_id"`
	Login  string `json:"login"`
	Email  string `json:"email"`
}

type projectRepo struct {
	ID            int64     `json:"id"`
	Type          string    `json:"type"`
	Name          string    `json:"name"`
	DescriptionMD string   `json:"description_md"`
	SizeBytes     int64     `json:"size_bytes"`
	AutoScan      bool      `json:"auto_scan"`
	PublicRead    bool      `json:"public_read"`
	CreatedAt     time.Time `json:"created_at"`
}

type projectBucket struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	SizeBytes   int64     `json:"size_bytes"`
	ObjectCount int64     `json:"object_count"`
	CreatedAt   time.Time `json:"created_at"`
}

func (d Deps) handleListProjects(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	allProjects, err := d.Projects.ListAll(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Filter to actor's projects unless super-admin.
	var memberSet map[int64]struct{}
	if !actor.IsSuperAdmin {
		ids, _ := d.Members.ListProjectIDsForUser(r.Context(), actor.ID)
		memberSet = make(map[int64]struct{}, len(ids))
		for _, pid := range ids {
			memberSet[pid] = struct{}{}
		}
	}

	pp := ParsePaginationParams(r)
	items := make([]projectListItem, 0)
	count := 0
	skipping := pp.Cursor != nil

	for _, p := range allProjects {
		if memberSet != nil {
			if _, ok := memberSet[p.ID]; !ok {
				continue
			}
		}

		if skipping {
			if p.ID == pp.Cursor.ID {
				skipping = false
			}
			continue
		}

		if count >= pp.Limit {
			break
		}

		memberIDs, _ := d.Members.ListUserIDsInProject(r.Context(), p.ID)
		repos, _ := d.Repos.ListByProject(r.Context(), p.ID)
		ids := make([]int64, len(repos))
		for i, rr := range repos {
			ids[i] = rr.ID
		}
		sizes := d.liveRepoSizes(r.Context(), ids)
		var totalSize int64
		for _, rr := range repos {
			totalSize += sizes[rr.ID]
		}
		// F-S3-B: fold S3 bucket bytes into the project total so the
		// dashboard card and the list card agree (both charge the
		// project for bucket bytes, per-bucket breakdown is in detail).
		if d.S3Backend != nil {
			if buckets, berr := d.S3Backend.ListBucketsForProject(r.Context(), p.ID); berr == nil {
				for _, b := range buckets {
					totalSize += b.SizeBytes
				}
			}
		}

		items = append(items, projectListItem{
			ID:            p.ID,
			Name:          p.Name,
			DescriptionMD: p.DescriptionMD,
			MemberCount:   len(memberIDs),
			RepoCount:     len(repos),
			SizeBytes:     totalSize,
			CreatedAt:     p.CreatedAt,
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

func (d Deps) handleGetProject(w http.ResponseWriter, r *http.Request) {
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

	memberIDs, _ := d.Members.ListUserIDsInProject(r.Context(), p.ID)
	members := make([]projectMember, 0, len(memberIDs))
	for _, uid := range memberIDs {
		u, err := d.Users.FindByID(r.Context(), uid)
		if err != nil {
			continue
		}
		members = append(members, projectMember{
			UserID: u.ID, Login: u.Login, Email: u.Email,
		})
	}

	repos, _ := d.Repos.ListByProject(r.Context(), p.ID)
	ids := make([]int64, len(repos))
	for i, rr := range repos {
		ids[i] = rr.ID
	}
	sizes := d.liveRepoSizes(r.Context(), ids)
	repoItems := make([]projectRepo, 0, len(repos))
	for _, rr := range repos {
		repoItems = append(repoItems, projectRepo{
			ID: rr.ID, Type: rr.Type, Name: rr.Name,
			DescriptionMD: rr.DescriptionMD, SizeBytes: sizes[rr.ID],
			AutoScan: rr.AutoScan, PublicRead: rr.PublicRead,
			CreatedAt: rr.CreatedAt,
		})
	}

	// F-S3-B: surface S3 buckets alongside repos so the Overview storage
	// card and the S3 tab can both read from one response.
	bucketItems := make([]projectBucket, 0)
	if d.S3Backend != nil {
		if rows, err := d.S3Backend.ListBucketsForProject(r.Context(), p.ID); err == nil {
			for _, b := range rows {
				bucketItems = append(bucketItems, projectBucket{
					ID:          b.ID,
					Name:        b.Name,
					SizeBytes:   b.SizeBytes,
					ObjectCount: b.ObjectCount,
					CreatedAt:   b.CreatedAt,
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, projectDetailResponse{
		ID: p.ID, Name: p.Name, DescriptionMD: p.DescriptionMD,
		CreatedAt: p.CreatedAt, Members: members, Repos: repoItems,
		Buckets: bucketItems,
	})
}

func (d Deps) handleProjectActivity(w http.ResponseWriter, r *http.Request) {
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

	// Escape LIKE wildcards in project name to prevent wildcard injection.
	escapedName := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(p.Name)

	// Query audit_log for events scoped to this project.
	// The details_json LIKE clause was removed — it was overly broad and
	// could leak audit data from other projects.
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, event_kind, actor_user_id, target_kind, target_id,
		       outcome, details_json, ip, user_agent, occurred_at
		FROM audit_log
		WHERE (target_kind='project' AND target_id=?)
		   OR (target_kind IN ('repo','member') AND target_id LIKE ? ESCAPE '\')
		ORDER BY id DESC
		LIMIT 50
	`, p.Name, escapedName+"/%")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()

	type activityItem struct {
		ID         int64   `json:"id"`
		Action     string  `json:"action"`
		ActorID    *int64  `json:"actor_user_id,omitempty"`
		TargetKind string  `json:"target_kind"`
		TargetID   string  `json:"target_id"`
		Outcome    string  `json:"outcome,omitempty"`
		Details    string  `json:"details,omitempty"`
		CreatedAt  string  `json:"created_at"`
	}

	items := make([]activityItem, 0)
	for rows.Next() {
		var item activityItem
		var actorID *int64
		var details, ip, ua, outcome *string
		if err := rows.Scan(&item.ID, &item.Action, &actorID, &item.TargetKind,
			&item.TargetID, &outcome, &details, &ip, &ua, &item.CreatedAt); err != nil {
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		item.ActorID = actorID
		if outcome != nil {
			item.Outcome = *outcome
		}
		if details != nil {
			item.Details = *details
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
