package api

import (
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
)

// SetActor populates e.ActorUserID and e.ActorAPIKeyID from actor, choosing
// the correct nullable-column combination per the v1.6 audit-attribution
// rules (REQUIREMENTS.md AUDITATTR-01..03; CONTEXT.md D-01). It is the
// single decision point for "how does this actor land in audit_log?" so
// every state-changing handler can share one mapping rather than reinvent
// it (and reintroduce the project-owned-API-key bug — audit finding #7).
//
// Mapping:
//
//   - actor.Kind == ActorKindUser
//     → ActorUserID = &actor.ID, ActorAPIKeyID = nil.
//   - actor.Kind == ActorKindAPIKey, actor.OwnerKind == OwnerKindUser
//     → ActorUserID = &actor.ID (the owning user),
//     ActorAPIKeyID = &actor.APIKeyID.
//   - actor.Kind == ActorKindAPIKey, actor.OwnerKind == OwnerKindProject
//     → ActorUserID = nil (NEVER 0 — that is the FK violation case
//     the helper exists to prevent), ActorAPIKeyID = &actor.APIKeyID.
//   - actor.Kind == ActorKindS3Key
//     → both nil. Phase 2's Actor.S3KeyID is not surfaced in audit_log
//     columns yet (deferred to v1.7+; CONTEXT.md "Deferred Ideas").
//   - actor.Kind == ActorKindAnonymous (or zero value)
//     → both nil; the existing audit-event semantics for unauthenticated
//     flows (e.g. EvtAuthLoginFailure) are preserved.
//
// SetActor is mutating: it writes through *e and leaves every other field
// unchanged. The signature is `(*audit.Event, auth.Actor)` rather than
// `(audit.Event, auth.Actor) audit.Event` because callers build Events
// incrementally and the in-place form composes cleanly with the existing
// `d.recordAudit(r, audit.Event{...})` call shape (CONTEXT.md D-02).
//
// Package placement: CONTEXT.md D-01 authorized either `internal/audit/`
// or `internal/api/`. The audit package cannot import internal/auth
// without creating a build-time import cycle
// (audit → auth → httpx → audit/sync_rest.go), so the helper lives here
// in the api package. Plan 03-02's call-site migration consumes
// `api.SetActor` directly via the same-package `recordAuditAs` wrapper.
func SetActor(e *audit.Event, actor auth.Actor) {
	// Take a local copy of actor.ID / actor.APIKeyID before taking a
	// pointer — modernc.org/sqlite binds *int64 directly and we must
	// not capture parameter address sharing.
	switch actor.Kind {
	case auth.ActorKindUser:
		uid := actor.ID
		e.ActorUserID = &uid
		e.ActorAPIKeyID = nil
	case auth.ActorKindAPIKey:
		switch actor.OwnerKind {
		case auth.OwnerKindUser:
			uid := actor.ID
			kid := actor.APIKeyID
			e.ActorUserID = &uid
			e.ActorAPIKeyID = &kid
		case auth.OwnerKindProject:
			// CRITICAL: never write &0 here — that's the FK violation
			// against users(id) that audit finding #7 documented.
			kid := actor.APIKeyID
			e.ActorUserID = nil
			e.ActorAPIKeyID = &kid
		default:
			// Defensive: an APIKey actor with no OwnerKind is a
			// middleware bug. Leave both nil so the row is recognizable
			// as malformed in post-mortem rather than silently mis-attributing.
			e.ActorUserID = nil
			e.ActorAPIKeyID = nil
		}
	default:
		// ActorKindS3Key, ActorKindAnonymous, and the zero value all
		// collapse to "no user/api-key attribution." S3 SigV4 keys may
		// get their own column in v1.7+ (CONTEXT.md Deferred Ideas).
		e.ActorUserID = nil
		e.ActorAPIKeyID = nil
	}
}
