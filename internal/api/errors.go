// Package api hosts the hand-written HTTP handlers for OmniRepo's REST surface
// (D-36). Types live in types_phase1.go; handlers live in admin_phase1.go;
// this file defines the shared JSON error envelope + response helpers used by
// every endpoint.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ErrorResponse is the canonical JSON error envelope (D-36).
type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

// Stable error codes consumed by UI and tests.
const (
	ErrPasswordChangeRequired = "password-change-required"
	ErrUnauthenticated        = "unauthenticated"
	ErrForbidden              = "forbidden"
	ErrNotFound               = "not_found"
	ErrValidationFailed       = "validation_failed"
	ErrConflict               = "conflict"
	ErrInternal               = "internal"
)

// writeJSONError emits a JSON error envelope with status code.
func writeJSONError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: code, Detail: detail})
}

// writeJSON emits status + JSON body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

// maxAdminJSONBodyBytes is the default cap for small admin JSON POST/PATCH
// bodies (create project, create repo, create user, etc.). Larger payloads
// (e.g. description_md in repo PATCH) already use repos.maxRepoPatchBodyBytes.
// Audit finding #10.
const maxAdminJSONBodyBytes int64 = 64 << 10 // 64 KiB

// decodeJSONBody wraps r.Body with http.MaxBytesReader(limit) and decodes it
// into out. On overflow it writes 413 + validation_failed; on malformed JSON
// it writes 400. Returns true on success. Callers just `return` on false.
//
// Audit finding #10: standardizes body-size defense so new handlers don't
// forget MaxBytesReader and existing unbounded JSON decodes (handleCreateUser,
// handleCreateProject, handleCreateRepo) cannot force unbounded buffering.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, ErrValidationFailed, "request body too large")
			return false
		}
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return false
	}
	return true
}
