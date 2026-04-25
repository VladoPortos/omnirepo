package api_test

import (
	"testing"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
)

// ptrInt64 is a tiny helper so the want-table reads naturally.
func ptrInt64(v int64) *int64 { return &v }

func TestSetActor(t *testing.T) {
	cases := []struct {
		name       string
		actor      auth.Actor
		wantUserID *int64
		wantKeyID  *int64
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
			name:       "s3_sigv4_actor",
			actor:      auth.Actor{Kind: auth.ActorKindS3Key},
			wantUserID: nil,
			wantKeyID:  nil,
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
			// middleware bug. Helper must leave both nil rather than
			// silently mis-attributing as user 0.
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
