package driftpurge

import (
	"context"
	"testing"

	"github.com/vladoportos/omnirepo/internal/audit"
)

// capturingAudit records emitted events for assertions.
type capturingAudit struct{ events []audit.Event }

func (c *capturingAudit) Record(_ context.Context, e audit.Event) error {
	c.events = append(c.events, e)
	return nil
}

// TestEmitReportAudit pins the four-case audit-emission contract that the
// deb/rpm/pypi/helm sync handlers previously duplicated verbatim.
func TestEmitReportAudit(t *testing.T) {
	ctx := context.Background()
	const repoID, jobID = int64(7), int64(42)
	const upstream = "https://up.example.com/charts"

	cases := []struct {
		name       string
		report     DriftReport
		wantKind   audit.EventKind // "" means no audit event expected
		wantDetail map[string]any  // subset of Details that must match
	}{
		{
			name: "threshold_blocked",
			report: DriftReport{
				Protocol: "helm", Skipped: true, Reason: reasonThresholdExceeded,
				LocalCount: 100, BlockedCount: 80,
			},
			wantKind: audit.EvtMirrorDriftPurgeSkipped,
			wantDetail: map[string]any{
				"protocol": "helm", "reason": reasonThresholdExceeded,
				"local_count": int64(100), "blocked_count": int64(80),
				"threshold_pct": int64(50),
			},
		},
		{
			name:       "empty_upstream_skip",
			report:     DriftReport{Protocol: "rpm", Skipped: true, Reason: reasonUpstreamEmpty, LocalCount: 5},
			wantKind:   audit.EvtMirrorDriftPurgeSkipped,
			wantDetail: map[string]any{"protocol": "rpm", "reason": reasonUpstreamEmpty, "local_count": int64(5)},
		},
		{
			name:       "purged",
			report:     DriftReport{Protocol: "deb", PurgedCount: 3, Sample: []string{"a", "b"}},
			wantKind:   audit.EvtMirrorDriftPurged,
			wantDetail: map[string]any{"protocol": "deb", "count": int64(3)},
		},
		{
			name:     "zero_count_no_audit",
			report:   DriftReport{Protocol: "pypi", PurgedCount: 0},
			wantKind: "", // run-evidence only — summary stamped, no audit event
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &capturingAudit{}
			EmitReportAudit(ctx, AuditSink{Audit: rec, ThresholdPct: 50}, tc.report, repoID, jobID, upstream)
			if tc.wantKind == "" {
				if len(rec.events) != 0 {
					t.Fatalf("expected no audit event, got %d: %+v", len(rec.events), rec.events)
				}
				return
			}
			if len(rec.events) != 1 {
				t.Fatalf("expected 1 audit event, got %d", len(rec.events))
			}
			ev := rec.events[0]
			if ev.Kind != tc.wantKind {
				t.Fatalf("Kind = %q, want %q", ev.Kind, tc.wantKind)
			}
			if ev.TargetKind != "repo" || ev.TargetID != "7" {
				t.Fatalf("Target = %s/%s, want repo/7", ev.TargetKind, ev.TargetID)
			}
			if ev.Details["sync_job_id"] != jobID || ev.Details["upstream_url"] != upstream {
				t.Fatalf("missing job/upstream details: %+v", ev.Details)
			}
			for k, want := range tc.wantDetail {
				if ev.Details[k] != want {
					t.Fatalf("Details[%q] = %v (%T), want %v (%T)", k, ev.Details[k], ev.Details[k], want, want)
				}
			}
		})
	}
}

// TestEmitReportAudit_NilSinksNoPanic: nil Audit + nil SyncJobs (the nil-safe
// handler deps used in minimal deployments and tests) must not panic.
func TestEmitReportAudit_NilSinksNoPanic(t *testing.T) {
	ctx := context.Background()
	EmitReportAudit(ctx, AuditSink{}, DriftReport{Protocol: "deb", PurgedCount: 1, Sample: []string{"x"}}, 1, 1, "u")
	EmitReportAudit(ctx, AuditSink{}, DriftReport{Protocol: "deb", Skipped: true, Reason: reasonUpstreamEmpty}, 1, 1, "u")
	EmitReportAudit(ctx, AuditSink{}, DriftReport{Protocol: "deb", Skipped: true, Reason: reasonThresholdExceeded, BlockedCount: 9}, 1, 1, "u")
	EmitReportAudit(ctx, AuditSink{}, DriftReport{Protocol: "deb"}, 1, 1, "u")
}
