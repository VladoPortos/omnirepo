// Package auth is OmniRepo's authentication and authorization substrate.
//
// It defines the Actor type (who is making the request), password hashing
// (argon2id), API-key generation/parsing/verification (omr_<u|p>_<28>), opaque
// session tokens (base64url of 32 random bytes), the single
// Can(actor, action, target) policy engine with the must_change_password
// short-circuit, and validators for project/login names that reject reserved
// prefixes.
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
	// against an s3_access_keys row. Always project-scoped — ProjectScope
	// MUST be set and pins every bucket check to the project that owns the
	// AKID.
	ActorKindS3Key ActorKind = "s3_key"
)

// OwnerKind is the owner class of an API key: either a user or a project.
// For session-authenticated actors, this is irrelevant and left empty.
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

	// IsSuperAdmin mirrors users.is_super_admin at auth time.
	IsSuperAdmin bool

	// MustChangePassword mirrors users.must_change_password at auth time.
	// When this is true, Can() short-circuits every action except
	// ActionChangeOwnPassword / ActionLogout.
	MustChangePassword bool

	// APIKeyRole is the minted role for project-scoped API keys.
	// Set by the auth middleware when loading the api_keys row (non-empty only
	// when Kind == ActorKindAPIKey and ProjectScope != nil). Empty for user-owned
	// keys and user actors — their role derives from project_members at request
	// time via ResolveMembership.
	APIKeyRole string

	// S3KeyID is non-nil only when Kind == ActorKindS3Key. It carries the
	// resolved s3_access_keys.id stamped by the SigV4 middleware after a
	// successful verify, so downstream handlers (multipart attribution) can
	// record which key initiated a request without re-querying. Other
	// authentication middlewares (session, API key) leave this nil.
	S3KeyID *int64
}

// ctxKey is the unexported context key for Actor.
type ctxKey struct{}

// loginBoxKey stashes a *LoginBox on the context. Outer middlewares (e.g.
// StructuredLogger) seed one on entry; auth middlewares update it via
// WithActor so the log record can carry the authenticated login even
// though the outer handler never sees the inner r.WithContext chain.
type loginBoxKey struct{}

// LoginBox is a tiny mutable holder for the authenticated login. The
// outer StructuredLogger middleware attaches a pointer to one via
// WithLoginBox; every call to WithActor that follows updates it, so the
// log record at request exit can read the final login without knowing the
// inner middleware chain. Anonymous actors leave it empty.
type LoginBox struct {
	// Login is the actor's login. Concurrent access is not expected —
	// the box is scoped to a single request and Go's HTTP server serves
	// each request on one goroutine.
	Login string
}

// WithLoginBox returns ctx annotated with box. StructuredLogger is the
// sole caller in production; tests may also seed one to assert that the
// login propagates through their chains.
func WithLoginBox(ctx context.Context, box *LoginBox) context.Context {
	return context.WithValue(ctx, loginBoxKey{}, box)
}

// WithActor returns ctx annotated with a. Middlewares call this after
// successful authentication. If ctx carries a *LoginBox (attached by the
// outer StructuredLogger), its Login field is updated so request logs
// pick up the authenticated login automatically.
func WithActor(ctx context.Context, a Actor) context.Context {
	if box, _ := ctx.Value(loginBoxKey{}).(*LoginBox); box != nil {
		box.Login = a.Login
	}
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

// ActorLoginFromContext is a convenience helper for audit-adjacent call
// sites that only need the login string (e.g. the trash sidecar's
// "deleted_by" field). Returns "" when the context has no actor or when
// the actor is a project-owned API key (no user login).
func ActorLoginFromContext(ctx context.Context) string {
	a, ok := ActorFromContext(ctx)
	if !ok {
		return ""
	}
	return a.Login
}
