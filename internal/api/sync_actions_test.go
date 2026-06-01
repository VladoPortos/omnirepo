package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
)

// TestSyncActorBridge_UserOwnedAPIKey pins that a user-owned API-key actor
// (OwnerKind=user, ProjectScope=nil) must surface the owning user's id as
// UserID on the bridged httpx.SyncActor — otherwise handleSync's
// `actor.UserID != 0` branch is skipped, the project-member lookup never
// runs, and every user-owned key 403s on POST .../sync.
func TestSyncActorBridge_UserOwnedAPIKey(t *testing.T) {
	a := auth.Actor{
		ID:        42,
		Kind:      auth.ActorKindAPIKey,
		APIKeyID:  7,
		OwnerKind: auth.OwnerKindUser,
	}
	r := httptest.NewRequest("POST", "/doesnt/matter", nil).
		WithContext(auth.WithActor(context.Background(), a))

	got := SyncActorBridge(r)

	if !got.Authenticated {
		t.Fatalf("want Authenticated=true, got false")
	}
	if got.UserID != 42 {
		t.Errorf("want UserID=42 (owning user surfaced), got %d", got.UserID)
	}
	if got.APIKeyID != 7 {
		t.Errorf("want APIKeyID=7, got %d", got.APIKeyID)
	}
	if got.ProjectID != 0 {
		t.Errorf("want ProjectID=0 for non-project-scoped key, got %d", got.ProjectID)
	}
}

// TestSyncActorBridge_ProjectScopedAPIKey regresses the existing contract:
// a project-owned key (ProjectScope set) must pin ProjectID and leave
// UserID at 0 — the handler's second branch `ProjectID != 0` takes it.
func TestSyncActorBridge_ProjectScopedAPIKey(t *testing.T) {
	pid := int64(99)
	a := auth.Actor{
		Kind:         auth.ActorKindAPIKey,
		APIKeyID:     5,
		ProjectScope: &pid,
	}
	r := httptest.NewRequest("POST", "/doesnt/matter", nil).
		WithContext(auth.WithActor(context.Background(), a))

	got := SyncActorBridge(r)

	if got.ProjectID != 99 {
		t.Errorf("want ProjectID=99, got %d", got.ProjectID)
	}
	if got.UserID != 0 {
		t.Errorf("want UserID=0 for project-scoped key, got %d", got.UserID)
	}
}

// TestSyncActorBridge_User regresses the ActorKindUser branch: UserID
// surfaces, APIKeyID stays zero.
func TestSyncActorBridge_User(t *testing.T) {
	a := auth.Actor{ID: 2, Kind: auth.ActorKindUser}
	r := httptest.NewRequest("POST", "/doesnt/matter", nil).
		WithContext(auth.WithActor(context.Background(), a))

	got := SyncActorBridge(r)

	if got.UserID != 2 {
		t.Errorf("want UserID=2, got %d", got.UserID)
	}
	if got.APIKeyID != 0 {
		t.Errorf("want APIKeyID=0, got %d", got.APIKeyID)
	}
}

// TestSyncActorBridge_Anonymous regresses the anonymous fast-path: returns
// a zero-value SyncActor (Authenticated=false) so the handler 401s.
func TestSyncActorBridge_Anonymous(t *testing.T) {
	a := auth.Actor{Kind: auth.ActorKindAnonymous}
	r := httptest.NewRequest("POST", "/doesnt/matter", nil).
		WithContext(auth.WithActor(context.Background(), a))

	got := SyncActorBridge(r)

	if got.Authenticated {
		t.Errorf("want Authenticated=false for anonymous, got true")
	}
}
