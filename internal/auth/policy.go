package auth

import "context"

// Action is the lookup key for the policy table. Every state-changing code
// path MUST consult Can(actor, action, target) and either allow (true, "") or
// return the (false, reason) phrase to the caller as the audit-log outcome
// and the 403 body's error field.
type Action string

const (
	ActionLogin               Action = "auth.login"
	ActionLogout              Action = "auth.logout"
	ActionChangeOwnPassword   Action = "auth.change_own_password"
	ActionCreateUser          Action = "user.create"
	ActionDeleteUser          Action = "user.delete"
	ActionEditOwnUser         Action = "user.edit_own"
	ActionDeleteOwnUser       Action = "user.delete_own"
	ActionCreateProject       Action = "project.create"
	ActionDeleteProject       Action = "project.delete"
	ActionAddProjectMember    Action = "project.member.add"
	ActionRemoveProjectMember Action = "project.member.remove"
	ActionCreateRepo          Action = "repo.create"
	ActionDeleteRepo          Action = "repo.delete"
	ActionUpdateRepo          Action = "repo.update"
	ActionWipeRepo            Action = "repo.wipe"
	ActionUploadTLSCert       Action = "tls.upload"
	ActionApplyBootstrap      Action = "bootstrap.apply"

	// Phase 02-02: project-scoped upstream credential management (D-11).
	// Read/list/write all gated by project membership; no secret is ever
	// returned via these endpoints — Lookup happens only server-side at
	// pull-external time.
	ActionManageUpstreamCreds Action = "upstream_cred.manage"

	// Phase 02-05: repo read (D-32, D-33, REPO-09). The only action an
	// anonymous actor may ever be allowed — and only when the target repo's
	// PublicRead is true. Authenticated actors reach it via project
	// membership (or super-admin bypass).
	ActionRepoRead Action = "repo.read"

	// Phase 02-12: admin-triggered garbage collection (D-37, OPS-06).
	// Super-admin only — falls into the same per-action table branch as
	// ActionUploadTLSCert / ActionApplyBootstrap (super-admin returns above
	// in step 2; everyone else gets ReasonSuperAdminRequired).
	ActionTriggerGC Action = "gc.trigger"
)

// AllActions enumerates every Action constant in the package. Downstream
// tests (the P5 matrix in particular) iterate this slice to prove every
// action observes the must_change_password short-circuit.
var AllActions = []Action{
	ActionLogin,
	ActionLogout,
	ActionChangeOwnPassword,
	ActionCreateUser,
	ActionDeleteUser,
	ActionEditOwnUser,
	ActionDeleteOwnUser,
	ActionCreateProject,
	ActionDeleteProject,
	ActionAddProjectMember,
	ActionRemoveProjectMember,
	ActionCreateRepo,
	ActionDeleteRepo,
	ActionUpdateRepo,
	ActionWipeRepo,
	ActionUploadTLSCert,
	ActionApplyBootstrap,
	ActionManageUpstreamCreds,
	ActionRepoRead,
	ActionTriggerGC,
}

// Target is the object the actor is operating on.
// Kind is one of "project" | "repo" | "user" | "global" | "" (login/logout).
//
// PublicRead is only meaningful when Kind == "repo"; it mirrors
// repos.public_read at the time the caller built the Target and feeds the
// anonymous-read branch of Can (D-33). Every non-repo target should leave it
// at its zero value (false) and anonymous actors will be denied uniformly.
type Target struct {
	Kind       string
	ProjectID  int64
	UserID     int64
	RepoID     int64
	PublicRead bool
}

// Reason strings returned by Can when it denies. These double as audit-log
// outcome tokens, so they are stable package constants.
const (
	ReasonPasswordChangeRequired = "password-change-required"
	ReasonSuperAdminRequired     = "super_admin_required"
	ReasonNotAProjectMember      = "not_a_project_member"
	ReasonNotSelf                = "not_self"
	ReasonUnknownAction          = "unknown_action"
	ReasonRequiresAuth           = "requires_auth"
	ReasonAnonymousPublicRead    = "anonymous_public_read"
)

// membershipCtxKey is the unexported ctx key for stashing a membership set.
// Middlewares / handlers populate this BEFORE calling Can for project-scoped
// actions. The policy engine stays pure (no DB access) by reading the set
// out of ctx — this keeps Can synchronous and trivially unit-testable.
//
// Plan 05's handlers resolve membership via metadata.MembersRepo (to be
// introduced in 01-05) and then call WithProjectMembership(ctx, projectIDs)
// immediately before dispatching to Can.
type membershipCtxKey struct{}

