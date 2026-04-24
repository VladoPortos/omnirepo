package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// SessionOrAPIKey returns a chi middleware that accepts either:
//
//   - `Authorization: Bearer <omr_u|p_...>` → API key auth, OR
//   - `Cookie: omnirepo_session=<token>`   → session auth.
//
// On success it stashes an auth.Actor on r.Context() and calls next. On
// failure (missing/invalid/expired) it writes 401 unauthenticated.
//
// Bearer takes precedence: if both are present and the bearer is malformed,
// the request is rejected rather than falling back to the cookie. This is
// deliberate — mixing credentials usually signals a client bug we want to
// surface.
//
// KEY-08: on every successful API-key auth, api_keys.last_used_at is bumped
// via APIKeysRepo.TouchLastUsed. On every successful session auth,
// sessions.last_seen_at is bumped via SessionsRepo.TouchLastSeen.
func SessionOrAPIKey(d Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if actor, handled, ok := tryBasicAPIKey(r, d); handled {
				if !ok {
					writeJSON401(w, r)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
				return
			}
			if bearer := stripBearer(r.Header.Get("Authorization")); bearer != "" {
				actor, ok := authenticateAPIKey(r.Context(), d, bearer)
				if !ok {
					writeJSON401(w, r)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
				return
			}
			if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
				actor, ok := authenticateSession(r.Context(), d, c.Value)
				if !ok {
					writeJSON401(w, r)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
				return
			}
			writeJSON401(w, r)
		})
	}
}

// tryBasicAPIKey returns (actor, handled, ok):
//   - handled=false → no Basic header (caller should fall through to Bearer/cookie).
//   - handled=true, ok=true → Basic header carried a valid API key.
//   - handled=true, ok=false → Basic header looked like an API-key request but
//     auth failed; caller should 401 rather than fall through.
//
// Accepts either `login:<omr_u|p_...>` or the project:<name>:<omr_p_...>
// variant already supported by BasicOrAPIKey (KEY-06 / D-31). Password-only
// basic auth is intentionally NOT accepted here — /api/v1 stays on
// session-cookie or Bearer for interactive logins.
func tryBasicAPIKey(r *http.Request, d Deps) (auth.Actor, bool, bool) {
	login, pw, ok := r.BasicAuth()
	if !ok {
		return auth.Actor{}, false, false
	}
	if login == "project" && d.Projects != nil {
		pwParts := strings.SplitN(pw, ":", 2)
		if len(pwParts) == 2 && pwParts[0] != "" && auth.APIKeyRegex.MatchString(pwParts[1]) {
			actor, authed := authenticateProjectKey(r.Context(), d, pwParts[0], pwParts[1])
			return actor, true, authed
		}
		return auth.Actor{}, true, false
	}
	if auth.APIKeyRegex.MatchString(pw) {
		actor, authed := authenticateAPIKey(r.Context(), d, pw)
		return actor, true, authed
	}
	return auth.Actor{}, false, false
}

