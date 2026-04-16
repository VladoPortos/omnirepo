// Package middleware wires the auth primitives into chi as two Phase 1
// middlewares (D-20, D-21):
//
//   - SessionOrAPIKey: cookie OR Bearer api-key. Used by REST + Web UI.
//   - BasicOrAPIKey:   Basic login:password or Basic login:omr_<u|p>_... key
//     used by Docker/Git/apt/yum clients that send Basic (KEY-06).
//
// Both populate auth.Actor on the request context via auth.WithActor. They
// do NOT evaluate policy — that's the RequireCan helper's job, called
// AFTER middleware on handler-scoped routes. This keeps "who is this" and
// "may they do this" separated for the must_change_password wall to layer
// correctly (pitfall P5): MCP users still identify successfully, but
// RequireCan returns 403 password-change-required for any action other than
// auth.change_own_password / auth.logout.
package middleware

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// Deps is the shared dependency bundle for both middlewares. Plan 05 adds a
// Members field when the MembersRepo lands; Phase 1 leaves it out.
type Deps struct {
	Users    *metadata.UsersRepo
	Sessions *metadata.SessionsRepo
	APIKeys  *metadata.APIKeysRepo
	Projects *metadata.ProjectsRepo // Phase 04-09: needed for project:<proj>:<key> variant
	Clock    func() time.Time       // default time.Now; injectable for tests

	// SessionTTL is the D-07 sliding window (default 12h). SessionHardTTL
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

// writeJSON401 emits a 401 with JSON body {"error":"unauthenticated"}.
func writeJSON401(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated"})
}

// writeJSON403 emits a 403 with JSON body {"error": reason}.
func writeJSON403(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
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
				writeJSON401(w)
				return
			}
			allowed, reason := auth.Can(r.Context(), actor, action, resolveTarget(r))
			if !allowed {
				writeJSON403(w, reason)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
