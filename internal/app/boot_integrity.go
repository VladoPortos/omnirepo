package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// bootAuditAdapter bridges metadata.AuditRecorder (the minimal interface
// metadata.RunBootIntegrityCheck consumes) to the concrete audit.Logger
// wired by app.Run.
//
// The metadata package cannot import internal/audit directly (audit imports
// metadata — a direct back-edge would create a cycle). RunBootIntegrityCheck
// emits kind strings; this adapter translates them to audit.EventKind and
// records with a nil actor (boot-time has no user context), outcome="boot",
// and the details map serialised to JSON.
type bootAuditAdapter struct {
	logger audit.Logger
}

// newBootAuditAdapter constructs the adapter. logger may be nil — if so,
// Record is a no-op so boot-time integrity-check emission never crashes
// even when the audit logger is misconfigured.
func newBootAuditAdapter(logger audit.Logger) *bootAuditAdapter {
	return &bootAuditAdapter{logger: logger}
}

// Record implements metadata.AuditRecorder.
//
// kind is a string value of audit.EventKind; this adapter re-widens to
// EventKind when constructing the event. Unknown kinds still write
// correctly — the audit table stores event_kind as TEXT.
//
// On any error from the underlying audit.Logger.Record we log at WARN
// but do NOT propagate — the integrity-check path is log+cache+continue
// (Pitfall 10.3); a best-effort audit write here must match.
func (a *bootAuditAdapter) Record(ctx context.Context, kind string, details map[string]any) {
	if a == nil || a.logger == nil {
		return
	}
	ev := audit.Event{
		Kind:       audit.EventKind(kind),
		TargetKind: "db",
		Outcome:    "boot",
		Details:    details,
	}
	if err := a.logger.Record(ctx, ev); err != nil {
		// Best-effort: never break boot on audit failure.
		detailsJSON, _ := json.Marshal(details)
		slog.WarnContext(ctx, "boot.integrity_check.audit_record_failed",
			"err", err, "kind", kind, "details", string(detailsJSON))
	}
}

// Compile-time interface check — guards against metadata.AuditRecorder
// drift without needing a dedicated test.
var _ metadata.AuditRecorder = (*bootAuditAdapter)(nil)
