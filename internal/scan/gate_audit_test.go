package scan_test

import (
	"context"
	"testing"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/scan"
)

type captureLogger struct{ events []audit.Event }

func (c *captureLogger) Record(_ context.Context, e audit.Event) error {
	c.events = append(c.events, e)
	return nil
}

func TestEmitGateAudit(t *testing.T) {
	// Nil logger is a no-op, not a panic.
	scan.EmitGateAudit(context.Background(), nil, 1, "docker", "sha256:x", scan.CacheEntry{}, "cache")

	logger := &captureLogger{}
	entry := scan.CacheEntry{Severity: "critical", CVECount: 3, ScanID: 42, Blocked: true}
	scan.EmitGateAudit(context.Background(), logger, 7, "docker", "sha256:abc", entry, "db")

	if len(logger.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(logger.events))
	}
	ev := logger.events[0]
	if ev.Kind != audit.EvtScanGateBlocked || ev.TargetKind != "docker" || ev.TargetID != "sha256:abc" || ev.Outcome != "blocked" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Details["repo_id"] != int64(7) || ev.Details["severity"] != "critical" || ev.Details["cve_count"] != 3 || ev.Details["scan_id"] != int64(42) || ev.Details["source"] != "db" {
		t.Fatalf("details = %+v", ev.Details)
	}
}
