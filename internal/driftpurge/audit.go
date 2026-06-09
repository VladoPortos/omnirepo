package driftpurge

import (
	"context"
	"strconv"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// AuditSink carries the (nil-safe) sinks EmitReportAudit writes to. Each
// per-protocol sync handler populates it from its handler deps.
type AuditSink struct {
	Audit        audit.Logger           // nil-safe; when nil no audit event is recorded
	SyncJobs     *metadata.SyncJobsRepo // nil-safe; when nil no summary is stamped
	ThresholdPct int                    // cfg.DriftPurgeThresholdPct, for the blocked-event detail
}

// EmitReportAudit translates a completed DriftReport into the audit events and
// sync_jobs.summary writes that follow a drift-purge run. It centralizes the
// switch the deb/rpm/pypi/helm sync handlers previously duplicated verbatim;
// the protocol string comes from report.Protocol (set by Run from
// adapter.Protocol), which is why no per-protocol literal is needed here.
//
// repoID/jobID/upstreamURL identify the run. The four cases mirror Run's
// outcomes: a threshold-blocked skip stamps summary.drift_blocked + a skipped
// audit event; an empty-upstream skip emits only the skipped event; a real
// purge emits the purged event + stamps summary.drift_purged; and a zero-count
// run stamps summary.drift_purged(0) as run-evidence with no audit event.
func EmitReportAudit(ctx context.Context, sink AuditSink, report DriftReport, repoID, jobID int64, upstreamURL string) {
	targetID := strconv.FormatInt(repoID, 10)
	switch {
	case report.Skipped && report.Reason == reasonThresholdExceeded:
		// Percent-threshold guard tripped. Stamp summary.drift_blocked so the
		// UI can render the override banner; reuse EvtMirrorDriftPurgeSkipped.
		if sink.SyncJobs != nil {
			_ = sink.SyncJobs.SetSummaryDriftBlocked(ctx, jobID, int64(report.BlockedCount))
		}
		if sink.Audit != nil {
			_ = sink.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtMirrorDriftPurgeSkipped,
				TargetKind: "repo",
				TargetID:   targetID,
				Details: map[string]any{
					"protocol":      report.Protocol,
					"reason":        report.Reason,
					"local_count":   int64(report.LocalCount),
					"blocked_count": int64(report.BlockedCount),
					"threshold_pct": int64(sink.ThresholdPct),
					"sync_job_id":   jobID,
					"upstream_url":  upstreamURL,
				},
			})
		}
	case report.Skipped:
		// Empty-upstream guard tripped. SetSummaryDriftPurged NOT called per
		// the absence rule.
		if sink.Audit != nil {
			_ = sink.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtMirrorDriftPurgeSkipped,
				TargetKind: "repo",
				TargetID:   targetID,
				Details: map[string]any{
					"protocol":     report.Protocol,
					"reason":       report.Reason,
					"local_count":  int64(report.LocalCount),
					"sync_job_id":  jobID,
					"upstream_url": upstreamURL,
				},
			})
		}
	case report.PurgedCount > 0:
		// Drift purged with count > 0 — emit audit + summary.
		if sink.Audit != nil {
			_ = sink.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtMirrorDriftPurged,
				TargetKind: "repo",
				TargetID:   targetID,
				Details: map[string]any{
					"protocol":     report.Protocol,
					"count":        int64(report.PurgedCount),
					"sample":       report.Sample,
					"sync_job_id":  jobID,
					"upstream_url": upstreamURL,
				},
			})
		}
		if sink.SyncJobs != nil {
			_ = sink.SyncJobs.SetSummaryDriftPurged(ctx, jobID, int64(report.PurgedCount))
		}
	default:
		// Zero-count drift run is run-evidence only: no audit emission, but
		// stamp the summary integer so the sync record proves drift ran.
		if sink.SyncJobs != nil {
			_ = sink.SyncJobs.SetSummaryDriftPurged(ctx, jobID, 0)
		}
	}
}
