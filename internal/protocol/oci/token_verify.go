package oci

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// challenge writes the WWW-Authenticate Bearer challenge response per the
// Docker Registry Token Authentication spec. The exact wire format MUST be:
//
//	Bearer realm="<scheme>://<host>/v2/token",service="omnirepo"
//
// Downstream clients (docker CLI, crane, podman) parse this header and
// follow up with a Basic-authed GET to /v2/token.
func (h *Handler) challenge(w http.ResponseWriter, r *http.Request) {
	// WR-01 partial fix. Previously we unconditionally honored
	// X-Forwarded-Proto: https from any client and combined it with r.Host
	// — so a malicious client could flip the scheme and influence the realm
	// host that docker / crane / podman use to fetch a token, pointing them
	// at an attacker-controlled endpoint.
	//
	// Until we introduce config.HTTP.ExternalURL + a TrustedProxies allowlist
	// (WR-01 full fix, deferred — needs a design decision), trust only the
	// terminating TLS state of THIS listener to decide scheme. An operator
	// fronting OmniRepo with a reverse proxy that terminates TLS must either
	// (a) enable the HTTPS listener directly on OmniRepo, or (b) wait for
	// the ExternalURL config knob.
	//
	// r.Host is still client-influenceable in theory, but it defaults to the
	// server's listen host when the client doesn't send a Host header and
	// chi does not forward arbitrary values. Revisit with ExternalURL.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf(`Bearer realm="%s://%s/v2/token",service="omnirepo"`, scheme, r.Host))
	writeOCIErr(w, http.StatusUnauthorized, ErrCodeUnauthorized, errors.New("authentication required"))
}

// VerifyBearer is the middleware for /v2/<name>/... routes. It parses the
// Bearer token, enforces the HS256 alg (explicit alg-confusion guard —
// T-02-05-01), re-resolves the Actor from the DB using the claims'
// (actor_id, kind), and attaches the Actor to ctx via auth.WithActor.
//
// On any failure (missing header, wrong alg, expired, unknown actor),
// responds with the WWW-Authenticate Bearer challenge and 401. Does NOT
// call next.
//
// Chain position: AFTER AnonymousReadOK. If AnonymousReadOK already
// attached an anonymous Actor, VerifyBearer passes through untouched so
// public_read repo reads continue to work without credentials.
func (h *Handler) VerifyBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If an actor is already in ctx (e.g., AnonymousReadOK upstream),
		// trust the upstream middleware and pass through. VerifyBearer's
		// job is to authenticate requests WITHOUT a prior actor.
		if _, ok := auth.ActorFromContext(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}

		raw := r.Header.Get("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			h.challenge(w, r)
			return
		}
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
		if raw == "" {
			h.challenge(w, r)
			return
		}

		var claims identityClaims
		tok, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
			// T-02-05-01: alg-confusion guard. Only accept HS256 — reject
			// "none", "RS256", and every other alg outright. Without this
			// check an attacker could present a JWT signed with alg=none
			// and our parser would validate it.
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected jwt alg %v", t.Header["alg"])
			}
			return h.hmacSecret, nil
		})
		if err != nil || tok == nil || !tok.Valid {
			h.challenge(w, r)
			return
		}

		actor, err := h.resolveActor(r.Context(), claims.ActorID, claims.Kind)
		if err != nil {
			h.challenge(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
	})
}
