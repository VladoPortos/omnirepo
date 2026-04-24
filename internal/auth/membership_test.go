package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// stubLister captures the userID ResolveMembership asked for and returns a
// canned ([]int64, error) pair.
type stubLister struct {
	seen   int64
	ids    []int64
	err    error
	called bool
}

func (s *stubLister) ListProjectIDsForUser(_ context.Context, userID int64) ([]int64, error) {
	s.called = true
	s.seen = userID
	return s.ids, s.err
}

func (s *stubLister) ListProjectRolesForUser(_ context.Context, userID int64) (map[int64]string, error) {
	s.called = true
	s.seen = userID
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[int64]string, len(s.ids))
	for _, id := range s.ids {
		out[id] = "maintainer" // tests default to maintainer unless a specific test overrides
	}
	return out, nil
}

// memberOf reads the membership set out of ctx via the same path Can uses.
// Since isMemberOfProject is unexported, we observe its behaviour by calling
// auth.Can indirectly — but that's coupling to policy. Instead, pair
// WithProjectMembership → ResolveMembership round-trip by checking that the
// ctx keys compare: re-applying WithProjectMembership with the same ids
// should yield a ctx whose membership set equals what ResolveMembership
// installed. Easier: rely on Can's observable behaviour for a project-scoped
// action (ActionCreateRepo). We do that below with a dedicated pinning test.
func memberOf(t *testing.T, ctx context.Context, actor auth.Actor, projectID int64) bool {
	t.Helper()
	ok, _ := auth.Can(ctx, actor, auth.ActionCreateRepo, auth.Target{
		Kind:      "repo",
		ProjectID: projectID,
	})
	return ok
}

func TestResolveMembership_UserActor_PopulatesFromRepo(t *testing.T) {
	lister := &stubLister{ids: []int64{7, 42}}
	actor := auth.Actor{ID: 5, Kind: auth.ActorKindUser}

	ctx := auth.ResolveMembership(context.Background(), actor, lister)

	if !lister.called {
		t.Fatal("expected members lister to be called")
	}
	if lister.seen != 5 {
		t.Fatalf("wrong user id: got %d want 5", lister.seen)
	}
	if !memberOf(t, ctx, actor, 42) {
		t.Fatal("actor should see project 42 as a member")
	}
	if memberOf(t, ctx, actor, 99) {
		t.Fatal("actor should NOT see project 99 as a member")
	}
}

// Regression coverage for F-05.1: a user-owned API key MUST inherit the
// owning user's project memberships. Before the fix, this branch fell
// through to the deny path because the open-coded pattern only resolved
// memberships for ActorKindUser and project-scoped keys.
func TestResolveMembership_UserOwnedAPIKey_ResolvesViaOwnerID(t *testing.T) {
	lister := &stubLister{ids: []int64{3}}
	actor := auth.Actor{
		ID:        5, // Actor.ID on a user-owned key == owning user id
		Kind:      auth.ActorKindAPIKey,
		APIKeyID:  17,
		OwnerKind: auth.OwnerKindUser,
		// ProjectScope intentionally nil — this is the case that broke
	}

	ctx := auth.ResolveMembership(context.Background(), actor, lister)

	if !lister.called {
		t.Fatal("members lister must be called for user-owned API keys")
	}
	if lister.seen != 5 {
		t.Fatalf("lister got user id %d, want 5 (Actor.ID)", lister.seen)
	}
	if !memberOf(t, ctx, actor, 3) {
		t.Fatal("user-owned API key should see its owner's project 3 as a member (F-05.1)")
	}
}

func TestResolveMembership_ProjectScopedAPIKey_Singleton(t *testing.T) {
	pid := int64(11)
	lister := &stubLister{} // should not be called
	actor := auth.Actor{
		Kind:         auth.ActorKindAPIKey,
		APIKeyID:     22,
		OwnerKind:    auth.OwnerKindProject,
		ProjectScope: &pid,
	}

	ctx := auth.ResolveMembership(context.Background(), actor, lister)

	if lister.called {
		t.Fatal("project-scoped keys must not hit the members table")
	}
	if !memberOf(t, ctx, actor, 11) {
		t.Fatal("project-scoped key should see its bound project as a member")
	}
	if memberOf(t, ctx, actor, 12) {
		t.Fatal("project-scoped key must NOT leak to other projects")
	}
}

func TestResolveMembership_AnonymousActor_NoChange(t *testing.T) {
	lister := &stubLister{}
	actor := auth.Actor{Kind: auth.ActorKindAnonymous}

	ctx := auth.ResolveMembership(context.Background(), actor, lister)
	_ = ctx

	if lister.called {
		t.Fatal("anonymous actors must not hit the members table")
	}
}

func TestResolveMembership_ListerError_DenyConservative(t *testing.T) {
	lister := &stubLister{err: errors.New("db closed")}
	actor := auth.Actor{ID: 5, Kind: auth.ActorKindUser}

	ctx := auth.ResolveMembership(context.Background(), actor, lister)

	// With an erroring lister the membership set must stay absent →
	// Can denies. Use a non-zero projectID the stub WOULD otherwise have
	// granted via ids if there were no error; we rely on the stub's ids
	// being empty by default, so granting would require the absent-set
	// path to mistakenly allow.
	if memberOf(t, ctx, actor, 42) {
		t.Fatal("lister error must deny conservatively, not open the gate")
	}
}

func TestResolveMembership_NilLister_NoChange(t *testing.T) {
	actor := auth.Actor{ID: 5, Kind: auth.ActorKindUser}
	ctx := auth.ResolveMembership(context.Background(), actor, nil)
	if memberOf(t, ctx, actor, 42) {
		t.Fatal("nil lister must deny conservatively")
	}
}
