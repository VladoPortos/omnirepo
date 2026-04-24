package auth

import "context"

// MembershipLister is the narrow read surface ResolveMembership needs from
// the metadata layer. internal/metadata.MembersRepo satisfies it
// structurally; tests can stub it without pulling the DB.
type MembershipLister interface {
	ListProjectIDsForUser(ctx context.Context, userID int64) ([]int64, error)
	// ListProjectRolesForUser returns a map of projectID → role for every
	// non-deleted project the user is a member of. Added in v1.5 Phase 2
	// (RBAC-03) so the policy engine can carry role alongside membership.
	// Plan 03 implements this on MembersRepo; until then the stub in
	// membership_test.go derives roles from ListProjectIDsForUser.
	ListProjectRolesForUser(ctx context.Context, userID int64) (map[int64]string, error)
}

// ResolveMembership returns ctx annotated with the (project_id → role) map
// for this actor, as Can's memberRoleOfProject consumes.
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
// v1.5 Phase 2: now calls ListProjectRolesForUser (returns map[int64]string)
// instead of ListProjectIDsForUser, so the policy engine can distinguish
// viewer vs maintainer role. Project-scoped API keys use actor.APIKeyRole
// (the minted role from api_keys.role); an empty APIKeyRole falls back to
// "maintainer" for legacy/backfilled keys (D-24).
//
// Shape dispatch:
//
//	ActorKindUser                       → members.ListProjectRolesForUser(actor.ID)
//	ActorKindAPIKey + ProjectScope set  → singleton {*ProjectScope: actor.APIKeyRole}
//	ActorKindAPIKey + OwnerKindUser     → members.ListProjectRolesForUser(actor.ID)
//	anything else                       → ctx unchanged (deny-conservative)
//
// A members lookup error is swallowed on purpose: the membership set stays
// absent from ctx, so memberRoleOfProject returns false and Can denies.
func ResolveMembership(ctx context.Context, actor Actor, members MembershipLister) context.Context {
	switch actor.Kind {
	case ActorKindUser:
		if members == nil || actor.ID == 0 {
			return ctx
		}
		roles, err := members.ListProjectRolesForUser(ctx, actor.ID)
		if err != nil {
			return ctx
		}
		return WithProjectMembership(ctx, roles)
	case ActorKindAPIKey:
		if actor.ProjectScope != nil {
			// Project-scoped key: use the key's minted role (D-26).
			// actor.APIKeyRole is populated by auth middleware from api_keys.role.
			role := actor.APIKeyRole
			if role == "" {
				role = "maintainer" // safe fallback for legacy/backfilled keys (D-24)
			}
			return WithProjectMembership(ctx, map[int64]string{*actor.ProjectScope: role})
		}
		if actor.OwnerKind == OwnerKindUser && members != nil && actor.ID != 0 {
			roles, err := members.ListProjectRolesForUser(ctx, actor.ID)
			if err != nil {
				return ctx
			}
			return WithProjectMembership(ctx, roles)
		}
	}
	return ctx
}
