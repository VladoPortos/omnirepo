package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// BasicOrAPIKey returns a chi middleware that accepts HTTP Basic auth in
// either of two forms (KEY-06):
//
//   - `Authorization: Basic base64(login:password)` → argon2id VerifyPassword
//   - `Authorization: Basic base64(login:<omr_u|p_...>)` → API-key path, password
//     is literally the API key plaintext
//
// The dispatch is a single pre-check: if the password field matches
// auth.APIKeyRegex, take the API-key path unconditionally (we never fall
// through to password verification with an API-key-shaped password — doing
// so would rate-limit legitimate key use behind wrong-password throttles).
//
// On success an auth.Actor is stashed on r.Context(). On failure 401.
//
// API-key path ignores the login field: Docker/Git/apt tooling often sends
// arbitrary usernames when the server only cares about the key. We accept
// any username so long as the key is valid. (Basic path still requires
// login to match a user.)
func BasicOrAPIKey(d Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			login, pw, ok := r.BasicAuth()
			if !ok {
				writeJSON401Basic(w, r)
				return
			}

			// Phase 04-09 D-31: project:<projname>:<omr_p_...> variant.
			//
			// HTTP Basic auth format: base64("project:<projname>:<omr_p_...>")
			// Go's r.BasicAuth() splits on the FIRST ":", so we get:
			//   login = "project"
			//   pw    = "<projname>:<omr_p_...>"
			//
			// Parse BEFORE the existing APIKeyRegex branch so the project:
			// variant never falls through to the generic API-key path.
			if login == "project" && d.Projects != nil {
				pwParts := strings.SplitN(pw, ":", 2) // ["<projname>", "<omr_p_...>"]
				if len(pwParts) == 2 && pwParts[0] != "" && auth.APIKeyRegex.MatchString(pwParts[1]) {
					actor, authed := authenticateProjectKey(r.Context(), d, pwParts[0], pwParts[1])
					if !authed {
						writeJSON401Basic(w, r)
						return
					}
					next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
					return
				}
				writeJSON401Basic(w, r)
				return
			}

			// KEY-06: API key presented in the password field.
			if auth.APIKeyRegex.MatchString(pw) {
				actor, authed := authenticateAPIKey(r.Context(), d, pw)
				if !authed {
					writeJSON401Basic(w, r)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
				return
			}

			// Password path (argon2id).
			actor, authed := authenticatePassword(r.Context(), d, login, pw)
			if !authed {
				writeJSON401Basic(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
		})
	}
}

// authenticatePassword looks up the user by login, verifies the password via
// argon2id, and returns an Actor on success.
func authenticatePassword(ctx context.Context, d Deps, login, password string) (auth.Actor, bool) {
	if err := auth.LoginValid(login); err != nil {
		auth.VerifyFixedCost(password)
		return auth.Actor{}, false
	}
	u, err := d.Users.FindByLogin(ctx, login)
	if err != nil {
		auth.VerifyFixedCost(password)
		return auth.Actor{}, false
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, password)
	if err != nil || !ok {
		return auth.Actor{}, false
	}
	return auth.Actor{
		ID:                 u.ID,
		Login:              u.Login,
		Kind:               auth.ActorKindUser,
		IsSuperAdmin:       u.IsSuperAdmin,
		MustChangePassword: u.MustChangePassword,
	}, true
}

// authenticateProjectKey handles the "project:<projname>" login variant
// (D-31). It verifies the password is a valid project API key that belongs
// to the named project. On mismatch (key owned by a different project) or
// unknown project, returns false — preventing cross-project privilege
// escalation (T-04-09-03).
func authenticateProjectKey(ctx context.Context, d Deps, projectName, apiKey string) (auth.Actor, bool) {
	_, prefix, sha, err := auth.ParseAPIKey(apiKey)
	if err != nil {
		return auth.Actor{}, false
	}
	row, err := d.APIKeys.FindByPrefixSha(ctx, prefix, sha)
	if err != nil {
		return auth.Actor{}, false
	}
	if !auth.EqualSHA256(row.TokenSHA256, sha) {
		return auth.Actor{}, false
	}
	// Must be a project-owned key.
	if row.OwnerKind != "project" || row.OwnerProjectID == nil {
		return auth.Actor{}, false
	}
	// Look up the project by name and verify ownership match.
	proj, err := d.Projects.FindByName(ctx, projectName)
	if err != nil {
		return auth.Actor{}, false
	}
	if proj.ID != *row.OwnerProjectID {
		return auth.Actor{}, false // T-04-09-03: project mismatch
	}
	pid := proj.ID
	actor := auth.Actor{
		Kind:         auth.ActorKindAPIKey,
		APIKeyID:     row.ID,
		OwnerKind:    auth.OwnerKindProject,
		ProjectScope: &pid,
	}
	_ = d.APIKeys.TouchLastUsed(ctx, row.ID, d.clock())
	return actor, true
}
