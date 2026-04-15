// Package api hosts the hand-written HTTP handlers for OmniRepo's REST surface
// (D-36). Types live in types_phase1.go; handlers live in admin_phase1.go;
// this file defines the shared JSON error envelope + response helpers used by
// every endpoint.
package api

import (
	"encoding/json"
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
