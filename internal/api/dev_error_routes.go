package api

// Dev-only canned-error routes that the UI story page
// (web/src/pages/_dev/ErrorClassStoryPage.tsx) hits to exercise
// ErrorEnvelopeRenderer against every ApiErrorClass end-to-end. Gated
// behind the OMNIREPO_DEV=1 environment variable at mount time so they
// never exist on a production binary.

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/httperr"
)

// devEnabled reports whether dev-only routes should be registered. Read
// once at MountDevErrorRoutes call time — flipping the env var requires
// a restart, which matches the "dev routes never land in prod binaries"
// invariant from the threat model.
func devEnabled() bool {
	return os.Getenv("OMNIREPO_DEV") == "1"
}

// MountDevErrorRoutes registers the canned-error routes that let the UI
// story page exercise ErrorEnvelopeRenderer against every
// ApiErrorClass. No-op when OMNIREPO_DEV is not set to "1".
//
// Routes:
//
//	GET /api/v1/_dev/error/validation
//	GET /api/v1/_dev/error/permission
//	GET /api/v1/_dev/error/transient
//	GET /api/v1/_dev/error/operator_action_required
//
// Unknown :class values return 400 so Playwright can distinguish a
// "route not registered" condition (dev disabled, 404) from "valid
// route, unknown class param" (dev enabled, 400).
func MountDevErrorRoutes(r chi.Router) {
	if !devEnabled() {
		return
	}
	r.Get("/api/v1/_dev/error/{class}", handleDevError)
}

func handleDevError(w http.ResponseWriter, r *http.Request) {
	class := chi.URLParam(r, "class")
	var e *httperr.Error
	switch class {
	case "validation":
		e = httperr.ValidationFields(
			"dev.validation",
			"Some fields need your attention.",
			map[string]string{"user.email": "invalid"},
		)
		// Surface the single-field shortcut too so the UI's dual-path
		// normalisation in useApiError is exercised on the same call.
		e.Envelope.Details["field"] = "user.email"
	case "permission":
		e = httperr.Permission(
			"dev.permission",
			"You do not have permission to view this.",
			httperr.WithHint("Ask a project owner."),
		)
	case "transient":
		e = httperr.Transient(
			"dev.transient",
			"We couldn't reach the server.",
			3000,
			httperr.WithHint("Please try again in a few seconds."),
		)
	case "operator_action_required":
		e = httperr.OperatorRequired(
			"dev.operator",
			"OmniRepo needs an administrator to finish setup.",
			"/admin/trivy",
			"Go to Admin → Trivy",
		)
	default:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "unknown class",
		})
		return
	}
	httperr.Write(w, r, e)
}