// stripBearer returns the token portion of `Authorization: Bearer <token>` or
// "" when the header is missing or not a Bearer scheme.
func stripBearer(h string) string {
	const prefix = "Bearer "
	if len(h) < len(prefix) {
		return ""
	}
	if !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// authenticateAPIKey parses bearer, looks the row up by (prefix, sha256),
// loads the owning user, bumps last_used_at, and returns an Actor.
func authenticateAPIKey(ctx context.Context, d Deps, bearer string) (auth.Actor, bool) {
	kind, prefix, sha, err := auth.ParseAPIKey(bearer)
	if err != nil {
		return auth.Actor{}, false
	}
	_ = kind // kind is already encoded inside the plaintext; OwnerKind comes from the DB row.
	row, err := d.APIKeys.FindByPrefixSha(ctx, prefix, sha)
	if err != nil {
		return auth.Actor{}, false
	}
	if !auth.EqualSHA256(row.TokenSHA256, sha) {
		// Belt-and-braces: FindByPrefixSha already matched on sha, but the
		// policy here is "never trust a row whose sha doesn't constant-time
		// match". Cheap and keeps the invariant local to this function.
		return auth.Actor{}, false
	}
	actor := auth.Actor{
		Kind:     auth.ActorKindAPIKey,
		APIKeyID: row.ID,
	}
	switch row.OwnerKind {
	case "user":
		if row.OwnerUserID == nil {
			return auth.Actor{}, false
		}
		u, err := d.Users.FindByID(ctx, *row.OwnerUserID)
		if err != nil {
			return auth.Actor{}, false
		}
		actor.ID = u.ID
		actor.Login = u.Login
		actor.IsSuperAdmin = u.IsSuperAdmin
		actor.MustChangePassword = u.MustChangePassword
		actor.OwnerKind = auth.OwnerKindUser
	case "project":
		if row.OwnerProjectID == nil {
			return auth.Actor{}, false
		}
		pid := *row.OwnerProjectID
		actor.ProjectScope = &pid
		actor.OwnerKind = auth.OwnerKindProject
		// Thread the minted role into the policy layer so viewer project
		// keys are gated correctly. NULL role is a pre-v1.5 backfilled key;
		// those rows are defaulted to "maintainer" (D-24) at the app layer
		// to preserve legacy publishing behavior. ResolveMembership reads
		// APIKeyRole when ProjectScope is set.
		if row.Role != nil {
			actor.APIKeyRole = *row.Role
		} else {
			actor.APIKeyRole = "maintainer"
		}
	default:
		return auth.Actor{}, false
	}
	// KEY-08: bump last_used_at. This is a background-ish update in the
	// request path; we tolerate errors (log nothing here — the middleware
	// must not 500 because of a timestamp update). Plan 05's audit layer
	// will pick up the identification event regardless.
	_ = d.APIKeys.TouchLastUsed(ctx, row.ID, d.clock())
	return actor, true
}

// authenticateSession loads a session row by (prefix, sha256), loads the
// user, enforces the D-07 hard cap (issued_at + 7d) and slides expires_at
// forward to min(now + session_ttl, issued_at + hard_cap), then returns an
// Actor.
//
// Hard-cap enforcement: FindByPrefixSha filters WHERE expires_at > NOW which
// catches "natural" 12h-idle expiry. But a still-active session that has
// been touched every 11h for a week must also be rejected once now >
// issued_at + 7d. We check that here explicitly — past the cap, the session
// is invalid even if expires_at in the row is still in the future.
func authenticateSession(ctx context.Context, d Deps, token string) (auth.Actor, bool) {
	prefix, ok := auth.SessionPrefix(token)
	if !ok {
		return auth.Actor{}, false
	}
	sha := auth.SessionSHA256(token)
	row, err := d.Sessions.FindByPrefixSha(ctx, prefix, sha)
	if err != nil {
		return auth.Actor{}, false
	}
	if !auth.EqualSHA256(row.TokenSHA256, sha) {
		return auth.Actor{}, false
	}
	now := d.clock()
	hardCap := row.IssuedAt.Add(d.sessionHardTTL())
	if !now.Before(hardCap) {
		// Past hard cap — reject regardless of expires_at.
		return auth.Actor{}, false
	}
	u, err := d.Users.FindByID(ctx, row.UserID)
	if err != nil {
		return auth.Actor{}, false
	}
	// Slide expires_at forward up to min(now + TTL, issued_at + hard_cap).
	// Never push expires_at past the cap.
	sliding := now.Add(d.sessionTTL())
	newExpires := sliding
	if newExpires.After(hardCap) {
		newExpires = hardCap
	}
	_ = d.Sessions.SlideExpiry(ctx, row.ID, now, newExpires)
	return auth.Actor{
		ID:                 u.ID,
		Login:              u.Login,
		Kind:               auth.ActorKindUser,
		IsSuperAdmin:       u.IsSuperAdmin,
		MustChangePassword: u.MustChangePassword,
	}, true
}

// OptionalSessionOrAPIKey is like SessionOrAPIKey except that requests
// carrying no credentials, or a stale session cookie, are allowed
// through (no actor on context). Used for endpoints like GET /me that
// the SPA polls before login to decide "logged in?" without generating
// 401s in devtools.
//
// F-03.5 (wt3): when explicit API-key credentials (Basic Auth or
// Bearer) are provided but invalid, this MUST 401 — previously it
// silently dropped the auth and let the request through as anonymous,
// which gave protocol CLIs no feedback distinguishing "wrong key" from
// "server OK" (they just saw 200 null) and masked brute-force probes
// from audit / rate-limiters that key off 401 count. The strict
// SessionOrAPIKey twin already 401s on invalid creds — the divergence
// here was accidental.
//
// Stale session cookies remain a silent pass-through: a cookie from a
// tab where the user logged out is common and expected, and the SPA
// relies on 200-null to decide "show the login page" without a red
// 401 in devtools.
func OptionalSessionOrAPIKey(d Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if actor, handled, ok := tryBasicAPIKey(r, d); handled {
				if !ok {
					writeJSON401(w, r)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
				return
			}
			// If the caller supplied a Basic header whose password did NOT
			// look like an API key, tryBasicAPIKey returns handled=false to
			// preserve the /api/v1 contract that only api-key-shaped Basic
			// is accepted. But from an auth-optional endpoint's POV, the
			// caller DID explicitly attempt auth — return 401 instead of
			// silently demoting them to anonymous (F-03.5 wt3).
			if _, _, ok := r.BasicAuth(); ok {
				writeJSON401(w, r)
				return
			}
			if authz := r.Header.Get("Authorization"); authz != "" {
				bearer := stripBearer(authz)
				if bearer == "" {
					// Authorization header was set to something (Bearer with
					// empty token, or a scheme we don't accept) — that's an
					// explicit auth attempt, reject as malformed.
					writeJSON401(w, r)
					return
				}
				actor, ok := authenticateAPIKey(r.Context(), d, bearer)
				if !ok {
					writeJSON401(w, r)
					return
				}
				next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
				return
			}
			if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
				if actor, ok := authenticateSession(r.Context(), d, c.Value); ok {
					r = r.WithContext(auth.WithActor(r.Context(), actor))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ensure metadata import used even if the compiler does inlining tricks.
var _ = metadata.ErrNotFound
