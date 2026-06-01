// Package api — "my S3 keys" endpoints (profile page UX).
//
// Endpoints live under /api/v1/me/s3-keys and let a user see and manage the
// S3 access keys THEY created, across every project they belong to. Distinct
// from /api/v1/projects/{name}/s3-access-keys which exposes project-scoped
// CRUD for any member.
//
// Authorization model:
//   - List: returns rows where created_by_user_id = actor.ID, no project
//     filtering beyond that. Super-admins see only their own minted keys
//     here too — the project-scoped endpoint remains the cross-user view.
//   - Create: requires auth.ActionManageS3Keys on the target project
//     (same gate as the project-scoped create, so super-admin + member
//     both succeed).
//   - Delete: requires ownership (created_by_user_id = actor.ID). Cannot
//     revoke someone else's key through this path.
//
// The shown-once secret is returned ONLY on create.
package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	s3keys "github.com/vladoportos/omnirepo/internal/protocol/s3/keys"
)

// mountMeS3Keys installs /me/s3-keys list/create/delete under the already-auth'd
// /api/v1 subtree. nil-safe: no-op when S3KeysRepo or S3AEAD is not wired.
func (d Deps) mountMeS3Keys(r chi.Router) {
	if d.S3Keys == nil || d.S3AEAD == nil {
		return
	}
	r.Route("/me/s3-keys", func(r chi.Router) {
		r.Get("/", d.handleListMyS3Keys)
		r.Post("/", d.handleCreateMyS3Key)
		r.Delete("/{id}", d.handleDeleteMyS3Key)
	})
}

// myS3KeyItem mirrors the frontend S3Key type. project_id is included so the
// profile page can look up the project name without a second query per key.
type myS3KeyItem struct {
	ID          int64      `json:"id"`
	AccessKeyID string     `json:"access_key_id"`
	ProjectID   int64      `json:"project_id"`
	Label       string     `json:"label"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

type myS3KeyListResponse struct {
	Items []myS3KeyItem `json:"items"`
}

type myS3KeyCreateRequest struct {
	ProjectID int64  `json:"project_id"`
	Label     string `json:"label"`
}

// myS3KeyCreateResponse matches the frontend S3KeyCreateResponse contract
// (fields: id, access_key_id, secret_access_key, project_id, created_at).
type myS3KeyCreateResponse struct {
	ID              int64     `json:"id"`
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	ProjectID       int64     `json:"project_id"`
	Label           string    `json:"label"`
	CreatedAt       time.Time `json:"created_at"`
}

func (d Deps) handleListMyS3Keys(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}
	rows, err := d.S3Keys.ListByCreatedByUser(r.Context(), actor.ID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	out := make([]myS3KeyItem, 0, len(rows))
	for _, k := range rows {
		out = append(out, myS3KeyItem{
			ID:          k.ID,
			AccessKeyID: k.AccessKeyID,
			ProjectID:   k.ProjectID,
			Label:       k.Label,
			CreatedAt:   k.CreatedAt,
			LastUsedAt:  k.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, myS3KeyListResponse{Items: out})
}

func (d Deps) handleCreateMyS3Key(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}
	var req myS3KeyCreateRequest
	r.Body = http.MaxBytesReader(nil, r.Body, maxS3KeysBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if req.ProjectID <= 0 {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "project_id required")
		return
	}
	p, err := d.Projects.FindByID(r.Context(), req.ProjectID)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}
	if allowed, reason := auth.Can(r.Context(), actor, auth.ActionManageS3Keys,
		auth.Target{Kind: "project", ProjectID: p.ID}); !allowed {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, reason)
		return
	}

	label := req.Label
	if label == "" {
		label = fmt.Sprintf("Profile-created %s", d.clock().Format("2006-01-02"))
	}

	var (
		akid, secret string
		rowID        int64
		lastErr      error
	)
	for attempt := 0; attempt <= maxAKIDCollisionRetries; attempt++ {
		var err error
		akid, secret, err = s3keys.GenerateS3AccessKey()
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		enc, err := d.S3AEAD.Encrypt([]byte(secret))
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		err = d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
			var ie error
			rowID, ie = d.S3Keys.Insert(r.Context(), tx, &metadata.S3AccessKey{
				ProjectID:       p.ID,
				AccessKeyID:     akid,
				SecretEnc:       []byte(enc),
				Label:           label,
				CreatedByUserID: actor.ID,
			})
			return ie
		})
		if err == nil {
			break
		}
		if isUniqueConstraintErr(err) {
			lastErr = err
			continue
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if lastErr != nil && rowID == 0 {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "akid collision exhausted")
		return
	}

	row, err := d.S3Keys.FindByID(r.Context(), rowID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	uid := actor.ID
	d.recordAudit(r, audit.Event{
		Kind:        audit.EvtS3AccessKeyCreated,
		ActorUserID: &uid,
		TargetKind:  "s3_access_key",
		TargetID:    strconv.FormatInt(rowID, 10),
		Details: map[string]any{
			"project":       p.Name,
			"id":            rowID,
			"label":         label,
			"access_key_id": akid,
			"via":           "profile",
		},
	})

	writeJSON(w, http.StatusCreated, myS3KeyCreateResponse{
		ID:              row.ID,
		AccessKeyID:     row.AccessKeyID,
		SecretAccessKey: secret,
		ProjectID:       row.ProjectID,
		Label:           row.Label,
		CreatedAt:       row.CreatedAt,
	})
}

func (d Deps) handleDeleteMyS3Key(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid id")
		return
	}
	// Atomic owner-checked revoke: collapses
	// FindByID + ownership check + Revoke into a single UPDATE so a
	// concurrent revoke / re-mint cannot create a TOCTOU window between
	// "I see your key" and "I revoke it". Rows-affected of zero covers
	// missing, already-revoked, AND owned-by-someone-else without a
	// separate read leak.
	var revoked bool
	var auditDetails map[string]any
	if err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		ok, ierr := d.S3Keys.RevokeIfOwnedBy(r.Context(), tx, id, actor.ID)
		if ierr != nil {
			return ierr
		}
		revoked = ok
		return nil
	}); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	if !revoked {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "s3 access key not found")
		return
	}

	// Best-effort enrichment of the audit event — read AFTER the revoke
	// committed so we surface the canonical AKID + project on the event.
	if row, ferr := d.S3Keys.FindByID(r.Context(), id); ferr == nil && row != nil {
		auditDetails = map[string]any{
			"id":            id,
			"access_key_id": row.AccessKeyID,
			"project_id":    row.ProjectID,
			"via":           "profile",
		}
	} else {
		auditDetails = map[string]any{
			"id":  id,
			"via": "profile",
		}
	}
	uid := actor.ID
	d.recordAudit(r, audit.Event{
		Kind:        audit.EvtS3AccessKeyRevoked,
		ActorUserID: &uid,
		TargetKind:  "s3_access_key",
		TargetID:    strconv.FormatInt(id, 10),
		Details:     auditDetails,
	})

	w.WriteHeader(http.StatusNoContent)
}
