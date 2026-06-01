// Package oci — GET /v2/_catalog.
//
// Scoping:
//   - super-admin → every docker repo
//   - anonymous → only public_read=true repos
//   - authenticated non-super-admin → repos whose project the actor belongs
//     to, unioned with public_read repos
//
// Pagination mirrors tags/list: ?n=&last=.
package oci

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// catalogAuth is the middleware dedicated to /v2/_catalog. Semantics:
//   - No Authorization header → attach anonymous Actor, continue.
//   - Authorization header present → must be a valid Bearer. Bad tokens
//     (missing, wrong alg, expired, bad signature, unknown actor) 401 with
//     the standard WWW-Authenticate challenge, matching VerifyBearer.
//
// This preserves the invariant that every OCI test asserting "bad auth →
// 401" continues to hold for /v2/_catalog while still allowing genuine
// anonymous catalog reads.
func (h *Handler) catalogAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		if raw == "" {
			// Pure anonymous path.
			next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(),
				auth.Actor{Kind: auth.ActorKindAnonymous})))
			return
		}
		if !strings.HasPrefix(raw, "Bearer ") {
			h.challenge(w, r)
			return
		}
		tokStr := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
		if tokStr == "" {
			h.challenge(w, r)
			return
		}
		var claims identityClaims
		tok, err := jwt.ParseWithClaims(tokStr, &claims, func(t *jwt.Token) (any, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("bad alg %v", t.Header["alg"])
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

type catalogResponse struct {
	Repositories []string `json:"repositories"`
}

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		// Without AnonymousReadOK attaching anonymous, we fall through to
		// VerifyBearer which already 401'd. Defensive 401 here.
		h.challenge(w, r)
		return
	}

	q := r.URL.Query()
	limit := 100
	if n := q.Get("n"); n != "" {
		if v, err := strconv.Atoi(n); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	after := q.Get("last")

	scope := metadata.CatalogScope{}
	switch actor.Kind {
	case auth.ActorKindAnonymous:
		scope.Anonymous = true
	default:
		if actor.IsSuperAdmin {
			scope.SuperAdmin = true
		} else {
			// Resolve project memberships.
			if h.members != nil && actor.ID != 0 {
				ids, err := h.members.ListProjectIDsForUser(r.Context(), actor.ID)
				if err != nil {
					writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
					return
				}
				scope.UserProjectIDs = ids
			}
			// Project-scoped API keys: their scope project is the only member.
			if actor.Kind == auth.ActorKindAPIKey && actor.ProjectScope != nil {
				scope.UserProjectIDs = append(scope.UserProjectIDs, *actor.ProjectScope)
			}
		}
	}

	paths, err := h.repos.ListDockerCatalog(r.Context(), scope, limit, after)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	if len(paths) == limit && len(paths) > 0 {
		last := paths[len(paths)-1]
		next := url.Values{}
		next.Set("n", strconv.Itoa(limit))
		next.Set("last", last)
		w.Header().Set("Link", fmt.Sprintf(`</v2/_catalog?%s>; rel="next"`, next.Encode()))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(catalogResponse{Repositories: paths})
}
