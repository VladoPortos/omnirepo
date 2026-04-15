package audit_test

import (
	"testing"

	"github.com/dxc-internal/omnirepo/internal/audit"
)

// AllPhase1EventKinds is the authoritative Phase 1 enumeration per RESEARCH
// lines 588-608. Used by this test and by TestEveryStateChangingActionEmitsEvent
// in audit_test.go.
var AllPhase1EventKinds = []audit.EventKind{
	audit.EvtAuthLoginSuccess,
	audit.EvtAuthLoginFailure,
	audit.EvtAuthLogout,
	audit.EvtAuthPasswordChanged,
	audit.EvtUserCreated,
	audit.EvtUserUpdated,
	audit.EvtUserDeleted,
	audit.EvtProjectCreated,
	audit.EvtProjectUpdated,
	audit.EvtProjectDeleted,
	audit.EvtMemberAdded,
	audit.EvtMemberRemoved,
	audit.EvtRepoCreated,
	audit.EvtRepoUpdated,
	audit.EvtRepoDeleted,
	audit.EvtRepoWiped,
	audit.EvtTLSCertUploaded,
	audit.EvtBootstrapApplied,
	audit.EvtMaintenanceToggled,
	audit.EvtUpstreamCredCreated,
	audit.EvtUpstreamCredUpdated,
	audit.EvtUpstreamCredDeleted,
	audit.EvtUpstreamCredUsed,
}

func TestAllEventKindsDistinctAndCount(t *testing.T) {
	if got := len(AllPhase1EventKinds); got != 23 {
		t.Fatalf("EventKind count = %d, want 23", got)
	}
	seen := make(map[audit.EventKind]struct{}, len(AllPhase1EventKinds))
	for _, k := range AllPhase1EventKinds {
		if k == "" {
			t.Fatalf("empty EventKind in enumeration")
		}
		if _, dup := seen[k]; dup {
			t.Fatalf("duplicate EventKind: %q", k)
		}
		seen[k] = struct{}{}
	}
}
