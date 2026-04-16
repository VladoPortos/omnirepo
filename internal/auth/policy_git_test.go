package auth_test

import (
	"context"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// Test 7: member can read git repo.
func TestPolicyGitRepoRead_MemberAllowed(t *testing.T) {
	actor := auth.Actor{ID: 10, Kind: auth.ActorKindUser}
	ctx := auth.WithProjectMembership(context.Background(), []int64{42})

	ok, reason := auth.Can(ctx, actor, auth.ActionGitRepoRead, auth.Target{Kind: "repo", ProjectID: 42, RepoID: 1})
	if !ok {
		t.Fatalf("member read denied: %q", reason)
	}
}

// Test 8: member can write git repo.
func TestPolicyGitRepoWrite_MemberAllowed(t *testing.T) {
	actor := auth.Actor{ID: 10, Kind: auth.ActorKindUser}
	ctx := auth.WithProjectMembership(context.Background(), []int64{42})

	ok, reason := auth.Can(ctx, actor, auth.ActionGitRepoWrite, auth.Target{Kind: "repo", ProjectID: 42, RepoID: 1})
	if !ok {
		t.Fatalf("member write denied: %q", reason)
	}
}

// Test 9: non-member denied.
func TestPolicyGitRepoRead_NonMemberDenied(t *testing.T) {
	actor := auth.Actor{ID: 11, Kind: auth.ActorKindUser}
	ctx := auth.WithProjectMembership(context.Background(), []int64{99})

	ok, reason := auth.Can(ctx, actor, auth.ActionGitRepoRead, auth.Target{Kind: "repo", ProjectID: 42, RepoID: 1})
	if ok || reason != auth.ReasonNotAProjectMember {
		t.Fatalf("non-member read ok=%v reason=%q, want false/%s", ok, reason, auth.ReasonNotAProjectMember)
	}
}

// Test 10: project-scoped API key can write in own project.
func TestPolicyGitRepoWrite_ProjectAPIKeySameProject(t *testing.T) {
	pid := int64(42)
	actor := auth.Actor{
		Kind:         auth.ActorKindAPIKey,
		OwnerKind:    auth.OwnerKindProject,
		ProjectScope: &pid,
	}
	ctx := auth.WithProjectMembership(context.Background(), []int64{42})

	ok, reason := auth.Can(ctx, actor, auth.ActionGitRepoWrite, auth.Target{Kind: "repo", ProjectID: 42, RepoID: 1})
	if !ok {
		t.Fatalf("project key write in own project denied: %q", reason)
	}
}

// Test 11: project-scoped API key denied in different project.
func TestPolicyGitRepoWrite_ProjectAPIKeyCrossProject(t *testing.T) {
	pid := int64(99)
	actor := auth.Actor{
		Kind:         auth.ActorKindAPIKey,
		OwnerKind:    auth.OwnerKindProject,
		ProjectScope: &pid,
	}
	ctx := auth.WithProjectMembership(context.Background(), []int64{99})

	ok, reason := auth.Can(ctx, actor, auth.ActionGitRepoWrite, auth.Target{Kind: "repo", ProjectID: 42, RepoID: 1})
	if ok || reason != auth.ReasonNotAProjectMember {
		t.Fatalf("project key cross-project ok=%v reason=%q, want false/%s",
			ok, reason, auth.ReasonNotAProjectMember)
	}
}
