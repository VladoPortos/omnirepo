package api

import "testing"

func TestUnknownScanFindingsAreNotClean(t *testing.T) {
	if got := deriveScanSeverity("done", `{"unknown":1}`); got != "unknown" {
		t.Fatalf("unknown-only scan=%q, want unknown", got)
	}
	if got := deriveScanSeverity("done", `{"unknown":1,"critical":1}`); got != "critical" {
		t.Fatalf("rated severity should take precedence: %q", got)
	}
}
