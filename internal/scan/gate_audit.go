package scan

import (
	"context"

	"github.com/vladoportos/omnirepo/internal/audit"
)

// EmitGateAudit records one scan.gate.blocked audit event. Shared by the
// raw and OCI severity gates (previously duplicated per package). source
// distinguishes a cache-hit decision ("cache") from a DB-read one ("db").
// Nil-safe on logger.
func EmitGateAudit(
	ctx context.Context,
	logger audit.Logger,
	repoID int64,
	kind, artifactID string,
	entry CacheEntry,
	source string,
) {
	if logger == nil {
		return
	}
	_ = logger.Record(ctx, audit.Event{
		Kind:       audit.EvtScanGateBlocked,
		TargetKind: kind,
		TargetID:   artifactID,
		Outcome:    "blocked",
		Details: map[string]any{
			"repo_id":   repoID,
			"severity":  entry.Severity,
			"cve_count": entry.CVECount,
			"scan_id":   entry.ScanID,
			"source":    source,
		},
	})
}
