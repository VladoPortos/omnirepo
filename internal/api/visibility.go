// Package api — actor-aware project visibility helper.
//
// Audit finding #8: several handlers (search, projects list, dashboard) were
// keying visibility off Members.ListProjectIDsForUser(actor.ID) alone, so a
// project-owned API key — which has actor.ID == 0 and carries its scope in
// actor.ProjectScope — saw no projects. Other handlers in the codebase
// already handled ProjectScope correctly; this helper centralizes the rule
// so the behavior can't drift again.
package api

import (
	"context"

	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// visibleProjectIDs returns the project IDs the actor can see, or nil when
// the actor can see every project (super-admin). An empty (non-nil) slice
// means "can see no project" — callers MUST treat nil and empty differently.
//
// Rules:
//   - Super-admin: nil (no filter).
//   - Project-scoped API key: single-element slice with actor.ProjectScope.
//   - Any other actor: project IDs from Members.ListProjectIDsForUser.
//
// Members must be non-nil; nil triggers panic to surface wiring bugs early.
func visibleProjectIDs(ctx context.Context, members *metadata.MembersRepo, actor auth.Actor) []int64 {
	if actor.IsSuperAdmin {
		return nil
	}
	if actor.Kind == auth.ActorKindAPIKey && actor.ProjectScope != nil {
		return []int64{*actor.ProjectScope}
	}
	ids, _ := members.ListProjectIDsForUser(ctx, actor.ID)
	if ids == nil {
		ids = []int64{}
	}
	return ids
}
