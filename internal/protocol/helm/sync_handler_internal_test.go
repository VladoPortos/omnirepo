package helm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/audit"
)

// capturingAudit is a test-only audit.Logger that records every Event the
// SUT emits so tests can inspect the Details map. Used by Plan 05-01
// TestHelmSync_Fail_AuditEmitsPartialDetails (HELMRETRY-03 / D-03a) to
// verify fail() mirrors PartialSyncErr counts into EvtSyncFailed.
type capturingAudit struct {
	events []audit.Event
}

func (c *capturingAudit) Record(_ context.Context, ev audit.Event) error {
	c.events = append(c.events, ev)
	return nil
}

// TestIsNonChartManifestErr pins the Helm SDK / ORAS error-string match
// that lets fetchAndCommitOCI skip non-chart OCI sidecar manifests
// (Bitnami `-metadata`, future conventions) instead of aborting the
// whole sync batch. Changes to the upstream error wording require
// updating both the matcher and this test.
func TestIsNonChartManifestErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"helm sdk wording", errors.New("manifest does not contain minimum number of descriptors (2), descriptors found: 1"), true},
		{"wrapped", errors.New("ociclient: pull oci://.../nginx:22.0.7-metadata: manifest does not contain minimum number of descriptors"), true},
		{"unrelated auth error", errors.New("401 Unauthorized"), false},
		{"unrelated network error", errors.New("connection reset by peer"), false},
	}
	for _, tc := range cases {
		if got := isNonChartManifestErr(tc.err); got != tc.want {
			t.Errorf("%s: isNonChartManifestErr(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestHelmSync_Fail_AuditEmitsPartialDetails locks the v1.5 Phase 5
// HELMRETRY-03 / D-03a contract: when fail() is handed a *PartialSyncErr,
// EvtSyncFailed.Details MUST mirror the persisted/expected counts into
// audit observability. Closes RESEARCH.md open-question #3 — the
// sync_jobs.log JSON and audit stream carry the same signal so operators
// grepping either source see partial syncs identically.
func TestHelmSync_Fail_AuditEmitsPartialDetails(t *testing.T) {
	cap := &capturingAudit{}
	h := &SyncHandler{deps: SyncDeps{Audit: cap}}

	pl := SyncPayload{UpstreamURL: "https://upstream.example/charts"}
	pse := newPartialSyncErr(2, 3, errors.New("upstream 500"))

	_ = h.fail(context.Background(), int64(42), pl, time.Now(), pse)

	if len(cap.events) != 1 {
		t.Fatalf("events recorded = %d; want 1", len(cap.events))
	}
	ev := cap.events[0]
	if ev.Kind != audit.EvtSyncFailed {
		t.Errorf("Kind = %q; want %q", ev.Kind, audit.EvtSyncFailed)
	}
	if got, _ := ev.Details["partial"].(bool); !got {
		t.Errorf(`Details["partial"] = %v; want true`, ev.Details["partial"])
	}
	if got, _ := ev.Details["files_persisted"].(int64); got != 2 {
		t.Errorf(`Details["files_persisted"] = %v (%T); want int64(2)`, ev.Details["files_persisted"], ev.Details["files_persisted"])
	}
	if got, _ := ev.Details["files_expected"].(int64); got != 3 {
		t.Errorf(`Details["files_expected"] = %v (%T); want int64(3)`, ev.Details["files_expected"], ev.Details["files_expected"])
	}
}

// TestHelmSync_Fail_NonPartialErrOmitsDetails locks the negative side of
// the contract: generic errors must NOT cause fail() to stamp partial
// details on the audit row (threat T-5-01 — error-type confusion). Only
// *PartialSyncErr satisfies errors.As(err, &pse).
func TestHelmSync_Fail_NonPartialErrOmitsDetails(t *testing.T) {
	cap := &capturingAudit{}
	h := &SyncHandler{deps: SyncDeps{Audit: cap}}

	pl := SyncPayload{UpstreamURL: "https://upstream.example/charts"}
	_ = h.fail(context.Background(), int64(42), pl, time.Now(), errors.New("plain failure"))

	if len(cap.events) != 1 {
		t.Fatalf("events recorded = %d; want 1", len(cap.events))
	}
	ev := cap.events[0]
	if _, ok := ev.Details["partial"]; ok {
		t.Errorf(`Details["partial"] present = %v; want absent for non-PartialSyncErr`, ev.Details["partial"])
	}
	if _, ok := ev.Details["files_persisted"]; ok {
		t.Errorf(`Details["files_persisted"] present; want absent for non-PartialSyncErr`)
	}
	if _, ok := ev.Details["files_expected"]; ok {
		t.Errorf(`Details["files_expected"] present; want absent for non-PartialSyncErr`)
	}
}
