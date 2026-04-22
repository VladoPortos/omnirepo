package auth

import "context"

// MembershipLister is the narrow read surface ResolveMembership needs from
// the metadata layer. internal/metadata.MembersRepo satisfies it
// structurally; tests can stub it without pulling the DB.
type MembershipLister interface {
	ListProjectIDsForUser(ctx context.Context, userID int64) ([]int64, error)
}

// ResolveMembership returns ctx annotated with the set of project ids
// this actor is a member of, as Can's isMemberOfProject consumes.
//
// Before F-05.1 each protocol handler open-coded membership resolution with
// the same two-branch pattern:
//
//	if actor.Kind == ActorKindUser                → look up by actor.ID
//	if actor.Kind == ActorKindAPIKey && ProjectScope != nil → singleton
//
// which silently dropped the user-owned-API-key case: Actor.ID is already
// the owning user's id (per the doc comment on Actor.ID), so that path
// should resolve exactly like ActorKindUser. Nine call sites missed it,
// breaking `docker login -u alice -p <user-api-key>` + push/pull against
// every protocol. This helper closes that gap once.
//
// Shape dispatch:
//
//	ActorKindUser                       → members.ListProjectIDsForUser(actor.ID)
//	ActorKindAPIKey + ProjectScope set  → singleton {*ProjectScope}
//	ActorKindAPIKey + OwnerKindUser     → members.ListProjectIDsForUser(actor.ID)
//	anything else                       → ctx unchanged (deny-conservative)
//
// A members lookup error is swallowed on purpose: the membership set stays
// absent from ctx, so isMemberOfProject returns false and Can denies. That
// matches the pre-F-05.1 contract (handlers wrote `if err == nil`).
func ResolveMembership(ctx context.Context, actor Actor, members MembershipLister) context.Context {
	switch actor.Kind {
	case ActorKindUser:
		if members == nil || actor.ID == 0 {
			return ctx
		}
		ids, err := members.ListProjectIDsForUser(ctx, actor.ID)
		if err != nil {
			return ctx
		}
		return WithProjectMembership(ctx, ids)
	case ActorKindAPIKey:
		if actor.ProjectScope != nil {
			return WithProjectMembership(ctx, []int64{*actor.ProjectScope})
		}
		if actor.OwnerKind == OwnerKindUser && members != nil && actor.ID != 0 {
			ids, err := members.ListProjectIDsForUser(ctx, actor.ID)
			if err != nil {
				return ctx
			}
			return WithProjectMembership(ctx, ids)
		}
	}
	return ctx
}
