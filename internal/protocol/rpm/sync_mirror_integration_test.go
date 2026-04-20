package rpm_test

// Phase 8 Plan 06 / M6.2 — RPM fake-upstream integration test.
//
// Complements sync_progress_test.go (byte-level progress assertion) by
// proving the end-to-end mirror flow:
//   1. First sync ingests the upstream .rpm (testdata/sample.rpm) via a
//      fake repomd.xml + primary.xml.gz served over httptest.NewServer
//   2. Progress row reaches a final state (progress_bytes > 0;
//      progress_bytes == total_bytes; current_step matches "done" or
//      "pulling <stem>.rpm")
//   3. Second sync is idempotent: rpm_packages row count unchanged
//
// Uses real metadata.NewReposRepo / metadata.NewSyncJobsRepo /
// metadata.NewRPMPackagesRepo — no fictitious env.* helpers. The fake
// upstream serves repodata/repomd.xml pointing at primary.xml.gz, which
// lists one package with correct size + sha256 + href. The actual .rpm
// blob served at /Packages/sample.rpm is the canonical sample used by
// parse_test.go.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

func TestMirrorSync_RPM_Idempotent(t *testing.T) {
	h, db, repoID, upURL := newRPMProgressFixture(t)
	ctx := context.Background()

	// First sync.
	jobID := seedRPMSyncJob(t, db, repoID)
	payload := map[string]any{"upstream_url": upURL}
	pb, _ := json.Marshal(payload)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID); err != nil {
		t.Fatalf("first Handle: %v", err)
	}

	rpmPkgs := metadata.NewRPMPackagesRepo(db)
	rowsAfterFirst, err := rpmPkgs.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("list after first: %v", err)
	}
	if len(rowsAfterFirst) != 1 {
		t.Fatalf("first sync package count = %d; want 1 (sample)", len(rowsAfterFirst))
	}

	// Progress row final state.
	var (
		progressBytes, totalBytes int64
		currentStep               string
	)
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT progress_bytes, total_bytes, current_step FROM sync_jobs WHERE id=?`, jobID,
	).Scan(&progressBytes, &totalBytes, &currentStep); err != nil {
		t.Fatalf("scan progress: %v", err)
	}
	if totalBytes <= 0 {
		t.Errorf("total_bytes=%d; want >0 (summed primary.xml <size package>)", totalBytes)
	}
	if progressBytes <= 0 {
		t.Errorf("progress_bytes=%d; want >0", progressBytes)
	}
	if progressBytes != totalBytes {
		t.Errorf("progress_bytes=%d != total_bytes=%d; want equal at end of sync",
			progressBytes, totalBytes)
	}
	if currentStep == "" {
		t.Errorf("current_step=%q; want non-empty", currentStep)
	}

	// Second sync — idempotency gate.
	jobID2 := seedRPMSyncJob(t, db, repoID)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID2); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	rowsAfterSecond, err := rpmPkgs.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("list after second: %v", err)
	}
	if len(rowsAfterSecond) != len(rowsAfterFirst) {
		t.Fatalf("idempotency violated: first=%d, second=%d",
			len(rowsAfterFirst), len(rowsAfterSecond))
	}

	// Second-sync total_bytes == 0: all entries filtered out by
	// FindByDigest in collectFn.
	var totalBytes2 int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT total_bytes FROM sync_jobs WHERE id=?`, jobID2,
	).Scan(&totalBytes2); err != nil {
		t.Fatalf("scan progress (second): %v", err)
	}
	if totalBytes2 != 0 {
		t.Errorf("second-sync total_bytes=%d; want 0 (all entries already present)", totalBytes2)
	}
}
