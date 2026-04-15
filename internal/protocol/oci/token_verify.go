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
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// If the request carried an X-Forwarded-Proto, honor it (TLS terminates
	// at a reverse proxy in some deployments). We never let the client set
	// an arbitrary realm host — Host comes from the trusted server-side
	// config via r.Host, which chi normalizes.
	if xfp := r.Header.Get("X-Forwarded-Proto"); xfp == "https" {
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
