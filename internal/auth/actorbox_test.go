package auth_test

import (
	"context"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
)

// TestWithActor_FillsActorBox verifies that WithActor mutates a seeded
// *ActorBox in place. This is what lets a middleware running BEFORE auth
// (e.g. the Git AuditMiddleware) recover the authenticated actor that auth
// adds to a derived context downstream.
func TestWithActor_FillsActorBox(t *testing.T) {
	box := &auth.ActorBox{}
	ctx := auth.WithActorBox(context.Background(), box)

	if got := auth.ActorBoxFromContext(ctx); got != box {
		t.Fatalf("ActorBoxFromContext = %p, want %p", got, box)
	}

	a := auth.Actor{ID: 9, Kind: auth.ActorKindUser, Login: "bob"}
	_ = auth.WithActor(ctx, a) // returned ctx discarded; the box poke is the point
	if box.Actor.ID != 9 || box.Actor.Kind != auth.ActorKindUser || box.Actor.Login != "bob" {
		t.Fatalf("box.Actor = %+v, want {ID:9 user bob}", box.Actor)
	}

	// No box on the context: WithActor must not panic.
	_ = auth.WithActor(context.Background(), a)

	// ActorBoxFromContext returns nil when no box was seeded.
	if got := auth.ActorBoxFromContext(context.Background()); got != nil {
		t.Fatalf("ActorBoxFromContext(empty) = %p, want nil", got)
	}
}
