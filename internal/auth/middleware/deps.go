// Package middleware wires the auth primitives into chi as two
// middlewares:
//
//   - SessionOrAPIKey: cookie OR Bearer api-key. Used by REST + Web UI.
//   - BasicOrAPIKey:   Basic login:password or Basic login:omr_<u|p>_... key
//     used by Docker/Git/apt/yum clients that send Basic.
//
// Both populate auth.Actor on the request context via auth.WithActor. They
// do NOT evaluate policy — that's the RequireCan helper's job, called
// AFTER middleware on handler-scoped routes. This keeps "who is this" and
// "may they do this" separated for the must_change_password wall to layer
// correctly: MCP users still identify successfully, but
// RequireCan returns 403 password-change-required for any action other than
// auth.change_own_password / auth.logout.
package middleware

import (
	"net/http"
	"time"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/httperr"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// Deps is the shared dependency bundle for both middlewares.
type Deps struct {
	Users    *metadata.UsersRepo
	Sessions *metadata.SessionsRepo
	APIKeys  *metadata.APIKeysRepo
	Projects *metadata.ProjectsRepo // needed for project:<proj>:<key> variant
	Clock    func() time.Time       // default time.Now; injectable for tests

	// SessionTTL is the sliding window (default 12h). SessionHardTTL
	// is the absolute cap from issuance (default 7d). Middleware extends
	// sessions.expires_at to min(now+TTL, issued_at+HardTTL) on every
	// successful session auth.
	SessionTTL     time.Duration
	SessionHardTTL time.Duration
}

// sessionTTL returns d.SessionTTL or 12h when unset.
func (d Deps) sessionTTL() time.Duration {
	if d.SessionTTL <= 0 {
		return 12 * time.Hour
	}
	return d.SessionTTL
}

// sessionHardTTL returns d.SessionHardTTL or 7d when unset.
func (d Deps) sessionHardTTL() time.Duration {
	if d.SessionHardTTL <= 0 {
		return 7 * 24 * time.Hour
	}
	return d.SessionHardTTL
}

// clock returns d.Clock() or time.Now() when d.Clock is nil.
func (d Deps) clock() time.Time {
	if d.Clock == nil {
		return time.Now().UTC()
	}
	return d.Clock().UTC()
}

// writeJSON401 emits a 401 with an ApiErrorEnvelope JSON body carrying
// code=auth.unauthenticated and class=permission. Does NOT include
// WWW-Authenticate so browsers won't pop the native Basic Auth dialog
// on /api/v1/ requests from the SPA. Routed through httperr.Write so
// the envelope's incident_id is stamped from chi middleware and the
// cause is logged (none here — caller has no cause to attach).
//
// The legacy {"error": "unauthenticated"} body was retired
// across the auth middleware surface so every 401/403 on /api/v1/*
// ships the canonical envelope.
func writeJSON401(w http.ResponseWriter, r *http.Request) {
	httperr.Write(w, r, httperr.Permission(
		"auth.unauthenticated",
		"You must be signed in to do that.",
		httperr.WithStatus(http.StatusUnauthorized),
	))
}

// writeJSON401Basic emits a 401 with WWW-Authenticate: Basic so CLI
// clients (git, docker, apt, yum) retry with credentials.
func writeJSON401Basic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Basic realm="omnirepo"`)
	writeJSON401(w, r)
}

// writeJSON403 emits a 403 with an ApiErrorEnvelope JSON body. The
// reason string is the policy-engine outcome token (stable tokens
// defined in internal/auth/policy.go as ReasonPasswordChangeRequired
// et al.) and becomes the envelope's code with a "auth." prefix so the
// UI can branch on it. The human-facing message comes from
// reasonMessage, which is a developer-authored static sentence — no
// server-internal data is ever interpolated into the wire message.
func writeJSON403(w http.ResponseWriter, r *http.Request, reason string) {
	httperr.Write(w, r, httperr.Permission(
		reasonCode(reason),
		reasonMessage(reason),
	))
}

// reasonCode maps a policy-engine reason token to a dotted envelope
// code. Unknown reasons get "auth.forbidden" so the wire envelope
// always matches the schema regex.
func reasonCode(reason string) string {
	switch reason {
	case auth.ReasonPasswordChangeRequired:
		return "auth.password_change_required"
	case auth.ReasonSuperAdminRequired:
		return "auth.super_admin_required"
	case auth.ReasonNotAProjectMember:
		return "auth.not_a_project_member"
	case auth.ReasonNotSelf:
		return "auth.not_self"
	case auth.ReasonRequiresAuth:
		return "auth.requires_auth"
	case auth.ReasonAnonymousPublicRead:
		return "auth.anonymous_public_read"
	case auth.ReasonUnknownAction:
		return "auth.unknown_action"
	default:
		return "auth.forbidden"
	}
}

// reasonMessage returns a user-facing static sentence for each known
// policy reason. Values are developer-authored; never interpolated
// from request or server state.
func reasonMessage(reason string) string {
	switch reason {
	case auth.ReasonPasswordChangeRequired:
		return "Your password must be changed before you can do that."
	case auth.ReasonSuperAdminRequired:
		return "This action requires a super-admin account."
	case auth.ReasonNotAProjectMember:
		return "You are not a member of this project."
	case auth.ReasonNotSelf:
		return "You can only do that for your own account."
	case auth.ReasonRequiresAuth:
		return "You must be signed in to do that."
	case auth.ReasonAnonymousPublicRead:
		return "This resource does not allow anonymous access."
	case auth.ReasonUnknownAction:
		return "You are not allowed to do that."
	default:
		return "You do not have permission to do that."
	}
}

// RequireCan returns a chi middleware that calls auth.Can(actor, action,
// Target{}) on the actor in the request context. On false, writes JSON 403
// with the returned reason. When auth.Can returns true, passes through.
//
// For handlers that need a non-empty Target (most project/repo actions), use
// RequireCanWith. This simpler form is for self-scoped / super-admin-only
// actions where Target{} is sufficient.
func RequireCan(action auth.Action) func(http.Handler) http.Handler {
	return RequireCanWith(action, func(r *http.Request) auth.Target {
		return auth.Target{}
	})
}

// RequireCanWith is the same as RequireCan but lets the caller derive a
// Target from the request (e.g., URL path parameters).
func RequireCanWith(action auth.Action, resolveTarget func(r *http.Request) auth.Target) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, ok := auth.ActorFromContext(r.Context())
			if !ok {
				writeJSON401(w, r)
				return
			}
			allowed, reason := auth.Can(r.Context(), actor, action, resolveTarget(r))
			if !allowed {
				writeJSON403(w, r, reason)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
