package api

// v1.5 Phase 1 — DEV-only state reset endpoint (E2E-01).
//
// POST /api/v1/admin/_reset wipes every non-super-admin table in a single
// SQLite transaction so Playwright specs can call it in beforeEach and see
// a deterministic per-test DB. Preserves the super-admin users row + two
// bootstrap settings (docker_token_hmac_secret, upstream_creds_aead_key).
//
// SECURITY: two gates are stacked (CONTEXT.md D-02, belt-and-suspenders):
//   1. OMNIREPO_DEV=1 at mount time — when unset, the route is NOT
//      registered and chi returns a literal 404. Production binaries
//      never expose this surface.
//   2. auth.ActionResetState policy — super-admin only; anonymous callers
//      get 401 auth.unauthenticated from RequireCan, non-super get 403
//      auth.super_admin_required.
//
// An operator who sets OMNIREPO_DEV=1 in a shared environment AND grants
// super-admin credentials to an untrusted user creates a data-wipe
// capability. Accepted as residual risk — this endpoint exists solely to
// unblock test-suite determinism in dev/CI.
//
// Repudiation: the endpoint wipes audit_log itself, so emitting a
// self-referential audit event that always gets wiped is noise. Not
// recorded — dev-only by construction.

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/httperr"
)

// mountAdminReset installs POST /api/v1/admin/_reset when OMNIREPO_DEV=1.
// No-op in production binaries (route literally not registered → chi
// default 404). Uses devEnabled() from dev_error_routes.go (same package).
//
// NOTE: mounted INSIDE the /api/v1 authenticated chi.Group in
// admin_phase1.go so SessionOrAPIKey + membershipResolver populate the
// actor context before RequireCan runs. Mounting at top-level alongside
// MountDevErrorRoutes would bypass SessionOrAPIKey and every call would
// 401 even for a valid super-admin session.
func (d Deps) mountAdminReset(r chi.Router) {
	if !devEnabled() {
		return
	}
	r.With(authmw.RequireCan(auth.ActionResetState)).
		Post("/admin/_reset", d.handleAdminReset)
}

// handleAdminReset wipes non-super-admin state via metadata.DB.Reset and
// returns {"ok": true}. All error classes route through httperr.Write
// so the ApiErrorEnvelope contract is preserved (no naked stdlib error
// helpers — grep-enforced).
func (d Deps) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	// Destructive wipe with no DB audit trail (audit_log is itself wiped;
	// see header comment). Emit a structured slog line BEFORE the wipe so
	// an operator grepping server logs can reconstruct "who reset what
	// when" even after the next reset removes whatever trace the first
	// reset tried to leave.
	actorID := int64(0)
	actorLogin := ""
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		actorID = a.ID
		if u, err := d.Users.FindByID(r.Context(), a.ID); err == nil {
			actorLogin = u.Login
		}
	}
	slog.WarnContext(r.Context(), "admin.reset.triggered",
		"actor_id", actorID, "actor_login", actorLogin)
	if err := d.DB.Reset(r.Context()); err != nil {
		httperr.Write(w, r, httperr.Internal("e2e.reset.failed", err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
