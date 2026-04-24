package auth_test

import (
	"context"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

func TestAnonymousCanReadPublicRepo(t *testing.T) {
	anon := auth.Actor{Kind: auth.ActorKindAnonymous}
	target := auth.Target{Kind: "repo", RepoID: 7, PublicRead: true}
	ok, reason := auth.Can(context.Background(), anon, auth.ActionRepoRead, target)
	if !ok || reason != auth.ReasonAnonymousPublicRead {
		t.Fatalf("anonymous + repo.read + PublicRead=true: got (%v, %q); want (true, %q)",
			ok, reason, auth.ReasonAnonymousPublicRead)
	}
}

func TestAnonymousDeniedOnPrivateRepo(t *testing.T) {
	anon := auth.Actor{Kind: auth.ActorKindAnonymous}
	target := auth.Target{Kind: "repo", RepoID: 7, PublicRead: false}
	ok, reason := auth.Can(context.Background(), anon, auth.ActionRepoRead, target)
	if ok || reason != auth.ReasonRequiresAuth {
		t.Fatalf("anonymous + repo.read + PublicRead=false: got (%v, %q); want (false, %q)",
			ok, reason, auth.ReasonRequiresAuth)
	}
}

func TestAnonymousDeniedOnNonRepoTarget(t *testing.T) {
	anon := auth.Actor{Kind: auth.ActorKindAnonymous}
	// PublicRead=true on a non-repo target must NOT open a back-door.
	target := auth.Target{Kind: "project", ProjectID: 9, PublicRead: true}
	ok, reason := auth.Can(context.Background(), anon, auth.ActionRepoRead, target)
	if ok || reason != auth.ReasonRequiresAuth {
		t.Fatalf("anonymous + repo.read + non-repo target: got (%v, %q); want (false, %q)",
			ok, reason, auth.ReasonRequiresAuth)
	}
}

func TestAnonymousDeniedOnNonReadActions(t *testing.T) {
	anon := auth.Actor{Kind: auth.ActorKindAnonymous}
	target := auth.Target{Kind: "repo", RepoID: 7, PublicRead: true}
	// Iterate every non-read action and assert denial with requires_auth.
	// ActionRepoRead and ActionGitRepoRead both cover public-read clone and
	// must pass when target.PublicRead=true — skip both in the denial loop.
	for _, action := range auth.AllActions {
		if action == auth.ActionRepoRead || action == auth.ActionGitRepoRead {
			continue
		}
		ok, reason := auth.Can(context.Background(), anon, action, target)
		if ok || reason != auth.ReasonRequiresAuth {
			t.Errorf("anonymous + %s: got (%v, %q); want (false, %q)",
				action, ok, reason, auth.ReasonRequiresAuth)
		}
	}
}

func TestAnonymousGitRepoReadAllowedOnPublicRepo(t *testing.T) {
	anon := auth.Actor{Kind: auth.ActorKindAnonymous}
	pub := auth.Target{Kind: "repo", RepoID: 7, PublicRead: true}
	if ok, reason := auth.Can(context.Background(), anon, auth.ActionGitRepoRead, pub); !ok ||
		reason != auth.ReasonAnonymousPublicRead {
		t.Fatalf("anonymous + git:repo:read + PublicRead=true: got (%v, %q); want (true, %q)",
			ok, reason, auth.ReasonAnonymousPublicRead)
	}
	priv := auth.Target{Kind: "repo", RepoID: 7, PublicRead: false}
	if ok, reason := auth.Can(context.Background(), anon, auth.ActionGitRepoRead, priv); ok ||
		reason != auth.ReasonRequiresAuth {
		t.Fatalf("anonymous + git:repo:read + PublicRead=false: got (%v, %q); want (false, %q)",
			ok, reason, auth.ReasonRequiresAuth)
	}
}

func TestAnonymousMCPShortCircuitNotReached(t *testing.T) {
	// An anonymous actor has MustChangePassword=false by construction; but
	// the branch ordering must be explicit so the MCP check never runs for
	// an anonymous actor. We assert by setting MCP=true AND Kind=anonymous
	// (a pathological case) — the anonymous branch must still win.
	anon := auth.Actor{Kind: auth.ActorKindAnonymous, MustChangePassword: true}
	target := auth.Target{Kind: "repo", PublicRead: true}
	ok, reason := auth.Can(context.Background(), anon, auth.ActionRepoRead, target)
	if !ok || reason != auth.ReasonAnonymousPublicRead {
		t.Fatalf("anonymous ordering: got (%v, %q); want (true, %q)",
			ok, reason, auth.ReasonAnonymousPublicRead)
	}
}

func TestAuthenticatedMemberCanReadRepo(t *testing.T) {
	user := auth.Actor{ID: 3, Login: "bob"}
	ctx := auth.WithProjectMembership(context.Background(), map[int64]string{10: "maintainer"})
	target := auth.Target{Kind: "repo", ProjectID: 10, RepoID: 5}
	ok, reason := auth.Can(ctx, user, auth.ActionRepoRead, target)
	if !ok || reason != "" {
		t.Fatalf("member + repo.read: got (%v, %q); want (true, \"\")", ok, reason)
	}
}

func TestAuthenticatedNonMemberCanReadPublicRepo(t *testing.T) {
	user := auth.Actor{ID: 3, Login: "bob"}
	ctx := context.Background() // no membership
	target := auth.Target{Kind: "repo", ProjectID: 10, RepoID: 5, PublicRead: true}
	ok, reason := auth.Can(ctx, user, auth.ActionRepoRead, target)
	if !ok || reason != "" {
		t.Fatalf("non-member + public repo.read: got (%v, %q); want (true, \"\")", ok, reason)
	}
}

func TestAuthenticatedNonMemberDeniedPrivateRepo(t *testing.T) {
	user := auth.Actor{ID: 3, Login: "bob"}
	ctx := context.Background() // no membership
	target := auth.Target{Kind: "repo", ProjectID: 10, RepoID: 5, PublicRead: false}
	ok, reason := auth.Can(ctx, user, auth.ActionRepoRead, target)
	if ok || reason != auth.ReasonNotAProjectMember {
		t.Fatalf("non-member + private repo.read: got (%v, %q); want (false, %q)",
			ok, reason, auth.ReasonNotAProjectMember)
	}
}