// WithProjectMembership returns ctx annotated with the projects that the
// current actor is a member of. Pass a non-nil slice (possibly empty).
func WithProjectMembership(ctx context.Context, projectIDs []int64) context.Context {
	set := make(map[int64]struct{}, len(projectIDs))
	for _, id := range projectIDs {
		set[id] = struct{}{}
	}
	return context.WithValue(ctx, membershipCtxKey{}, set)
}

// isMemberOfProject returns true when ctx carries a membership set that
// contains projectID. Returns false if the set is absent or does not contain
// projectID. The "absent set" case means Can denies conservatively.
func isMemberOfProject(ctx context.Context, projectID int64) bool {
	v := ctx.Value(membershipCtxKey{})
	if v == nil {
		return false
	}
	set, ok := v.(map[int64]struct{})
	if !ok {
		return false
	}
	_, member := set[projectID]
	return member
}

// Can returns (allowed, reason).
//
// Evaluation order (pitfall P5 + TEN-13):
//
//  1. If actor.MustChangePassword is true, return
//     (false, "password-change-required") for every action EXCEPT
//     ActionChangeOwnPassword and ActionLogout. This is the single line of
//     code that prevents the UI-redirect-only bypass that's broken in a
//     million password flows.
//  2. If actor.IsSuperAdmin is true, return (true, ""). Super-admin bypasses
//     every authz check (TEN-01). (Step 1 still runs first, so a super-admin
//     flagged must_change_password still sees the change-password wall.)
//  3. Otherwise, consult the per-action table below.
//
// For project-scoped actions (repo create/delete, member add/remove), Can
// reads the membership set out of ctx (see WithProjectMembership). Handlers
// populate it before dispatching.
func Can(ctx context.Context, actor Actor, action Action, target Target) (bool, string) {
	// 0. Anonymous-actor short-circuit (D-33, REPO-09). Must precede the
	// must_change_password check because anonymous actors have no password
	// to change; the MCP gate is undefined for them. Only repo.read on a
	// target flagged PublicRead is ever allowed; every other action
	// returns "requires_auth" so the chi middleware can 401 and trigger
	// the Bearer challenge.
	if actor.Kind == ActorKindAnonymous {
		if action == ActionRepoRead && target.Kind == "repo" && target.PublicRead {
			return true, ReasonAnonymousPublicRead
		}
		return false, ReasonRequiresAuth
	}

	// 1. MustChangePassword short-circuit (P5 mitigation).
	if actor.MustChangePassword {
		switch action {
		case ActionChangeOwnPassword, ActionLogout:
			// fall through
		default:
			return false, ReasonPasswordChangeRequired
		}
	}

	// 2. Super-admin bypass (TEN-01).
	if actor.IsSuperAdmin {
		return true, ""
	}

	// 3. Per-action table.
	switch action {
	case ActionLogin, ActionLogout:
		// Gate for these is the credential itself, not policy.
		return true, ""

	case ActionChangeOwnPassword, ActionEditOwnUser, ActionDeleteOwnUser:
		if target.UserID == actor.ID && target.UserID != 0 {
			return true, ""
		}
		return false, ReasonNotSelf

	case ActionCreateUser, ActionDeleteUser,
		ActionCreateProject, ActionDeleteProject,
		ActionUploadTLSCert, ActionApplyBootstrap,
		ActionTriggerGC:
		// Only super-admins; we already returned above if actor was one.
		return false, ReasonSuperAdminRequired

	case ActionCreateRepo, ActionDeleteRepo,
		ActionUpdateRepo, ActionWipeRepo,
		ActionAddProjectMember, ActionRemoveProjectMember,
		ActionManageUpstreamCreds:
		if target.ProjectID != 0 && isMemberOfProject(ctx, target.ProjectID) {
			return true, ""
		}
		return false, ReasonNotAProjectMember

	case ActionRepoRead:
		// Authenticated project members may always read their repos.
		// Anonymous actors are already handled at the top of Can (step 0).
		// A repo target with PublicRead=true is also readable by any
		// authenticated actor (whether they're a member or not) — the
		// flag is a superset permission, not a restriction.
		if target.PublicRead {
			return true, ""
		}
		if target.ProjectID != 0 && isMemberOfProject(ctx, target.ProjectID) {
			return true, ""
		}
		return false, ReasonNotAProjectMember

	default:
		return false, ReasonUnknownAction
	}
}
