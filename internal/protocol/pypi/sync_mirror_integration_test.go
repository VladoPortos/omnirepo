package pypi_test

// Phase 8 Plan 06 / M6.3 — PyPI fake-upstream integration test.
//
// Complements sync_progress_test.go (byte-level progress assertion) by
// proving the end-to-end mirror flow via the PEP 691 JSON Simple API:
//   1. First sync ingests two wheel-shaped files from a fake /simple/
//      endpoint serving application/vnd.pypi.simple.v1+json
//   2. Progress row reaches a final state (progress_bytes > 0;
//      progress_bytes == total_bytes; current_step non-empty)
//   3. Second sync is idempotent: pypi_files row count unchanged
//
// Uses real metadata.NewReposRepo / metadata.NewSyncJobsRepo /
// metadata.NewPyPIFilesRepo — no fictitious env.* helpers. Fake upstream
// lives in sync_progress_test.go's newPyPIProgressFixture and already
// serves PEP 691 JSON with accurate sha256 + size on both files.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

func TestMirrorSync_PyPI_Idempotent(t *testing.T) {
	h, db, repoID, upURL := newPyPIProgressFixture(t)
	ctx := context.Background()

	// First sync.
	jobID := seedPyPISyncJob(t, db, repoID)
	payload := map[string]any{"upstream_url": upURL}
	pb, _ := json.Marshal(payload)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID); err != nil {
		t.Fatalf("first Handle: %v", err)
	}

	pypiFiles := metadata.NewPyPIFilesRepo(db)
	projs, err := pypiFiles.ListProjects(ctx, repoID)
	if err != nil {
		t.Fatalf("list projects after first: %v", err)
	}
	if len(projs) != 1 {
		t.Fatalf("first sync project count = %d; want 1 (acme)", len(projs))
	}
	rowsAfterFirst, err := pypiFiles.ListByProject(ctx, repoID, projs[0])
	if err != nil {
		t.Fatalf("list files after first: %v", err)
	}
	if len(rowsAfterFirst) != 2 {
		t.Fatalf("first sync file count = %d; want 2 (acme-1.0.0 + acme-1.1.0)", len(rowsAfterFirst))
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
		t.Errorf("total_bytes=%d; want >0 (summed PEP 691 file.size)", totalBytes)
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
	jobID2 := seedPyPISyncJob(t, db, repoID)
	if err := h.Handle(ctx, string(pb), 0, repoID, jobID2); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	rowsAfterSecond, err := pypiFiles.ListByProject(ctx, repoID, projs[0])
	if err != nil {
		t.Fatalf("list files after second: %v", err)
	}
	if len(rowsAfterSecond) != len(rowsAfterFirst) {
		t.Fatalf("idempotency violated: first=%d, second=%d",
			len(rowsAfterFirst), len(rowsAfterSecond))
	}

	// Second-sync total_bytes == 0 because every file is filtered via
	// FindByDigest during the collect pass.
	var totalBytes2 int64
	if err := db.Reader.QueryRowContext(ctx,
		`SELECT total_bytes FROM sync_jobs WHERE id=?`, jobID2,
	).Scan(&totalBytes2); err != nil {
		t.Fatalf("scan progress (second): %v", err)
	}
	if totalBytes2 != 0 {
		t.Errorf("second-sync total_bytes=%d; want 0 (all files already present)", totalBytes2)
	}
}
