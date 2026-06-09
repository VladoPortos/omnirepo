// Package api — full projects list + detail + activity.
//
// GET  /api/v1/projects            — paginated list with member_count, repo_count
// GET  /api/v1/projects/{name}     — full detail with members + repos
// GET  /api/v1/projects/{name}/activity — recent audit events
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/auth"
)

// liveRepoSizes returns a map repoID → summed bytes across every artifact
// table. `repos.size_bytes` is never written, so the raw column reads 0.
// This helper gives callers (projects list / detail, repo list, repo
// detail) the real stored size.
//
// Returns an empty map for an empty input slice, never nil.
func (d Deps) liveRepoSizes(ctx context.Context, ids []int64) map[int64]int64 {
	return d.liveRepoAggregate(ctx, ids, repoSizeExpr)
}

// liveRepoAggregate runs `SELECT r.id, <expr> FROM repos r WHERE r.id IN
// (ids...)` and returns id → value. Shared core of liveRepoSizes /
// liveRepoItemCounts. Returns an empty map (never nil) on empty input or
// DB error — callers treat missing entries as "unknown".
func (d Deps) liveRepoAggregate(ctx context.Context, ids []int64, expr string) map[int64]int64 {
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
	query := `SELECT r.id, ` + expr + ` FROM repos r WHERE r.id IN (` + strings.Join(ph, ",") + `)`
	rows, err := d.DB.Reader.QueryContext(ctx, query, args...)
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, v int64
		if err := rows.Scan(&id, &v); err == nil {
			out[id] = v
		}
	}
	return out
}

// repoItemCountExpr returns item counts per repo, dispatching on r.type.
// The repo header previously rendered "<type> · <bytes>"; an item count
// ("42 packages · 180 MB") communicates ingest progress at a glance.
// Per-type meaning:
//   - docker : number of (image, tag) rows — one per push
//   - rpm    : one row per ingested .rpm
//   - deb    : distinct (package, architecture) — dedups the same package
//     appearing in multiple suites (a common apt-mirror layout)
//   - pypi   : one row per uploaded sdist/wheel
//   - helm   : one row per chart version
//   - raw    : one row per stored object
//   - git    : branches + tags only (symbolic HEAD is excluded — it's an
//     internal bookkeeping ref that never appears in the refs list API or
//     in `git ls-remote` output, so counting it here would contradict what
//     users see and break the UI badge).
//   - s3     : repo-level count is 0 here (buckets carry their own counts)
const repoItemCountExpr = `(
    CASE r.type
        WHEN 'docker' THEN (SELECT COUNT(*) FROM docker_tags WHERE repo_id = r.id)
        WHEN 'rpm'    THEN (SELECT COUNT(*) FROM rpm_packages WHERE repo_id = r.id)
        WHEN 'deb'    THEN (SELECT COUNT(*) FROM (SELECT DISTINCT package, architecture FROM deb_packages WHERE repo_id = r.id))
        WHEN 'pypi'   THEN (SELECT COUNT(*) FROM pypi_files WHERE repo_id = r.id)
        WHEN 'helm'   THEN (SELECT COUNT(*) FROM helm_charts WHERE repo_id = r.id)
        WHEN 'raw'    THEN (SELECT COUNT(*) FROM raw_files WHERE repo_id = r.id)
        WHEN 'git'    THEN (SELECT COUNT(*) FROM git_refs WHERE repo_id = r.id AND type IN ('branch','tag'))
        WHEN 'go'     THEN (SELECT COUNT(*) FROM go_modules WHERE repo_id = r.id)
        ELSE 0
    END
)`

// liveRepoItemCounts mirrors liveRepoSizes but counts items. Returns an empty
// map (never nil) on an empty input or a DB error — callers treat missing
// entries as "unknown" and suppress the badge.
func (d Deps) liveRepoItemCounts(ctx context.Context, ids []int64) map[int64]int64 {
	return d.liveRepoAggregate(ctx, ids, repoItemCountExpr)
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
	DescriptionMD string    `json:"description_md"`
	MemberCount   int       `json:"member_count"`
	RepoCount     int       `json:"repo_count"`
	SizeBytes     int64     `json:"size_bytes"`
	CreatedAt     time.Time `json:"created_at"`
}

