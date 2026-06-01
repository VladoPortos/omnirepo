package auth_test

import (
	"context"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
)

func TestActorContext_RoundTrip(t *testing.T) {
	a := auth.Actor{ID: 7, Login: "alice", Kind: auth.ActorKindUser, IsSuperAdmin: true}
	ctx := auth.WithActor(context.Background(), a)
	got, ok := auth.ActorFromContext(ctx)
	if !ok {
		t.Fatalf("ActorFromContext: ok=false, want true")
	}
	if got != a {
		t.Fatalf("ActorFromContext: got %+v, want %+v", got, a)
	}
}

func TestActorContext_Missing(t *testing.T) {
	_, ok := auth.ActorFromContext(context.Background())
	if ok {
		t.Fatalf("ActorFromContext on empty ctx: ok=true, want false")
	}
}
