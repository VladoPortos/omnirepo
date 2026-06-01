package oci

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/vladoportos/omnirepo/internal/auth"
)

// challenge writes the WWW-Authenticate Bearer challenge response per the
// Docker Registry Token Authentication spec. The wire format is:
//
//	Bearer realm="<scheme>://<host>/v2/token",service="omnirepo"[,scope="repository:<name>:<actions>"]
//
// Downstream clients (docker CLI, crane, podman) parse this header and
// follow up with a Basic-authed GET to the realm URL — they need `scope=`
// to know which repository+actions to request the token for. Without it,
// `crane copy` and `docker push` abort on the 401 instead of completing the
// Bearer exchange.
//
// Scope is omitted for `/v2/` (ping) and `/v2/_catalog` because those aren't
// repo-scoped; clients asking for them without auth get a plain challenge
// and can re-request with or without creds.
func (h *Handler) challenge(w http.ResponseWriter, r *http.Request) {
	// The realm host is resolved as:
	//   1. server.external_hostnames[0] when configured — a trust-pinned host
	//      provided by the operator, immune to client Host-header spoofing.
	//   2. r.Host otherwise — preserved as a fallback for dev/self-signed
	//      single-listener deployments that haven't configured an external
	//      hostname.
	// Accepting only entry 0 (not iterating) intentionally: a Bearer realm
	// is a single URL; picking any element other than the first would make
	// behavior config-order-dependent in surprising ways.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if len(h.externalHostnames) > 0 && h.externalHostnames[0] != "" {
		host = h.externalHostnames[0]
	}
	header := fmt.Sprintf(`Bearer realm="%s://%s/v2/token",service="omnirepo"`, scheme, host)
	if scope := scopeForRequest(h, r); scope != "" {
		header += fmt.Sprintf(`,scope="%s"`, scope)
	}
	w.Header().Set("WWW-Authenticate", header)
	writeOCIErr(w, http.StatusUnauthorized, ErrCodeUnauthorized, errors.New("authentication required"))
}

// scopeForRequest builds the `repository:<name>:<actions>` scope value for
// a given /v2/... request. Returns "" for non-repo-scoped paths (/v2 itself,
// /v2/_catalog, /v2/token).
//
// <actions> is derived from the HTTP method:
//
//	GET, HEAD  → "pull"
//	PUT, POST, PATCH, DELETE → "pull,push" (pull is required to verify
//	    the manifest references before push completes)
func scopeForRequest(h *Handler, r *http.Request) string {
	project, repoType, repoName, ok := h.extractRepoFromV2URL(r)
	if !ok {
		return ""
	}
	name := project + "/" + repoType + "/" + repoName
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return "repository:" + name + ":pull"
	default:
		return "repository:" + name + ":pull,push"
	}
}

// tryBasicAuth resolves the request's Authorization: Basic header against
// either an API key (password field) or a user's argon2id password hash,
// returning (actor, true) on success. Returns (_, false) when no Basic
// header is present OR when the creds don't verify — the caller then falls
// through to the Bearer path.
//
// Mirrors internal/auth/middleware.BasicOrAPIKey but is inline here so the
// OCI middleware can attach the actor without taking the challenge branch
// on a missing Bearer token. Enables crane/docker push+pull to work after
// `crane auth login` without requiring a separate /v2/token exchange that
// those clients don't perform when /v2/ returns 200 anonymously.
func (h *Handler) tryBasicAuth(r *http.Request) (auth.Actor, bool) {
	login, pw, ok := r.BasicAuth()
	if !ok {
		return auth.Actor{}, false
	}
	if h.apiKeys != nil && auth.APIKeyRegex.MatchString(pw) {
		_, prefix, sha, perr := auth.ParseAPIKey(pw)
		if perr != nil {
			return auth.Actor{}, false
		}
		row, err := h.apiKeys.FindByPrefixSha(r.Context(), prefix, sha)
		if err != nil || !auth.EqualSHA256(row.TokenSHA256, sha) {
			return auth.Actor{}, false
		}
		actor := auth.Actor{
			Kind:      auth.ActorKindAPIKey,
			APIKeyID:  row.ID,
			OwnerKind: auth.OwnerKindUser,
		}
		if row.OwnerKind == "user" && row.OwnerUserID != nil && h.users != nil {
			u, uerr := h.users.FindByID(r.Context(), *row.OwnerUserID)
			if uerr != nil {
				return auth.Actor{}, false
			}
			actor.ID = u.ID
			actor.Login = u.Login
			actor.IsSuperAdmin = u.IsSuperAdmin
			actor.MustChangePassword = u.MustChangePassword
		} else if row.OwnerKind == "project" && row.OwnerProjectID != nil {
			pid := *row.OwnerProjectID
			actor.OwnerKind = auth.OwnerKindProject
			actor.ProjectScope = &pid
			// Thread the minted role so viewer project keys cannot push via
			// the direct-Basic path. ResolveMembership defaults an empty
			// APIKeyRole to "maintainer"; NULL role = legacy key.
			if row.Role != nil {
				actor.APIKeyRole = *row.Role
			} else {
				actor.APIKeyRole = "maintainer"
			}
		} else {
			return auth.Actor{}, false
		}
		return actor, true
	}
	// Password path — argon2id verify.
	if h.users == nil {
		return auth.Actor{}, false
	}
	if err := auth.LoginValid(login); err != nil {
		auth.VerifyFixedCost(pw)
		return auth.Actor{}, false
	}
	u, err := h.users.FindByLogin(r.Context(), login)
	if err != nil {
		auth.VerifyFixedCost(pw)
		return auth.Actor{}, false
	}
	ok, verr := auth.VerifyPassword(u.PasswordHash, pw)
	if verr != nil || !ok {
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

// VerifyBearer is the middleware for /v2/<name>/... routes. It parses the
// Bearer token, enforces the HS256 alg (explicit alg-confusion guard),
// re-resolves the Actor from the DB using the claims'
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

		// Accept HTTP Basic auth directly on resource paths.
		// go-containerregistry/crane probes /v2/ once; when it returns 200
		// (anonymous-accessible), the client never learns about the Bearer
		// realm and therefore doesn't perform the token exchange on
		// resource 401s. Accepting Basic here bypasses that dance — the
		// client's cached creds from `crane auth login` work uniformly
		// across push and pull without a separate /v2/token round-trip.
		if actor, ok := h.tryBasicAuth(r); ok {
			next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
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
			// alg-confusion guard. Only accept HS256 — reject
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
