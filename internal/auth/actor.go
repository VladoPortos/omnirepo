// Package auth is OmniRepo's authentication and authorization substrate.
//
// It defines the Actor type (who is making the request), password hashing
// (argon2id per D-16), API-key generation/parsing/verification (omr_<u|p>_<28>
// per D-17), opaque session tokens (base64url of 32 random bytes, D-18), the
// single Can(actor, action, target) policy engine with the must_change_password
// short-circuit (pitfall P5), and validators for project/login names that
// reject reserved prefixes (FOUND-10).
//
// Middleware living in internal/auth/middleware wires these together onto the
// chi router.
package auth

import "context"

// ActorKind distinguishes how an Actor authenticated to this request.
type ActorKind string

const (
	ActorKindUser      ActorKind = "user"
	ActorKindAPIKey    ActorKind = "api_key"
	ActorKindAnonymous ActorKind = "anonymous"

	// ActorKindS3Key identifies an actor that authenticated via AWS SigV4
	// against an s3_access_keys row (Phase 04 Plan 05, D-08). Always
	// project-scoped — ProjectScope MUST be set and pins every bucket check
	// to the project that owns the AKID.
	ActorKindS3Key ActorKind = "s3_key"
)

// OwnerKind is the owner class of an API key (D-17): either a user or a
// project. For session-authenticated actors, this is irrelevant and left empty.
type OwnerKind string

const (
	OwnerKindUser    OwnerKind = "user"
	OwnerKindProject OwnerKind = "project"
)

// Actor is the authenticated principal threaded through the request context.
// For API-key actors, ID is the owning user's id (for user keys) — or zero
// when the key is project-owned (in which case ProjectScope is set).
type Actor struct {
	// ID is users.id when Kind == ActorKindUser, or the owning user for
	// user-owned API keys. For project-owned API keys it is 0.
	ID int64

	// Login is users.login for user-scoped actors; empty for project-owned
	// API keys.
	Login string

	// Kind records the authentication path (session/user vs API key).
	Kind ActorKind

	// APIKeyID is non-zero only when Kind == ActorKindAPIKey.
	APIKeyID int64

	// OwnerKind is only meaningful when Kind == ActorKindAPIKey.
	OwnerKind OwnerKind

	// ProjectScope is non-nil for project-owned API keys. It carries the
	// projects.id that scoped this actor. Nil otherwise.
	ProjectScope *int64

	// IsSuperAdmin mirrors users.is_super_admin at auth time. (TEN-01)
	IsSuperAdmin bool

	// MustChangePassword mirrors users.must_change_password at auth time.
	// Pitfall P5: Can() short-circuits every action except
	// ActionChangeOwnPassword / ActionLogout when this is true.
	MustChangePassword bool
}

// ctxKey is the unexported context key for Actor.
type ctxKey struct{}

// WithActor returns ctx annotated with a. Middlewares call this after
// successful authentication.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// ActorFromContext extracts the Actor stashed by WithActor. Returns the zero
// Actor and false when absent.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	v := ctx.Value(ctxKey{})
	if v == nil {
		return Actor{}, false
	}
	a, ok := v.(Actor)
	if !ok {
		return Actor{}, false
	}
	return a, true
}
