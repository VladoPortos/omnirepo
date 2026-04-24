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

// AllPhase2ScanEventKinds enumerates the Phase 02-09 scan event kinds.
var AllPhase2ScanEventKinds = []audit.EventKind{
	audit.EvtScanStarted,
	audit.EvtScanFinished,
	audit.EvtScanFailed,
	audit.EvtScanGateBlocked,
}

// AllPhase2GCEventKinds enumerates the Phase 02-12 admin GC event kinds.
var AllPhase2GCEventKinds = []audit.EventKind{
	audit.EvtGCTriggered,
	audit.EvtGCRun,
}

// AllPhase3EventKinds enumerates the Phase 3 Plan 01 additions — package-
// repo uploads, signing-key lifecycle, and repo-metadata regen events.
var AllPhase3EventKinds = []audit.EventKind{
	audit.EvtSigningKeyCreated,
	audit.EvtSigningKeyRotated,
	audit.EvtSigningKeyUsed,
	audit.EvtRPMUpload,
	audit.EvtRPMDelete,
	audit.EvtDEBUpload,
	audit.EvtDEBDelete,
	audit.EvtPyPIUpload,
	audit.EvtPyPIDelete,
	audit.EvtHelmUpload,
	audit.EvtHelmDelete,
	audit.EvtRepoMetadataRegen,
	audit.EvtRepoMetadataFailed,
}

func TestPhase3EventKindsDistinctAndCount(t *testing.T) {
	if got, want := len(AllPhase3EventKinds), 13; got != want {
		t.Fatalf("Phase3 EventKind count = %d, want %d", got, want)
	}
	seen := make(map[audit.EventKind]struct{}, len(AllPhase3EventKinds))
	for _, k := range AllPhase3EventKinds {
		if k == "" {
			t.Fatalf("empty Phase3 EventKind in enumeration")
		}
		if _, dup := seen[k]; dup {
			t.Fatalf("duplicate Phase3 EventKind: %q", k)
		}
		seen[k] = struct{}{}
	}
}

// AllRBACPhase2EventKinds enumerates the v1.5 Phase 2 RBAC event kinds.
// Separate slice because AllPhase1EventKinds count is locked at 23 per
// TestAllEventKindsDistinctAndCount (RESEARCH Pitfall 4).
var AllRBACPhase2EventKinds = []audit.EventKind{
	audit.EvtMemberRoleChanged,
}

func TestRBACPhase2EventKindsDistinctAndCount(t *testing.T) {
	if got, want := len(AllRBACPhase2EventKinds), 1; got != want {
		t.Fatalf("RBAC Phase2 EventKind count = %d, want %d", got, want)
	}
	seen := make(map[audit.EventKind]struct{}, len(AllRBACPhase2EventKinds))
	for _, k := range AllRBACPhase2EventKinds {
		if k == "" {
			t.Fatalf("empty RBAC Phase2 EventKind in enumeration")
		}
		if _, dup := seen[k]; dup {
			t.Fatalf("duplicate RBAC Phase2 EventKind: %q", k)
		}
		seen[k] = struct{}{}
	}
}

// AllPyPIFixPhase3EventKinds enumerates the v1.5 Phase 3 additions.
// Separate slice per v1.5 convention (precedent: AllRBACPhase2EventKinds)
// so future v1.5 phases don't blur milestone boundaries.
var AllPyPIFixPhase3EventKinds = []audit.EventKind{
	audit.EvtSyncFileSkipped,
}

func TestPyPIFixPhase3EventKindsDistinctAndCount(t *testing.T) {
	if got, want := len(AllPyPIFixPhase3EventKinds), 1; got != want {
		t.Fatalf("PyPIFix Phase3 EventKind count = %d, want %d", got, want)
	}
	seen := make(map[audit.EventKind]struct{}, len(AllPyPIFixPhase3EventKinds))
	for _, k := range AllPyPIFixPhase3EventKinds {
		if k == "" {
			t.Fatalf("empty PyPIFix Phase3 EventKind in enumeration")
		}
		if _, dup := seen[k]; dup {
			t.Fatalf("duplicate PyPIFix Phase3 EventKind: %q", k)
		}
		seen[k] = struct{}{}
	}
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
