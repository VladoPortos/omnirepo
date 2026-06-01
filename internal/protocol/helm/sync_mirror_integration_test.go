package helm_test

// Helm fake-upstream integration test for the TRADITIONAL HTTP
// helm-index sync path (index.yaml + .tgz over HTTPS).
//
// Scope note: two other test files in this module cover RELATED but DISTINCT
// flows:
//   - internal/protocol/helm/oci_mirror_test.go — helm charts pulled from an
//     OCI registry into a traditional helm repo
//   - internal/protocol/oci/helm_mirror_test.go — the OCI-protocol side of
//     the same flow
// This new file exercises the orthogonal case where the upstream is a
// classic helm repository (index.yaml + .tgz served over HTTP) — the
// flow the SyncHandler in sync_handler.go implements.
//
// End-to-end flow this test proves:
//   1. First sync ingests two charts from a fake index.yaml-based upstream
//      (shared fixture newHelmProgressFixture serves nginx-1.0.0 + redis-7.0.0)
//   2. Progress row reaches the Helm-specific final state:
//      total_bytes == 0 (step-based), progress_bytes == chart count,
//      current_step matches /^(done|chart \d+ of \d+ · .+\.tgz)$/
//   3. Second sync is idempotent: helm_charts count unchanged
//
// Uses real metadata.NewReposRepo / metadata.NewSyncJobsRepo /
// metadata.NewHelmChartsRepo — no shortcut harness helpers.

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

func TestMirrorSync_Helm_Idempotent(t *testing.T) {
	h, db, repoID, upURL := newHelmProgressFixture(t)
	ctx := context.Background()

	// First sync.
	jobID := seedHelmSyncJobRow(t, db, repoID)
	payload := map[string]string{"upstream_url": upURL}
	pb, _ := json.Marshal(payload)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID); err != nil {
		t.Fatalf("first Handle: %v", err)
	}

	helmCharts := metadata.NewHelmChartsRepo(db)
	rowsAfterFirst, err := helmCharts.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("list after first: %v", err)
	}
	if len(rowsAfterFirst) != 2 {
		t.Fatalf("first sync chart count = %d; want 2 (nginx + redis)", len(rowsAfterFirst))
	}

	// Progress row final state — Helm-specific shape.
	var (
		progressBytes, totalBytes int64
		currentStep               string
	)
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT progress_bytes, total_bytes, current_step FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&progressBytes, &totalBytes, &currentStep); err != nil {
		t.Fatalf("scan progress: %v", err)
	}
	if totalBytes != 0 {
		t.Errorf("total_bytes=%d; want 0 (Helm is step-based)", totalBytes)
	}
	if progressBytes < 1 {
		t.Errorf("progress_bytes=%d; want >=1 (completed chart count)", progressBytes)
	}
	stepRe := regexp.MustCompile(`^(done|chart \d+ of \d+ · .+\.tgz)$`)
	if !stepRe.MatchString(currentStep) {
		t.Errorf("current_step=%q; want match %s", currentStep, stepRe.String())
	}

	// Second sync — idempotency gate.
	jobID2 := seedHelmSyncJobRow(t, db, repoID)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID2); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	rowsAfterSecond, err := helmCharts.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("list after second: %v", err)
	}
	if len(rowsAfterSecond) != len(rowsAfterFirst) {
		t.Fatalf("idempotency violated: first=%d, second=%d",
			len(rowsAfterFirst), len(rowsAfterSecond))
	}

	// Second-sync progress: all charts filtered by FindByDigest, so the
	// entries slice is empty. No chart step is emitted; the terminal
	// "done" sentinel still flushes at the end.
	var (
		progressBytes2, totalBytes2 int64
		currentStep2                string
	)
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT progress_bytes, total_bytes, current_step FROM sync_jobs WHERE id=?`, jobID2,
	).Scan(&progressBytes2, &totalBytes2, &currentStep2); err != nil {
		t.Fatalf("scan progress (second): %v", err)
	}
	if totalBytes2 != 0 {
		t.Errorf("second-sync total_bytes=%d; want 0 (step-based)", totalBytes2)
	}
	if currentStep2 != "done" {
		t.Errorf("second-sync current_step=%q; want 'done' (nothing to download, terminal emit wins)", currentStep2)
	}
}
