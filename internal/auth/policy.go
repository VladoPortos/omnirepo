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

	// v1.5 Phase 1: DEV-only test state reset (CONTEXT D-02 belt-and-suspenders).
	// Super-admin only; also gated by OMNIREPO_DEV=1 at mount time in
	// internal/api/admin_reset.go so production binaries never expose the route.
	ActionResetState Action = "e2e.reset"

	// Phase 03 Plan 01 — per-protocol package uploads (D-34). These collapse
	// to the same member-or-super-admin branch as ActionRepoRead/Write: a
	// caller may upload RPM/APT/PyPI/Helm artifacts only if they're a member
	// of the target repo's project (or super-admin). Anonymous callers are
	// rejected at the top of Can via ReasonRequiresAuth.
	ActionRPMUpload  Action = "rpm.upload"
	ActionDEBUpload  Action = "deb.upload"
	ActionPyPIUpload Action = "pypi.upload"
	ActionHelmUpload Action = "helm.upload"

	// Phase 04 Plan 05 — S3 bucket permission actions (D-07).
	// Read/Write require project membership; Admin requires super-admin.
	// For S3-key actors (ActorKindS3Key), membership is implied when the
	// key's ProjectScope matches the target bucket's ProjectID.
	ActionS3BucketRead  Action = "s3:bucket:read"
	ActionS3BucketWrite Action = "s3:bucket:write"
	ActionS3BucketAdmin Action = "s3:bucket:admin"

	// Phase 04 Plan 05 — S3 access-key management (project-scoped).
	// Create/list/revoke S3 keys within a project. Same membership
	// semantics as ActionManageUpstreamCreds.
	ActionManageS3Keys Action = "s3_key.manage"

	// D-1 — Project-owned API key management (project-scoped). Same
	// member-or-super-admin policy as ManageS3Keys today, but a distinct
	// action so a future S3-keys policy tweak can't silently widen or
	// narrow API-token management. Per Codex review on D-1.
	ActionManageProjectAPIKeys Action = "project_api_key.manage"

	// Phase 04 Plan 09 — Git repo permission actions (D-30).
	// Read/Write both require project membership (flat model) or super-admin.
	// Project-scoped API keys count as members of their bound project.
	ActionGitRepoRead  Action = "git:repo:read"
	ActionGitRepoWrite Action = "git:repo:write"

	// v1.5 Phase 2 — role-change action (D-15). Distinct from
	// ActionAddProjectMember so a future policy tweak can't silently widen.
	ActionChangeProjectMemberRole Action = "project.member.role.change"
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
	ActionResetState, // v1.5 Phase 1
	ActionRPMUpload,
	ActionDEBUpload,
	ActionPyPIUpload,
	ActionHelmUpload,
	ActionS3BucketRead,
	ActionS3BucketWrite,
	ActionS3BucketAdmin,
	ActionManageS3Keys,
	ActionManageProjectAPIKeys,
	ActionGitRepoRead,
	ActionGitRepoWrite,
	ActionChangeProjectMemberRole, // v1.5 Phase 2
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
	// v1.5 Phase 2 — distinct from ReasonNotAProjectMember: a viewer IS a member
	// but is denied for role. UI reads this to render "maintainer role required"
	// copy rather than "you are not a member".
	ReasonNotAMaintainer      = "not_a_maintainer"
	ReasonNotSelf             = "not_self"
	ReasonUnknownAction       = "unknown_action"
	ReasonRequiresAuth        = "requires_auth"
	ReasonAnonymousPublicRead = "anonymous_public_read"
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

// WithProjectMembership returns ctx annotated with the (project_id → role)
// map for this actor. Pass a non-nil map (possibly empty).
//
// v1.5 Phase 2: signature changed from []int64 to map[int64]string to carry
// the actor's role (maintainer|viewer) alongside membership. Read with
// memberRoleOfProject.
func WithProjectMembership(ctx context.Context, projectRoles map[int64]string) context.Context {
	return context.WithValue(ctx, membershipCtxKey{}, projectRoles)
}

