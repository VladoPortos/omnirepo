package api_test

import (
	"testing"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
)

// ptrInt64 is a tiny helper so the want-table reads naturally.
func ptrInt64(v int64) *int64 { return &v }

func TestSetActor(t *testing.T) {
	cases := []struct {
		name        string
		actor       auth.Actor
		wantUserID  *int64
		wantKeyID   *int64
		wantS3KeyID *int64
	}{
		{
			name:       "session_user",
			actor:      auth.Actor{Kind: auth.ActorKindUser, ID: 42},
			wantUserID: ptrInt64(42),
			wantKeyID:  nil,
		},
		{
			name:       "user_owned_api_key",
			actor:      auth.Actor{Kind: auth.ActorKindAPIKey, OwnerKind: auth.OwnerKindUser, ID: 42, APIKeyID: 7},
			wantUserID: ptrInt64(42),
			wantKeyID:  ptrInt64(7),
		},
		{
			// The audit-finding-#7 case: project-owned API key MUST
			// leave ActorUserID nil. Writing &0 violates the FK against
			// users(id) and was the silent-drop trigger.
			name:       "project_owned_api_key_no_fk_violation",
			actor:      auth.Actor{Kind: auth.ActorKindAPIKey, OwnerKind: auth.OwnerKindProject, ID: 0, APIKeyID: 9},
			wantUserID: nil,
			wantKeyID:  ptrInt64(9),
		},
		{
			// v1.7 / S3AUDIT-02: S3 actor without S3KeyID is a defensive
			// branch (sigv4 middleware should always populate it). Leave
			// all three nil rather than fabricate.
			name:       "s3_sigv4_actor_no_id_defensive",
			actor:      auth.Actor{Kind: auth.ActorKindS3Key},
			wantUserID: nil,
			wantKeyID:  nil,
		},
		{
			// v1.7 / S3AUDIT-02: the canonical happy path. ActorS3KeyID
			// is populated from actor.S3KeyID (deep-copied so a later
			// mutation of actor cannot retroactively change a recorded
			// audit row).
			name:        "s3_sigv4_actor_with_id",
			actor:       auth.Actor{Kind: auth.ActorKindS3Key, S3KeyID: ptrInt64(55)},
			wantUserID:  nil,
			wantKeyID:   nil,
			wantS3KeyID: ptrInt64(55),
		},
		{
			name:       "anonymous",
			actor:      auth.Actor{Kind: auth.ActorKindAnonymous},
			wantUserID: nil,
			wantKeyID:  nil,
		},
		{
			name:       "zero_value_actor",
			actor:      auth.Actor{},
			wantUserID: nil,
			wantKeyID:  nil,
		},
		{
			// Defensive: APIKey actor with empty OwnerKind is a
			// middleware bug. Helper must leave all three nil rather
			// than silently mis-attributing as user 0.
			name:       "api_key_missing_owner_kind_defensive",
			actor:      auth.Actor{Kind: auth.ActorKindAPIKey, APIKeyID: 13},
			wantUserID: nil,
			wantKeyID:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := audit.Event{
				Kind:       audit.EvtS3BucketCreated,
				TargetKind: "s3_bucket",
				TargetID:   "alpha",
			}
			api.SetActor(&e, tc.actor)

			// Negative-shape assertion: the FK-violation case must produce
			// a literal nil, not &0.
			assertPtrEq(t, "ActorUserID", e.ActorUserID, tc.wantUserID)
			assertPtrEq(t, "ActorAPIKeyID", e.ActorAPIKeyID, tc.wantKeyID)
			assertPtrEq(t, "ActorS3KeyID", e.ActorS3KeyID, tc.wantS3KeyID)

			// Untouched fields stay untouched.
			if e.Kind != audit.EvtS3BucketCreated {
				t.Fatalf("Kind mutated: %v", e.Kind)
			}
			if e.TargetKind != "s3_bucket" || e.TargetID != "alpha" {
				t.Fatalf("Target* mutated: %q/%q", e.TargetKind, e.TargetID)
			}
		})
	}
}

// TestSetActor_S3KeyID_DeepCopy proves SetActor copies the value behind
// actor.S3KeyID into the event rather than aliasing the same pointer.
// A subsequent mutation of the actor's underlying *int64 must not
// retroactively change e.ActorS3KeyID — that would silently rewrite
// already-recorded audit rows if the same actor reused storage.
func TestSetActor_S3KeyID_DeepCopy(t *testing.T) {
	id := int64(77)
	actor := auth.Actor{Kind: auth.ActorKindS3Key, S3KeyID: &id}

	e := audit.Event{Kind: audit.EvtS3BucketCreated}
	api.SetActor(&e, actor)
	if e.ActorS3KeyID == nil || *e.ActorS3KeyID != 77 {
		t.Fatalf("initial: ActorS3KeyID = %v, want &77", e.ActorS3KeyID)
	}

	// Mutate the storage that actor.S3KeyID points at.
	id = 999
	if *e.ActorS3KeyID != 77 {
		t.Fatalf("alias bug: ActorS3KeyID drifted to %d after actor mutation; want 77",
			*e.ActorS3KeyID)
	}
}

func TestSetActor_Idempotent(t *testing.T) {
	actor := auth.Actor{Kind: auth.ActorKindAPIKey, OwnerKind: auth.OwnerKindProject, APIKeyID: 9}
	e := audit.Event{Kind: audit.EvtS3BucketDeleted}
	api.SetActor(&e, actor)
	first := struct {
		u *int64
		k *int64
	}{e.ActorUserID, e.ActorAPIKeyID}
	api.SetActor(&e, actor)
	if e.ActorUserID != nil || first.u != nil {
		t.Fatalf("ActorUserID drift: first=%v second=%v", first.u, e.ActorUserID)
	}
	if e.ActorAPIKeyID == nil || first.k == nil || *e.ActorAPIKeyID != *first.k {
		t.Fatalf("ActorAPIKeyID drift: first=%v second=%v", first.k, e.ActorAPIKeyID)
	}
}

func assertPtrEq(t *testing.T, label string, got, want *int64) {
	t.Helper()
	switch {
	case got == nil && want == nil:
		return
	case got == nil && want != nil:
		t.Fatalf("%s: got nil, want &%d", label, *want)
	case got != nil && want == nil:
		t.Fatalf("%s: got &%d, want nil (FK-violation guard)", label, *got)
	case *got != *want:
		t.Fatalf("%s: got &%d, want &%d", label, *got, *want)
	}
}
