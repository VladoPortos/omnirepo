package helm_test

// Phase 5 Plan 01 HELMRETRY-03 — ctx-cancel partial-sync integration test.
//
// Lives in package helm_test (external) so it can reuse
// newHelmProgressFixture from sync_progress_test.go. The Details-shape
// audit test lives in sync_handler_internal_test.go (package helm) because
// it needs the unexported newPartialSyncErr constructor + unexported
// fail() method.
//
// Pre-cancels ctx BEFORE h.Handle(...) so the dispatch loop's ctx.Err()
// gate fires on iteration 1 (or at least before totalCharts entries are
// committed). Asserts the returned error carries the D-03a typed shape:
//
//   - errors.Is(err, helm.ErrHelmPartialSync) == true
//   - errors.As(err, &*helm.PartialSyncErr) unwraps persisted/expected
//   - persisted is in [0, totalCharts] (goroutines in flight may still land)
//   - expected == len(entries) (2 in the fixture)

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
)

func TestHelmSync_CtxCancel_ReturnsPartial(t *testing.T) {
	h, db, repoID, upURL := newHelmProgressFixture(t)
	jobID := seedHelmSyncJobRow(t, db, repoID)

	payload := map[string]string{"upstream_url": upURL}
	pb, _ := json.Marshal(payload)

	// Pre-cancel ctx — the dispatch loop's ctx.Err() gate must observe
	// this and return *PartialSyncErr rather than nil.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := h.Handle(ctx, string(pb), 0, repoID, jobID)
	if err == nil {
		t.Fatalf("Handle returned nil; want partial-sync error after pre-cancel")
	}
	if !errors.Is(err, helm.ErrHelmPartialSync) {
		t.Fatalf("errors.Is(err, ErrHelmPartialSync) = false; got err=%v", err)
	}

	var pse *helm.PartialSyncErr
	if !errors.As(err, &pse) {
		t.Fatalf("errors.As(err, &pse) = false; got err=%v", err)
	}
	if pse.Expected() != 2 {
		t.Errorf("Expected() = %d; want 2 (fixture defines 2 charts)", pse.Expected())
	}
	if p := pse.Persisted(); p < 0 || p > 2 {
		t.Errorf("Persisted() = %d; want [0,2] (bounded by fixture chart count)", p)
	}
}
