// Package api — S3 bucket provisioning + browsing.
//
// Without these endpoints the S3 protocol's CreateBucket path is disabled
// (DefaultProjectID=0 by design, so gofakes3 CreateBucket returns
// "bucket provisioning is administrative; use the REST API") and there is
// no operator-facing way to create, list, or browse a bucket's contents in
// production — conformance tests bypass all of that with direct SQLite hits.
//
// Endpoints live under /api/v1/projects/{name}/s3-buckets. All require
// session-or-API-key auth plus project authorization:
//
//   - POST   /                          ActionS3BucketWrite (create)
//   - GET    /                          ActionS3BucketRead  (list)
//   - GET    /{bucket}                  ActionS3BucketRead  (detail+size)
//   - DELETE /{bucket}                  ActionS3BucketWrite (empty-only)
//   - GET    /{bucket}/objects          ActionS3BucketRead  (paginated list)
//
// Super-admin bypass applies to all actions.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/johannesboyne/gofakes3"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
)

const maxS3BucketsBodyBytes = 16 * 1024

// maxBucketObjectsPage caps the page size of the objects-list endpoint. The
// underlying repo already clamps to 1000 (AWS default) but the UI uses a
// smaller default for responsiveness.
const (
	defaultBucketObjectsPageSize = 100
	maxBucketObjectsPageSize     = 1000
)

type s3BucketCreateRequest struct {
	Name string `json:"name"`
}

type s3BucketItem struct {
	Name        string    `json:"name"`
	SizeBytes   int64     `json:"size_bytes"`
	ObjectCount int64     `json:"object_count"`
	CreatedAt   time.Time `json:"created_at"`
}

type s3ObjectItem struct {
	Key          string    `json:"key"`
	SizeBytes    int64     `json:"size_bytes"`
	ETag         string    `json:"etag"`
	ContentType  string    `json:"content_type,omitempty"`
	SHA256       string    `json:"sha256,omitempty"`
	LastModified time.Time `json:"last_modified"`
}

type s3ObjectsPage struct {
	Items      []s3ObjectItem `json:"items"`
	NextMarker string         `json:"next_marker,omitempty"`
	Truncated  bool           `json:"truncated"`
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
		r.Get("/{bucket}", d.handleGetS3Bucket)
		r.Delete("/{bucket}", d.handleDeleteS3Bucket)
		r.Get("/{bucket}/objects", d.handleListS3Objects)
	})
}

// resolveBucketAccess returns the project id on success and writes the
// appropriate 401/403/404 on failure. The read flag determines which action
// is enforced via auth.Can.
func (d Deps) resolveBucketAccess(w http.ResponseWriter, r *http.Request, writeAction bool) (int64, string, auth.Actor, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return 0, "", auth.Actor{}, false
	}
	projectName := chi.URLParam(r, "name")
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return 0, "", auth.Actor{}, false
	}
	action := auth.ActionS3BucketRead
	if writeAction {
		action = auth.ActionS3BucketWrite
	}
	if allowed, reason := auth.Can(r.Context(), actor, action,
		auth.Target{Kind: "project", ProjectID: p.ID}); !allowed {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, reason)
		return 0, "", auth.Actor{}, false
	}
	return p.ID, p.Name, actor, true
}

func (d Deps) handleCreateS3Bucket(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveBucketAccess(w, r, true)
	if !ok {
		return
	}

	var req s3BucketCreateRequest
	r.Body = http.MaxBytesReader(nil, r.Body, maxS3BucketsBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeFieldValidationError(w, r, "name", "name required")
		return
	}

	if err := d.S3Backend.CreateBucketForProject(name, projectID); err != nil {
		switch {
		case gofakes3.HasErrorCode(err, gofakes3.ErrBucketAlreadyExists):
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "bucket name already in use")
		case gofakes3.HasErrorCode(err, gofakes3.ErrInvalidBucketName):
			writeFieldValidationError(w, r, "name", err.Error())
		default:
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		}
		return
	}

	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtS3BucketCreated,
		TargetKind: "s3_bucket",
		TargetID:   name,
		Details: map[string]any{
			"project": projectName,
			"name":    name,
		},
	}, actor)

	writeJSON(w, http.StatusCreated, s3BucketItem{
		Name:      name,
		CreatedAt: time.Now().UTC(),
	})
}

func (d Deps) handleListS3Buckets(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveBucketAccess(w, r, false)
	if !ok {
		return
	}
	rows, err := d.S3Backend.ListBucketsForProject(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	out := make([]s3BucketItem, 0, len(rows))
	for _, b := range rows {
		out = append(out, s3BucketItem{
			Name:        b.Name,
			SizeBytes:   b.SizeBytes,
			ObjectCount: b.ObjectCount,
			CreatedAt:   b.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (d Deps) handleGetS3Bucket(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveBucketAccess(w, r, false)
	if !ok {
		return
	}
	name := chi.URLParam(r, "bucket")
	info, found, err := d.S3Backend.GetBucketForProject(r.Context(), projectID, name)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if !found {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "bucket not found")
		return
	}
	writeJSON(w, http.StatusOK, s3BucketItem{
		Name:        info.Name,
		SizeBytes:   info.SizeBytes,
		ObjectCount: info.ObjectCount,
		CreatedAt:   info.CreatedAt,
	})
}

func (d Deps) handleDeleteS3Bucket(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveBucketAccess(w, r, true)
	if !ok {
		return
	}
	name := chi.URLParam(r, "bucket")
	info, found, err := d.S3Backend.GetBucketForProject(r.Context(), projectID, name)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if !found {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "bucket not found")
		return
	}

	if err := d.S3Backend.DeleteBucket(name); err != nil {
		switch {
		case gofakes3.HasErrorCode(err, gofakes3.ErrBucketNotEmpty):
			writeJSONError(w, r, http.StatusConflict, ErrConflict, "bucket not empty")
		default:
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		}
		return
	}

	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtS3BucketDeleted,
		TargetKind: "s3_bucket",
		TargetID:   name,
		Details: map[string]any{
			"project":              projectName,
			"name":                 name,
			"size_bytes_at_delete": info.SizeBytes,
		},
	}, actor)
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleListS3Objects(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveBucketAccess(w, r, false)
	if !ok {
		return
	}
	name := chi.URLParam(r, "bucket")
	info, found, err := d.S3Backend.GetBucketForProject(r.Context(), projectID, name)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if !found {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "bucket not found")
		return
	}

	q := r.URL.Query()
	prefix := q.Get("prefix")
	marker := q.Get("marker")

	limit := defaultBucketObjectsPageSize
	if lim := q.Get("limit"); lim != "" {
		if n, convErr := strconv.Atoi(lim); convErr == nil && n > 0 {
			limit = n
			if limit > maxBucketObjectsPageSize {
				limit = maxBucketObjectsPageSize
			}
		} else if convErr != nil && !errors.Is(convErr, strconv.ErrSyntax) {
			writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid limit")
			return
		}
	}

	page, err := d.S3ObjectsRepo.ListByBucket(r.Context(), info.ID, prefix, marker, limit)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	items := make([]s3ObjectItem, 0, len(page.Objects))
	for _, o := range page.Objects {
		items = append(items, s3ObjectItem{
			Key:          o.Key,
			SizeBytes:    o.SizeBytes,
			ETag:         o.ETag,
			ContentType:  o.ContentType,
			SHA256:       o.SHA256,
			LastModified: o.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, s3ObjectsPage{
		Items:      items,
		NextMarker: page.NextToken,
		Truncated:  page.IsTruncated,
	})
}