// memberRoleOfProject returns the actor's role in projectID and whether they
// are a member. Returns ("", false) when the project is not in the map or
// the ctx value is absent.
func memberRoleOfProject(ctx context.Context, projectID int64) (role string, member bool) {
	v := ctx.Value(membershipCtxKey{})
	if v == nil {
		return "", false
	}
	m, ok := v.(map[int64]string)
	if !ok {
		return "", false
	}
	role, member = m[projectID]
	return
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
	// to change; the MCP gate is undefined for them. Only read actions on a
	// target flagged PublicRead are ever allowed; every other action
	// returns "requires_auth" so the chi middleware can 401 and trigger
	// the Bearer challenge.
	//
	// Both the generic ActionRepoRead (protocol-neutral read used by deb /
	// rpm / pypi / helm / raw / docker) and ActionGitRepoRead (the
	// git-specific Smart-HTTP upload-pack) pass through this gate — `git
	// clone` against a public_read=true repo must succeed anonymously.
	if actor.Kind == ActorKindAnonymous {
		if target.Kind == "repo" && target.PublicRead &&
			(action == ActionRepoRead || action == ActionGitRepoRead) {
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
		ActionTriggerGC,
		ActionResetState:
		// Only super-admins; we already returned above if actor was one.
		return false, ReasonSuperAdminRequired

	// maintainer-required branch (D-20): 15 write actions (excluding S3BucketWrite
	// which has its own case below due to the S3-key actor bypass).
	// Viewers (IS a member, role != "maintainer") get ReasonNotAMaintainer.
	// Non-members (absent from map) get ReasonNotAProjectMember.
	case ActionCreateRepo, ActionDeleteRepo,
		ActionUpdateRepo, ActionWipeRepo,
		ActionAddProjectMember, ActionRemoveProjectMember,
		ActionChangeProjectMemberRole,
		ActionManageUpstreamCreds, ActionManageS3Keys,
		ActionManageProjectAPIKeys,
		ActionRPMUpload, ActionDEBUpload,
		ActionPyPIUpload, ActionHelmUpload,
		ActionGitRepoWrite:
		role, member := memberRoleOfProject(ctx, target.ProjectID)
		if !member {
			return false, ReasonNotAProjectMember
		}
		if role != "maintainer" {
			return false, ReasonNotAMaintainer
		}
		return true, ""

	// Phase 04 Plan 05 — S3 bucket admin (D-07).
	// Super-admin already handled above in step 2.
	case ActionS3BucketAdmin:
		return false, ReasonSuperAdminRequired

	// S3 read/write: S3-key actors use their project scope directly (D-08).
	// For session/API-key actors: read is member-any-role; write requires maintainer.
	case ActionS3BucketRead:
		if actor.Kind == ActorKindS3Key && actor.ProjectScope != nil {
			if target.ProjectID != 0 && *actor.ProjectScope == target.ProjectID {
				return true, ""
			}
			return false, ReasonNotAProjectMember
		}
		if target.ProjectID != 0 {
			if _, member := memberRoleOfProject(ctx, target.ProjectID); member {
				return true, ""
			}
		}
		return false, ReasonNotAProjectMember

	case ActionS3BucketWrite:
		// S3-key actors are implicitly maintainer for their bound project.
		if actor.Kind == ActorKindS3Key && actor.ProjectScope != nil {
			if target.ProjectID != 0 && *actor.ProjectScope == target.ProjectID {
				return true, ""
			}
			return false, ReasonNotAProjectMember
		}
		// Session/API-key actors: write requires maintainer role (D-20).
		role, member := memberRoleOfProject(ctx, target.ProjectID)
		if !member {
			return false, ReasonNotAProjectMember
		}
		if role != "maintainer" {
			return false, ReasonNotAMaintainer
		}
		return true, ""

	// member-any-role: Git read (viewers allowed).
	case ActionGitRepoRead:
		if target.ProjectID != 0 {
			if _, member := memberRoleOfProject(ctx, target.ProjectID); member {
				return true, ""
			}
		}
		return false, ReasonNotAProjectMember

	// member-any-role: generic repo read (viewers allowed).
	// Anonymous actors are already handled at the top of Can (step 0).
	// A repo target with PublicRead=true is readable by any authenticated actor.
	case ActionRepoRead:
		if target.PublicRead {
			return true, ""
		}
		if target.ProjectID != 0 {
			if _, member := memberRoleOfProject(ctx, target.ProjectID); member {
				return true, ""
			}
		}
		return false, ReasonNotAProjectMember

	default:
		return false, ReasonUnknownAction
	}
}
