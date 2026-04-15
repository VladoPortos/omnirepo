package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// identityClaims is the D-06 JWT payload: identity only, NO scope/access
// claims. Every `/v2/*` request is re-authorized against auth.Can, which
// reads the DB as the single source of truth. A leaked JWT proves who you
// are but grants no permissions on its own.
type identityClaims struct {
	ActorID int64  `json:"actor_id"`
	Kind    string `json:"kind"` // "user" | "api_key"
	jwt.RegisteredClaims
}

// tokenResponse is the body of a successful /v2/token exchange.
type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
	IssuedAt  string `json:"issued_at"`
}

// signToken mints an HS256 JWT carrying only identity claims.
// The secret MUST be 32 random bytes (generated on first boot by
// app.bootEnsureDockerJWTSecret); shorter secrets are a hard error.
func (h *Handler) signToken(actor auth.Actor) (string, time.Time, time.Time, error) {
	if len(h.hmacSecret) < 32 {
		return "", time.Time{}, time.Time{}, errors.New("oci: hmac secret too short (need 32 bytes)")
	}
	iat := time.Now().UTC()
	exp := iat.Add(h.jwtTTL)
	claims := identityClaims{
		ActorID: actor.ID,
		Kind:    string(actor.Kind),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "omnirepo",
			Subject:   strconv.FormatInt(actor.ID, 10),
			IssuedAt:  jwt.NewNumericDate(iat),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(h.hmacSecret)
	if err != nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("oci: sign token: %w", err)
	}
	return signed, iat, exp, nil
}

// issueToken is the GET /v2/token handler. The /v2/token route runs under
// BasicOrAPIKey, so an authenticated Actor is already on ctx when we
// arrive here. If somehow missing, emit a WWW-Authenticate challenge so
// the Docker client retries with Basic.
func (h *Handler) issueToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok || actor.Kind == auth.ActorKindAnonymous {
		h.challenge(w, r)
		return
	}
	signed, _, _, err := h.signToken(actor)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	resp := tokenResponse{
		Token:     signed,
		ExpiresIn: int(h.jwtTTL.Seconds()),
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// resolveActor is called by verifyBearer to re-derive an Actor from the
// JWT's claims. Identity lookups go straight to the metadata DB (Users or
// APIKeys); no scope/permission is read from the JWT.
func (h *Handler) resolveActor(ctx context.Context, actorID int64, kind string) (auth.Actor, error) {
	switch kind {
	case string(auth.ActorKindUser):
		if h.users == nil {
			return auth.Actor{}, errors.New("oci: users repo not wired")
		}
		u, err := h.users.FindByID(ctx, actorID)
		if err != nil {
			return auth.Actor{}, fmt.Errorf("oci: resolve user %d: %w", actorID, err)
		}
		return auth.Actor{
			ID:                 u.ID,
			Login:              u.Login,
			Kind:               auth.ActorKindUser,
			IsSuperAdmin:       u.IsSuperAdmin,
			MustChangePassword: u.MustChangePassword,
		}, nil
	case string(auth.ActorKindAPIKey):
		if h.apiKeys == nil {
			return auth.Actor{}, errors.New("oci: api_keys repo not wired")
		}
		k, err := h.apiKeys.FindByID(ctx, actorID)
		if err != nil {
			return auth.Actor{}, fmt.Errorf("oci: resolve api_key %d: %w", actorID, err)
		}
		a := auth.Actor{
			Kind:     auth.ActorKindAPIKey,
			APIKeyID: k.ID,
		}
		switch k.OwnerKind {
		case "user":
			if k.OwnerUserID == nil || h.users == nil {
				return auth.Actor{}, errors.New("oci: api_key missing owner user")
			}
			u, err := h.users.FindByID(ctx, *k.OwnerUserID)
			if err != nil {
				return auth.Actor{}, fmt.Errorf("oci: resolve api_key owner: %w", err)
			}
			a.ID = u.ID
			a.Login = u.Login
			a.IsSuperAdmin = u.IsSuperAdmin
			a.MustChangePassword = u.MustChangePassword
			a.OwnerKind = auth.OwnerKindUser
		case "project":
			if k.OwnerProjectID == nil {
				return auth.Actor{}, errors.New("oci: api_key missing owner project")
			}
			pid := *k.OwnerProjectID
			a.ProjectScope = &pid
			a.OwnerKind = auth.OwnerKindProject
		default:
			return auth.Actor{}, fmt.Errorf("oci: unknown api_key owner_kind %q", k.OwnerKind)
		}
		return a, nil
	default:
		return auth.Actor{}, fmt.Errorf("oci: unknown actor kind %q", kind)
	}
}