// projectDetailResponse is the full project detail.
type projectDetailResponse struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	DescriptionMD string          `json:"description_md"`
	CreatedAt     time.Time       `json:"created_at"`
	Members       []projectMember `json:"members"`
	Repos         []projectRepo   `json:"repos"`
	// Buckets is the S3 bucket list for this project, with live
	// size_bytes / object_count. Absent when the S3 backend is not wired
	// into Deps.
	Buckets []projectBucket `json:"buckets"`
}

type projectMember struct {
	UserID int64  `json:"user_id"`
	Login  string `json:"login"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type projectRepo struct {
	ID            int64     `json:"id"`
	Type          string    `json:"type"`
	Name          string    `json:"name"`
	DescriptionMD string    `json:"description_md"`
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
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	allProjects, err := d.Projects.ListAll(r.Context())
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Filter to visible projects (handles project-scoped API keys).
	// nil = super-admin, no filter.
	var memberSet map[int64]struct{}
	if ids := visibleProjectIDs(r.Context(), d.Members, actor); ids != nil {
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
		// Fold S3 bucket bytes into the project total so the dashboard
		// card and the list card agree (both charge the project for
		// bucket bytes, per-bucket breakdown is in detail).
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

	// Single JOIN query returns members with their roles in one round-trip
	// (avoids the old N+1 FindByID loop). pm.role is the RBAC column added
	// by migration 034; it is required on every member entry.
	memberRows, mErr := d.DB.Reader.QueryContext(r.Context(), `
		SELECT pm.user_id, u.login, u.email, pm.role
		FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = ? AND u.deleted_at IS NULL
		ORDER BY u.login
	`, p.ID)
	members := make([]projectMember, 0)
	if mErr == nil {
		defer func() { _ = memberRows.Close() }()
		for memberRows.Next() {
			var m projectMember
			if err := memberRows.Scan(&m.UserID, &m.Login, &m.Email, &m.Role); err == nil {
				members = append(members, m)
			}
		}
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

	// Surface S3 buckets alongside repos so the Overview storage card and
	// the S3 tab can both read from one response.
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

	// Escape LIKE wildcards in project name to prevent wildcard injection.
	escapedName := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(p.Name)

	// Query audit_log for events scoped to this project.
	//
	// - target_kind='project' keyed on the slug directly.
	// - target_kind in (repo, member) keyed with the slug as the first
	//   path segment.
	// - target_kind='project_api_key' stores the numeric api-key id as
	//   target_id (that's the audit target — the key, not the project),
	//   so we match on the project slug embedded in details_json. Without
	//   this branch, the project-overview Activity widget silently dropped
	//   every project.api-key.{create,revoke} event — operators could see
	//   them on the global dashboard but not on the project they belonged
	//   to. json_extract is exact-match, so no wildcard escaping.
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT id, event_kind, actor_user_id, target_kind, target_id,
		       outcome, details_json, ip, user_agent, occurred_at
		FROM audit_log
		WHERE (target_kind='project' AND target_id=?)
		   OR (target_kind IN ('repo','member') AND target_id LIKE ? ESCAPE '\')
		   OR (target_kind='project_api_key'
		       AND json_valid(details_json)
		       AND json_extract(details_json, '$.project')=?)
		ORDER BY id DESC
		LIMIT 50
	`, p.Name, escapedName+"/%", p.Name)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()

	type activityItem struct {
		ID         int64  `json:"id"`
		Action     string `json:"action"`
		ActorID    *int64 `json:"actor_user_id,omitempty"`
		TargetKind string `json:"target_kind"`
		TargetID   string `json:"target_id"`
		Outcome    string `json:"outcome,omitempty"`
		Details    string `json:"details,omitempty"`
		CreatedAt  string `json:"created_at"`
	}

	items := make([]activityItem, 0)
	for rows.Next() {
		var item activityItem
		var actorID *int64
		var details, ip, ua, outcome *string
		if err := rows.Scan(&item.ID, &item.Action, &actorID, &item.TargetKind,
			&item.TargetID, &outcome, &details, &ip, &ua, &item.CreatedAt); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
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
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
