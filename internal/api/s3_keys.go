// Package api — S3 access-key CRUD handlers.
//
// Endpoints live under /api/v1/projects/{name}/s3-access-keys. All require
// session-or-API-key auth AND project-member authorization via
// auth.ActionManageS3Keys.
//
// The create response is the ONLY time the plaintext secret is ever returned
// (shown-once discipline). List and revoke never expose it.
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	s3keys "github.com/vladoportos/omnirepo/internal/protocol/s3/keys"
)

// maxS3KeysBodyBytes caps request bodies (same as upstream_creds).
const maxS3KeysBodyBytes = 64 * 1024

// maxAKIDCollisionRetries is the number of times we retry on AKID UNIQUE
// collision before giving up with a 500.
const maxAKIDCollisionRetries = 3

// s3KeyCreateRequest is the POST body.
type s3KeyCreateRequest struct {
	Label string `json:"label"`
}

// s3KeyCreateResponse is the shown-once response. The Secret field
// is present ONLY in this struct — never in s3KeyListItem.
type s3KeyCreateResponse struct {
	ID          int64     `json:"id"`
	AccessKeyID string    `json:"access_key_id"`
	Secret      string    `json:"secret"`
	Label       string    `json:"label"`
	CreatedAt   time.Time `json:"created_at"`
}

// s3KeyListItem is the safe projection returned by List. No secret field.
type s3KeyListItem struct {
	ID          int64      `json:"id"`
	AccessKeyID string     `json:"access_key_id"`
	Label       string     `json:"label"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// mountS3Keys installs the s3-access-keys CRUD endpoints under the
// already-auth'd /api/v1 subrouter. If S3KeysRepo or S3AEAD is nil the
// routes are skipped (nil-safe).
func (d Deps) mountS3Keys(r chi.Router) {
	if d.S3Keys == nil || d.S3AEAD == nil {
		return
	}
	r.Route("/projects/{name}/s3-access-keys", func(r chi.Router) {
		r.Post("/", d.handleCreateS3Key)
		r.Get("/", d.handleListS3Keys)
		r.Delete("/{id}", d.handleRevokeS3Key)
	})
}

func (d Deps) handleCreateS3Key(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectForAction(w, r, auth.ActionManageS3Keys)
	if !ok {
		return
	}

	var req s3KeyCreateRequest
	r.Body = http.MaxBytesReader(nil, r.Body, maxS3KeysBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, err.Error())
		return
	}
	if strings.TrimSpace(req.Label) == "" {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "label required")
		return
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
				ProjectID:       projectID,
				AccessKeyID:     akid,
				SecretEnc:       []byte(enc),
				Label:           req.Label,
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

	// Reload to get DB-generated timestamps.
	row, err := d.S3Keys.FindByID(r.Context(), rowID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtS3AccessKeyCreated,
		TargetKind: "s3_access_key",
		TargetID:   strconv.FormatInt(rowID, 10),
		Details: map[string]any{
			"project":       projectName,
			"id":            rowID,
			"label":         req.Label,
			"access_key_id": akid,
			// NOTE: secret is deliberately excluded.
		},
	}, actor)

	writeJSON(w, http.StatusCreated, s3KeyCreateResponse{
		ID:          row.ID,
		AccessKeyID: row.AccessKeyID,
		Secret:      secret,
		Label:       row.Label,
		CreatedAt:   row.CreatedAt,
	})
}

func (d Deps) handleListS3Keys(w http.ResponseWriter, r *http.Request) {
	projectID, _, _, ok := d.resolveProjectForAction(w, r, auth.ActionManageS3Keys)
	if !ok {
		return
	}

	rows, err := d.S3Keys.ListByProject(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	out := make([]s3KeyListItem, 0, len(rows))
	for _, k := range rows {
		out = append(out, s3KeyListItem{
			ID:          k.ID,
			AccessKeyID: k.AccessKeyID,
			Label:       k.Label,
			CreatedAt:   k.CreatedAt,
			LastUsedAt:  k.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (d Deps) handleRevokeS3Key(w http.ResponseWriter, r *http.Request) {
	projectID, projectName, actor, ok := d.resolveProjectForAction(w, r, auth.ActionManageS3Keys)
	if !ok {
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid id")
		return
	}

	// Verify the key belongs to this project (don't revoke other projects' keys).
	row, err := d.S3Keys.FindByID(r.Context(), id)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "s3 access key not found")
		return
	}
	if row.ProjectID != projectID {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "s3 access key not found")
		return
	}

	if err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		return d.S3Keys.Revoke(r.Context(), tx, id)
	}); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	d.recordAuditAs(r, audit.Event{
		Kind:       audit.EvtS3AccessKeyRevoked,
		TargetKind: "s3_access_key",
		TargetID:   strconv.FormatInt(id, 10),
		Details: map[string]any{
			"project":       projectName,
			"id":            id,
			"access_key_id": row.AccessKeyID,
		},
	}, actor)

	w.WriteHeader(http.StatusNoContent)
}
