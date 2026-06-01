package auth_test

import (
	"context"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
)

// maintainerWriteActions is the list of 16 write actions that require
// maintainer role. Viewers and non-members are denied these.
var maintainerWriteActions = []auth.Action{
	auth.ActionCreateRepo, auth.ActionDeleteRepo,
	auth.ActionUpdateRepo, auth.ActionWipeRepo,
	auth.ActionAddProjectMember, auth.ActionRemoveProjectMember,
	auth.ActionChangeProjectMemberRole,
	auth.ActionManageUpstreamCreds, auth.ActionManageS3Keys,
	auth.ActionManageProjectAPIKeys,
	auth.ActionRPMUpload, auth.ActionDEBUpload,
	auth.ActionPyPIUpload, auth.ActionHelmUpload,
	auth.ActionS3BucketWrite,
	auth.ActionGitRepoWrite,
}

// memberReadActions is the list of 3 read actions that any member
// (viewer or maintainer) may perform.
var memberReadActions = []auth.Action{
	auth.ActionRepoRead,
	auth.ActionS3BucketRead,
	auth.ActionGitRepoRead,
}

// TestViewerDeniedWriteActions asserts that a viewer actor (member of project 42
// with role "viewer") is denied every write action with reason
// ReasonNotAMaintainer.
func TestViewerDeniedWriteActions(t *testing.T) {
	t.Parallel()
	viewer := auth.Actor{ID: 10, Kind: auth.ActorKindUser}
	ctxViewer := auth.WithProjectMembership(context.Background(), map[int64]string{42: "viewer"})

	for _, action := range maintainerWriteActions {
		action := action
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			ok, reason := auth.Can(ctxViewer, viewer, action, auth.Target{Kind: "repo", ProjectID: 42})
			if ok || reason != auth.ReasonNotAMaintainer {
				t.Errorf("viewer action=%s: ok=%v reason=%q; want false/%s",
					action, ok, reason, auth.ReasonNotAMaintainer)
			}
		})
	}
}

// TestViewerAllowedReadActions asserts that a viewer actor (member of project 42
// with role "viewer") is allowed every read action. PublicRead is
// explicitly false — the allow branch comes from membership, not the public flag.
func TestViewerAllowedReadActions(t *testing.T) {
	t.Parallel()
	viewer := auth.Actor{ID: 10, Kind: auth.ActorKindUser}
	ctxViewer := auth.WithProjectMembership(context.Background(), map[int64]string{42: "viewer"})

	for _, action := range memberReadActions {
		action := action
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			ok, reason := auth.Can(ctxViewer, viewer, action, auth.Target{Kind: "repo", ProjectID: 42, PublicRead: false})
			if !ok {
				t.Errorf("viewer action=%s: ok=%v reason=%q; want true/\"\"", action, ok, reason)
			}
		})
	}
}

// TestMaintainerAllowedAllWriteActions asserts that a maintainer actor
// (member of project 42 with role "maintainer") is allowed every
// write action.
func TestMaintainerAllowedAllWriteActions(t *testing.T) {
	t.Parallel()
	maintainer := auth.Actor{ID: 10, Kind: auth.ActorKindUser}
	ctxMaintainer := auth.WithProjectMembership(context.Background(), map[int64]string{42: "maintainer"})

	for _, action := range maintainerWriteActions {
		action := action
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			ok, reason := auth.Can(ctxMaintainer, maintainer, action, auth.Target{Kind: "repo", ProjectID: 42})
			if !ok || reason != "" {
				t.Errorf("maintainer action=%s: ok=%v reason=%q; want true/\"\"",
					action, ok, reason)
			}
		})
	}
}

// TestNonMemberGetsNotAProjectMember asserts that an actor with an empty
// membership map is denied write actions with ReasonNotAProjectMember —
// distinct from the ReasonNotAMaintainer returned for viewers (who ARE
// members but lack the maintainer role).
func TestNonMemberGetsNotAProjectMember(t *testing.T) {
	t.Parallel()
	nonMember := auth.Actor{ID: 99, Kind: auth.ActorKindUser}
	ctxEmpty := auth.WithProjectMembership(context.Background(), map[int64]string{})

	ok, reason := auth.Can(ctxEmpty, nonMember, auth.ActionCreateRepo, auth.Target{Kind: "repo", ProjectID: 42})
	if ok || reason != auth.ReasonNotAProjectMember {
		t.Errorf("non-member ActionCreateRepo: ok=%v reason=%q; want false/%s",
			ok, reason, auth.ReasonNotAProjectMember)
	}
}
