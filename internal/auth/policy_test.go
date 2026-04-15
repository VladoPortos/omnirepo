package auth_test

import (
	"context"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

func TestMustChangePasswordEnforcedAcrossAllActions(t *testing.T) {
	mcpActor := auth.Actor{ID: 1, Login: "alice", MustChangePassword: true}
	saActor := auth.Actor{ID: 2, Login: "root", IsSuperAdmin: true}
	ctx := auth.WithProjectMembership(context.Background(), []int64{42})

	for _, action := range auth.AllActions {
		// MCP user: only ChangeOwnPassword + Logout bypass the wall.
		target := auth.Target{UserID: mcpActor.ID, ProjectID: 42}
		allowed, reason := auth.Can(ctx, mcpActor, action, target)
		switch action {
		case auth.ActionChangeOwnPassword, auth.ActionLogout:
			if !allowed {
				t.Errorf("MCP actor action=%s: denied (reason=%q); want allowed", action, reason)
			}
		default:
			if allowed || reason != auth.ReasonPasswordChangeRequired {
				t.Errorf("MCP actor action=%s: allowed=%v reason=%q; want (false, %q)",
					action, allowed, reason, auth.ReasonPasswordChangeRequired)
			}
		}
		// Super-admin: always allowed when MCP=false.
		allowedSA, reasonSA := auth.Can(ctx, saActor, action, auth.Target{UserID: saActor.ID, ProjectID: 42})
		if !allowedSA || reasonSA != "" {
			t.Errorf("super-admin action=%s: allowed=%v reason=%q; want (true, \"\")",
				action, allowedSA, reasonSA)
		}
	}
}

func TestSuperAdminBypassesEveryAction(t *testing.T) {
	sa := auth.Actor{ID: 1, IsSuperAdmin: true}
	for _, action := range auth.AllActions {
		ok, reason := auth.Can(context.Background(), sa, action, auth.Target{UserID: 1, ProjectID: 99})
		if !ok || reason != "" {
			t.Errorf("action=%s: %v/%q; want true/\"\"", action, ok, reason)
		}
	}
}

func TestNonMemberDeniedProjectScopedActions(t *testing.T) {
	user := auth.Actor{ID: 3, Login: "bob"}
	ctx := context.Background() // no membership set
	for _, action := range []auth.Action{
		auth.ActionCreateRepo, auth.ActionDeleteRepo,
		auth.ActionAddProjectMember, auth.ActionRemoveProjectMember,
	} {
		ok, reason := auth.Can(ctx, user, action, auth.Target{ProjectID: 10})
		if ok || reason != auth.ReasonNotAProjectMember {
			t.Errorf("action=%s non-member: %v/%q; want false/%s",
				action, ok, reason, auth.ReasonNotAProjectMember)
		}
	}
}

func TestMemberAllowedProjectScopedActions(t *testing.T) {
	user := auth.Actor{ID: 3, Login: "bob"}
	ctx := auth.WithProjectMembership(context.Background(), []int64{10})
	for _, action := range []auth.Action{
		auth.ActionCreateRepo, auth.ActionDeleteRepo,
		auth.ActionAddProjectMember, auth.ActionRemoveProjectMember,
	} {
		ok, reason := auth.Can(ctx, user, action, auth.Target{ProjectID: 10})
		if !ok || reason != "" {
			t.Errorf("action=%s member: %v/%q; want true/\"\"", action, ok, reason)
		}
	}
}

func TestSelfScopedActions(t *testing.T) {
	user := auth.Actor{ID: 3}
	ctx := context.Background()
	for _, action := range []auth.Action{
		auth.ActionChangeOwnPassword, auth.ActionEditOwnUser, auth.ActionDeleteOwnUser,
	} {
		ok, reason := auth.Can(ctx, user, action, auth.Target{UserID: 3})
		if !ok || reason != "" {
			t.Errorf("self action=%s: %v/%q; want true/\"\"", action, ok, reason)
		}
		ok, reason = auth.Can(ctx, user, action, auth.Target{UserID: 99})
		if ok || reason != auth.ReasonNotSelf {
			t.Errorf("other action=%s: %v/%q; want false/%s",
				action, ok, reason, auth.ReasonNotSelf)
		}
	}
}

func TestSuperAdminOnlyActionsDeniedToRegularUsers(t *testing.T) {
	user := auth.Actor{ID: 3}
	ctx := context.Background()
	for _, action := range []auth.Action{
		auth.ActionCreateUser, auth.ActionDeleteUser,
		auth.ActionCreateProject, auth.ActionDeleteProject,
		auth.ActionUploadTLSCert, auth.ActionApplyBootstrap,
	} {
		ok, reason := auth.Can(ctx, user, action, auth.Target{})
		if ok || reason != auth.ReasonSuperAdminRequired {
			t.Errorf("action=%s: %v/%q; want false/%s",
				action, ok, reason, auth.ReasonSuperAdminRequired)
		}
	}
}

func TestAPIKeyActorTreatedAsOwner(t *testing.T) {
	// KEY-07: Can treats an api_key actor identically to its owning user.
	// We model this by setting Kind=ActorKindAPIKey but with the same
	// ID/IsSuperAdmin/MustChangePassword as the owning user.
	apiKeySA := auth.Actor{ID: 5, Kind: auth.ActorKindAPIKey, APIKeyID: 77, IsSuperAdmin: true}
	apiKeyMCP := auth.Actor{ID: 6, Kind: auth.ActorKindAPIKey, APIKeyID: 88, MustChangePassword: true}
	ctx := context.Background()

	ok, _ := auth.Can(ctx, apiKeySA, auth.ActionCreateProject, auth.Target{})
	if !ok {
		t.Fatalf("api-key super-admin denied CreateProject")
	}
	ok, reason := auth.Can(ctx, apiKeyMCP, auth.ActionCreateRepo, auth.Target{ProjectID: 1})
	if ok || reason != auth.ReasonPasswordChangeRequired {
		t.Fatalf("api-key MCP CreateRepo: %v/%q; want false/%s",
			ok, reason, auth.ReasonPasswordChangeRequired)
	}
}

func TestUnknownActionDenied(t *testing.T) {
	user := auth.Actor{ID: 1}
	ok, reason := auth.Can(context.Background(), user, auth.Action("frobnicate"), auth.Target{})
	if ok || reason != auth.ReasonUnknownAction {
		t.Fatalf("unknown: %v/%q; want false/%s", ok, reason, auth.ReasonUnknownAction)
	}
}

func TestAllActionsSliceMatchesConstants(t *testing.T) {
	// Sanity check: every Action constant appears in AllActions. The sum of
	// these constants should equal len(AllActions). Phase 3 Plan 01 adds
	// four package-upload actions (RPM/DEB/PyPI/Helm).
	want := 24
	if len(auth.AllActions) != want {
		t.Fatalf("AllActions length: %d, want %d", len(auth.AllActions), want)
	}
	seen := make(map[auth.Action]struct{}, len(auth.AllActions))
	for _, a := range auth.AllActions {
		if _, dup := seen[a]; dup {
			t.Errorf("AllActions duplicate %q", a)
		}
		seen[a] = struct{}{}
	}
}

func TestPackageUploadActionsMemberOnly(t *testing.T) {
	// Phase 3 Plan 01: RPM/DEB/PyPI/Helm uploads follow the same project-
	// membership gate as ActionCreateRepo. Non-member is denied
	// ReasonNotAProjectMember; member is allowed; anonymous is rejected
	// ReasonRequiresAuth.
	member := auth.Actor{ID: 10, Kind: auth.ActorKindUser}
	outsider := auth.Actor{ID: 11, Kind: auth.ActorKindUser}
	anon := auth.Actor{Kind: auth.ActorKindAnonymous}
	ctxMember := auth.WithProjectMembership(context.Background(), []int64{42})
	ctxOutsider := auth.WithProjectMembership(context.Background(), []int64{99})

	for _, action := range []auth.Action{
		auth.ActionRPMUpload,
		auth.ActionDEBUpload,
		auth.ActionPyPIUpload,
		auth.ActionHelmUpload,
	} {
		ok, reason := auth.Can(ctxMember, member, action, auth.Target{Kind: "repo", ProjectID: 42})
		if !ok {
			t.Errorf("%s: member denied (%q), want allow", action, reason)
		}
		ok, reason = auth.Can(ctxOutsider, outsider, action, auth.Target{Kind: "repo", ProjectID: 42})
		if ok || reason != auth.ReasonNotAProjectMember {
			t.Errorf("%s: outsider ok=%v reason=%q, want false/%s", action, ok, reason, auth.ReasonNotAProjectMember)
		}
		ok, reason = auth.Can(context.Background(), anon, action, auth.Target{Kind: "repo", ProjectID: 42})
		if ok || reason != auth.ReasonRequiresAuth {
			t.Errorf("%s: anon ok=%v reason=%q, want false/%s", action, ok, reason, auth.ReasonRequiresAuth)
		}
	}
}
