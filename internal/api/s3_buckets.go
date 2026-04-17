// Package api — S3 bucket provisioning (walkthrough 2026-04-17).
//
// Without this endpoint the S3 protocol's CreateBucket path is disabled
// (DefaultProjectID=0 by design, so gofakes3 CreateBucket returns
// "bucket provisioning is administrative; use the REST API") and there is
// no other operator-facing way to create a bucket in production. Conformance
// tests worked around the gap by inserting directly into SQLite; that is not
// a path real users can take.
//
// Endpoints live under /api/v1/projects/{name}/s3-buckets. Both require
// session-or-API-key auth plus project-member authorization via
// auth.ActionS3BucketWrite (same membership model as create-repo; super-admin
// bypass applies).
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/johannesboyne/gofakes3"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
)

const maxS3BucketsBodyBytes = 16 * 1024

type s3BucketCreateRequest struct {
	Name string `json:"name"`
}

type s3BucketItem struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// mountS3Buckets installs the s3-buckets endpoints. nil-safe — if the backend
// was not wired into Deps the routes are skipped.
func (d Deps) mountS3Buckets(r chi.Router) {
	if d.S3Backend == nil {
		return
	}
	r.Route("/projects/{name}/s3-buckets", func(r chi.Router) {
		r.Post("/", d.handleCreateS3Bucket)
		r.Get("/", d.handleListS3Buckets)
	})
}

// resolveProjectAndCheckBucketAccess returns the project id on success and
// writes the appropriate 401/403/404 on failure. Shared by create and list.
func (d Deps) resolveProjectAndCheckBucketAccess(w http.ResponseWriter, r *http.Request) (int64, string, auth.Actor, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, ErrUnauthenticated, "")
		return 0, "", auth.Actor{}, false
	}
	projectName := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return 0, "", auth.Actor{}, false
	}
	if allowed, reason := auth.Can(r.Context(), actor, auth.ActionS3BucketWrite,
		auth.Target{Kind: "project", ProjectID: p.ID}); !allowed {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, reason)
		return 0, "", auth.Actor{}, false
	}
	return p.ID, p.Name, actor, true
}

func (d Deps) handleCreateS3Bucket(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectAndCheckBucketAccess(w, r)
	if !ok {
		return
	}

	var req s3BucketCreateRequest
	r.Body = http.MaxBytesReader(nil, r.Body, maxS3BucketsBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "name required")
		return
	}

	if err := d.S3Backend.CreateBucketForProject(name, projectID); err != nil {
		switch {
		case gofakes3.HasErrorCode(err, gofakes3.ErrBucketAlreadyExists):
			writeJSONError(w, http.StatusConflict, ErrConflict, "bucket name already in use")
		case gofakes3.HasErrorCode(err, gofakes3.ErrInvalidBucketName):
			writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, err.Error())
		default:
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		}
		return
	}

	uid := actor.ID
	d.recordAudit(r, audit.Event{
		Kind:        audit.EvtS3BucketCreated,
		ActorUserID: &uid,
		TargetKind:  "s3_bucket",
		TargetID:    name,
		Details: map[string]any{
			"project": projectName,
			"name":    name,
		},
	})

	writeJSON(w, http.StatusCreated, s3BucketItem{
		Name:      name,
		CreatedAt: time.Now().UTC(),
	})
}

func (d Deps) handleListS3Buckets(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveProjectAndCheckBucketAccess(w, r)
	if !ok {
		return
	}
	rows, err := d.S3Backend.ListBucketsForProject(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	out := make([]s3BucketItem, 0, len(rows))
	for _, b := range rows {
		out = append(out, s3BucketItem{Name: b.Name, CreatedAt: b.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}
