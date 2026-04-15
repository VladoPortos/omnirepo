package middleware

import (
	"context"
	"net/http"

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
				writeJSON401(w)
				return
			}

			// KEY-06: API key presented in the password field.
			if auth.APIKeyRegex.MatchString(pw) {
				actor, authed := authenticateAPIKey(r.Context(), d, pw)
				if !authed {
					writeJSON401(w)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
				return
			}

			// Password path (argon2id).
			actor, authed := authenticatePassword(r.Context(), d, login, pw)
			if !authed {
				writeJSON401(w)
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
		return auth.Actor{}, false
	}
	u, err := d.Users.FindByLogin(ctx, login)
	if err != nil {
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
